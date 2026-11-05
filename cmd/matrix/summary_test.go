package main

import (
	"strings"
	"testing"

	"github.com/conor-sometimes/mayfly/internal/report"
)

// TestMatrixTable: the summary grid lists every check column, every distro row,
// and renders each verdict, a not-applicable check as "-", and a no-report
// distro as "ERR".
func TestMatrixTable(t *testing.T) {
	distros := []Distro{{Name: "ubuntu-2404"}, {Name: "fedora-44"}, {Name: "rocky-9"}}
	reports := []report.Report{
		{Distro: "ubuntu-2404", Checks: []report.Check{{ID: "revshell", Status: "pass"}, {ID: "sandbox", Status: "pass"}}},
		{Distro: "fedora-44", Checks: []report.Check{{ID: "revshell", Status: "fail", Detail: "missed"}, {ID: "sandbox", Status: "skip"}}},
		// rocky ran revshell only (sandbox's `when` filtered it out there).
		{Distro: "rocky-9", Checks: []report.Check{{ID: "revshell", Status: "pass"}}},
	}
	got := matrixTable(distros, reports)
	for _, want := range []string{"distro", "revshell", "sandbox", "ubuntu-2404", "fedora-44", "rocky-9", "PASS", "FAIL", "SKIP"} {
		if !strings.Contains(got, want) {
			t.Errorf("table missing %q:\n%s", want, got)
		}
	}
	// rocky's missing sandbox cell is "-", not a verdict.
	rockyLine := ""
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "rocky-9") {
			rockyLine = l
		}
	}
	if !strings.Contains(rockyLine, "-") {
		t.Errorf("rocky row should mark the inapplicable check with '-': %q", rockyLine)
	}
}

// TestMatrixTableNoReport: a distro that returned nothing shows ERR across.
func TestMatrixTableNoReport(t *testing.T) {
	got := matrixTable(
		[]Distro{{Name: "a"}, {Name: "b"}},
		[]report.Report{{Checks: []report.Check{{ID: "x", Status: "pass"}}}, {Checks: nil}},
	)
	if !strings.Contains(got, "ERR") {
		t.Errorf("a no-report distro should show ERR:\n%s", got)
	}
}

// TestMatrixTableSingleCell: one distro, one check is not a matrix - no table.
func TestMatrixTableSingleCell(t *testing.T) {
	if got := matrixTable(
		[]Distro{{Name: "a"}},
		[]report.Report{{Checks: []report.Check{{ID: "x", Status: "pass"}}}},
	); got != "" {
		t.Errorf("a single cell should not render a matrix, got:\n%s", got)
	}
}
