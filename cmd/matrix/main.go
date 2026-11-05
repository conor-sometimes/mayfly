// Command matrix is the host side of the distro matrix. It boots each enabled
// distro from its pinned cloud image under KVM, provisions it over SSH, runs
// the in-guest oracle (distrocheck), reads the report back on SSH stdout, and
// tears every VM down. One distro's failure never stops another; the process
// exits non-zero if any check failed.
//
// Two ways in, one engine:
//
//   - A manifest (the pure Nix path). -manifest names a JSON that mkMatrix
//     built: the enabled distros with their store-path images, and the shared
//     subjects and checks. Everything is in the store.
//
//   - CLI flags (the impure "way 1" path). -binary, -unit, -file, -package,
//     -test and -check assemble one subject and its checks from the working
//     directory; -catalog names the distro catalog (store images). -distro
//     filters to a subset.
//
// Teardown is guaranteed twice over: this process kills every QEMU and removes
// every work dir on exit or signal, and the app wrapper's shell trap repeats it
// if this process is killed outright.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/conor-sometimes/mayfly/internal/report"
)

// Distro is one bootable image plus the per-distro knowledge the guest needs.
type Distro struct {
	Name     string            `json:"name"`
	Title    string            `json:"title"`
	Image    string            `json:"image"` // store path to the qcow2
	PM       string            `json:"pm"`
	Family   string            `json:"family"`
	Packages map[string]string `json:"packages"`
	Boot     Boot              `json:"boot"`
	// Plan is this distro's applicable check ids, already filtered by each entry's
	// `when` predicate in Nix (spec 4.3). When nil the shared Manifest.Plan applies.
	Plan []report.PlanEntry `json:"plan"`
}

// Boot overrides an image's resources. Zero values take the defaults.
type Boot struct {
	Mem  int `json:"mem"`  // MiB, default 2048
	CPUs int `json:"cpus"` // default 2
	Disk int `json:"disk"` // GiB, default max(image size, 20)
}

// Manifest is what mkMatrix hands the runner: the enabled distros and the
// shared subjects and checks. Payload is the dir holding distrocheck and every
// source file the subjects and checks reference.
type Manifest struct {
	Distros    []Distro           `json:"distros"`
	Subjects   []report.Subject   `json:"subjects"`
	Entrypoint string             `json:"entrypoint"` // payload-relative; the consumer's one script
	Plan       []report.PlanEntry `json:"plan"`       // the shared plan, per-distro filtered in Distro.Plan
	TestSugar  bool               `json:"test_sugar"`
	TestUser   string             `json:"test_user"`
	Audit      bool               `json:"audit"`
	Payload    string             `json:"payload"`
}

const (
	minDiskGiB   = 20
	reportMarker = "MATRIX-REPORT:"
)

var (
	manifestPath = flag.String("manifest", "", "JSON manifest of distros, subjects, checks and the payload dir (the pure Nix path)")
	catalogPath  = flag.String("catalog", "", "JSON array of distros with store images (baked into the default app)")
	distrocheck  = flag.String("distrocheck", "", "store path to the distrocheck binary (for the CLI-flag path)")
	payloadFlag  = flag.String("payload", "", "payload dir holding distrocheck (defaults to the manifest's)")
	parallel     = flag.Int("parallel", 0, "max distros booted at once (0 = bound by host CPU and RAM)")
	keep         = flag.Bool("keep", false, "keep each VM's work dir and print where, pass or fail")
	jsonOut      = flag.String("json", "", "also write the full report array to this file")
	bootTimeout  = flag.Duration("boot-timeout", 30*time.Minute, "longest one distro may take, boot to report")
	sshTimeout   = flag.Duration("ssh-timeout", 8*time.Minute, "longest to wait for the guest to answer SSH")
	initTimeout  = flag.Duration("cloud-init-timeout", 10*time.Minute, "longest to wait for cloud-init to finish")
	distroFilter stringList
	// CLI (way 1) subject assembly:
	binary      = flag.String("binary", "", "a static subject binary to install (CLI path)")
	installPath = flag.String("install-path", "", "where to install -binary (default /usr/local/bin/<name>)")
	test        = flag.String("test", "", "a check script; exit 0 is a pass (CLI path)")
	subjectName = flag.String("name", "subject", "the subject's name (CLI path)")
	unitList    stringList
	fileList    stringList
	pkgList     stringList
	enableList  stringList
)

func main() {
	flag.Var(&distroFilter, "distro", "only this distro (repeatable); default every enabled distro")
	flag.Var(&unitList, "unit", "a systemd unit file to install and enable (repeatable, CLI path)")
	flag.Var(&fileList, "file", "guest=source file to copy (repeatable, CLI path)")
	flag.Var(&pkgList, "package", "a generic package to install (repeatable, CLI path)")
	flag.Var(&enableList, "enable", "a unit to enable --now (repeatable, CLI path)")
	flag.Parse()

	m, err := loadManifest()
	if err != nil {
		fmt.Fprintln(os.Stderr, "matrix:", err)
		os.Exit(2)
	}
	if err := requireKVM(); err != nil {
		fmt.Fprintln(os.Stderr, "matrix:", err)
		os.Exit(2)
	}
	os.Exit(runMatrix(m))
}

// loadManifest builds the run manifest from -manifest (the pure Nix path), or
// assembles one from the CLI flags against -catalog (the no-Nix path).
func loadManifest() (Manifest, error) {
	if *manifestPath != "" {
		var m Manifest
		data, err := os.ReadFile(*manifestPath)
		if err != nil {
			return m, err
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return m, fmt.Errorf("parse manifest: %w", err)
		}
		if *payloadFlag != "" {
			m.Payload = *payloadFlag
		}
		if m.Payload == "" {
			return m, fmt.Errorf("manifest has no payload dir")
		}
		m.Distros = filterDistros(m.Distros, distroFilter)
		if len(m.Distros) == 0 {
			return m, fmt.Errorf("no distros selected")
		}
		return m, nil
	}
	return assembleImpure()
}

// runSpec is one subject and its checks, assembled from the CLI flags (the
// no-Nix one-off path). The mayfly.nix module expresses the same fields, typed.
type runSpec struct {
	Distros     []string
	Name        string
	Binary      string
	InstallPath string
	Packages    []string
	Unit        string
	Units       []string
	Enable      []string
	Files       map[string]string
	Setup       string
	Test        string
}

// assembleImpure builds the manifest for the CLI-flag path: a subject and a test
// from the working directory, staged (with distrocheck) into a payload dir the
// guest reads. The Nix-native path builds a manifest in the store instead and
// the runner takes it through -manifest.
func assembleImpure() (Manifest, error) {
	var m Manifest
	if *catalogPath == "" {
		return m, fmt.Errorf("no -manifest and no -catalog; run through the mayfly app")
	}
	data, err := os.ReadFile(*catalogPath)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m.Distros); err != nil {
		return m, fmt.Errorf("parse catalog: %w", err)
	}

	run, err := loadRun()
	if err != nil {
		return m, err
	}

	payload, err := os.MkdirTemp(runRoot(), "payload-")
	if err != nil {
		return m, err
	}
	if *distrocheck != "" {
		if err := copyInto(*distrocheck, filepath.Join(payload, "distrocheck")); err != nil {
			return m, fmt.Errorf("stage distrocheck: %w", err)
		}
		_ = os.Chmod(filepath.Join(payload, "distrocheck"), 0o755) // #nosec G302 -- an executable needs its exec bit
	}
	m.Payload = payload

	name := run.Name
	if name == "" {
		name = "subject"
	}
	s := report.Subject{
		Name: name, InstallPath: run.InstallPath, Packages: run.Packages,
		Enable: run.Enable, Files: map[string]string{},
	}
	stage := func(src, rel string) error { return copyInto(src, filepath.Join(payload, rel)) }

	if run.Binary != "" {
		rel := "subjects/" + name + "/bin"
		if err := stage(run.Binary, rel); err != nil {
			return m, fmt.Errorf("stage binary: %w", err)
		}
		s.Binary = rel
	}
	units := run.Units
	if run.Unit != "" {
		units = append(units, run.Unit)
	}
	for _, u := range units {
		// Keep the original unit name, under a per-subject units/ dir: the guest
		// installs it as this basename, so `systemctl start <unit>` finds it.
		rel := fmt.Sprintf("subjects/%s/units/%s", name, filepath.Base(u))
		if err := stage(u, rel); err != nil {
			return m, fmt.Errorf("stage unit %s: %w", u, err)
		}
		s.Units = append(s.Units, rel)
		if len(run.Enable) == 0 {
			s.Enable = append(s.Enable, filepath.Base(u))
		}
	}
	i := 0
	for guest, src := range run.Files {
		rel := fmt.Sprintf("subjects/%s/file%d", name, i)
		if err := stage(src, rel); err != nil {
			return m, fmt.Errorf("stage file %s: %w", src, err)
		}
		s.Files[guest] = rel
		i++
	}
	if run.Setup != "" {
		rel := "subjects/" + name + "/setup"
		if err := stage(run.Setup, rel); err != nil {
			return m, fmt.Errorf("stage setup: %w", err)
		}
		s.Setup = rel
	}
	m.Subjects = []report.Subject{s}

	if run.Test != "" {
		rel := "entrypoint"
		if err := stage(run.Test, rel); err != nil {
			return m, fmt.Errorf("stage test: %w", err)
		}
		m.Entrypoint = rel
		m.TestSugar = true
		m.Plan = []report.PlanEntry{{ID: "test"}}
	}

	want := run.Distros
	if len(distroFilter) > 0 {
		want = distroFilter
	}
	m.Distros = filterDistros(m.Distros, want)
	if len(m.Distros) == 0 {
		return m, fmt.Errorf("no distros selected; name catalog distros with -distro")
	}
	return m, nil
}

// loadRun assembles the run spec from the CLI flags. Structured detect checks
// are the Nix-native (mayfly.nix module) path, not the flag path; the flag path
// is a static binary and a pass/fail test.
func loadRun() (runSpec, error) {
	if *binary == "" && *test == "" && len(unitList) == 0 && len(fileList) == 0 {
		return runSpec{}, fmt.Errorf("no -manifest and no subject flags; write a mayfly.nix or pass -binary/-test (see the README)")
	}
	run := runSpec{
		Name: *subjectName, Binary: *binary, InstallPath: *installPath,
		Packages: pkgList, Units: unitList, Enable: enableList, Test: *test, Files: map[string]string{},
	}
	for _, f := range fileList {
		guest, src, ok := strings.Cut(f, "=")
		if !ok {
			return run, fmt.Errorf("-file wants guest=source, got %q", f)
		}
		run.Files[guest] = src
	}
	return run, nil
}

func filterDistros(all []Distro, only []string) []Distro {
	if len(only) == 0 {
		return all
	}
	want := map[string]bool{}
	for _, n := range only {
		want[n] = true
	}
	var out []Distro
	for _, d := range all {
		if want[d.Name] {
			out = append(out, d)
		}
	}
	return out
}

// runMatrix boots every selected distro, bounded by parallelism, and prints a
// go-test-style report. It returns the process exit code.
func runMatrix(m Manifest) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	n := *parallel
	if n <= 0 {
		n = hostParallelism()
	}
	if n > len(m.Distros) {
		n = len(m.Distros)
	}
	fmt.Printf("matrix: %d distro(s), %d at a time\n", len(m.Distros), n)

	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	reports := make([]report.Report, len(m.Distros))

	for i, d := range m.Distros {
		wg.Add(1)
		go func(i int, d Distro) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			rep := bootOne(ctx, m, d)
			mu.Lock()
			reports[i] = rep
			mu.Unlock()
		}(i, d)
	}
	wg.Wait()

	return summarize(m.Distros, reports)
}

// renderStatus maps a protocol status to the grid vocabulary (spec 6.3).
func renderStatus(s string) string {
	switch s {
	case "pass":
		return "PASS"
	case "fail":
		return "FAIL"
	case "skip":
		return "SKIP"
	case "error":
		return "ERROR"
	case "xfail":
		return "XFAIL"
	case "xpass":
		return "XPASS"
	case "", "na":
		return "-"
	}
	return strings.ToUpper(s)
}

// statusFails reports whether a status makes the run fail (spec 6.3).
func statusFails(s string) bool { return s == "fail" || s == "error" || s == "xpass" }

func summarize(distros []Distro, reports []report.Report) int {
	failed := 0
	for i, d := range distros {
		rep := reports[i]
		if len(rep.Checks) == 0 {
			fmt.Printf("--- FAIL: %s (no report; booted or provisioned?)\n", d.Name)
			failed++
			continue
		}
		distroFailed := false
		for _, ck := range rep.Checks {
			r := renderStatus(ck.Status)
			switch {
			case statusFails(ck.Status):
				fmt.Printf("--- %s: %s/%s\n    %s\n", r, d.Name, ck.ID, ck.Detail)
				distroFailed = true
			case ck.Status == "skip":
				fmt.Printf("--- SKIP: %s/%s %s\n", d.Name, ck.ID, ck.Detail)
			default:
				fmt.Printf("--- %s: %s/%s\n", r, d.Name, ck.ID)
			}
		}
		for _, k := range sortedKeys(rep.Info) {
			fmt.Printf("    %s: %s = %s\n", d.Name, k, rep.Info[k])
		}
		if distroFailed {
			failed++
		}
	}
	if t := matrixTable(distros, reports); t != "" {
		fmt.Printf("\nsummary:\n%s", t)
	}
	if *jsonOut != "" {
		if data, err := json.MarshalIndent(reports, "", "  "); err == nil {
			_ = os.WriteFile(*jsonOut, data, 0o600) // a report the user asked us to write
		}
	}
	if failed > 0 {
		fmt.Printf("FAIL: %d/%d distro(s) failed\n", failed, len(distros))
		return 1
	}
	fmt.Printf("ok: all %d distro(s) passed\n", len(distros))
	return 0
}

// matrixTable renders the at-a-glance grid that reads a multi-distro run at a
// glance: rows are distros, columns are check ids, and each cell is PASS, FAIL,
// SKIP, "-" (the check's `when` did not apply to that distro) or ERR (the distro
// returned no report at all). The per-line output above stays the grep-able
// record; this is the human summary. Returns "" for a single cell, which is not
// a matrix. Pure (unit-tested in summary_test.go).
func matrixTable(distros []Distro, reports []report.Report) string {
	var cols []string
	seen := map[string]bool{}
	for _, rep := range reports {
		for _, ck := range rep.Checks {
			if !seen[ck.ID] {
				seen[ck.ID] = true
				cols = append(cols, ck.ID)
			}
		}
	}
	if len(distros) < 2 && len(cols) < 2 {
		return ""
	}

	cell := func(rep report.Report, id string) string {
		if len(rep.Checks) == 0 {
			return "ERR"
		}
		for _, ck := range rep.Checks {
			if ck.ID == id {
				return renderStatus(ck.Status)
			}
		}
		return "-"
	}

	nameW := len("distro")
	for _, d := range distros {
		if len(d.Name) > nameW {
			nameW = len(d.Name)
		}
	}
	colW := make([]int, len(cols))
	for j, c := range cols {
		if colW[j] = len(c); colW[j] < 4 {
			colW[j] = 4
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-*s", nameW, "distro")
	for j, c := range cols {
		fmt.Fprintf(&b, "  %-*s", colW[j], c)
	}
	b.WriteByte('\n')
	for i, d := range distros {
		fmt.Fprintf(&b, "%-*s", nameW, d.Name)
		for j, c := range cols {
			fmt.Fprintf(&b, "  %-*s", colW[j], cell(reports[i], c))
		}
		b.WriteByte('\n')
	}

	// Drop the padding whitespace the last column leaves at each line's end.
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

// bootOne runs one distro end to end and returns its report. Every resource it
// makes is removed by the deferred teardown, on success, failure or signal.
func bootOne(ctx context.Context, m Manifest, d Distro) (rep report.Report) {
	rep = report.Report{Distro: d.Name}
	work, err := os.MkdirTemp(runRoot(), d.Name+"-")
	if err != nil {
		rep.Info = map[string]string{"error": "work dir: " + err.Error()}
		return rep
	}

	ctx, cancel := context.WithTimeout(ctx, *bootTimeout)
	defer cancel()

	g := &guest{ctx: ctx, d: d, m: m, work: work}
	defer g.teardown()

	if err := g.run(&rep); err != nil {
		if rep.Info == nil {
			rep.Info = map[string]string{}
		}
		rep.Info["failed_phase"] = g.phase
		rep.Info["error"] = err.Error()
		if serial := g.serialTail(40); serial != "" {
			rep.Info["serial_tail"] = serial
		}
	}
	return rep
}

type guest struct {
	ctx   context.Context
	d     Distro
	m     Manifest
	work  string
	port  int
	qemu  *exec.Cmd
	phase string // the lifecycle phase in progress, for a per-phase failure report
}

func (g *guest) path(name string) string { return filepath.Join(g.work, name) }

// run walks the lifecycle one named phase at a time. A failure returns an error
// and g.phase names where it stopped (boot / ssh / cloud-init / copy / run);
// install, unit-start and test phases are the guest's own and come back in the
// report's "setup" check and per-check detail.
func (g *guest) run(rep *report.Report) error {
	g.phase = "overlay"
	overlay := g.path("overlay.qcow2")
	size := g.diskBytes()
	if out, err := output(g.ctx, "qemu-img", "create", "-q", "-f", "qcow2", "-F", "qcow2",
		"-b", g.d.Image, overlay, strconv.FormatInt(size, 10)); err != nil {
		return fmt.Errorf("qemu-img create: %w: %s", err, out)
	}
	g.phase = "seed"
	if err := g.writeSeed(); err != nil {
		return err
	}
	g.phase = "boot"
	port, err := freePort()
	if err != nil {
		return err
	}
	g.port = port
	if err := g.startQEMU(overlay); err != nil {
		return err
	}
	g.phase = "ssh"
	if err := g.waitSSH(); err != nil {
		return err
	}
	g.phase = "cloud-init"
	if err := g.waitCloudInit(); err != nil {
		return err
	}
	g.phase = "copy"
	if err := g.provisionConfig(); err != nil {
		return err
	}
	if err := g.copyPayload(); err != nil {
		return err
	}
	g.phase = "run"
	out, err := g.ssh(*bootTimeout, "sudo", remotePayload+"/distrocheck", "-root", remotePayload)
	r, ok := lastReport(out)
	if !ok {
		return fmt.Errorf("no report from distrocheck (%v): %s", err, tail(out, 20))
	}
	*rep = r
	rep.Distro = g.d.Name
	return nil
}

const remotePayload = "/home/matrix/payload"

// diskBytes is the overlay size: the image's own virtual size or minDiskGiB,
// whichever is larger, unless the distro overrides it.
func (g *guest) diskBytes() int64 {
	if g.d.Boot.Disk > 0 {
		return int64(g.d.Boot.Disk) << 30
	}
	min := int64(minDiskGiB) << 30
	out, err := output(g.ctx, "qemu-img", "info", "--output=json", g.d.Image)
	if err != nil {
		return min
	}
	var info struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if json.Unmarshal([]byte(out), &info) == nil && info.VirtualSize > min {
		return info.VirtualSize
	}
	return min
}

// writeSeed generates the per-run SSH key and the NoCloud seed ISO: a matrix
// user with passwordless sudo and that key. The key never leaves the host and
// dies with the run.
func (g *guest) writeSeed() error {
	key := g.path("id")
	if out, err := output(g.ctx, "ssh-keygen", "-t", "ed25519", "-N", "", "-f", key, "-q"); err != nil {
		return fmt.Errorf("ssh-keygen: %w: %s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return err
	}
	seedDir := g.path("seed")
	if err := os.MkdirAll(seedDir, 0o700); err != nil { // holds the run's cloud-init seed; owner-only
		return err
	}
	meta := fmt.Sprintf("instance-id: matrix-%s\nlocal-hostname: matrix-%s\n", g.d.Name, g.d.Name)
	user := "#cloud-config\n" +
		"users:\n" +
		"  - name: matrix\n" +
		"    sudo: 'ALL=(ALL) NOPASSWD:ALL'\n" +
		"    shell: /bin/bash\n" +
		"    lock_passwd: true\n" +
		"    ssh_authorized_keys:\n" +
		"      - " + strings.TrimSpace(string(pub)) + "\n" +
		"growpart:\n  mode: auto\n  devices: ['/']\n" +
		"ssh_pwauth: false\n"
	if err := os.WriteFile(filepath.Join(seedDir, "meta-data"), []byte(meta), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(seedDir, "user-data"), []byte(user), 0o600); err != nil {
		return err
	}
	iso := g.path("seed.iso")
	if out, err := output(g.ctx, "xorriso", "-as", "mkisofs", "-quiet", "-output", iso,
		"-volid", "cidata", "-joliet", "-rock", seedDir); err != nil {
		return fmt.Errorf("xorriso: %w: %s", err, out)
	}
	return nil
}

func (g *guest) startQEMU(overlay string) error {
	mem := g.d.Boot.Mem
	if mem == 0 {
		mem = 2048
	}
	cpus := g.d.Boot.CPUs
	if cpus == 0 {
		cpus = 2
	}
	args := []string{
		"-enable-kvm", "-cpu", "host", "-m", strconv.Itoa(mem), "-smp", strconv.Itoa(cpus),
		"-display", "none", "-monitor", "none", "-no-reboot", "-pidfile", g.path("qemu.pid"),
		"-drive", "file=" + overlay + ",if=none,id=root,format=qcow2",
		"-device", "virtio-blk-pci,drive=root,bootindex=0",
		"-drive", "file=" + g.path("seed.iso") + ",media=cdrom,readonly=on",
		"-netdev", fmt.Sprintf("user,id=n0,hostfwd=tcp:127.0.0.1:%d-:22", g.port),
		"-device", "virtio-net-pci,netdev=n0",
		"-serial", "file:" + g.path("serial.log"),
	}
	// Own process group, so teardown kills QEMU and any child with one signal.
	cmd := exec.Command("qemu-system-x86_64", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start qemu: %w", err)
	}
	g.qemu = cmd
	return nil
}

// waitSSH polls until the guest answers SSH, with a capped backoff, bounded by
// the SSH timeout. A guest that never comes up fails with a clear, distro-named
// error; the serial log is attached by the caller.
func (g *guest) waitSSH() error {
	deadline := time.Now().Add(*sshTimeout)
	backoff := 2 * time.Second
	for time.Now().Before(deadline) {
		if g.ctx.Err() != nil {
			return g.ctx.Err()
		}
		if _, err := g.ssh(15*time.Second, "true"); err == nil {
			return nil
		}
		time.Sleep(backoff)
		if backoff < 10*time.Second {
			backoff += 2 * time.Second
		}
	}
	return fmt.Errorf("%s did not answer SSH within %s", g.d.Name, *sshTimeout)
}

// waitCloudInit blocks until cloud-init has finished, so provisioning never
// races the first boot's user/key/growpart work. `cloud-init status --wait`
// prints dots until done and exits non-zero only on a real error; a degraded
// status still means "finished", so a non-zero exit is recorded, not fatal.
func (g *guest) waitCloudInit() error {
	out, err := g.ssh(*initTimeout, "cloud-init", "status", "--wait")
	if err != nil && strings.Contains(out, "command not found") {
		return nil // no cloud-init on this image; the key/user must already be in
	}
	if g.ctx.Err() != nil {
		return fmt.Errorf("%s: cloud-init did not finish within %s", g.d.Name, *initTimeout)
	}
	return nil
}

// provisionConfig writes the per-distro config.json into the run's own payload
// copy, merging this distro's facts with the shared subjects and checks.
func (g *guest) provisionConfig() error {
	plan := g.d.Plan
	if plan == nil {
		plan = g.m.Plan
	}
	cfg := report.Config{
		Distro: g.d.Name, PM: g.d.PM, Family: g.d.Family, Packages: g.d.Packages,
		Proxy: guestProxy(), Subjects: g.m.Subjects,
		Entrypoint: g.m.Entrypoint, Plan: plan, TestSugar: g.m.TestSugar,
		TestUser: g.m.TestUser, Audit: g.m.Audit,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(g.path("config.json"), data, 0o600)
}

// copyPayload scps the store payload and this run's config.json to the guest.
func (g *guest) copyPayload() error {
	if _, err := g.ssh(time.Minute, "rm", "-rf", remotePayload, "&&", "mkdir", "-p", remotePayload); err != nil {
		return err
	}
	if out, err := g.scp(g.m.Payload+"/.", remotePayload); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	if out, err := g.scp(g.path("config.json"), remotePayload+"/config.json"); err != nil {
		return fmt.Errorf("config: %w: %s", err, out)
	}
	return nil
}

func (g *guest) teardown() {
	if g.port != 0 {
		_, _ = g.ssh(10*time.Second, "sudo", "systemctl", "poweroff")
		time.Sleep(2 * time.Second)
	}
	if data, err := os.ReadFile(g.path("qemu.pid")); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	if g.qemu != nil && g.qemu.Process != nil {
		_ = g.qemu.Process.Kill()
		_, _ = g.qemu.Process.Wait()
	}
	if *keep {
		fmt.Printf("    %s: work dir kept: %s\n", g.d.Name, g.work)
		return
	}
	_ = os.RemoveAll(g.work)
}

func (g *guest) serialTail(n int) string {
	data, err := os.ReadFile(g.path("serial.log"))
	if err != nil {
		return ""
	}
	return tail(string(data), n)
}

// --- ssh/scp ---

func sshOpts(key string) []string {
	return []string{
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-i", key,
	}
}

func (g *guest) ssh(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(g.ctx, timeout)
	defer cancel()
	full := append(sshOpts(g.path("id")), "-p", strconv.Itoa(g.port), "matrix@127.0.0.1")
	full = append(full, args...)
	return output(ctx, "ssh", full...)
}

func (g *guest) scp(src, dst string) (string, error) {
	ctx, cancel := context.WithTimeout(g.ctx, 10*time.Minute)
	defer cancel()
	full := append(sshOpts(g.path("id")), "-r", "-P", strconv.Itoa(g.port), src, "matrix@127.0.0.1:"+dst)
	return output(ctx, "scp", full...)
}

// --- helpers ---

func output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func lastReport(out string) (report.Report, bool) {
	var found report.Report
	ok := false
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, reportMarker)
		if i < 0 {
			continue
		}
		var r report.Report
		if json.Unmarshal([]byte(line[i+len(reportMarker):]), &r) == nil {
			found, ok = r, true
		}
	}
	return found, ok
}

func guestProxy() string {
	p := os.Getenv("https_proxy")
	if p == "" {
		p = os.Getenv("http_proxy")
	}
	if p == "" {
		return ""
	}
	// QEMU user-net reaches the host loopback at 10.0.2.2.
	p = strings.ReplaceAll(p, "127.0.0.1", "10.0.2.2")
	p = strings.ReplaceAll(p, "localhost", "10.0.2.2")
	return p
}

func requireKVM() error {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("/dev/kvm not usable (%v); the matrix needs KVM, there is no TCG fallback", err)
	}
	_ = f.Close()
	return nil
}

// runRoot is the per-invocation work dir. The app wrapper creates one and names
// it in MAYFLY_RUN_DIR so its EXIT trap removes exactly this run's VMs
// and overlays, never a concurrent invocation's. Absent (a bare binary run), it
// falls back to a fresh dir under the runtime dir.
func runRoot() string {
	if d := os.Getenv("MAYFLY_RUN_DIR"); d != "" {
		_ = os.MkdirAll(d, 0o700) // holds each run's work dirs (ssh keys); owner-only
		return d
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	root := filepath.Join(base, "mayfly")
	_ = os.MkdirAll(root, 0o700) // holds each run's work dirs (ssh keys); owner-only
	return root
}

// hostParallelism bounds concurrency by the host: two vCPUs per guest, and
// 2.5 GiB per guest after 2 GiB of headroom.
func hostParallelism() int {
	byCPU := 1
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		byCPU = strings.Count(string(data), "processor\t") / 2
	}
	byRAM := 1
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if kb, ok := strings.CutPrefix(line, "MemTotal:"); ok {
				kb = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(kb), "kB"))
				if n, err := strconv.Atoi(kb); err == nil {
					gib := n / (1024 * 1024)
					byRAM = (gib - 2) * 10 / 25
				}
			}
		}
	}
	n := byCPU
	if byRAM < n {
		n = byRAM
	}
	if n < 1 {
		n = 1
	}
	return n
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func copyInto(src, dst string) error {
	data, err := os.ReadFile(src) //nolint:gosec // a path the user named on the CLI
	if err != nil {
		return err
	}
	fi, _ := os.Stat(src)
	mode := os.FileMode(0o644)
	if fi != nil {
		mode = fi.Mode().Perm()
	}
	return os.WriteFile(dst, data, mode)
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}
