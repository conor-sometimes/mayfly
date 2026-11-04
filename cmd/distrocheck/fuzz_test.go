package main

import "testing"

// FuzzParseStages: a stage script's "<id> PASS|FAIL|SKIP <detail>" output comes
// from whatever the user's check script printed, so it must not panic on any
// bytes, and a recorded result is never both a pass and a skip.
func FuzzParseStages(f *testing.F) {
	f.Add("install PASS\nscan FAIL something\nopt SKIP not applicable\n")
	f.Add("garbage line\n  leading spaces PASS\n")
	f.Add("")
	f.Add("x MAYBE\n") // unknown verdict is dropped
	f.Fuzz(func(t *testing.T, out string) {
		for id, r := range parseStages(out) {
			if r.pass && r.skip {
				t.Fatalf("stage %q recorded as both pass and skip", id)
			}
		}
	})
}
