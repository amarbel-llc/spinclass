package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/grantverify"
	"code.linenisgreat.com/spinclass/internal/operatoract"
	"code.linenisgreat.com/spinclass/internal/profilegrant"
	"code.linenisgreat.com/spinclass/internal/spawn"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

const (
	grantOperator = "operator"
	minGrantTTL   = time.Minute
	maxGrantTTL   = 7 * 24 * time.Hour
	defaultTTL    = 24 * time.Hour
)

// Seams for command tests. Production wiring goes through the agent that holds
// the card; tests inject a fake signer and a temp signers file.
//
// The signer gets the card keys loaded from the signers file so it signs only
// with an agent identity whose key is in that list, not whichever identity
// carries a "PIV_slot_9C" comment.
var profileGrantSigner = func(keys []ssh.PublicKey) (operatoract.Signer, error) {
	s, err := operatoract.NewAgentSigner()
	if err != nil {
		return nil, err
	}
	s.Keys = keys
	return s, nil
}

var profileGrantSigners = grantverify.DefaultSignersPath

// Guards against an agent driving the card prompt (see requireOperatorTerminal).
var (
	profileGrantStdinIsTerminal  = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	profileGrantStderrIsTerminal = func() bool { return term.IsTerminal(int(os.Stderr.Fd())) }
	// profileGrantAgentRuntime returns the name of the first environment
	// variable that marks an agent runtime, or "".
	profileGrantAgentRuntime = func() string {
		for _, v := range []string{"CLOWN_SESSION_ID", "SPINCLASS_SESSION_ID"} {
			if os.Getenv(v) != "" {
				return v
			}
		}
		return ""
	}
)

// requireOperatorTerminal refuses to run unless the operator is plausibly at
// the keyboard. Being CLI-only keeps this out of MCP, but an agent with a shell
// tool could still run it and spam the operator's PIN prompt with a grant of
// its choosing.
//
// The environment check is advisory: an agent can unset the variables. The TTY
// check is defence in depth, for the same reason a pty can be allocated. The
// real boundary is the prompt surface: the card shows the exact act text and
// only the operator's PIN and touch sign it (FDR 0034, "Where the card root is
// real").
func requireOperatorTerminal() error { return requireOperatorTerminalFor("profile-grant-bless") }

// requireOperatorTerminalFor is requireOperatorTerminal for any command that
// asks the card for an act (also `sc resume --repin-profile`, #407).
func requireOperatorTerminalFor(command string) error {
	if v := profileGrantAgentRuntime(); v != "" {
		return fmt.Errorf("%s: %s is set, so this looks like an agent runtime; a card act must be started from the operator's own terminal, because an agent-started act would put an approval of the agent's choosing in front of the operator's PIN prompt", command, v)
	}
	if !profileGrantStdinIsTerminal() || !profileGrantStderrIsTerminal() {
		return fmt.Errorf("%s: stdin and stderr must both be terminals; a card act must be started from the operator's own terminal, not piped or driven by a program", command)
	}
	return nil
}

var profileGrantResolve = func(name string) (string, error) {
	rp, err := spawn.ResolveProfile(name)
	return rp.Digest, err
}

// registerProfileGrantCommands registers the card-signing command. It is
// CLI-only by construction: it sets RunCLI and no Run, and RegisterMCPToolsV1
// skips commands without Run, so no MCP tool can trigger a card act. A test
// pins that.
func registerProfileGrantCommands(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "profile-grant-bless",
		Title: "Card-sign a profile grant",
		Description: command.Description{
			Short: "Have the operator's card sign a profile grant (human authority; FDR 0034)",
			Long: "Builds a root profile grant, shows its exact act text, and asks the SSH agent holding the operator card to sign it " +
				"(PIN and touch at the card's prompt; piggy operator-act@piggy). The signed record is verified against the profile-grant " +
				"signers file before it is emitted, and is NOT stored: the output is the record JSON, on stdout or --out. " +
				"Each --profile is resolved through clown's profile library and pinned to its content digest. " +
				"A widening of an earlier root grant needs --widens <its digest> and --widened-record <path to its JSON>. " +
				"Without a readable signers file the command refuses before asking the card, unless --no-verify is given.",
		},
		Params: []command.Param{
			{Name: "grantee", Type: command.String, Description: "Who receives the right: a session key or a principal id (required)"},
			{Name: "session", Type: command.String, Description: "Session the grant is bound to; \"-\" for a grantee that is a service (default \"-\")"},
			{Name: "profile", Type: command.Array, Description: "Clown profile to grant, pinned to its resolved digest; repeatable, at least one"},
			{Name: "ttl", Type: command.String, Description: "How long the grant lasts, a Go duration (default 24h, at least 1m, at most 168h)"},
			{Name: "onward", Type: command.String, Description: "none (default) or grant: whether the grantee may pass the rights on"},
			{Name: "widens", Type: command.String, Description: "Digest of the earlier root record this widens; omit for an initial grant"},
			{Name: "widened-record", Type: command.String, Description: "Path to the JSON of the record named by --widens (needed to verify the widening offline)"},
			{Name: "out", Type: command.String, Description: "Write the record JSON here instead of stdout"},
			{Name: "no-verify", Type: command.Bool, Description: "Emit the record without verifying it against the signers file"},
		},
		RunCLI: runProfileGrantBless,
	})
}

type blessFlags struct {
	Grantee       string   `json:"grantee"`
	Session       string   `json:"session"`
	Profile       []string `json:"profile"`
	TTL           string   `json:"ttl"`
	Onward        string   `json:"onward"`
	Widens        string   `json:"widens"`
	WidenedRecord string   `json:"widened-record"`
	Out           string   `json:"out"`
	NoVerify      bool     `json:"no-verify"`
}

func runProfileGrantBless(ctx context.Context, args json.RawMessage) error {
	// First: before the signers file is read or the card is contacted.
	if err := requireOperatorTerminal(); err != nil {
		return err
	}
	var f blessFlags
	if err := json.Unmarshal(args, &f); err != nil {
		return fmt.Errorf("profile-grant-bless: %w", err)
	}
	in, err := f.toInput()
	if err != nil {
		return err
	}

	var keys []ssh.PublicKey
	signersPath := profileGrantSigners()
	if !f.NoVerify {
		// Load before the card is asked: a signature that cannot then be
		// verified or emitted would cost the operator a PIN and a touch for nothing.
		if keys, err = grantverify.LoadSigners(signersPath); err != nil {
			return fmt.Errorf("%w (create it, or pass --no-verify to skip verification)", err)
		}
	} else if _, statErr := os.Stat(signersPath); statErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %s is not readable, so the record cannot be verified here; verify it where the signers file lives\n", signersPath)
	}
	in.SkipVerify = f.NoVerify

	signer, err := profileGrantSigner(keys)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	in.ActOut = os.Stderr
	rec, err := blessRecord(ctx, signer, keys, in)
	if err != nil {
		return err
	}
	return emitRecord(rec, f.Out)
}

// toInput resolves the flags into a blessInput: profiles are pinned to the
// digests clown's library reports, the widened record is loaded, the clock and
// nonce are drawn.
func (f blessFlags) toInput() (blessInput, error) {
	if f.Grantee == "" {
		return blessInput{}, errors.New("profile-grant-bless: --grantee is required")
	}
	if len(f.Profile) == 0 {
		return blessInput{}, errors.New("profile-grant-bless: at least one --profile is required")
	}
	ttl := defaultTTL
	if f.TTL != "" {
		var err error
		if ttl, err = time.ParseDuration(f.TTL); err != nil {
			return blessInput{}, fmt.Errorf("profile-grant-bless: --ttl: %w", err)
		}
	}
	onward := profilegrant.Onward(f.Onward)
	if f.Onward == "" {
		onward = profilegrant.OnwardNone
	}
	session := f.Session
	if session == "" {
		session = "-"
	}
	profiles := map[string]string{}
	for _, name := range f.Profile {
		digest, err := profileGrantResolve(name)
		if err != nil {
			return blessInput{}, fmt.Errorf("profile-grant-bless: profile %q: %w", name, err)
		}
		profiles[name] = digest
	}
	var widened *profilegrant.Record
	switch {
	case f.Widens != "" && f.WidenedRecord == "":
		return blessInput{}, errors.New("profile-grant-bless: --widens needs --widened-record: a widening cannot be verified offline without the record it widens")
	case f.Widens == "" && f.WidenedRecord != "":
		return blessInput{}, errors.New("profile-grant-bless: --widened-record is only meaningful with --widens")
	case f.Widens != "":
		raw, err := os.ReadFile(f.WidenedRecord)
		if err != nil {
			return blessInput{}, fmt.Errorf("profile-grant-bless: --widened-record: %w", err)
		}
		widened = new(profilegrant.Record)
		if err := json.Unmarshal(raw, widened); err != nil {
			return blessInput{}, fmt.Errorf("profile-grant-bless: --widened-record %s: %w", f.WidenedRecord, err)
		}
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return blessInput{}, fmt.Errorf("profile-grant-bless: nonce: %w", err)
	}
	return blessInput{
		Grantee:  f.Grantee,
		Session:  session,
		Profiles: profiles,
		TTL:      ttl,
		Onward:   onward,
		Widens:   f.Widens,
		Widened:  widened,
		Now:      time.Now(),
		Nonce:    hex.EncodeToString(nonce),
	}, nil
}

// blessInput is everything blessRecord needs, with the clock, nonce and
// resolved profile digests already drawn so the function is deterministic.
type blessInput struct {
	Grantee  string
	Session  string
	Profiles map[string]string // name -> pinned digest
	TTL      time.Duration
	Onward   profilegrant.Onward
	Widens   string               // "" for an initial grant
	Widened  *profilegrant.Record // required iff Widens is set
	Now      time.Time
	Nonce    string

	// SkipVerify emits the signed record without checking it (--no-verify).
	SkipVerify bool
	// ActOut receives the act text before the card is asked, so the operator
	// can compare it with the card's prompt. May be nil.
	ActOut io.Writer
}

// blessRecord builds the root grant, has the card sign its act text, and
// returns the record only if it verifies against signers (or SkipVerify).
func blessRecord(ctx context.Context, signer operatoract.Signer, signers []ssh.PublicKey, in blessInput) (profilegrant.Record, error) {
	// The minimum is not cosmetic: the act text renders expiry at second
	// precision, so a sub-minute TTL could read as expired to the post-sign
	// verify after the card act (PIN and touch) is already spent.
	if in.TTL < minGrantTTL || in.TTL > maxGrantTTL {
		return profilegrant.Record{}, fmt.Errorf("--ttl %s out of range: must be at least %s and at most %s", in.TTL, minGrantTTL, maxGrantTTL)
	}
	if in.Onward != profilegrant.OnwardNone && in.Onward != profilegrant.OnwardGrant {
		return profilegrant.Record{}, fmt.Errorf("--onward %q: want none or grant", in.Onward)
	}
	if !in.SkipVerify && len(signers) == 0 {
		return profilegrant.Record{}, errors.New("no signers to verify against; refusing to ask the card for a record that cannot be checked")
	}
	widens := "-"
	if in.Widens != "" {
		widens = in.Widens
		if in.Widened == nil || in.Widened.Digest != in.Widens {
			return profilegrant.Record{}, fmt.Errorf("the record to widen must be supplied with digest %s", in.Widens)
		}
	}

	grant := profilegrant.Grant{
		Grantor:  grantOperator,
		Grantee:  in.Grantee,
		Session:  in.Session,
		Profiles: in.Profiles,
		Onward:   in.Onward,
		Expires:  in.Now.UTC().Truncate(time.Second).Add(in.TTL),
		Nonce:    in.Nonce,
		Widens:   widens,
		Prev:     "-",
	}
	text := grant.CanonicalText()
	digest, err := profilegrant.DigestOf(text)
	if err != nil {
		return profilegrant.Record{}, err
	}
	act, err := profilegrant.ActText(grant, digest)
	if err != nil {
		return profilegrant.Record{}, err
	}
	if in.ActOut != nil {
		fmt.Fprintf(in.ActOut, "Asking the operator card to sign this act (the card prompt shows the same text):\n\n%s\n", act)
	}

	sig, err := signer.Sign(ctx, operatoract.NamespaceEscalation, act)
	if err != nil {
		return profilegrant.Record{}, err
	}
	rec := profilegrant.Record{Digest: digest, Text: text, Sig: sig, Root: true}
	if in.SkipVerify {
		return rec, nil
	}

	known := map[string]profilegrant.Record{rec.Digest: rec}
	if in.Widened != nil {
		known[in.Widened.Digest] = *in.Widened
	}
	resolve := func(d string) (profilegrant.Record, bool) { r, ok := known[d]; return r, ok }
	verifier := profilegrant.NewVerifier(grantverify.NewRootVerifier(signers), profilegrant.LocalLinkVerifier{})
	if _, err := profilegrant.Walk(in.Now, rec, resolve, verifier, profilegrant.WalkOptions{}); err != nil {
		return profilegrant.Record{}, fmt.Errorf("the signed record does not verify, not emitting it: %w", err)
	}
	return rec, nil
}

func emitRecord(rec profilegrant.Record, out string) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if out == "" {
		_, err = os.Stdout.Write(b)
		return err
	}
	return writeRecordFile(out, b)
}

// writeRecordFile writes atomically (temp file in the same directory, then
// rename) with mode 0600 regardless of any existing file, and refuses a
// symlink at the target: os.WriteFile follows links, applies the mode only on
// create, and can leave a partial file.
func writeRecordFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("--out %s is a symlink; refusing to write through it", path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".grant-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
