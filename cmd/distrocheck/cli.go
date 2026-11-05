package main

// The in-guest `mayfly` CLI (spec 9): the same binary the host runs as the
// engine, dispatched on its first argument. A consumer's entrypoint calls these
// to report; it never formats a record itself. Each reporting call writes exactly
// one record to $MAYFLY_RESULTS and exits 0, or writes nothing and exits 2 on a
// usage error (spec 9.2, 9.6).

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/conor-sometimes/mayfly/internal/oracle"
)

var reportVerbs = map[string]oracle.Status{
	"pass":  oracle.Pass,
	"fail":  oracle.Fail,
	"skip":  oracle.Skip,
	"error": oracle.Error,
}

// isCLIVerb reports whether argv[1] selects the `mayfly` CLI rather than the
// engine's run (the engine is the default when no verb matches).
func isCLIVerb(v string) bool {
	if v == "info" {
		return true
	}
	_, ok := reportVerbs[v]
	return ok
}

// runCLI handles one `mayfly <verb> ...` call and returns its process exit code.
func runCLI(args []string) int {
	verb, rest := args[0], args[1:]

	if verb == "info" {
		if len(rest) < 2 {
			return usageErr("info <key> <value ...>")
		}
		if !oracle.ValidID(rest[0]) {
			return usageErr("info: malformed key " + rest[0])
		}
		return appendRecord(oracle.InfoRecord(rest[0], strings.Join(rest[1:], " ")))
	}

	status, ok := reportVerbs[verb]
	if !ok {
		return usageErr("unknown subcommand " + verb)
	}
	if len(rest) < 1 {
		return usageErr(verb + " <id> [detail ...]")
	}
	id, detail := rest[0], strings.Join(rest[1:], " ")

	// An ill-formed, reserved or unplanned id is not a plain usage error: the CLI
	// also writes a `protocol` error so the engine applies rule 5.2 (spec 9.2).
	if !oracle.ValidID(id) || oracle.Reserved(id) {
		return unplanned(id, "malformed or reserved id "+id)
	}
	if !inPlan(id) {
		return unplanned(id, "id "+id+" is not in MAYFLY_PLAN")
	}
	if (status == oracle.Skip || status == oracle.Error) && detail == "" {
		return usageErr(verb + " " + id + " needs a reason")
	}
	return appendRecord(oracle.ResultRecord(id, status, detail))
}

func inPlan(id string) bool {
	for _, p := range strings.Fields(os.Getenv("MAYFLY_PLAN")) {
		if p == id {
			return true
		}
	}
	return false
}

// usageErr: nothing is written, exit 2 (spec 9.2, 9.6).
func usageErr(msg string) int {
	fmt.Fprintln(os.Stderr, "mayfly: "+msg)
	return 2
}

// unplanned: write the `protocol` error the engine will also synthesise (5.2),
// print the reason, exit 2 (spec 9.2).
func unplanned(id, reason string) int {
	fmt.Fprintln(os.Stderr, "mayfly: "+reason)
	_ = appendRecord(oracle.ResultRecord(oracle.ProtocolID, oracle.Error, reason))
	return 2
}

// appendRecord writes one JSON record line to $MAYFLY_RESULTS as a single write
// (spec 7.5 (b)). Returns 0 on success, 1 on an I/O failure.
func appendRecord(r oracle.Record) int {
	path := os.Getenv("MAYFLY_RESULTS")
	if path == "" {
		return usageErr("MAYFLY_RESULTS is unset (the mayfly CLI runs only under the engine)")
	}
	line, err := json.Marshal(r)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mayfly: marshal:", err)
		return 1
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // $MAYFLY_RESULTS, the engine's own file in the VM
	if err != nil {
		fmt.Fprintln(os.Stderr, "mayfly: open results:", err)
		return 1
	}
	defer f.Close() //nolint:errcheck // append-only; the write error below is what matters
	if _, err := f.Write(append(line, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, "mayfly: write results:", err)
		return 1
	}
	return 0
}
