package bridgeserver

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
)

// admission-vectors.json is the one file both repositories run (clown vendors
// it from spinclass's internal/0/bridge/testdata; here it comes through
// bridge.AdmissionVectors): the listener's admission decision for a cgroup file
// as placed and a peer's cgroup. Each case is a list of steps that share one
// file path, so a rewrite or removal between steps shows that the file is read
// per connection.

type admitStep struct {
	File  string `json:"file"`
	Peer  string `json:"peer"`
	Admit bool   `json:"admit"`
}

type admitVector struct {
	Name  string      `json:"name"`
	Steps []admitStep `json:"steps"`
}

func placeCgroupFile(t *testing.T, path, spec string) {
	t.Helper()
	_ = os.RemoveAll(path)
	_ = os.RemoveAll(path + ".target")
	switch {
	case spec == "absent":
	case spec == "dir":
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	case spec == "long":
		if err := os.WriteFile(path, []byte("/"+strings.Repeat("x", 2*maxCgroupFileBytes)), 0o600); err != nil {
			t.Fatal(err)
		}
	case strings.HasPrefix(spec, "symlink:"):
		target := path + ".target"
		if err := os.WriteFile(target, []byte(strings.TrimPrefix(spec, "symlink:")), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	case strings.HasPrefix(spec, "abs:"), strings.HasPrefix(spec, "raw:"):
		if err := os.WriteFile(path, []byte(spec[4:]), 0o600); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown file spec %q", spec)
	}
}

func TestAdmissionVectors(t *testing.T) {
	var vs []admitVector
	if err := json.Unmarshal(bridge.AdmissionVectors, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) < 20 {
		t.Fatalf("only %d vectors", len(vs))
	}
	// checkPeer wants a unix connection (it reads the peer's uid first); the
	// vector's peer cgroup is carried by the fake exact check below.
	dir := shortDir(t)
	l, err := net.Listen("unix", filepath.Join(dir, "p.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	conn, err := net.Dial("unix", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	for _, v := range vs {
		t.Run(v.Name, func(t *testing.T) {
			path := filepath.Join(shortDir(t), "bridge.cgroup")
			var peer string
			s := &Server{opts: Options{
				CgroupFile: path,
				// Strict equality is clown's CheckPeerExact; the vector's peer
				// stands for the connector's cgroup here.
				CheckPeer: func(_ net.Conn, want string) error {
					if peer == "" || peer != want {
						return os.ErrPermission
					}
					return nil
				},
			}}
			for i, st := range v.Steps {
				placeCgroupFile(t, path, st.File)
				peer = st.Peer
				_, err := s.checkPeer(conn)
				if (err == nil) != st.Admit {
					t.Errorf("step %d (file %q, peer %q): err = %v, want admit=%v", i, st.File, st.Peer, err, st.Admit)
				}
			}
		})
	}
}
