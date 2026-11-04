package oracle

// Identity-based attribution for a detect check.
//
// The old method scored a check by diffing the shared finding sink across a
// wall-clock window: run the emulation, scan, and blame it for any finding that
// appeared since the last scan. auditd flushes records asynchronously and can
// complete them out of order, so one emulation's exec bleeds into the next
// one's window. Sleeps, a quiescence drain and daemon-masking all just lowered
// that probability; they could not remove it.
//
// This attributes by IDENTITY instead. Every emulation runs under its own
// launcher process (runuser -> bash). Every exec auditd records carries pid and
// ppid, and every finding's evidence carries the triggering process's pid and
// its parent's pid. So a finding belongs to a check if and only if its process
// is in that check's launcher subtree. A periodic daemon (cron, fwupd) runs
// under a different subtree and is simply not attributed - no masking, no
// settle, no drain. Async flush cannot change a process's parentage.

// Descendants returns the launcher and every process whose parent chain reaches
// it, from a pid -> ppid map built from the window's audit SYSCALL records. The
// launcher itself is included so a direct child of it attributes.
func Descendants(ppidOf map[int]int, launcher int) map[int]bool {
	children := make(map[int][]int, len(ppidOf))
	for pid, ppid := range ppidOf {
		children[ppid] = append(children[ppid], pid)
	}
	seen := map[int]bool{launcher: true}
	stack := []int{launcher}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range children[p] {
			if !seen[c] {
				seen[c] = true
				stack = append(stack, c)
			}
		}
	}
	return seen
}

// InSubtree reports whether a finding whose evidence process has the given pid
// and ppid belongs to the launcher subtree. The pid is checked first; ppid is
// the fallback for a short-lived exec that had already exited (so only its
// parent is still resolvable in the tree). A pid of 0 means "not recorded" and
// never matches.
func InSubtree(subtree map[int]bool, pid, ppid int) bool {
	if pid > 0 && subtree[pid] {
		return true
	}
	if ppid > 0 && subtree[ppid] {
		return true
	}
	return false
}
