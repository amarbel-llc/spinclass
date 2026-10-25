package sysprompt

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// hostTimezone returns a best-effort display of the host's local timezone, e.g.
// "America/New_York (UTC-04:00)". Resolution is fully local — the IANA zone
// name from $TZ or the /etc/localtime symlink, the current offset from
// time.Now — so it is safe to call before `initialize` and never blocks.
func hostTimezone() string {
	return formatTimezone(ianaZoneName(os.Getenv, os.Readlink), time.Now())
}

// ianaZoneName resolves the host's IANA timezone name (e.g. "America/New_York")
// from $TZ first, then the /etc/localtime symlink target. Returns "" when
// neither yields a name; getenv/readlink are injected for testing.
func ianaZoneName(getenv func(string) string, readlink func(string) (string, error)) string {
	if name := zoneFromPath(getenv("TZ")); name != "" {
		return name
	}
	if target, err := readlink("/etc/localtime"); err == nil {
		if name := zoneFromPath(target); name != "" {
			return name
		}
	}
	return ""
}

// zoneFromPath extracts an IANA zone name from a $TZ value or a zoneinfo path.
// It accepts a bare name ("America/New_York"), a ":"-prefixed name
// (":America/New_York"), or any path with a "zoneinfo/" segment
// (".../zoneinfo/America/New_York"). An unrecognised absolute path (e.g. a bare
// "/etc/localtime" with no zoneinfo segment) and empty input yield "".
func zoneFromPath(s string) string {
	s = strings.TrimPrefix(s, ":")
	if s == "" {
		return ""
	}
	if i := strings.LastIndex(s, "zoneinfo/"); i >= 0 {
		return s[i+len("zoneinfo/"):]
	}
	if strings.HasPrefix(s, "/") {
		return ""
	}
	return s
}

// formatTimezone renders the display string from an IANA name (possibly empty)
// and the moment whose offset to report: "Name (UTC±HH:MM)" with a name,
// "UTC±HH:MM" with only an offset, and a bare "UTC" special-cased to avoid the
// redundant "UTC (UTC+00:00)".
func formatTimezone(name string, now time.Time) string {
	_, offsetSec := now.Zone()
	offset := formatUTCOffset(offsetSec)
	switch name {
	case "":
		return "UTC" + offset
	case "UTC":
		return "UTC"
	default:
		return name + " (UTC" + offset + ")"
	}
}

// formatUTCOffset formats a zone offset in seconds as "±HH:MM".
func formatUTCOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return fmt.Sprintf("%s%02d:%02d", sign, seconds/3600, (seconds%3600)/60)
}
