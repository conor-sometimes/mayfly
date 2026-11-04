package oracle

import (
	"reflect"
	"testing"
)

// Thorough edge-case coverage of the oracle's small pure helpers: boundaries
// (off-by-one), empty input, and the negative branch of each predicate. These
// kill the mutants (boundary flips, condition negations) that the happy-path
// tests leave alive, and pin the exact behaviour each caller relies on.

func TestFromBoundaries(t *testing.T) {
	lines := []string{"a", "b", "c"}
	if got := From(lines, 1); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("From(_,1) = %v", got)
	}
	// n == len: nothing left, and specifically nil (not a non-nil empty slice),
	// so a caller comparing to nil behaves.
	if got := From(lines, 3); got != nil {
		t.Errorf("From(_,len) = %v, want nil", got)
	}
	// n > len (a rotated/shrunk log): nil, never a panic.
	if got := From(lines, 9); got != nil {
		t.Errorf("From(_,>len) = %v, want nil", got)
	}
	if got := From(nil, 0); got != nil {
		t.Errorf("From(nil,0) = %v, want nil", got)
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine("\n\n  first \nsecond"); got != "first" {
		t.Errorf("FirstLine = %q", got)
	}
	if got := FirstLine("   \n\t\n"); got != "" {
		t.Errorf("FirstLine(blank) = %q, want empty", got)
	}
}

func TestLastLines(t *testing.T) {
	// len > n: keep the last n.
	if got := LastLines("a\nb\nc\nd", 2); got != "c | d" {
		t.Errorf("LastLines(4,2) = %q", got)
	}
	// len == n: all of them, in order.
	if got := LastLines("a\nb", 2); got != "a | b" {
		t.Errorf("LastLines(2,2) = %q", got)
	}
	// len < n: all of them, no panic.
	if got := LastLines("only", 5); got != "only" {
		t.Errorf("LastLines(1,5) = %q", got)
	}
	// blank lines are dropped first.
	if got := LastLines("a\n\n  \nb", 2); got != "a | b" {
		t.Errorf("LastLines(blanks) = %q", got)
	}
}

func TestFirstN(t *testing.T) {
	l := []string{"a", "b", "c"}
	if got := FirstN(l, 2); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("FirstN(_,2) = %v", got)
	}
	// n >= len: all of them (the slice itself).
	if got := FirstN(l, 3); !reflect.DeepEqual(got, l) {
		t.Errorf("FirstN(_,len) = %v", got)
	}
	if got := FirstN(l, 9); !reflect.DeepEqual(got, l) {
		t.Errorf("FirstN(_,>len) = %v", got)
	}
}

func TestLinesContaining(t *testing.T) {
	in := "alpha\nbravo\nalphabet\n\ncharlie"
	got := LinesContaining(in, "alpha")
	if !reflect.DeepEqual(got, []string{"alpha", "alphabet"}) {
		t.Errorf("LinesContaining = %v", got)
	}
	// No match -> empty, not the whole input.
	if got := LinesContaining(in, "zzz"); len(got) != 0 {
		t.Errorf("LinesContaining(no match) = %v, want none", got)
	}
}

func TestStatusField(t *testing.T) {
	status := "enabled 1\nfailure 2\npid 612\nbacklog_limit 8192\n"
	// Picks the named field's value, not the first line's.
	if got := StatusField(status, "failure"); got != "2" {
		t.Errorf("StatusField(failure) = %q, want 2", got)
	}
	if got := StatusField(status, "pid"); got != "612" {
		t.Errorf("StatusField(pid) = %q, want 612", got)
	}
	// A name that is not present -> empty.
	if got := StatusField(status, "nope"); got != "" {
		t.Errorf("StatusField(absent) = %q, want empty", got)
	}
	// A line that is not exactly "name value" (two fields) is ignored.
	if got := StatusField("enabled yes really\n", "enabled"); got != "" {
		t.Errorf("StatusField(3 fields) = %q, want empty", got)
	}
}

func TestKeys(t *testing.T) {
	got := Keys(map[string]bool{"b": true, "a": true, "c": true})
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("Keys = %v, want sorted", got)
	}
	if got := Keys(map[string]bool{}); len(got) != 0 {
		t.Errorf("Keys(empty) = %v", got)
	}
}

func TestEqual(t *testing.T) {
	if !Equal([]string{"a", "b"}, []string{"a", "b"}) {
		t.Error("Equal(identical) = false")
	}
	// Different length.
	if Equal([]string{"a"}, []string{"a", "b"}) {
		t.Error("Equal(diff len) = true")
	}
	// Same length, differing element.
	if Equal([]string{"a", "b"}, []string{"a", "x"}) {
		t.Error("Equal(diff element) = true")
	}
	if !Equal(nil, nil) {
		t.Error("Equal(nil,nil) = false")
	}
}

// TestAuditExeMoreForms covers the paths the happy-path quoted/hex cases miss:
// a bare unquoted path, an exe that is the last field (no trailing space), and
// hex that is not actually decodable is left as-is.
func TestAuditExeMoreForms(t *testing.T) {
	cases := []struct{ name, line, want string }{
		{"bare unquoted path", `type=SYSCALL pid=2 exe=/bin/busybox comm=x`, "busybox"},
		{"exe is the final field (no trailing space)", `type=SYSCALL pid=2 exe="/usr/sbin/sshd"`, "sshd"},
		{"odd-length hex is not decoded, treated as a name", `type=SYSCALL exe=abc key=k`, "abc"},
	}
	for _, c := range cases {
		if got := AuditExe(c.line); got != c.want {
			t.Errorf("%s: AuditExe = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestAuditIntBoundaries pins the digit scan: it stops at the first non-digit
// (just below '0' and just above '9'), reads a value at end of line, and reads
// a key at the very start of the line.
func TestAuditIntBoundaries(t *testing.T) {
	cases := []struct {
		name, line, key string
		want            int
	}{
		{"value then a char just above '9' (':')", "a pid=12:34", "pid=", 12},
		{"value then a char just below '0' ('/')", "a pid=12/7", "pid=", 12},
		{"value at end of line", "x pid=77", "pid=", 77},
		{"key at the very start of the line", "pid=55 x", "pid=", 55},
		{"all digits consumed", " ppid=100 ", "ppid=", 100},
		{"absent key", "no such thing", "pid=", 0},
		{"leading zeros", "a pid=007", "pid=", 7},
	}
	for _, c := range cases {
		if got := AuditInt(c.line, c.key); got != c.want {
			t.Errorf("%s: AuditInt(%q,%q) = %d, want %d", c.name, c.line, c.key, got, c.want)
		}
	}
}

// TestAuditExeHexEdges exercises isHexStr's digit/letter range checks: an
// even-length all-hex value decodes, and an even-length value with one non-hex
// byte is NOT decoded (left as a literal name).
func TestAuditExeHexEdges(t *testing.T) {
	cases := []struct{ name, line, want string }{
		// 2f62696e2f6c73 = "/bin/ls" -> basename "ls".
		{"lowercase hex decodes", `type=SYSCALL exe=2f62696e2f6c73 key=k`, "ls"},
		// 2F62696E2F6C73 = "/bin/ls" in uppercase hex -> "ls".
		{"uppercase hex decodes", `type=SYSCALL exe=2F62696E2F6C73 key=k`, "ls"},
		// Even length but 'g'/'G' are not hex -> treated as a literal name.
		{"even length, non-hex byte -> literal", `type=SYSCALL exe=6g key=k`, "6g"},
	}
	for _, c := range cases {
		if got := AuditExe(c.line); got != c.want {
			t.Errorf("%s: AuditExe = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestOCSFNilBranches: the finding readers return their zero value on each
// missing-field branch (no evidences, nil process, nil parent) rather than
// panicking or returning a stale value.
func TestOCSFNilBranches(t *testing.T) {
	noEvidence := `{"finding_info":{"analytic":{"name":"r"}}}`
	nilProcess := `{"evidences":[{}]}`
	noParent := `{"evidences":[{"process":{"pid":7}}]}`

	if got := CommandLine(noEvidence); got != "?" {
		t.Errorf("CommandLine(no evidence) = %q, want ?", got)
	}
	if got := CommandLine(nilProcess); got != "?" {
		t.Errorf("CommandLine(nil process) = %q, want ?", got)
	}
	if pid, ppid := AlertProcess(noEvidence); pid != 0 || ppid != 0 {
		t.Errorf("AlertProcess(no evidence) = (%d,%d)", pid, ppid)
	}
	if pid, ppid := AlertProcess(nilProcess); pid != 0 || ppid != 0 {
		t.Errorf("AlertProcess(nil process) = (%d,%d)", pid, ppid)
	}
	// Parent missing: pid is read, ppid stays 0.
	if pid, ppid := AlertProcess(noParent); pid != 7 || ppid != 0 {
		t.Errorf("AlertProcess(no parent) = (%d,%d), want (7,0)", pid, ppid)
	}
	// Severity / AnalyticName on junk never panic.
	if Severity("not json") != 0 {
		t.Error("Severity(junk) != 0")
	}
}
