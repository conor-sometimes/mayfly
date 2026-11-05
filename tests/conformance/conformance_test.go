// Package conformance holds the result-protocol conformance suite (docs/protocol.md
// section 10). It is run by the `gotest` flake check (go test ./...). The pure
// engine obligations are asserted here against internal/oracle; the live
// consumer and CLI obligations are exercised by the examples (examples/protocol
// is the demonstration) and by the per-package tests in cmd/distrocheck.
package conformance

import (
	"testing"

	"github.com/conor-sometimes/mayfly/internal/oracle"
)

// TestProtocolExampleGrid mirrors examples/protocol: the entrypoint reports a and
// c, reports an unplanned id x (so the CLI writes a `protocol` error, spec 9.2),
// and never reports b. The engine MUST render a=pass, c=pass, b=error (unreported,
// 5.1) and protocol=error (unplanned, 5.2) -- exactly two ERROR cells, the two
// kinds of engine-synthesised error (E4, 6.1.3).
func TestProtocolExampleGrid(t *testing.T) {
	plan := []string{"a", "b", "c"}
	recs := []oracle.Record{
		oracle.ResultRecord("a", oracle.Pass, "reported cleanly"),
		oracle.ResultRecord("c", oracle.Pass, ""),
		oracle.ResultRecord(oracle.ProtocolID, oracle.Error, "result for unplanned id x"),
	}
	got := map[string]oracle.Status{}
	errCells := 0
	for _, v := range oracle.Reconcile(plan, recs, 0, true, nil) {
		got[v.ID] = v.Status
		if v.Status == oracle.Error {
			errCells++
		}
	}
	if got["a"] != oracle.Pass || got["c"] != oracle.Pass {
		t.Fatalf("a and c must be pass, got %v", got)
	}
	if got["b"] != oracle.Error {
		t.Fatalf("unreported b must be error (5.1), got %q", got["b"])
	}
	if got[oracle.ProtocolID] != oracle.Error {
		t.Fatalf("unplanned id must raise a protocol error (5.2), got %q", got[oracle.ProtocolID])
	}
	if errCells != 2 {
		t.Fatalf("want exactly two ERROR cells (b + protocol), got %d: %v", errCells, got)
	}
}

// TestNeverInfersPass is E6 / 6.1.3: a pass is only ever a reported pass; absence
// is ERROR, never PASS.
func TestNeverInfersPass(t *testing.T) {
	for _, v := range oracle.Reconcile([]string{"x"}, nil, 0, true, nil) {
		if v.ID == "x" && v.Status == oracle.Pass {
			t.Fatal("engine inferred a pass from an absent result")
		}
	}
}

// TestCrashKeepsReportedDropsRest is E4 / 5.4: a non-zero entrypoint exit makes
// unreported ids ERROR, but a reported result is a complete fact and stands.
func TestCrashKeepsReportedDropsRest(t *testing.T) {
	recs := []oracle.Record{oracle.ResultRecord("done", oracle.Pass, "")}
	got := map[string]oracle.Status{}
	for _, v := range oracle.Reconcile([]string{"done", "late"}, recs, 1, true, nil) {
		got[v.ID] = v.Status
	}
	if got["done"] != oracle.Pass {
		t.Fatalf("a reported result must survive a crash, got %q", got["done"])
	}
	if got["late"] != oracle.Error {
		t.Fatalf("an unreported id after a crash must be error, got %q", got["late"])
	}
}

// TestUnknownSchemaIgnored is E3 / 2.2: a record from a future schema is ignored,
// not misread; the plan rules then turn the gap red.
func TestUnknownSchemaIgnored(t *testing.T) {
	recs, bad := oracle.ParseRecords([]string{`mayfly:{"schema":"mayfly/99","t":"result","id":"a","status":"pass"}`}, true)
	if len(recs) != 0 || len(bad) != 0 {
		t.Fatalf("a future-schema record must be ignored, got recs=%v bad=%v", recs, bad)
	}
}

// TestIDGrammar is H2 / 3.1: id validation at the call site.
func TestIDGrammar(t *testing.T) {
	for _, ok := range []string{"a", "sysmon/S1", "a.b-c_d", "A1", "1abc"} { // ALNUM first (digits allowed, spec 3.1)
		if !oracle.ValidID(ok) {
			t.Errorf("%q should be a valid id", ok)
		}
	}
	for _, bad := range []string{"", "has space", "bad!", "/leading", "_leading"} {
		if oracle.ValidID(bad) {
			t.Errorf("%q should be an invalid id", bad)
		}
	}
	for _, r := range []string{"protocol", "test"} {
		if !oracle.Reserved(r) {
			t.Errorf("%q should be reserved", r)
		}
	}
}
