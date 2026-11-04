package main

import (
	"strings"
	"testing"
)

// FuzzLastReport: the runner scrapes the per-distro report out of the guest's
// serial console, which is untrusted (a wedged or hostile guest can emit any
// bytes, including a half-written or enormous marker line). Scraping must never
// panic, and must only report ok when it actually decoded a report after the
// marker.
func FuzzLastReport(f *testing.F) {
	f.Add("boot noise\nMATRIX-REPORT:{\"distro\":\"ubuntu\",\"checks\":[]}\ndone\n")
	f.Add("MATRIX-REPORT:not json\n")
	f.Add("MATRIX-REPORT:")                        // marker, no payload
	f.Add(strings.Repeat("MATRIX-REPORT:{}\n", 3)) // several markers, last wins
	f.Add("no marker here at all")
	f.Add("")
	f.Fuzz(func(t *testing.T, out string) {
		rep, ok := lastReport(out)
		if ok && !strings.Contains(out, reportMarker) {
			t.Fatalf("lastReport ok without a marker present")
		}
		_ = rep
	})
}
