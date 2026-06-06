package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

// queryTimeout bounds each per-host list query. Tuning lever (see the
// design doc's table): LAN/tailnet ssh typically answers in <500ms and 3s
// tolerates wake-from-sleep; raise it if WAN hosts misfire, lower it if
// `sc list` feels slow. A caller ctx with an earlier deadline still wins.
const queryTimeout = 3 * time.Second

// QueryHost runs `ssh <r.Dest()> spinclass list --format json` and parses
// the session.ListRow wire array the remote spinclass serves. Non-zero
// exit, timeout, or unparseable output (e.g. an old remote binary without
// json support) yields an error carrying any stderr detail — never partial
// rows. Error shape mirrors internal/clown's run().
func QueryHost(ctx context.Context, r sweatfile.Remote) ([]session.ListRow, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh", r.Dest(), "spinclass", "list", "--format", "json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := bytes.TrimSpace(stderr.Bytes())
		if len(detail) > 0 {
			return nil, fmt.Errorf("ssh %s: %w: %s", r.Dest(), err, detail)
		}
		return nil, fmt.Errorf("ssh %s: %w", r.Dest(), err)
	}

	var rows []session.ListRow
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		return nil, fmt.Errorf("ssh %s: parse spinclass list json: %w", r.Dest(), err)
	}
	return rows, nil
}
