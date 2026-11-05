// Command distrocheck is mayfly's in-guest engine and CLI. The host (cmd/matrix)
// scps this binary and the payload into one throwaway VM, runs it as root over
// SSH to provision and run the consumer's entrypoint, reads the report, and tears
// the VM down. The same binary, invoked as `mayfly <verb>` by that entrypoint, is
// the result-protocol CLI (docs/protocol.md, spec 9) -- see cli.go and detect.go.
//
// It must never run anywhere but such a VM: it installs packages, rewrites the
// audit rules, enables units, and runs the benign malware look-alikes a detection
// consumer drives through `mayfly detect`.
//
// The report travels on SSH stdout: every line beginning reportMarker is a JSON
// report. The engine prints one as it goes (so a guest that dies mid-run still
// tells the host how far it got); the host keeps the last.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/conor-sometimes/mayfly/internal/oracle"
	"github.com/conor-sometimes/mayfly/internal/report"
)

const (
	auditLog   = "/var/log/audit/audit.log"
	auditRules = "/etc/audit/rules.d"
	// scanSettle is how long a drain waits for auditd to flush the previous
	// command's tail before re-scanning, so a straggler exec is attributed to its
	// own check.
	scanSettle = 2 * time.Second
	// maxDrain bounds the quiescence loop so a chatty subject cannot hang a run; a
	// quiet VM settles in one iteration.
	maxDrain = 8
	// reportMarker prefixes every report line on stdout, so the host finds the
	// report among the guest's own chatter and keeps the last (complete) one.
	reportMarker = "MATRIX-REPORT:"
	// guestResults is where the engine points $MAYFLY_RESULTS: the records file the
	// `mayfly` CLI appends to and the engine reconciles (spec 7.5 (b), 8.2).
	guestResults = "/run/mayfly-results.jsonl"
)

func main() {
	// The same binary is the in-guest `mayfly` CLI (spec 9): if the first argument
	// is a CLI verb, act as the CLI. Otherwise run the engine (the host invokes
	// `distrocheck -root ...`).
	if len(os.Args) > 1 && isCLIVerb(os.Args[1]) {
		os.Exit(runCLI(os.Args[1:]))
	}

	root := flag.String("root", "/opt/matrix", "the payload root (config.json and every source path are under it)")
	flag.Parse()

	var cfg report.Config
	data, err := os.ReadFile(filepath.Join(*root, "config.json"))
	if err == nil {
		err = json.Unmarshal(data, &cfg)
	}
	if err != nil {
		fatal(fmt.Errorf("read config.json: %w", err))
	}

	c := &checker{cfg: cfg, root: *root, rep: report.Report{Distro: cfg.Distro, Info: map[string]string{}}}
	c.run()
	emit(c.rep)
}

// fatal reports a guest that could not even start, so the host sees a reason
// instead of a parse failure.
func fatal(err error) {
	emit(report.Report{Checks: []report.Check{{ID: "protocol", Status: "error", Detail: "distrocheck: " + err.Error()}}})
	os.Exit(1)
}

// appendRecordLine writes one record to the guest records file. The engine uses
// it for the `done` record (spec 7.4.1); the CLI verbs use appendRecord, which
// honours $MAYFLY_RESULTS.
func appendRecordLine(r oracle.Record) {
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	f, err := os.OpenFile(guestResults, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the engine's own records file in the VM
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck // append-only; a lost done record is caught by the host (spec 7.4.2)
	_, _ = f.Write(append(line, '\n'))
}

// emit prints the report as one marked JSON line on stdout.
func emit(r report.Report) {
	data, err := json.Marshal(r)
	if err != nil {
		data = []byte(`{"checks":[{"id":"protocol","status":"error","detail":"distrocheck: cannot encode report"}]}`)
	}
	fmt.Println(reportMarker + string(data))
}

type checker struct {
	cfg     report.Config
	root    string
	rep     report.Report
	workDir string
}

func (c *checker) src(rel string) string { return filepath.Join(c.root, rel) }

func (c *checker) record(ck report.Check) {
	ck.Detail = trim(ck.Detail, 400)
	c.rep.Checks = append(c.rep.Checks, ck)
	emit(c.rep) // a partial report after every step
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// run provisions the distro, installs the CLI, runs the consumer's entrypoint,
// and reconciles its records into the report. A provisioning failure marks every
// planned id ERROR "not reached: ..." (spec 5.7) rather than dropping it.
func (c *checker) run() {
	c.recordPlatform()
	phases := []struct {
		what string
		fn   func() error
	}{
		{"install packages", c.installPackages},
		{"audit setup", c.auditSetup},
		{"provision subjects", c.provision},
		{"install mayfly cli", c.installSelf},
	}
	for _, p := range phases {
		if err := p.fn(); err != nil {
			c.notReached(p.what + ": " + err.Error())
			return
		}
	}
	defer c.recordAuditState()
	c.runEntrypoint()
}

// notReached renders rule 5.7: a phase before the entrypoint failed, so every
// planned id is ERROR. With an empty plan the error lands on `protocol`.
func (c *checker) notReached(reason string) {
	if len(c.cfg.Plan) == 0 {
		c.record(report.Check{ID: "protocol", Status: "error", Detail: "not reached: " + reason})
		return
	}
	for _, p := range c.cfg.Plan {
		c.record(report.Check{ID: p.ID, Status: "error", Detail: "not reached: " + reason})
	}
}

// planIDs is the plan's ids in order.
func planIDs(plan []report.PlanEntry) []string {
	ids := make([]string, len(plan))
	for i, p := range plan {
		ids[i] = p.ID
	}
	return ids
}

// installSelf puts this binary on PATH as `mayfly` so the entrypoint can call it
// (spec 9.1). It is the same build as the engine by construction.
func (c *checker) installSelf() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return copyFile(self, "/usr/local/bin/mayfly", 0o755)
}

// runEntrypoint runs the consumer's one entrypoint (spec 8) with the protocol
// environment (spec 8.2), then reconciles the records it produced (spec 5) into
// the report. The `test` sugar (spec 4.5) maps the exit status instead.
func (c *checker) runEntrypoint() {
	_ = os.Remove(guestResults)
	plan := planIDs(c.cfg.Plan)
	env := append(os.Environ(),
		"MAYFLY_PROTOCOL="+oracle.Protocol,
		"MAYFLY_DISTRO="+c.cfg.Distro,
		"MAYFLY_PM="+c.cfg.PM,
		"MAYFLY_FAMILY="+c.cfg.Family,
		"MAYFLY_PLAN="+strings.Join(plan, " "),
		"MAYFLY_RESULTS="+guestResults,
	)
	if b := c.installedBinary(); b != "" {
		env = append(env, "MAYFLY_BINARY="+b)
	}
	if c.cfg.TestUser != "" {
		env = append(env, "MAYFLY_TEST_USER="+c.cfg.TestUser)
	}
	if c.cfg.Proxy != "" {
		for _, k := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY"} {
			env = append(env, k+"="+c.cfg.Proxy)
		}
		for _, k := range []string{"no_proxy", "NO_PROXY"} {
			env = append(env, k+"=localhost,127.0.0.1,::1")
		}
	}

	out, exit := runScript(c.src(c.cfg.Entrypoint), env)

	if c.cfg.TestSugar {
		c.applyInfo()
		c.renderTestSugar(exit, out)
		return
	}

	data, _ := os.ReadFile(guestResults) //nolint:gosec // the engine's own records file in the VM
	recs, bad := oracle.ParseRecords(oracle.NonEmpty(string(data)), false)
	c.applyRecordsInfo(recs)
	// The engine writes the one `done` record after the entrypoint exits (spec
	// 7.4.1); guest-side reconciliation therefore sees done=true and uses `exit`
	// for the crash rule.
	for _, v := range oracle.Reconcile(plan, recs, exit, true, bad) {
		c.rep.Checks = append(c.rep.Checks, report.Check{ID: v.ID, Status: string(v.Status), Detail: trim(v.Detail, 400)})
	}
	appendRecordLine(oracle.DoneRecord(exit))
	emit(c.rep)
}

// renderTestSugar maps the entrypoint's exit status to the single `test` verdict
// (spec 9.3).
func (c *checker) renderTestSugar(exit int, out string) {
	ck := report.Check{ID: "test", Status: "pass"}
	switch {
	case exit == 0:
		ck.Status = "pass"
	case exit == 126 || exit == 127:
		ck.Status = "error"
		ck.Detail = "could not execute: " + oracle.LastLines(out, 6)
	default:
		ck.Status = "fail"
		ck.Detail = oracle.LastLines(out, 6)
	}
	c.record(ck)
}

// applyRecordsInfo copies info records (spec 7.3) into the report's Info map.
func (c *checker) applyRecordsInfo(recs []oracle.Record) {
	for _, r := range recs {
		if r.T == "info" {
			c.rep.Info[r.K] = r.V
		}
	}
}

// applyInfo reads info records a `test`-sugar script may have written (spec 4.5).
func (c *checker) applyInfo() {
	data, err := os.ReadFile(guestResults) //nolint:gosec // the engine's own records file
	if err != nil {
		return
	}
	recs, _ := oracle.ParseRecords(oracle.NonEmpty(string(data)), false)
	c.applyRecordsInfo(recs)
}

// --- platform ---

func (c *checker) recordPlatform() {
	info := c.rep.Info
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		info["os"] = oracle.OSRelease(string(data))
	}
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		info["kernel"] = strings.TrimSpace(string(data))
	}
	if out, err := c.sh(time.Minute, "systemctl", "--version"); err == nil {
		info["systemd"] = oracle.FirstLine(out)
	}
	if data, err := os.ReadFile("/sys/kernel/security/lsm"); err == nil {
		info["lsm"] = strings.TrimSpace(string(data))
	}
	if have("getenforce") {
		out, _ := c.sh(time.Minute, "getenforce")
		info["selinux"] = strings.TrimSpace(out)
	}
}

func (c *checker) recordAuditState() {
	info := c.rep.Info
	out, _ := c.sh(time.Minute, "systemctl", "is-active", "auditd")
	info["auditd"] = strings.TrimSpace(out)
	if fi, err := os.Stat(auditLog); err == nil {
		info["audit_log_bytes"] = strconv.FormatInt(fi.Size(), 10)
	}
}

// --- install ---

// subjectPackages translates every subject's generic package names through this
// distro's map. An unmapped name is a hard error naming the distro and the name,
// never a silent skip: the consumer must extend the distro attr.
func (c *checker) subjectPackages() ([]string, error) {
	var pkgs []string
	for _, s := range c.cfg.Subjects {
		for _, name := range s.Packages {
			pkg, ok := c.cfg.Packages[name]
			if !ok {
				return nil, fmt.Errorf("distro %s has no package for %q; add %q to its packages map", c.cfg.Distro, name, name)
			}
			pkgs = append(pkgs, pkg)
		}
	}
	return pkgs, nil
}

func (c *checker) installPackages() error {
	if c.cfg.Proxy != "" {
		if err := c.useProxy(); err != nil {
			return fmt.Errorf("configure %s for the proxy: %w", c.cfg.PM, err)
		}
	}
	pkgs, err := c.subjectPackages()
	if err != nil {
		return err
	}
	pkgs = dedup(pkgs)
	if len(pkgs) == 0 {
		return nil
	}
	c.rep.Info["installed"] = strings.Join(pkgs, " ")
	if out, err := c.pmInstallRetry(pkgs); err != nil {
		return fmt.Errorf("install %s on %s: %w: %s", strings.Join(pkgs, " "), c.cfg.Distro, err, oracle.LastLines(out, 3))
	}
	return nil
}

// pmInstallRetry installs with the distro's package manager, retrying a few
// times with backoff: a mirror hiccup on a fresh cloud image is common and not a
// reason to fail the distro. A genuine failure returns the last error.
func (c *checker) pmInstallRetry(pkgs []string) (string, error) {
	var out string
	var err error
	backoff := 5 * time.Second
	for attempt := 1; attempt <= 3; attempt++ {
		out, err = c.pmInstall(pkgs)
		if err == nil {
			return out, nil
		}
		if attempt < 3 {
			time.Sleep(backoff)
			backoff *= 2
			if c.cfg.PM == "apt" {
				_, _ = c.sh(5*time.Minute, "apt-get", "update")
			}
		}
	}
	return out, err
}

func (c *checker) pmInstall(pkgs []string) (string, error) {
	switch c.cfg.PM {
	case "dnf":
		return c.sh(15*time.Minute, "dnf", append([]string{"-y", "--setopt=*.skip_if_unavailable=1", "install"}, pkgs...)...)
	case "apt":
		if out, err := c.sh(15*time.Minute, "apt-get", "update"); err != nil {
			return out, err
		}
		return c.sh(15*time.Minute, "apt-get", append([]string{"install", "-y"}, pkgs...)...)
	case "zypper":
		return c.sh(15*time.Minute, "zypper", append([]string{"-n", "--gpg-auto-import-keys", "install"}, pkgs...)...)
	case "pacman":
		return c.sh(15*time.Minute, "pacman", append([]string{"-Sy", "--noconfirm", "--needed"}, pkgs...)...)
	}
	return "", fmt.Errorf("unknown package manager %q", c.cfg.PM)
}

// useProxy points the package manager at the host's proxy and, for dnf and
// zypper, pins repos to the vendor's own host: a mirror list hands out
// third-party mirrors a proxy allow-list cannot name in advance.
func (c *checker) useProxy() error {
	p := c.cfg.Proxy
	switch c.cfg.PM {
	case "apt":
		return os.WriteFile("/etc/apt/apt.conf.d/99matrix-proxy",
			[]byte(fmt.Sprintf("Acquire::http::Proxy %q;\nAcquire::https::Proxy %q;\n", p, p)), 0o600)
	case "dnf":
		if err := appendFile("/etc/dnf/dnf.conf", "proxy="+p+"\n"); err != nil {
			return err
		}
		return rewriteFiles("/etc/yum.repos.d/*.repo", oracle.PinDNFRepo)
	case "zypper":
		conf := fmt.Sprintf("PROXY_ENABLED=\"yes\"\nHTTP_PROXY=%q\nHTTPS_PROXY=%q\n", p, p)
		if err := os.WriteFile("/etc/sysconfig/proxy", []byte(conf), 0o600); err != nil {
			return err
		}
		return rewriteFiles("/etc/zypp/repos.d/*.repo", func(s string) string {
			return strings.ReplaceAll(s, "download.opensuse.org", "downloadcontent.opensuse.org")
		})
	}
	return nil // pacman reads the proxy from the environment sh sets
}

// --- audit ---

// auditSetup enables auditd and drops any stock "-a task,never" (which turns
// syscall auditing off on Fedora and openSUSE). Only when the module set
// `audit = true`; a run with no detection does none of this.
func (c *checker) auditSetup() error {
	if !c.cfg.Audit {
		return nil
	}
	removed, err := dropTaskNever(auditRules + "/*.rules")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(removed) > 0 {
		c.rep.Info["audit_task_never_removed"] = strings.Join(removed, " ")
	}
	if _, err := c.sh(2*time.Minute, "systemctl", "enable", "--now", "auditd"); err != nil {
		_, _ = c.sh(2*time.Minute, "service", "auditd", "start")
	}
	return nil
}

// loadAuditRules loads the rules.d a subject just shipped into the kernel and
// confirms syscall auditing is on. Run after provisioning, because the rule
// files are a subject's own files.
func (c *checker) loadAuditRules() {
	if !c.cfg.Audit {
		return
	}
	if have("augenrules") {
		_, _ = c.sh(2*time.Minute, "augenrules", "--load")
	}
	// augenrules can compile rules.d without loading them (seen on Fedora). Load
	// each file straight into the kernel too; idempotent.
	files, _ := filepath.Glob(auditRules + "/*.rules")
	for _, f := range files {
		_, _ = c.sh(time.Minute, "auditctl", "-R", f)
	}
	_, _ = c.sh(time.Minute, "auditctl", "-e", "1")
	rules, _ := c.sh(time.Minute, "auditctl", "-l")
	c.rep.Info["audit_rules"] = strconv.Itoa(len(oracle.NonEmpty(rules)))
}

// --- provision ---

func (c *checker) provision() error {
	if c.cfg.TestUser != "" {
		if out, err := c.sh(time.Minute, "useradd", "-m", c.cfg.TestUser); err != nil && !strings.Contains(out, "exists") {
			return fmt.Errorf("useradd %s: %w: %s", c.cfg.TestUser, err, out)
		}
		// The entrypoint launches `mayfly detect --as USER` as this unprivileged
		// user, but the payload is staged under the run user's home, which useradd
		// makes 0700. Open the traversal path and make the tree world-readable (o+rX:
		// dirs and already-executable files only), or every as=user command dies with
		// "Permission denied" before it runs.
		for dir := c.root; dir != "/" && dir != "." && dir != ""; dir = filepath.Dir(dir) {
			_, _ = c.sh(time.Minute, "chmod", "o+x", dir)
		}
		if _, err := c.sh(time.Minute, "chmod", "-R", "o+rX", c.root); err != nil {
			return fmt.Errorf("open payload to %s: %w", c.cfg.TestUser, err)
		}
	}
	for _, s := range c.cfg.Subjects {
		if err := c.provisionSubject(s); err != nil {
			return fmt.Errorf("subject %s: %w", s.Name, err)
		}
	}
	// A subject may have shipped audit rules among its files; load them now.
	c.loadAuditRules()
	if _, err := c.sh(time.Minute, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	for _, s := range c.cfg.Subjects {
		for _, unit := range s.Enable {
			if out, err := c.sh(2*time.Minute, "systemctl", "enable", "--now", unit); err != nil {
				return fmt.Errorf("enable %s: %w: %s", unit, err, oracle.LastLines(out, 3))
			}
		}
	}
	return nil
}

// installDst is where a subject's binary lands: its explicit installPath, or
// /usr/local/bin/<name> by default. Exported to the entrypoint as $MAYFLY_BINARY.
func installDst(s report.Subject) string {
	if s.InstallPath != "" {
		return s.InstallPath
	}
	return "/usr/local/bin/" + s.Name
}

// installedBinary is the guest path of the thing under test, for $MAYFLY_BINARY.
// The first install that ships a binary wins; "" when none did.
func (c *checker) installedBinary() string {
	for _, s := range c.cfg.Subjects {
		if s.Binary != "" {
			return installDst(s)
		}
	}
	return ""
}

func (c *checker) provisionSubject(s report.Subject) error {
	if s.Binary != "" {
		if err := copyFile(c.src(s.Binary), installDst(s), 0o755); err != nil {
			return err
		}
	}
	for _, unit := range s.Units {
		if err := copyFile(c.src(unit), "/etc/systemd/system/"+filepath.Base(unit), 0o644); err != nil {
			return err
		}
	}
	for guestPath, source := range s.Files {
		// #nosec G301 -- a guest config dir (e.g. /etc/caddy) must stay readable by the service, which often runs as its own user
		if err := os.MkdirAll(filepath.Dir(guestPath), 0o755); err != nil {
			return err
		}
		if err := copyFile(c.src(source), guestPath, 0o644); err != nil {
			return err
		}
	}
	if s.Setup != "" {
		if out, err := c.sh(5*time.Minute, "bash", c.src(s.Setup)); err != nil {
			return fmt.Errorf("setup: %w: %s", err, oracle.LastLines(out, 4))
		}
	}
	return nil
}

// --- audit window helpers (used by `mayfly detect`, detect.go) ---

func auditLineN() int {
	data, err := os.ReadFile(auditLog) //nolint:gosec // the audit log, inside the test VM
	if err != nil {
		return 0
	}
	return len(oracle.NonEmpty(string(data)))
}

func auditSince(n int) string {
	data, err := os.ReadFile(auditLog) //nolint:gosec // the audit log, inside the test VM
	if err != nil {
		return ""
	}
	lines := oracle.NonEmpty(string(data))
	if n > len(lines) {
		n = len(lines)
	}
	return strings.Join(lines[n:], "\n")
}

func auditExecsSince(n int) string {
	data, err := os.ReadFile(auditLog) //nolint:gosec // the audit log, inside the test VM
	if err != nil {
		return ""
	}
	lines := oracle.NonEmpty(string(data))
	if n > len(lines) {
		n = len(lines)
	}
	seen := map[string]bool{}
	var exes []string
	for _, line := range lines[n:] {
		if base := oracle.AuditExe(line); base != "" && !seen[base] {
			seen[base] = true
			exes = append(exes, base)
		}
	}
	sort.Strings(exes)
	return strings.Join(exes, " ")
}

// exitStatus maps a launched command's error to an exit code: 0 on success, the
// process's own code on a normal non-zero exit, -1 for a non-exit failure
// (timeout, could not start).
func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// --- commands ---

func (c *checker) sh(timeout time.Duration, name string, args ...string) (string, error) {
	out, _, err := c.shPID(timeout, name, args...)
	return out, err
}

// shPID runs a provisioning command and returns its combined output and pid. On
// timeout the whole process group is killed.
func (c *checker) shPID(timeout time.Duration, name string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- running the distro's tools is this program's job, in a throwaway VM
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = c.workDir
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "MAYFLY_DISTRO="+c.cfg.Distro)
	if b := c.installedBinary(); b != "" {
		cmd.Env = append(cmd.Env, "MAYFLY_BINARY="+b)
	}
	if c.cfg.Proxy != "" {
		for _, k := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY"} {
			cmd.Env = append(cmd.Env, k+"="+c.cfg.Proxy)
		}
		for _, k := range []string{"no_proxy", "NO_PROXY"} {
			cmd.Env = append(cmd.Env, k+"=localhost,127.0.0.1,::1")
		}
	}
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

// runScript runs the consumer's entrypoint (bash) with the given environment,
// as root, capturing combined output and returning its exit status. The whole
// process group is killed on timeout.
func runScript(path string, env []string) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", path) // #nosec G204 -- the consumer's entrypoint, in a throwaway VM
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), -1
	}
	return string(out), exitStatus(err)
}

func have(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// --- files ---

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src) //nolint:gosec // our own payload files
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, mode); err != nil { // #nosec G306 -- modes are deliberate
		return err
	}
	return os.Chmod(dst, mode) // WriteFile keeps an existing file's mode
}

func appendFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600) //nolint:gosec // a system config path constant
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	return errors.Join(err, f.Close())
}

func dropTaskNever(glob string) ([]string, error) {
	var removed []string
	err := rewriteFiles(glob, func(text string) string {
		lines := strings.Split(text, "\n")
		for i, l := range lines {
			if oracle.TaskNever.MatchString(l) {
				removed = append(removed, strings.TrimSpace(l))
				lines[i] = "# removed by mayfly (turns syscall auditing off): " + l
			}
		}
		return strings.Join(lines, "\n")
	})
	return removed, err
}

func rewriteFiles(glob string, edit func(string) string) error {
	files, _ := filepath.Glob(glob)
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(f) //nolint:gosec // the distro's own config files
		if err != nil {
			return err
		}
		if err := os.WriteFile(f, []byte(edit(string(data))), fi.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func dedup(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
