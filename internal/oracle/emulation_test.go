package oracle

import "testing"

func TestEmulationRan(t *testing.T) {
	cases := []struct {
		name        string
		exit        int
		subtreeSize int
		wantRan     bool
	}{
		{"clean exit, children ran", 0, 4, true},
		{"clean exit, launcher only", 0, 1, true}, // ran, just fired nothing worth a process
		{"permission denied (126), empty", 126, 1, false},
		{"not found (127), empty", 127, 1, false},
		{"126 even with a subtree", 126, 3, false}, // launcher itself could not exec
		{"non-zero, nothing spawned", 1, 1, false}, // failed before forking
		{"non-zero but children ran", 1, 5, true},  // a failing attack command, judged normally
		{"timeout sentinel (-1), empty", -1, 1, false},
		{"timeout sentinel (-1), children ran", -1, 6, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ran, why := EmulationRan(c.exit, c.subtreeSize)
			if ran != c.wantRan {
				t.Errorf("EmulationRan(%d, %d) ran=%v, want %v (why=%q)", c.exit, c.subtreeSize, ran, c.wantRan, why)
			}
			if !ran && why == "" {
				t.Errorf("EmulationRan(%d, %d) returned ran=false with empty why", c.exit, c.subtreeSize)
			}
			if ran && why != "" {
				t.Errorf("EmulationRan(%d, %d) returned ran=true with why=%q", c.exit, c.subtreeSize, why)
			}
		})
	}
}
