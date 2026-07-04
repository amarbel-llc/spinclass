package sysprompt

import (
	"os"
	"testing"
	"time"
)

func TestZoneFromPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"America/New_York", "America/New_York"},
		{":America/New_York", "America/New_York"},
		{"/usr/share/zoneinfo/America/New_York", "America/New_York"},
		{"../usr/share/zoneinfo/Europe/London", "Europe/London"},
		{"/etc/localtime", ""}, // absolute path, no zoneinfo segment
		{":/etc/localtime", ""},
		{"UTC", "UTC"},
	}
	for _, c := range cases {
		if got := zoneFromPath(c.in); got != c.want {
			t.Errorf("zoneFromPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// $TZ wins over the /etc/localtime symlink when both resolve.
func TestIANAZoneNamePrefersTZ(t *testing.T) {
	getenv := func(k string) string {
		if k == "TZ" {
			return "America/Chicago"
		}
		return ""
	}
	readlink := func(string) (string, error) { return "/usr/share/zoneinfo/Europe/Paris", nil }
	if got := ianaZoneName(getenv, readlink); got != "America/Chicago" {
		t.Errorf("ianaZoneName: got %q, want TZ value America/Chicago", got)
	}
}

// With no $TZ, the name comes from the /etc/localtime symlink target.
func TestIANAZoneNameFallsBackToSymlink(t *testing.T) {
	getenv := func(string) string { return "" }
	readlink := func(p string) (string, error) {
		if p == "/etc/localtime" {
			return "/usr/share/zoneinfo/Europe/Paris", nil
		}
		return "", os.ErrNotExist
	}
	if got := ianaZoneName(getenv, readlink); got != "Europe/Paris" {
		t.Errorf("ianaZoneName: got %q, want symlink-derived Europe/Paris", got)
	}
}

// Neither $TZ nor a resolvable symlink yields a name.
func TestIANAZoneNameEmptyWhenNothingResolves(t *testing.T) {
	getenv := func(string) string { return "" }
	readlink := func(string) (string, error) { return "", os.ErrNotExist }
	if got := ianaZoneName(getenv, readlink); got != "" {
		t.Errorf("ianaZoneName: got %q, want empty", got)
	}
}

func TestFormatTimezone(t *testing.T) {
	// Fixed zones so the offset is deterministic regardless of the host's TZ.
	est := time.FixedZone("EST", -5*3600)
	ist := time.FixedZone("IST", 5*3600+30*60) // +05:30, a half-hour offset
	cases := []struct {
		name string
		loc  *time.Location
		want string
	}{
		{"America/New_York", est, "America/New_York (UTC-05:00)"},
		{"Asia/Kolkata", ist, "Asia/Kolkata (UTC+05:30)"}, // half-hour offset
		{"", est, "UTC-05:00"},                            // no name, only offset
		{"UTC", time.UTC, "UTC"},                          // bare UTC, no redundant offset
		{"", time.UTC, "UTC+00:00"},                       // no name at UTC
	}
	for _, c := range cases {
		now := time.Date(2026, 7, 4, 12, 0, 0, 0, c.loc)
		if got := formatTimezone(c.name, now); got != c.want {
			t.Errorf("formatTimezone(%q, %s) = %q, want %q", c.name, c.loc, got, c.want)
		}
	}
}
