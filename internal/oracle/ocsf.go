// Package oracle is the in-guest matrix oracle: it attributes a subject's OCSF
// findings to the check that provoked them by process-subtree identity, and
// parses the audit log and the tools distrocheck drives. It is the trustworthy
// core of the matrix, and the only part with unit tests, so it is a package of
// its own and cmd/distrocheck only sequences it.
//
// The OCSF finding type here is the minimal subset the oracle reads: a rule
// name and, per evidence, the triggering process's pid and its parent's pid.
// Any tool whose findings are OCSF 1.x with process evidence attributes; the
// oracle never depends on the subject.
package oracle

import "encoding/json"

// OCSFFinding is the slice of an OCSF detection finding the oracle reads. A
// subject may write far more; unknown fields are ignored.
type OCSFFinding struct {
	ClassUID    int    `json:"class_uid"`
	SeverityID  int    `json:"severity_id"`
	Metadata    meta   `json:"metadata"`
	FindingInfo fInfo  `json:"finding_info"`
	Evidences   []evid `json:"evidences"`
	Device      *dev   `json:"device"`
}

type meta struct {
	Version string `json:"version"`
}

type fInfo struct {
	Analytic analytic `json:"analytic"`
}

type analytic struct {
	Name string `json:"name"`
}

type evid struct {
	Process *proc `json:"process"`
}

type proc struct {
	PID           int    `json:"pid"`
	CmdLine       string `json:"cmd_line"`
	ParentProcess *proc  `json:"parent_process"`
}

type dev struct {
	Hostname string `json:"hostname"`
}

// AnalyticName returns a finding line's rule (finding_info.analytic.name), and
// whether the line parsed as a finding with a rule at all.
func AnalyticName(line string) (name string, ok bool) {
	var f OCSFFinding
	if json.Unmarshal([]byte(line), &f) != nil || f.FindingInfo.Analytic.Name == "" {
		return "", false
	}
	return f.FindingInfo.Analytic.Name, true
}

// Severity returns a finding line's severity_id (OCSF: 4 high, 5 critical).
func Severity(line string) int {
	var f OCSFFinding
	if json.Unmarshal([]byte(line), &f) != nil {
		return 0
	}
	return f.SeverityID
}

// CommandLine is the command line of a finding's first process evidence, for
// reporting a false positive. "?" when there is none.
func CommandLine(line string) string {
	var f OCSFFinding
	if json.Unmarshal([]byte(line), &f) != nil || len(f.Evidences) == 0 || f.Evidences[0].Process == nil {
		return "?"
	}
	return f.Evidences[0].Process.CmdLine
}

// AlertProcess reads the triggering process's pid and its parent's pid from a
// finding line's first process evidence, the two handles identity attribution
// needs. 0 means "not recorded".
func AlertProcess(line string) (pid, ppid int) {
	var f OCSFFinding
	if json.Unmarshal([]byte(line), &f) != nil || len(f.Evidences) == 0 || f.Evidences[0].Process == nil {
		return 0, 0
	}
	p := f.Evidences[0].Process
	pid = p.PID
	if p.ParentProcess != nil {
		ppid = p.ParentProcess.PID
	}
	return pid, ppid
}
