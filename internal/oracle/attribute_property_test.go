package oracle

import (
	"math/rand"
	"sort"
	"testing"
)

// Property tests for identity attribution. The example tests pin specific
// trees; these assert the laws hold over many random ones. Deterministic: each
// iteration seeds its own rand by index, so a failure reproduces.

// randTree builds a random process tree: node i's parent is an earlier node, so
// there are no cycles and every node reaches the root. Returns the pid->ppid map
// (as auditd's SYSCALL records give it), the pids, and the root pid.
func randTree(r *rand.Rand, base int) (ppidOf map[int]int, pids []int, root int) {
	n := 2 + r.Intn(30)
	ppidOf = make(map[int]int, n)
	pids = make([]int, n)
	for i := range pids {
		pids[i] = base + i
	}
	ppidOf[pids[0]] = 1 // root's parent is systemd, outside the forest
	for i := 1; i < n; i++ {
		ppidOf[pids[i]] = pids[r.Intn(i)]
	}
	return ppidOf, pids, pids[0]
}

// reach is the ground-truth subtree: the launcher plus every node whose parent
// chain reaches it, computed independently of Descendants.
func reach(ppidOf map[int]int, launcher int) map[int]bool {
	children := map[int][]int{}
	for pid, ppid := range ppidOf {
		children[ppid] = append(children[ppid], pid)
	}
	seen := map[int]bool{launcher: true}
	q := []int{launcher}
	for len(q) > 0 {
		p := q[0]
		q = q[1:]
		for _, c := range children[p] {
			if !seen[c] {
				seen[c] = true
				q = append(q, c)
			}
		}
	}
	return seen
}

func sameSet(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// TestPropDescendantsIsReachability: over many random trees and launchers,
// Descendants equals a ground-truth BFS, and attribution by pid (InSubtree with
// ppid 0) agrees with reachability for every node.
func TestPropDescendantsIsReachability(t *testing.T) {
	for i := 0; i < 2000; i++ {
		r := rand.New(rand.NewSource(int64(i)))
		ppidOf, pids, _ := randTree(r, 100)
		launcher := pids[r.Intn(len(pids))]

		subtree := Descendants(ppidOf, launcher)
		truth := reach(ppidOf, launcher)
		if !sameSet(subtree, truth) {
			t.Fatalf("iter %d: Descendants != reachability for launcher %d\n got %v\nwant %v", i, launcher, keysOf(subtree), keysOf(truth))
		}
		for _, p := range pids {
			if got := InSubtree(subtree, p, 0); got != truth[p] {
				t.Fatalf("iter %d: InSubtree(pid=%d) = %v, reachable = %v", i, p, got, truth[p])
			}
		}
	}
}

// TestPropDisjointSubtreesNeverCross is the whole reason identity attribution
// replaced the wall-clock method: two subtrees running side by side, in one
// audit window, never attribute each other's findings. Each tree gets its own
// pid range, so a finding from one can never be in the other's subtree - no
// timing, no settle, no masking.
func TestPropDisjointSubtreesNeverCross(t *testing.T) {
	for i := 0; i < 2000; i++ {
		r := rand.New(rand.NewSource(int64(i) + 1_000_000))
		treeA, pidsA, rootA := randTree(r, 100) // the check's launcher subtree
		treeB, pidsB, _ := randTree(r, 100_000) // a daemon beside it (cron, fwupd)

		// One audit window holds both forests (ranges are disjoint by construction).
		combined := map[int]int{}
		for k, v := range treeA {
			combined[k] = v
		}
		for k, v := range treeB {
			combined[k] = v
		}

		subtreeA := Descendants(combined, rootA)

		// Every node of A's real subtree attributes to A.
		truthA := reach(treeA, rootA)
		for _, p := range pidsA {
			if truthA[p] && !InSubtree(subtreeA, p, treeA[p]) {
				t.Fatalf("iter %d: A's own process %d not attributed to A", i, p)
			}
		}
		// No node of B ever attributes to A, by pid or by its (B-range) ppid.
		for _, p := range pidsB {
			if InSubtree(subtreeA, p, treeB[p]) {
				t.Fatalf("iter %d: B's process %d (ppid %d) wrongly attributed to A", i, p, treeB[p])
			}
		}
	}
}

func keysOf(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
