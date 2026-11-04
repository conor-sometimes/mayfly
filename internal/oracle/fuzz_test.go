package oracle

import (
	"regexp"
	"strings"
	"testing"
)

// Fuzz targets for the oracle's parsers. Every one of these reads text the
// in-guest engine does not control (an audit log, a subject's OCSF jsonl, the
// output of a distro's own tools), so the bar is: no input panics, and the
// documented invariant of each helper holds for arbitrary bytes. Run a bounded
// smoke in CI (go test -run=Fuzz covers the seeds; -fuzz with -fuzztime=Nx for
// a count-bounded sweep - never a wall-clock bound, per the family lesson).

// FuzzAuditExe: AuditExe returns a basename, so the result never contains '/',
// whatever the exe= field holds (quoted, hex-encoded, truncated, absent).
func FuzzAuditExe(f *testing.F) {
	f.Add("type=SYSCALL msg=audit(1): pid=10 ppid=1 exe=\"/usr/bin/touch\" ")
	f.Add("type=EXECVE exe=2F62696E2F7368 ") // hex /bin/sh
	f.Add("type=SYSCALL exe=(null) comm=\"x\"")
	f.Add("exe=\"/a b/c\"") // embedded space in quotes
	f.Add("garbage with no exe field at all")
	f.Fuzz(func(t *testing.T, line string) {
		got := AuditExe(line)
		if strings.ContainsRune(got, '/') {
			t.Fatalf("AuditExe returned a non-basename %q for %q", got, line)
		}
	})
}

// FuzzAuditInt: the value of a space-delimited audit field is a non-negative
// integer, and " pid=" must never read the digits of " ppid=".
func FuzzAuditInt(f *testing.F) {
	f.Add("type=SYSCALL pid=1234 ppid=1 ", "pid=")
	f.Add("ppid=99 pid=100", "ppid=")
	f.Add("pid=", "pid=") // key present, no digits
	f.Add("no such field here", "pid=")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, line, key string) {
		if got := AuditInt(line, key); got < 0 {
			t.Fatalf("AuditInt(%q,%q) = %d, want >= 0", line, key, got)
		}
	})
}

// FuzzProcTreeDescendants is the end-to-end subtree path on an attacker-shaped
// audit log: build the pid->ppid map, then walk it. The walk must terminate
// (a cyclic log must not hang - the `seen` guard is what guarantees it), always
// include the launcher, and never grow past the distinct pids plus the launcher.
func FuzzProcTreeDescendants(f *testing.F) {
	f.Add("type=SYSCALL pid=1 ppid=0\ntype=SYSCALL pid=2 ppid=1\n", 1)
	f.Add("type=SYSCALL pid=1 ppid=2\ntype=SYSCALL pid=2 ppid=1\n", 1) // a cycle
	f.Add("type=SYSCALL pid=5 ppid=5\n", 5)                            // self-parent
	f.Add("not an audit log", 42)
	f.Fuzz(func(t *testing.T, log string, launcher int) {
		tree := AuditProcTree(log)
		seen := Descendants(tree, launcher)
		if !seen[launcher] {
			t.Fatalf("Descendants dropped the launcher %d", launcher)
		}
		if len(seen) > len(tree)+1 {
			t.Fatalf("Descendants grew to %d from a %d-pid tree", len(seen), len(tree))
		}
	})
}

// FuzzOCSF: the four finding readers never panic on arbitrary bytes, and a
// finding that yields a rule name yields a non-empty one.
func FuzzOCSF(f *testing.F) {
	f.Add(`{"finding_info":{"analytic":{"name":"rule.x"}},"severity_id":4,"evidences":[{"process":{"pid":7,"cmd_line":"x","parent_process":{"pid":1}}}]}`)
	f.Add(`{"evidences":[]}`)
	f.Add(`not json`)
	f.Add(``)
	f.Fuzz(func(t *testing.T, line string) {
		name, ok := AnalyticName(line)
		if ok && name == "" {
			t.Fatalf("AnalyticName ok with empty name for %q", line)
		}
		_ = Severity(line)
		_ = CommandLine(line)
		_, _ = AlertProcess(line)
	})
}

// FuzzPinDNFRepo: the rewrite is line-by-line, so it preserves the line count
// for any input (no record is added or dropped).
func FuzzPinDNFRepo(f *testing.F) {
	f.Add("[baseos]\nmirrorlist=https://m/\n#baseurl=https://dl/\n")
	f.Add("")
	f.Add("\n\n\n")
	f.Add("download.example/pub/fedora/linux/x")
	f.Fuzz(func(t *testing.T, s string) {
		got := PinDNFRepo(s)
		if a, b := strings.Count(got, "\n"), strings.Count(s, "\n"); a != b {
			t.Fatalf("PinDNFRepo changed the line count: %d -> %d", b, a)
		}
	})
}

// FuzzTextHelpers: the remaining string readers never panic and respect their
// one bound (LastLines/FirstN never exceed n). These feed the report, so a
// panic here would crash a run on a weird distro banner.
func FuzzTextHelpers(f *testing.F) {
	f.Add("PRETTY_NAME=\"Ubuntu 24.04\"\nID=ubuntu\n", "enabled 1\nfailure 2\n")
	f.Add("", "")
	f.Add("a\nb\nc\nd\ne", "x y\nz")
	f.Fuzz(func(t *testing.T, a, b string) {
		_ = OSRelease(a)
		_ = StatusField(b, "enabled")
		_, _, _ = Exposure(a)
		_ = FirstLine(a)
		if got := LastLines(a, 3); strings.Count(got, " | ") > 2 {
			t.Fatalf("LastLines(_,3) joined more than 3 lines: %q", got)
		}
		if got := FirstN(NonEmpty(a), 2); len(got) > 2 {
			t.Fatalf("FirstN(_,2) returned %d", len(got))
		}
		_ = LinesContaining(a, "x")
		_ = LinesMatching(a, regexp.MustCompile(`x`))
	})
}
