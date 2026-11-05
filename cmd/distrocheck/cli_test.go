package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conor-sometimes/mayfly/internal/oracle"
)

// withCLIEnv runs fn with a fresh results file and the given plan, returning the
// lines written to $MAYFLY_RESULTS.
func withCLIEnv(t *testing.T, plan string, fn func()) []string {
	t.Helper()
	dir := t.TempDir()
	res := filepath.Join(dir, "results.jsonl")
	t.Setenv("MAYFLY_PLAN", plan)
	t.Setenv("MAYFLY_RESULTS", res)
	fn()
	data, err := os.ReadFile(res)
	if err != nil {
		return nil
	}
	return oracle.NonEmpty(string(data))
}

func TestRunCLI(t *testing.T) {
	t.Run("pass writes one result, exit 0", func(t *testing.T) {
		var code int
		lines := withCLIEnv(t, "a b", func() { code = runCLI([]string{"pass", "a", "served", "ok"}) })
		if code != 0 || len(lines) != 1 {
			t.Fatalf("code=%d lines=%v, want 0 and one record", code, lines)
		}
		recs, bad := oracle.ParseRecords(lines, false)
		if len(bad) != 0 || len(recs) != 1 || recs[0].Status != oracle.Pass || recs[0].ID != "a" || recs[0].Detail != "served ok" {
			t.Fatalf("bad record: %+v (bad=%v)", recs, bad)
		}
	})

	t.Run("unplanned id: protocol error + exit 2", func(t *testing.T) {
		var code int
		lines := withCLIEnv(t, "a", func() { code = runCLI([]string{"pass", "b"}) })
		if code != 2 {
			t.Fatalf("code=%d, want 2", code)
		}
		recs, _ := oracle.ParseRecords(lines, false)
		if len(recs) != 1 || recs[0].ID != oracle.ProtocolID || recs[0].Status != oracle.Error {
			t.Fatalf("want one protocol error record, got %+v", recs)
		}
	})

	t.Run("skip without reason: usage error, nothing written", func(t *testing.T) {
		var code int
		lines := withCLIEnv(t, "a", func() { code = runCLI([]string{"skip", "a"}) })
		if code != 2 || len(lines) != 0 {
			t.Fatalf("code=%d lines=%v, want 2 and nothing written", code, lines)
		}
	})

	t.Run("error with reason: one record", func(t *testing.T) {
		var code int
		lines := withCLIEnv(t, "a", func() { code = runCLI([]string{"error", "a", "auditd", "not", "running"}) })
		recs, _ := oracle.ParseRecords(lines, false)
		if code != 0 || len(recs) != 1 || recs[0].Status != oracle.Error || recs[0].Detail != "auditd not running" {
			t.Fatalf("code=%d recs=%+v", code, recs)
		}
	})

	t.Run("info writes an info record", func(t *testing.T) {
		var code int
		lines := withCLIEnv(t, "a", func() { code = runCLI([]string{"info", "exposure/x", "2.1", "OK"}) })
		recs, _ := oracle.ParseRecords(lines, false)
		if code != 0 || len(recs) != 1 || recs[0].T != "info" || recs[0].K != "exposure/x" || recs[0].V != "2.1 OK" {
			t.Fatalf("code=%d recs=%+v", code, recs)
		}
	})

	t.Run("reserved id rejected", func(t *testing.T) {
		var code int
		withCLIEnv(t, "test", func() { code = runCLI([]string{"pass", "test"}) })
		if code != 2 {
			t.Fatalf("reporting on reserved id test must be refused, code=%d", code)
		}
	})

	t.Run("malformed id rejected", func(t *testing.T) {
		var code int
		withCLIEnv(t, "a", func() { code = runCLI([]string{"pass", "bad id!"}) })
		if code != 2 {
			t.Fatalf("malformed id must be refused, code=%d", code)
		}
	})

	t.Run("missing MAYFLY_RESULTS is a usage error", func(t *testing.T) {
		t.Setenv("MAYFLY_PLAN", "a")
		os.Unsetenv("MAYFLY_RESULTS")
		if code := runCLI([]string{"pass", "a"}); code == 0 {
			t.Fatalf("want non-zero without MAYFLY_RESULTS, got 0")
		}
	})
}

func TestIsCLIVerb(t *testing.T) {
	for _, v := range []string{"pass", "fail", "skip", "error", "info"} {
		if !isCLIVerb(v) {
			t.Errorf("%q should be a CLI verb", v)
		}
	}
	for _, v := range []string{"-root", "run", "detect", ""} {
		if isCLIVerb(v) {
			t.Errorf("%q should not be a CLI verb (yet)", v)
		}
	}
}
