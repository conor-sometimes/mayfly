package oracle

import "fmt"

// EmulationRan reports whether a detect check's emulation actually executed,
// from its launcher's exit status and the size of the process subtree captured
// for it. Descendants always includes the launcher itself, so a subtree of size
// 1 means nothing below the launcher ran.
//
// It is deliberately conservative: it calls a run "did not happen" only when the
// evidence is unambiguous -- the launcher could not be executed at all (exit
// 126/127), or it failed and spawned no process. A non-zero exit from an
// emulation that DID fork children is a failing attack command, not a harness
// failure, and is left to the normal fired-set comparison.
//
// This is the guard that separates a vacuous pass (nothing ran, so nothing
// fired, so an empty expected set "matches" -> green) from a real one. The
// payload-permissions bug produced exit 126 with an empty subtree and read as
// PASS; with this, that run is a FAIL.
func EmulationRan(exitCode, subtreeSize int) (ran bool, why string) {
	switch {
	case exitCode == 126 || exitCode == 127:
		return false, fmt.Sprintf("launcher exited %d (command not found or not executable)", exitCode)
	case exitCode != 0 && subtreeSize <= 1:
		return false, fmt.Sprintf("launcher exited %d and spawned no process", exitCode)
	default:
		return true, ""
	}
}
