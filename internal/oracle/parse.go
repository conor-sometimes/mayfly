package oracle

import (
	"regexp"
	"sort"
	"strings"
)

// Pure helpers: parsing the output of the tools distrocheck drives, and the
// generic text munging the engine needs. Everything here is unit-tested
// (parse_test.go); cmd/distrocheck only sequences.

// NonEmpty splits s into its non-blank, space-trimmed lines.
func NonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// From returns lines[n:], or nothing if the source shrank (a rotated log)
// instead of panicking on a stale count.
func From(lines []string, n int) []string {
	if n >= len(lines) {
		return nil
	}
	return lines[n:]
}

// FirstLine is the first non-blank line of s.
func FirstLine(s string) string {
	l := NonEmpty(s)
	if len(l) == 0 {
		return ""
	}
	return l[0]
}

// LastLines is the last n non-blank lines of s, joined by " | ".
func LastLines(s string, n int) string {
	l := NonEmpty(s)
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " | ")
}

// FirstN is the first n elements of l, or all of them.
func FirstN(l []string, n int) []string {
	if len(l) > n {
		return l[:n]
	}
	return l
}

// LinesContaining is the lines of s that contain sub.
func LinesContaining(s, sub string) []string {
	var out []string
	for _, l := range NonEmpty(s) {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return out
}

// OSRelease is /etc/os-release's PRETTY_NAME.
func OSRelease(s string) string {
	for _, l := range NonEmpty(s) {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// StatusField reads one "name value" line of `auditctl -s`.
func StatusField(status, name string) string {
	for _, l := range NonEmpty(status) {
		if f := strings.Fields(l); len(f) == 2 && f[0] == name {
			return f[1]
		}
	}
	return ""
}

// PinDNFRepo points a .repo file at the vendor's own host: mirrorlist= and
// metalink= lines are commented out, the vendor's commented baseurl= is
// restored, and Fedora's download.example placeholder becomes its master site.
func PinDNFRepo(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "mirrorlist=") || strings.HasPrefix(t, "metalink="):
			lines[i] = "#" + l
		case strings.HasPrefix(t, "#") && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(t, "#")), "baseurl="):
			lines[i] = strings.TrimSpace(strings.TrimPrefix(t, "#"))
		}
		lines[i] = strings.ReplaceAll(lines[i], "download.example/pub/fedora/linux", "dl.fedoraproject.org/pub/fedora/linux")
	}
	return strings.Join(lines, "\n")
}

// TaskNever matches an audit rule that turns syscall auditing off for every
// process, in either order auditctl accepts or prints it.
var TaskNever = regexp.MustCompile(`^\s*-a\s+(task,never|never,task)(\s|$)`)

// LinesMatching is the trimmed lines of s that match re.
func LinesMatching(s string, re *regexp.Regexp) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if re.MatchString(l) {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

var exposureLine = regexp.MustCompile(`Overall exposure level for \S+: ([0-9.]+) (\w+)`)

// Exposure is the score and level from `systemd-analyze security UNIT`.
func Exposure(out string) (score, level string, ok bool) {
	m := exposureLine.FindStringSubmatch(out)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// Keys is the sorted keys of a set.
func Keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Equal reports whether two string slices are element-wise equal.
func Equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
