// Command distrocheck runs inside one throwaway VM of the distro matrix, as
// root, over SSH. It installs the subjects the host describes in config.json,
// runs the checks, prints a report on stdout, and exits. The host (cmd/matrix)
// scps this binary and the payload in, runs it, reads the report, and tears the
// VM down.
//
// It must never run anywhere but such a VM: it installs packages, rewrites the
// audit rules, enables units and runs the benign malware look-alikes a consumer
// passes as detect checks.
//
// The report travels on stdout: every line beginning reportMarker is a JSON
// report. The oracle prints one after every check (so a guest that dies mid-run
// still tells the host how far it got); the host keeps the last.
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
	testUser   = "alice" // detect checks with as="user" run under this unprivileged user
	auditLog   = "/var/log/audit/audit.log"
	auditRules = "/etc/audit/rules.d"
	// scanSettle is how long a drain waits for auditd to flush the previous check's tail before
	// re-scanning, so a straggler exec is attributed to its own check.
	scanSettle = 2 * time.Second
	// maxDrain bounds the quiescence loop so a chatty host cannot hang the matrix; a quiet VM
	// settles in one iteration.
	maxDrain = 8
	// reportMarker prefixes every report line on stdout, so the host finds the report among the
	// guest's own chatter and keeps the last (complete) one.
	reportMarker = "MATRIX-REPORT:"
)

func main() {
	// The same binary is the in-guest `mayfly` CLI (spec 9): if the first argument
	// is a reporting verb, act as the CLI. Otherwise run the engine (the host
	// invokes `distrocheck -root ...`).
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

// fatal reports a guest that could not even start checking, so the host sees a
// reason instead of a parse failure.
func fatal(err error) {
	emit(report.Report{Checks: []report.Check{{ID: "setup", Detail: "distrocheck: " + err.Error()}}})
	os.Exit(1)
}

// emit prints the report as one marked JSON line on stdout.
func emit(r report.Report) {
	data, err := json.Marshal(r)
	if err != nil {
		data = []byte(`{"checks":[{"id":"setup","detail":"distrocheck: cannot encode report"}]}`)
	}
	fmt.Println(reportMarker + string(data))
}

type checker struct {
	cfg     report.Config
	root    string
	rep     report.Report
	workDir string // for shIn
}

func (c *checker) src(rel string) string { return filepath.Join(c.root, rel) }

func (c *checker) check(id string, pass bool, detail string) {
	c.record(report.Check{ID: id, Pass: pass, Detail: trim(detail, 400)})
}

func (c *checker) skip(id, detail string) {
	c.record(report.Check{ID: id, Pass: true, Skip: true, Detail: trim(detail, 400)})
}

func (c *checker) record(ck report.Check) {
	c.rep.Checks = append(c.rep.Checks, ck)
	emit(c.rep) // a partial report after every check
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func (c *checker) run() {
	c.recordPlatform()
	// Install, then provision every subject, then (if anything needs the audit log) set auditd up.
	// A setup failure reports every remaining check as not reached rather than dropping it.
	steps := []struct {
		what string
		fn   func() error
	}{
		{"install packages", c.installPackages},
		{"audit setup", c.auditSetup},
		{"provision subjects", c.provision},
	}
	for _, s := range steps {
		if err := s.fn(); err != nil {
			c.check("setup", false, s.what+": "+err.Error())
			for _, ck := range c.cfg.Checks {
				for _, id := range checkIDs(ck) {
					c.check(id, false, "not reached: setup failed")
				}
			}
			return
		}
	}
	c.check("setup", true, "")
	defer c.recordAuditState()
	for _, ck := range c.cfg.Checks {
		c.runCheck(ck)
	}
}

// checkIDs is every report id a check produces: its own id, plus one per script id.
func checkIDs(ck report.CheckSpec) []string {
	if ck.Kind == "script" && len(ck.IDs) > 0 {
		out := make([]string, len(ck.IDs))
		for i, id := range ck.IDs {
			out[i] = ck.ID + "/" + id
		}
		return out
	}
	return []string{ck.ID}
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

// checkTools is the union of every command the checks name. required is the
// subset whose absence fails the distro; optional ones a missing tool skips.
func (c *checker) checkTools() (required, optional []string) {
	req, opt := map[string]bool{}, map[string]bool{}
	for _, ck := range c.cfg.Checks {
		for _, t := range ck.Tools {
			if ck.Optional {
				opt[t] = true
			} else {
				req[t] = true
			}
		}
	}
	for t := range req {
		required = append(required, t)
	}
	for t := range opt {
		if !req[t] {
			optional = append(optional, t)
		}
	}
	sort.Strings(required)
	sort.Strings(optional)
	return required, optional
}

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
	// Subject packages first: generic names mapped to this distro's spelling, installed via its own
	// package manager. The pm's install is idempotent, so an already-present package is a no-op.
	subjectPkgs, err := c.subjectPackages()
	if err != nil {
		return err
	}
	// Required check tools: commands, mapped to packages, installed when absent.
	required, optional := c.checkTools()
	var missing []string
	for _, cmd := range required {
		if !have(cmd) {
			missing = append(missing, c.pkg(cmd))
		}
	}
	install := dedup(append(subjectPkgs, missing...))
	if len(install) > 0 {
		c.rep.Info["installed"] = strings.Join(install, " ")
		if out, err := c.pmInstallRetry(install); err != nil {
			return fmt.Errorf("install %s on %s: %w: %s", strings.Join(install, " "), c.cfg.Distro, err, oracle.LastLines(out, 3))
		}
	}
	for _, cmd := range required {
		if !have(cmd) {
			return fmt.Errorf("%s still missing after install", cmd)
		}
	}
	// Optional tools are best effort: a distro whose repos lack one just skips the check that needs it.
	var optMissing []string
	for _, cmd := range optional {
		if !have(cmd) {
			optMissing = append(optMissing, c.pkg(cmd))
		}
	}
	if optMissing = dedup(optMissing); len(optMissing) > 0 {
		if out, err := c.pmInstallRetry(optMissing); err != nil {
			c.rep.Info["optional_unavailable"] = strings.Join(optMissing, " ") + ": " + oracle.LastLines(out, 2)
		}
	}
	return nil
}

// pmInstallRetry installs with the distro's package manager, retrying a few
// times with backoff: a mirror hiccup or a transient network failure is the
// common case on a fresh cloud image, and is not a reason to fail the distro. A
// genuine failure (a package that does not exist) returns the last error, which
// the caller wraps with the distro and package names.
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

// pkg maps a command to its package on this distro, defaulting to the command
// name when the distro attr does not name it.
func (c *checker) pkg(cmd string) string {
	if p, ok := c.cfg.Packages[cmd]; ok {
		return p
	}
	return cmd
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

// needsAudit reports whether any check reads the audit log: a detect check
// builds its process subtree from it, so the engine must get auditd running and
// syscall auditing on before provisioning. A matrix of only script/exit checks
// needs none of this.
func (c *checker) needsAudit() bool {
	for _, ck := range c.cfg.Checks {
		if ck.Kind == "detect" {
			return true
		}
	}
	return false
}

// auditSetup enables auditd, loads whatever rules a subject shipped under
// /etc/audit/rules.d, drops any stock "-a task,never" (which turns syscall
// auditing off on Fedora and openSUSE), and enables auditing. A subject that
// ships no audit rules and a matrix with no detect check skip this entirely.
func (c *checker) auditSetup() error {
	if !c.needsAudit() {
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
	if !c.needsAudit() {
		return
	}
	if have("augenrules") {
		_, _ = c.sh(2*time.Minute, "augenrules", "--load")
	}
	// augenrules can compile rules.d without loading them (seen on Fedora). Load each file straight
	// into the kernel too; idempotent (a duplicate is a harmless "rule exists").
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
	// The test user every detect check with as="user" runs under.
	if out, err := c.sh(time.Minute, "useradd", "-m", testUser); err != nil && !strings.Contains(out, "exists") {
		return fmt.Errorf("useradd %s: %w: %s", testUser, err, out)
	}
	// A detect check's emulation is launched as that unprivileged user, but the
	// payload (distrocheck + the check scripts) is staged under the run user's
	// home, which useradd makes 0700. Without this the test user cannot even
	// traverse to the script and every as="user" emulation dies with "Permission
	// denied" before it runs -- so nothing is ever detected. Open the traversal
	// path to the payload root and make its tree world-readable/executable
	// (o+rX: dirs and already-executable files only). Harmless in a throwaway VM.
	for dir := c.root; dir != "/" && dir != "." && dir != ""; dir = filepath.Dir(dir) {
		_, _ = c.sh(time.Minute, "chmod", "o+x", dir)
	}
	if _, err := c.sh(time.Minute, "chmod", "-R", "o+rX", c.root); err != nil {
		return fmt.Errorf("open payload to %s: %w", testUser, err)
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
// /usr/local/bin/<name> (on PATH) by default. Exported to stage commands via
// $MAYFLY_BINARY, so a check never hard-codes the path.
func installDst(s report.Subject) string {
	if s.InstallPath != "" {
		return s.InstallPath
	}
	return "/usr/local/bin/" + s.Name
}

// installedBinary is the guest path of the thing under test, for $MAYFLY_BINARY.
// The first install that ships a binary wins (the common case is one); "" when
// no binary was installed (a packages-only or distro-only run).
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

// --- checks ---

func (c *checker) runCheck(ck report.CheckSpec) {
	// An optional check whose tool did not install is skipped, not failed.
	if ck.Optional {
		for _, t := range ck.Tools {
			if !have(t) {
				c.skip(ck.ID, "optional tool "+t+" not available on this distro")
				return
			}
		}
	}
	switch ck.Kind {
	case "detect":
		c.detect(ck)
	case "script":
		c.script(ck)
	case "exit":
		c.exitCheck(ck)
	case "sandbox":
		c.sandbox(ck)
	case "lsm":
		c.lsm(ck)
	default:
		c.check(ck.ID, false, "unknown check kind "+ck.Kind)
	}
}

// findings is every OCSF line any subject has written so far.
func (c *checker) findings() []string {
	var lines []string
	for _, s := range c.cfg.Subjects {
		if s.OCSF == "" {
			continue
		}
		files, _ := filepath.Glob(s.OCSF)
		sort.Strings(files)
		for _, f := range files {
			if data, err := os.ReadFile(f); err == nil { //nolint:gosec // our own finding files, inside the test VM
				lines = append(lines, oracle.NonEmpty(string(data))...)
			}
		}
	}
	return lines
}

// drive runs every batch subject's scan command once (banken's oneshot scan).
// A subject with no scan command is a running daemon and needs no driving.
func (c *checker) drive() {
	for _, s := range c.cfg.Subjects {
		if len(s.Scan) == 0 {
			continue
		}
		_, _ = c.sh(2*time.Minute, s.Scan[0], s.Scan[1:]...)
	}
}

// drainQuiescent drives the subjects until a full settle passes with no new
// finding, so every exec is read and attributed before a count is taken.
// Bounded by maxDrain. If no subject needs driving, it is a single settle.
func (c *checker) drainQuiescent() {
	driven := false
	for _, s := range c.cfg.Subjects {
		if len(s.Scan) > 0 {
			driven = true
		}
	}
	if !driven {
		time.Sleep(scanSettle)
		return
	}
	for i := 0; i < maxDrain; i++ {
		c.drive()
		n := len(c.findings())
		time.Sleep(scanSettle)
		c.drive()
		if len(c.findings()) == n {
			return
		}
	}
}

// emulSkip is what an emulation prints when the tool it needs is not installable
// on this distro; the detect loop then skips rather than records a miss.
const emulSkip = "BANKEN-EMUL-SKIP"

// detect runs the emulation under its own launcher, builds the launcher's
// process subtree from the audit window, attributes every finding whose process
// is in the subtree, and checks the fired rule set equals expect exactly.
func (c *checker) detect(ck report.CheckSpec) {
	c.drainQuiescent() // a prior check's late exec lands in the pre-count, not this check's fired set
	before := len(c.findings())
	auditBefore := auditLineN()
	out, launcher, emulExit := c.runEmulation(ck)
	if strings.Contains(out, emulSkip) {
		c.skip(ck.ID, oracle.FirstLine(strings.TrimSpace(out)))
		return
	}
	c.drainQuiescent() // this check's own late-flushing exec is counted
	subtree := oracle.Descendants(oracle.AuditProcTree(auditSince(auditBefore)), launcher)
	// The emulation's exit status is evidence: if the launcher could not run the
	// attack at all, the subtree is empty and nothing fires -- which, against an
	// empty expect set, would otherwise read as a (vacuous) PASS. Fail instead.
	if ran, why := oracle.EmulationRan(emulExit, len(subtree)); !ran {
		c.check(ck.ID, false, "emulation did not run: "+why+": "+oracle.LastLines(out, 2))
		return
	}
	fired := map[string]bool{}
	offending := map[string]string{}
	for _, line := range oracle.From(c.findings(), before) {
		if pid, ppid := oracle.AlertProcess(line); !oracle.InSubtree(subtree, pid, ppid) {
			continue // another check's straggler or a background daemon, not this one
		}
		rule, ok := oracle.AnalyticName(line)
		if !ok {
			continue
		}
		fired[rule] = true
		if _, seen := offending[rule]; !seen {
			offending[rule] = line
		}
	}
	got := oracle.Keys(fired)
	want := append([]string(nil), ck.Expect...)
	sort.Strings(want)
	msg := fmt.Sprintf("fired %v, expected %v", got, want)
	if !oracle.Equal(got, want) {
		if execs := auditExecsSince(auditBefore); execs != "" {
			msg += "; execs: " + execs
		}
		wantSet := map[string]bool{}
		for _, r := range want {
			wantSet[r] = true
		}
		var extras []string
		for _, r := range got {
			if !wantSet[r] {
				extras = append(extras, "extra "+r+": "+oracle.FirstLine(offending[r]))
			}
		}
		if len(extras) > 0 {
			msg += "\n  " + strings.Join(extras, "\n  ")
		}
	}
	c.check(ck.ID, oracle.Equal(got, want), msg)
}

// runEmulation runs a detect check's script under a launcher process whose pid
// roots the check's subtree. as="root" launches directly; as="user" (default)
// goes through runuser, with a login-style PATH so tools in sbin resolve.
func (c *checker) runEmulation(ck report.CheckSpec) (out string, pid, exit int) {
	script := c.src(ck.Run)
	var err error
	if ck.As == "root" {
		out, pid, err = c.shPID(2*time.Minute, "bash", script)
	} else {
		const loginPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
		out, pid, err = c.shPID(2*time.Minute, "runuser", "-u", testUser, "--", "bash", "-c",
			"export PATH="+loginPath+":$PATH; bash "+script)
	}
	time.Sleep(time.Second) // let auditd write the records
	return out, pid, exitStatus(err)
}

// exitStatus maps a launched command's error to an exit code: 0 on success, the
// process's own code on a normal non-zero exit, and -1 for a non-exit failure
// (timeout, could not start). The emulation's exit status is evidence: a detect
// check cannot tell "ran, fired nothing" from "never ran" without it.
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

// script runs a stage script as root and parses its "<id> PASS|FAIL|SKIP <detail>"
// lines, recording one check per declared id ("<check>/<id>").
func (c *checker) script(ck report.CheckSpec) {
	out, err := c.sh(20*time.Minute, "bash", c.src(ck.Run), c.cfg.PM, c.cfg.Distro, testUser)
	results := parseStages(out)
	for _, id := range ck.IDs {
		r, ok := results[id]
		if !ok {
			detail := ck.ID + " did not report " + id + ": " + oracle.LastLines(out, 3)
			if err != nil {
				detail = fmt.Sprintf("%s failed before %s (%v): %s", ck.ID, id, err, oracle.LastLines(out, 3))
			}
			c.check(ck.ID+"/"+id, false, detail)
			continue
		}
		if r.skip {
			c.skip(ck.ID+"/"+id, r.detail)
			continue
		}
		c.check(ck.ID+"/"+id, r.pass, r.detail)
	}
}

// exitCheck is the dead-simple tier: run the script as root, exit 0 is a pass.
// This is `test = ./check.sh` in the module API.
func (c *checker) exitCheck(ck report.CheckSpec) {
	out, err := c.sh(10*time.Minute, "bash", c.src(ck.Run))
	c.check(ck.ID, err == nil, strings.TrimSpace(oracle.LastLines(out, 6)))
}

// sandbox (built-in): systemd's own rating of the unit's hardening must be SAFE
// or OK on this distro's systemd.
func (c *checker) sandbox(ck report.CheckSpec) {
	out, _ := c.sh(time.Minute, "systemd-analyze", "security", ck.Unit, "--no-pager")
	score, level, ok := oracle.Exposure(out)
	if !ok {
		c.check(ck.ID, false, "no exposure score from systemd-analyze security: "+oracle.LastLines(out, 2))
		return
	}
	c.rep.Info["exposure/"+ck.Unit] = score + " " + level
	c.check(ck.ID, level == "SAFE" || level == "OK", "exposure "+score+" "+level)
}

// lsm (built-in): the distro's SELinux or AppArmor, as shipped, logged no denial
// mentioning the subject.
func (c *checker) lsm(ck report.CheckSpec) {
	var denials []string
	if have("ausearch") {
		out, _ := c.sh(time.Minute, "ausearch", "-m", "AVC,USER_AVC", "-ts", "boot", "-i")
		denials = append(denials, oracle.LinesContaining(out, ck.Match)...)
	}
	out, _ := c.sh(time.Minute, "journalctl", "-k", "--no-pager", "-g", `apparmor="DENIED"`)
	denials = append(denials, oracle.LinesContaining(out, ck.Match)...)
	c.check(ck.ID, len(denials) == 0, fmt.Sprintf("lsm=%s selinux=%s; denials: %s",
		c.rep.Info["lsm"], c.rep.Info["selinux"], strings.Join(oracle.FirstN(denials, 3), " | ")))
}

// --- audit window helpers ---

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

// --- stage parsing ---

type stageResult struct {
	pass, skip bool
	detail     string
}

// parseStages reads a stage script's "<id> PASS|FAIL|SKIP <detail>" lines into a
// map keyed by id. A SKIP line is recorded, not failed.
func parseStages(out string) map[string]stageResult {
	res := map[string]stageResult{}
	for _, line := range oracle.NonEmpty(out) {
		parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
		if len(parts) < 2 {
			continue
		}
		verdict := parts[1]
		if verdict != "PASS" && verdict != "FAIL" && verdict != "SKIP" {
			continue
		}
		detail := ""
		if len(parts) == 3 {
			detail = parts[2]
		}
		res[parts[0]] = stageResult{pass: verdict == "PASS", skip: verdict == "SKIP", detail: detail}
	}
	return res
}

// --- commands ---

func (c *checker) sh(timeout time.Duration, name string, args ...string) (string, error) {
	out, _, err := c.shPID(timeout, name, args...)
	return out, err
}

// shPID runs a command with the host's proxy (if any) in its environment and
// returns its combined output and the launched process's pid. On timeout the
// whole process group is killed (an emulation's curl or nc would otherwise
// outlive its shell).
func (c *checker) shPID(timeout time.Duration, name string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- running the distro's tools is this program's job, in a throwaway VM
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = c.workDir
	cmd.Env = os.Environ()
	// A check script can name the distro and run the shipped binary without
	// hard-coding either: $MAYFLY_DISTRO is this distro's attr, $MAYFLY_BINARY the
	// installed binary's guest path (absent when no binary was installed).
	cmd.Env = append(cmd.Env, "MAYFLY_DISTRO="+c.cfg.Distro)
	if b := c.installedBinary(); b != "" {
		cmd.Env = append(cmd.Env, "MAYFLY_BINARY="+b)
	}
	if c.cfg.Proxy != "" {
		for _, k := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY"} {
			cmd.Env = append(cmd.Env, k+"="+c.cfg.Proxy)
		}
		// Never proxy loopback: a check almost always curls the service it just
		// started on 127.0.0.1, and routing that through the host proxy fails
		// confusingly (hit in the caddy example). Exempt localhost so test/setup
		// scripts reach their own service without a --noproxy workaround.
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
