// Package trustedhome resolves trust-anchor paths (the profile-grant signers
// file, the grant store) from the account database, never from the caller's
// environment. A process an agent starts carries the agent's XDG_* and HOME,
// so an env-derived anchor is one the agent chooses (audit row 34-31; EPIC 0001
// principle 2: a second place that decides what is allowed is a bug).
package trustedhome

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// Home is the passwd home of the running uid. It is a variable so tests can
// stand in for the account database.
var Home = func() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolving the passwd home: %w", err)
	}
	if u.HomeDir == "" {
		return "", fmt.Errorf("the passwd entry for uid %s has no home directory", u.Uid)
	}
	return u.HomeDir, nil
}

// Resolve returns <passwd home>/<baseRel>/<rel>. envVar is the XDG variable
// that would otherwise move the base: if it is set to anything but the
// passwd-home default, the override is refused rather than silently ignored,
// so a misconfigured operator sees why (principle 9) and an agent cannot
// redirect the anchor.
func Resolve(envVar, baseRel string, rel ...string) (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, baseRel)
	if v := os.Getenv(envVar); v != "" && filepath.Clean(v) != base {
		return "", fmt.Errorf("%s is set to %s, but trust anchors are read only from the passwd home (%s); unset it or point it there", envVar, v, base)
	}
	return filepath.Join(append([]string{base}, rel...)...), nil
}
