package oracle

import (
	"sort"
	"testing"
)

// verdictMap flattens a Reconcile result to id->status, collecting every
// `protocol` error's detail separately (there can be more than one).
func verdictMap(vs []Verdict) (map[string]Status, []string) {
	m := map[string]Status{}
	var proto []string
	for _, v := range vs {
		if v.ID == reservedProtocol {
			proto = append(proto, v.Detail)
			continue
		}
		m[v.ID] = v.Status
	}
	sort.Strings(proto)
	return m, proto
}

func res(id string, s Status) Record { return Record{Schema: Protocol, T: "result", ID: id, Status: s} }

func TestReconcile(t *testing.T) {
	t.Run("5.1 unreported -> error", func(t *testing.T) {
		vs := Reconcile([]string{"a", "b"}, []Record{res("a", Pass)}, 0, true, nil)
		m, _ := verdictMap(vs)
		if m["a"] != Pass || m["b"] != Error {
			t.Fatalf("got %v, want a=pass b=error", m)
		}
	})

	t.Run("6.1.3 engine never infers pass from absence", func(t *testing.T) {
		vs := Reconcile([]string{"a"}, nil, 0, true, nil)
		m, _ := verdictMap(vs)
		if m["a"] != Error {
			t.Fatalf("unreported id became %q, must be error (never pass)", m["a"])
		}
	})

	t.Run("5.2 unplanned -> protocol error, discarded", func(t *testing.T) {
		vs := Reconcile([]string{"a"}, []Record{res("a", Pass), res("stray", Pass)}, 0, true, nil)
		m, proto := verdictMap(vs)
		if m["a"] != Pass {
			t.Fatalf("a=%q, want pass", m["a"])
		}
		if _, ok := m["stray"]; ok {
			t.Fatalf("unplanned id was not discarded: %v", m)
		}
		if len(proto) != 1 {
			t.Fatalf("want one protocol error, got %v", proto)
		}
	})

	t.Run("5.4 crash: unreported get exit detail, reported kept", func(t *testing.T) {
		vs := Reconcile([]string{"a", "b"}, []Record{res("a", Pass)}, 1, true, nil)
		m, _ := verdictMap(vs)
		if m["a"] != Pass {
			t.Fatalf("a must keep its result across a crash, got %q", m["a"])
		}
		if m["b"] != Error {
			t.Fatalf("b=%q, want error", m["b"])
		}
		for _, v := range vs {
			if v.ID == "b" && v.Detail != "entrypoint exited 1" {
				t.Fatalf("b detail = %q, want exit detail", v.Detail)
			}
		}
	})

	t.Run("5.4 no done -> no completion", func(t *testing.T) {
		vs := Reconcile([]string{"a"}, nil, 0, false, nil)
		for _, v := range vs {
			if v.ID == "a" && (v.Status != Error || v.Detail != "no completion") {
				t.Fatalf("missing done: got %q/%q, want error/'no completion'", v.Status, v.Detail)
			}
		}
	})

	t.Run("5.5 duplicate -> first stands, protocol error", func(t *testing.T) {
		vs := Reconcile([]string{"a"}, []Record{res("a", Pass), res("a", Fail)}, 0, true, nil)
		m, proto := verdictMap(vs)
		if m["a"] != Pass {
			t.Fatalf("first result must stand, got %q", m["a"])
		}
		if len(proto) != 1 {
			t.Fatalf("want one protocol error for the duplicate, got %v", proto)
		}
	})

	t.Run("5.3 empty plan + no records -> protocol error", func(t *testing.T) {
		vs := Reconcile(nil, nil, 0, true, nil)
		_, proto := verdictMap(vs)
		if len(proto) != 1 {
			t.Fatalf("empty run must fail, got %v", proto)
		}
	})

	t.Run("bad status -> protocol error, not a silent pass", func(t *testing.T) {
		vs := Reconcile([]string{"a"}, []Record{{Schema: Protocol, T: "result", ID: "a", Status: "weird"}}, 0, true, nil)
		m, proto := verdictMap(vs)
		if m["a"] != Error {
			t.Fatalf("a with a bad-status record became %q, want error (unreported)", m["a"])
		}
		if len(proto) != 1 {
			t.Fatalf("want one protocol error for the bad status, got %v", proto)
		}
	})

	t.Run("malformed line -> protocol error", func(t *testing.T) {
		vs := Reconcile([]string{"a"}, []Record{res("a", Pass)}, 0, true, []int{7})
		_, proto := verdictMap(vs)
		if len(proto) != 1 {
			t.Fatalf("want one protocol error for the malformed line, got %v", proto)
		}
	})

	t.Run("info and done records are not verdicts", func(t *testing.T) {
		recs := []Record{
			{Schema: Protocol, T: "info", K: "exposure", V: "2.1 OK"},
			res("a", Pass),
			{Schema: Protocol, T: "done", Exit: 0},
		}
		vs := Reconcile([]string{"a"}, recs, 0, true, nil)
		m, proto := verdictMap(vs)
		if m["a"] != Pass || len(proto) != 0 {
			t.Fatalf("info/done leaked into verdicts: %v proto=%v", m, proto)
		}
	})
}

func TestParseRecords(t *testing.T) {
	t.Run("framed: prefix required, log lines ignored", func(t *testing.T) {
		lines := []string{
			"just some log output",
			`mayfly:{"schema":"mayfly/1","t":"result","id":"a","status":"pass"}`,
			"  mayfly:{\"schema\":\"mayfly/1\",\"t\":\"done\",\"exit\":0}", // leading WSP ok
		}
		recs, bad := ParseRecords(lines, true)
		if len(recs) != 2 || len(bad) != 0 {
			t.Fatalf("got %d recs, %d bad; want 2,0", len(recs), len(bad))
		}
	})

	t.Run("framed but unparseable -> bad line", func(t *testing.T) {
		recs, bad := ParseRecords([]string{`mayfly:{not json}`}, true)
		if len(recs) != 0 || len(bad) != 1 || bad[0] != 1 {
			t.Fatalf("got %v recs, bad=%v; want 0 recs, [1]", recs, bad)
		}
	})

	t.Run("unknown schema ignored", func(t *testing.T) {
		recs, bad := ParseRecords([]string{`mayfly:{"schema":"mayfly/2","t":"result","id":"a","status":"pass"}`}, true)
		if len(recs) != 0 || len(bad) != 0 {
			t.Fatalf("future schema must be ignored, got %v bad=%v", recs, bad)
		}
	})

	t.Run("file transport: every non-blank line is a record", func(t *testing.T) {
		lines := []string{
			`{"schema":"mayfly/1","t":"result","id":"a","status":"pass"}`,
			"",
			`{"schema":"mayfly/1","t":"result","id":"b","status":"fail"}`,
		}
		recs, bad := ParseRecords(lines, false)
		if len(recs) != 2 || len(bad) != 0 {
			t.Fatalf("got %d recs, %d bad; want 2,0", len(recs), len(bad))
		}
	})
}

func FuzzParseRecords(f *testing.F) {
	f.Add("mayfly:{\"schema\":\"mayfly/1\",\"t\":\"result\",\"id\":\"a\",\"status\":\"pass\"}")
	f.Add("mayfly:{bad")
	f.Add("ordinary log line")
	f.Add("")
	f.Fuzz(func(t *testing.T, line string) {
		// Must never panic on untrusted guest output, on either transport.
		_, _ = ParseRecords([]string{line}, true)
		_, _ = ParseRecords([]string{line}, false)
	})
}
