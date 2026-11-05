package main

// The `mayfly` CLI's judging subcommands (spec 9.4, 9.5): detect (identity-based
// attribution of a detection tool's findings to an emulation), and the sandbox /
// lsm built-ins. Each parses its flags, judges, and writes exactly one result
// record via appendRecord; each exits 0 on any outcome and 2 only on a usage
// error (spec 9.6). They run under the entrypoint, so $MAYFLY_RESULTS and the
// rest of the spec 8.2 environment are already set.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/conor-sometimes/mayfly/internal/oracle"
)

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

// loginPath gives an as=USER command the sbin directories a login shell has, so a
// tool in /usr/sbin resolves.
const loginPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// runDetect implements `mayfly detect` (spec 9.4).
func runDetect(args []string) int {
	if len(args) < 2 {
		return usageErr("detect <id> ( --expect RULE... | --expect-none ) --findings GLOB... [--as USER] [--scan CMD] [--needs TOOL...] [--timeout D] -- CMD...")
	}
	id := args[1]

	fs := flag.NewFlagSet("detect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var expect, findings, needs multiFlag
	fs.Var(&expect, "expect", "a rule that must fire (repeatable)")
	fs.Var(&findings, "findings", "glob of the tool's OCSF findings (repeatable)")
	fs.Var(&needs, "needs", "a tool the emulation needs; absent -> skip (repeatable)")
	expectNone := fs.Bool("expect-none", false, "assert no rule fires")
	as := fs.String("as", "", "run CMD as this user")
	scan := fs.String("scan", "", "command that drives a oneshot-scan subject")
	timeout := fs.Duration("timeout", 2*time.Minute, "kill CMD and its group after this")
	if err := fs.Parse(args[2:]); err != nil {
		return usageErr("detect: " + err.Error())
	}
	cmd := fs.Args()

	// Usage errors: exit 2, nothing written (spec 9.4.1, 9.4.2).
	if (len(expect) == 0) == !*expectNone { // neither or both
		return usageErr("detect " + id + ": give exactly one of --expect or --expect-none")
	}
	if len(findings) == 0 {
		return usageErr("detect " + id + ": at least one --findings glob is required")
	}
	if len(cmd) == 0 {
		return usageErr("detect " + id + ": a command is required after --")
	}
	if !oracle.ValidID(id) || oracle.Reserved(id) {
		return unplanned(id, "malformed or reserved id "+id)
	}
	if !inPlan(id) {
		return unplanned(id, "id "+id+" is not in MAYFLY_PLAN")
	}

	// A missing needed tool is a skip, decided before anything runs (spec 9.4.2).
	for _, tool := range needs {
		if !have(tool) {
			return appendRecord(oracle.ResultRecord(id, oracle.Skip, "needs "+tool+", not on this distro"))
		}
	}
	// Preconditions (spec 9.4.2): each a reason for `error`, not a crash.
	if _, err := os.Stat(auditLog); err != nil {
		return appendRecord(oracle.ResultRecord(id, oracle.Error, "audit log not readable: "+err.Error()))
	}
	if st, _ := psh(time.Minute, "auditctl", "-s"); oracle.StatusField(st, "enabled") == "0" {
		return appendRecord(oracle.ResultRecord(id, oracle.Error, "syscall auditing is disabled"))
	}
	if *as != "" {
		if _, err := psh(time.Minute, "id", *as); err != nil {
			return appendRecord(oracle.ResultRecord(id, oracle.Error, "user "+*as+" does not exist"))
		}
	}

	scanCmd := splitScan(*scan)
	drainQuiescent(scanCmd, findings)
	before := len(readFindings(findings))
	auditBefore := auditLineN()

	out, launcher, exit := launch(*timeout, *as, cmd)
	drainQuiescent(scanCmd, findings)

	// The emulation's exit status is evidence (spec 9.4.5).
	switch {
	case exit == 77:
		return appendRecord(oracle.ResultRecord(id, oracle.Skip, oracle.FirstLine(strings.TrimSpace(out))))
	case exit == 126 || exit == 127:
		return appendRecord(oracle.ResultRecord(id, oracle.Error, fmt.Sprintf("could not execute (the emulation did not run): exit %d: %s", exit, oracle.LastLines(out, 2))))
	case exit != 0:
		return appendRecord(oracle.ResultRecord(id, oracle.Error, fmt.Sprintf("emulation did not run cleanly: exit %d: %s", exit, oracle.LastLines(out, 2))))
	}

	// exit 0: judge the fired set against the expectation (spec 9.4.4, 9.4.6).
	subtree := oracle.Descendants(oracle.AuditProcTree(auditSince(auditBefore)), launcher)
	fired := map[string]bool{}
	offending := map[string]string{}
	for _, line := range oracle.From(readFindings(findings), before) {
		if pid, ppid := oracle.AlertProcess(line); !oracle.InSubtree(subtree, pid, ppid) {
			continue
		}
		if rule, ok := oracle.AnalyticName(line); ok {
			fired[rule] = true
			if _, seen := offending[rule]; !seen {
				offending[rule] = line
			}
		}
	}
	got := oracle.Keys(fired)
	want := append([]string(nil), expect...)
	sort.Strings(want)

	if oracle.Equal(got, want) {
		return appendRecord(oracle.ResultRecord(id, oracle.Pass, fmt.Sprintf("fired %v", got)))
	}
	detail := fmt.Sprintf("fired %v, expected %v", got, want)
	if execs := auditExecsSince(auditBefore); execs != "" {
		detail += "; execs: " + execs
	}
	wantSet := map[string]bool{}
	for _, r := range want {
		wantSet[r] = true
	}
	for _, r := range got {
		if !wantSet[r] {
			detail += "; extra " + r + ": " + oracle.FirstLine(offending[r])
		}
	}
	return appendRecord(oracle.ResultRecord(id, oracle.Fail, detail))
}

// runSandbox implements `mayfly sandbox <id> --unit UNIT` (spec 9.5).
func runSandbox(args []string) int {
	if len(args) < 2 {
		return usageErr("sandbox <id> --unit UNIT")
	}
	id := args[1]
	fs := flag.NewFlagSet("sandbox", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	unit := fs.String("unit", "", "the systemd unit to rate")
	if err := fs.Parse(args[2:]); err != nil {
		return usageErr("sandbox: " + err.Error())
	}
	if *unit == "" {
		return usageErr("sandbox " + id + ": --unit is required")
	}
	if !oracle.ValidID(id) || oracle.Reserved(id) {
		return unplanned(id, "malformed or reserved id "+id)
	}
	if !inPlan(id) {
		return unplanned(id, "id "+id+" is not in MAYFLY_PLAN")
	}
	out, _ := psh(time.Minute, "systemd-analyze", "security", *unit, "--no-pager")
	score, level, ok := oracle.Exposure(out)
	if !ok {
		return appendRecord(oracle.ResultRecord(id, oracle.Error, "no exposure score from systemd-analyze: "+oracle.LastLines(out, 2)))
	}
	_ = appendRecord(oracle.InfoRecord("exposure/"+*unit, score+" "+level))
	status := oracle.Fail
	if level == "SAFE" || level == "OK" {
		status = oracle.Pass
	}
	return appendRecord(oracle.ResultRecord(id, status, "exposure "+score+" "+level))
}

// runLsm implements `mayfly lsm <id> --match STRING` (spec 9.5).
func runLsm(args []string) int {
	if len(args) < 2 {
		return usageErr("lsm <id> --match STRING")
	}
	id := args[1]
	fs := flag.NewFlagSet("lsm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	match := fs.String("match", "", "the subject denials are searched for")
	if err := fs.Parse(args[2:]); err != nil {
		return usageErr("lsm: " + err.Error())
	}
	if *match == "" {
		return usageErr("lsm " + id + ": --match is required")
	}
	if !oracle.ValidID(id) || oracle.Reserved(id) {
		return unplanned(id, "malformed or reserved id "+id)
	}
	if !inPlan(id) {
		return unplanned(id, "id "+id+" is not in MAYFLY_PLAN")
	}
	var denials []string
	readable := false
	if have("ausearch") {
		out, err := psh(time.Minute, "ausearch", "-m", "AVC,USER_AVC", "-ts", "boot", "-i")
		if err == nil {
			readable = true
		}
		denials = append(denials, oracle.LinesContaining(out, *match)...)
	}
	if out, err := psh(time.Minute, "journalctl", "-k", "--no-pager", "-g", `apparmor="DENIED"`); err == nil {
		readable = true
		denials = append(denials, oracle.LinesContaining(out, *match)...)
	}
	if !readable {
		return appendRecord(oracle.ResultRecord(id, oracle.Error, "no LSM log source readable (ausearch and journalctl both failed)"))
	}
	if len(denials) > 0 {
		return appendRecord(oracle.ResultRecord(id, oracle.Fail, "denials: "+strings.Join(oracle.FirstN(denials, 3), " | ")))
	}
	return appendRecord(oracle.ResultRecord(id, oracle.Pass, "no LSM denial for "+*match))
}

// --- helpers ---

func splitScan(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Fields(s)
}

// readFindings reads every OCSF line in the files matching any glob.
func readFindings(globs []string) []string {
	var lines []string
	for _, g := range globs {
		files, _ := filepath.Glob(g)
		sort.Strings(files)
		for _, f := range files {
			if data, err := os.ReadFile(f); err == nil { //nolint:gosec // the tool's own finding files, in the VM
				lines = append(lines, oracle.NonEmpty(string(data))...)
			}
		}
	}
	return lines
}

// drive runs a oneshot-scan command once (e.g. banken's `systemctl start
// banken.service`); nil means a running daemon that needs no driving.
func drive(scan []string) {
	if len(scan) == 0 {
		return
	}
	_, _ = psh(2*time.Minute, scan[0], scan[1:]...)
}

// drainQuiescent drives the scan until a settle passes with no new finding, so
// every exec is read before a count is taken. With no scan it is one settle.
func drainQuiescent(scan, globs []string) {
	if len(scan) == 0 {
		time.Sleep(scanSettle)
		return
	}
	for i := 0; i < maxDrain; i++ {
		drive(scan)
		n := len(readFindings(globs))
		time.Sleep(scanSettle)
		drive(scan)
		if len(readFindings(globs)) == n {
			return
		}
	}
}

// launch runs CMD under a launcher process whose pid roots the attribution
// subtree (spec 9.4.3). With `as` it runs as that user with a login PATH. The
// whole group is killed on timeout. Returns combined output, launcher pid, exit.
func launch(timeout time.Duration, as string, cmd []string) (out string, pid, exit int) {
	full := cmd
	if as != "" {
		full = append([]string{"runuser", "-u", as, "--", "env", "PATH=" + loginPath}, cmd...)
	}
	s, pid, err := pshPID(timeout, full[0], full[1:]...)
	time.Sleep(time.Second) // let auditd write the records
	return s, pid, exitStatus(err)
}

// psh runs a command, returning combined output; the subcommand inherits the
// entrypoint's environment.
func psh(timeout time.Duration, name string, args ...string) (string, error) {
	out, _, err := pshPID(timeout, name, args...)
	return out, err
}

func pshPID(timeout time.Duration, name string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- running the distro's tools and the consumer's emulation, in a throwaway VM
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	if ctx.Err() != nil {
		return string(out), pid, fmt.Errorf("%s: timed out after %s", name, timeout)
	}
	return string(out), pid, err
}
