package oracle

// The result protocol's pure core (docs/protocol.md, mayfly/1): parse the
// records a run produced, then reconcile them against the plan into exactly one
// verdict per planned id. No I/O, no clock -- the guest-side and host-side
// engines both call this over whatever records they hold (spec 5.6), so a
// transport that drops records cannot turn a red cell green.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Protocol is the one wire-schema version this build speaks (spec 2). A record
// stamped with another schema is ignored (spec 2.2); the plan rules then turn
// the resulting gap red, so skipping is safe.
const Protocol = "mayfly/1"

// Status is a reported outcome (spec 6.1). xfail is a plan modifier, not a
// status, so it is absent here.
type Status string

const (
	Pass  Status = "pass"
	Fail  Status = "fail"
	Skip  Status = "skip"
	Error Status = "error"
)

func (s Status) valid() bool { return s == Pass || s == Fail || s == Skip || s == Error }

// reservedProtocol is the id the engine synthesises errors about the run onto
// (spec 3.2, 5.2, 5.5).
const reservedProtocol = "protocol"

// Record is a parsed protocol record (spec 7.2-7.4). The three kinds share one
// struct; only the members for a kind's `t` are meaningful.
type Record struct {
	Schema string `json:"schema"`
	T      string `json:"t"` // "result" | "info" | "done"
	ID     string `json:"id,omitempty"`
	Status Status `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
	K      string `json:"k,omitempty"`
	V      string `json:"v,omitempty"`
	Exit   int    `json:"exit,omitempty"`
}

// Verdict is one rendered cell: the final status for an id, after reconciliation.
type Verdict struct {
	ID     string
	Status Status // pass|fail|skip|error (xfail/xpass are added by the xfail layer, deferred)
	Detail string
}

// Fails reports whether a verdict makes the run fail (spec 6.3: FAIL or ERROR).
func (v Verdict) Fails() bool { return v.Status == Fail || v.Status == Error }

const framePrefix = "mayfly:"

// ParseRecords reads the record lines a run produced (framed stdout lines per
// spec 7.1.2, or raw jsonl file lines). It returns the well-formed records in
// order, plus one protocol-error message per line that is framed but does not
// parse (spec 7.1.2) -- the caller turns those into `protocol` ERROR verdicts.
// A line without the frame prefix is not a record and is ignored. framed selects
// the transport: true requires the `mayfly:` prefix (stdout), false treats every
// non-blank line as a record (results file).
func ParseRecords(lines []string, framed bool) (recs []Record, badLines []int) {
	for i, line := range lines {
		s := strings.TrimLeft(line, " \t")
		if framed {
			if !strings.HasPrefix(s, framePrefix) {
				continue // not a record (ordinary log output)
			}
			s = strings.TrimPrefix(s, framePrefix)
		} else if strings.TrimSpace(s) == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(s), &r); err != nil {
			badLines = append(badLines, i+1)
			continue
		}
		if r.Schema != Protocol {
			continue // unknown schema: ignore (spec 2.2)
		}
		recs = append(recs, r)
	}
	return recs, badLines
}

// Reconcile derives exactly one verdict per planned id from the records a run
// produced, applying rules 5.1-5.5 in order (spec 5). plan is this distro's
// applicable id list (when-filtering already done, spec 4.3); exit is the
// entrypoint's exit status; done is whether a `done` record was seen (spec 7.4).
// badLines are the line numbers ParseRecords rejected. It also returns `protocol`
// ERROR verdicts for stray or duplicate records and malformed lines (5.2, 5.5,
// 7.1.2). Engine-phase failure before the entrypoint (5.7) is the caller's: it
// does not call Reconcile, it marks every id ERROR itself.
func Reconcile(plan []string, recs []Record, exit int, done bool, badLines []int) []Verdict {
	planned := make(map[string]bool, len(plan))
	for _, id := range plan {
		planned[id] = true
	}

	results := map[string]Record{}
	var protoErrs []Verdict
	for _, r := range recs {
		if r.T != "result" {
			continue // info/done are not verdicts
		}
		switch {
		case !r.Status.valid():
			protoErrs = append(protoErrs, protoErr(fmt.Sprintf("record for %q has bad status %q", r.ID, r.Status)))
		case !planned[r.ID]:
			// 5.2 Unplanned: discard, error on `protocol`.
			protoErrs = append(protoErrs, protoErr("result for unplanned id "+r.ID))
		case r.ID == reservedProtocol:
			protoErrs = append(protoErrs, protoErr("result for reserved id protocol"))
		default:
			if _, dup := results[r.ID]; dup {
				// 5.5 Duplicate: discard the second, keep the first, error on `protocol`.
				protoErrs = append(protoErrs, protoErr("duplicate result for id "+r.ID))
				continue
			}
			results[r.ID] = r
		}
	}
	for _, n := range badLines {
		// 7.1.2 a framed line that does not parse.
		protoErrs = append(protoErrs, protoErr(fmt.Sprintf("malformed record on line %d", n)))
	}

	crashed := exit != 0 || !done
	crashDetail := "no completion"
	if exit != 0 {
		crashDetail = fmt.Sprintf("entrypoint exited %d", exit)
	}

	var out []Verdict
	for _, id := range plan {
		if r, ok := results[id]; ok {
			// A reported id keeps its result even if the run later crashed (5.4).
			out = append(out, Verdict{ID: id, Status: r.Status, Detail: r.Detail})
			continue
		}
		// 5.1 Unreported (with 5.4 detail if the run crashed).
		detail := "not reported"
		if crashed {
			detail = crashDetail
		}
		out = append(out, Verdict{ID: id, Status: Error, Detail: detail})
	}

	// 5.3 Empty: a run that produced no results fails even if the plan is empty
	// for this distro (the per-id errors above already fail a non-empty plan).
	if len(results) == 0 && len(plan) == 0 {
		protoErrs = append(protoErrs, protoErr("no checks ran"))
	}

	return append(out, protoErrs...)
}

func protoErr(detail string) Verdict {
	return Verdict{ID: reservedProtocol, Status: Error, Detail: detail}
}

// ValidID reports whether s is a well-formed check id or info key (spec 3.1):
// an alphanumeric first byte, then alphanumerics or _ . / - , at most 128 bytes.
func ValidID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		alnum := (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
		if i == 0 {
			if !alnum {
				return false
			}
			continue
		}
		if !alnum && b != '_' && b != '.' && b != '/' && b != '-' {
			return false
		}
	}
	return true
}

// Reserved reports whether id is an engine-reserved id a consumer must not report
// on (spec 3.2): `protocol` and `test`.
func Reserved(id string) bool { return id == reservedProtocol || id == "test" }

// ResultRecord builds a result record (spec 7.2) for the CLI to append.
func ResultRecord(id string, status Status, detail string) Record {
	return Record{Schema: Protocol, T: "result", ID: id, Status: status, Detail: detail}
}

// InfoRecord builds an info record (spec 7.3).
func InfoRecord(k, v string) Record {
	return Record{Schema: Protocol, T: "info", K: k, V: v}
}

// DoneRecord builds the completion record (spec 7.4) the guest engine writes.
func DoneRecord(exit int) Record {
	return Record{Schema: Protocol, T: "done", Exit: exit}
}

// ProtocolID is the reserved id for engine-synthesised errors about the run.
const ProtocolID = reservedProtocol
