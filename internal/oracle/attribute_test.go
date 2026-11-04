package oracle

import (
	"sort"
	"testing"
)

// TestDescendants builds a process tree and checks the launcher subtree is
// exactly the launcher plus everything under it, and nothing that runs beside
// it. This is the core of identity attribution: a periodic daemon that fires
// inside a check's window must NOT be in that check's subtree.
func TestDescendants(t *testing.T) {
	// A check launcher (100) runs: bash (101) -> its emulation's touch (102)
	// and a grandchild rm (103). Beside it, cron (900) spawns a dash (901) that
	// reads a credential file -- the classic bleed. systemd (1) is the root.
	ppidOf := map[int]int{
		100: 1,   // runuser (the launcher)
		101: 100, // bash running the emulation script
		102: 101, // touch
		103: 102, // rm (grandchild)
		900: 1,   // cron, beside the check
		901: 900, // dash spawned by cron
	}
	got := Descendants(ppidOf, 100)

	want := []int{100, 101, 102, 103}
	keys := make([]int, 0, len(got))
	for p := range got {
		keys = append(keys, p)
	}
	sort.Ints(keys)
	if len(keys) != len(want) {
		t.Fatalf("subtree = %v, want %v", keys, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("subtree missing %d", w)
		}
	}
	for _, bled := range []int{900, 901, 1} {
		if got[bled] {
			t.Errorf("subtree wrongly includes %d (a process beside the check)", bled)
		}
	}
}

// TestInSubtree is the attribution decision: a finding is the check's iff its
// evidence process (by pid, or by ppid for a short-lived exec) is in the
// subtree. The cron-bled finding is rejected by identity, no timing involved.
func TestInSubtree(t *testing.T) {
	subtree := map[int]bool{100: true, 101: true, 102: true, 103: true}
	cases := []struct {
		name      string
		pid, ppid int
		want      bool
	}{
		{"check exec by pid", 102, 101, true},
		{"check grandchild by pid", 103, 102, true},
		{"the launcher itself", 100, 1, true},
		{"short-lived check exec, only ppid resolves", 0, 101, true},
		{"cron-bled finding (different subtree)", 901, 900, false},
		{"cron itself", 900, 1, false},
		{"unrecorded process (pid and ppid 0)", 0, 0, false},
		{"straggler whose pid happens to be high but ppid is cron", 999, 900, false},
	}
	for _, c := range cases {
		if got := InSubtree(subtree, c.pid, c.ppid); got != c.want {
			t.Errorf("%s: InSubtree(pid=%d, ppid=%d) = %v, want %v", c.name, c.pid, c.ppid, got, c.want)
		}
	}
}

// TestAttributionIsOrderIndependent is the whole point: the same audit records
// in any flush order produce the same attribution, because it is by parentage,
// not by when the record appeared.
func TestAttributionIsOrderIndependent(t *testing.T) {
	inOrder := map[int]int{100: 1, 101: 100, 102: 101, 103: 102}
	shuffled := map[int]int{103: 102, 101: 100, 102: 101, 100: 1}

	a := Descendants(inOrder, 100)
	b := Descendants(shuffled, 100)
	if len(a) != len(b) {
		t.Fatalf("subtree size differs by order: %d vs %d", len(a), len(b))
	}
	for p := range a {
		if !b[p] {
			t.Errorf("subtree differs by order at %d", p)
		}
	}
}

// TestAuditInt pins the pid/ppid overlap trap: "pid=" occurs inside "ppid=", so
// a naive search misreads the parent as the process. Space-anchoring fixes it.
func TestAuditInt(t *testing.T) {
	line := `type=SYSCALL msg=audit(1700000000.1:9): arch=c000003e syscall=59 success=yes ppid=1000 pid=1001 auid=1000 uid=1000 comm="touch" exe="/usr/bin/touch"`
	if got := AuditInt(line, "pid="); got != 1001 {
		t.Errorf(`pid= = %d, want 1001 (must not read the ppid value)`, got)
	}
	if got := AuditInt(line, "ppid="); got != 1000 {
		t.Errorf(`ppid= = %d, want 1000`, got)
	}
	if got := AuditInt(line, "uid="); got != 1000 {
		t.Errorf(`uid= = %d, want 1000`, got)
	}
	if got := AuditInt(line, "notthere="); got != 0 {
		t.Errorf(`missing key = %d, want 0`, got)
	}
}

// TestAuditExe covers both ways auditd writes exe=: quoted, and hex-encoded.
// The hex here is /usr/bin/touch.
func TestAuditExe(t *testing.T) {
	cases := []struct{ name, line, want string }{
		{"quoted path", `type=SYSCALL msg=audit(1:1): pid=2 exe="/usr/bin/touch" key=k`, "touch"},
		{"hex-encoded path", `type=SYSCALL msg=audit(1:1): pid=2 exe=2f7573722f62696e2f746f756368 key=k`, "touch"},
		{"null exe", `type=SYSCALL msg=audit(1:1): pid=2 exe=(null) key=k`, ""},
		{"no exe field", `type=PATH msg=audit(1:1): item=0 name="/tmp/x"`, ""},
		{"root-level name", `type=SYSCALL msg=audit(1:1): pid=2 exe="/kmod" key=k`, "kmod"},
	}
	for _, c := range cases {
		if got := AuditExe(c.line); got != c.want {
			t.Errorf("%s: AuditExe = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestAuditProcTree builds the pid -> ppid map from a window of audit records.
// Only SYSCALL lines contribute; EXECVE/PATH lines are ignored.
func TestAuditProcTree(t *testing.T) {
	log := `type=SYSCALL msg=audit(1700000000.1:1): syscall=59 ppid=100 pid=101 comm="bash" exe="/usr/bin/bash"
type=EXECVE msg=audit(1700000000.1:1): argc=3 a0="bash" a1="-c" a2="touch ..."
type=SYSCALL msg=audit(1700000000.2:2): syscall=59 ppid=101 pid=102 comm="touch" exe="/usr/bin/touch"
type=PATH msg=audit(1700000000.2:2): item=0 name="/tmp/x" nametype=CREATE
type=SYSCALL msg=audit(1700000000.3:3): syscall=59 ppid=900 pid=901 comm="dash" exe="/usr/bin/dash"
`
	tree := AuditProcTree(log)
	want := map[int]int{101: 100, 102: 101, 901: 900}
	if len(tree) != len(want) {
		t.Fatalf("tree = %v, want %v", tree, want)
	}
	for pid, ppid := range want {
		if tree[pid] != ppid {
			t.Errorf("tree[%d] = %d, want %d", pid, tree[pid], ppid)
		}
	}
}

// TestAttributionEndToEnd wires the parsers to the core: given a window's audit
// records and a launcher pid, the findings whose evidence process is in the
// launcher subtree are the check's, and a cron-bled finding is not.
func TestAttributionEndToEnd(t *testing.T) {
	log := `type=SYSCALL msg=audit(1:1): syscall=59 ppid=1 pid=100 comm="runuser"
type=SYSCALL msg=audit(1:2): syscall=59 ppid=100 pid=101 comm="bash"
type=SYSCALL msg=audit(1:3): syscall=59 ppid=101 pid=102 comm="touch"
type=SYSCALL msg=audit(1:4): syscall=59 ppid=1 pid=900 comm="cron"
type=SYSCALL msg=audit(1:5): syscall=59 ppid=900 pid=901 comm="dash"
`
	subtree := Descendants(AuditProcTree(log), 100)

	familyFinding := `{"class_uid":2004,"evidences":[{"process":{"pid":102,"parent_process":{"pid":101}}}]}`
	cronFinding := `{"class_uid":2004,"evidences":[{"process":{"pid":901,"parent_process":{"pid":900}}}]}`

	pid, ppid := AlertProcess(familyFinding)
	if !InSubtree(subtree, pid, ppid) {
		t.Errorf("check finding (pid=%d ppid=%d) not attributed to the check", pid, ppid)
	}
	pid, ppid = AlertProcess(cronFinding)
	if InSubtree(subtree, pid, ppid) {
		t.Errorf("cron-bled finding (pid=%d ppid=%d) wrongly attributed to the check", pid, ppid)
	}
}

// TestAlertProcessMissing: a finding with no process evidence yields (0,0),
// which InSubtree never matches, so a malformed finding attributes to nothing.
func TestAlertProcessMissing(t *testing.T) {
	if pid, ppid := AlertProcess(`{"class_uid":2004}`); pid != 0 || ppid != 0 {
		t.Errorf("no-evidence finding = (%d,%d), want (0,0)", pid, ppid)
	}
	if pid, ppid := AlertProcess(`not json`); pid != 0 || ppid != 0 {
		t.Errorf("bad json = (%d,%d), want (0,0)", pid, ppid)
	}
}
