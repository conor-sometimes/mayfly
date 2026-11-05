// Package report is the distro matrix's wire format: the JSON config the host
// runner (cmd/matrix) hands the in-guest engine (cmd/distrocheck), and the JSON
// report the engine hands back. Typed both ways, so neither side scrapes text.
//
// The report travels on SSH stdout, not a raw disk, so there is no size cap to
// fit: a report is whatever JSON the engine prints on its last line. The records
// a consumer's entrypoint produces (docs/protocol.md) are a separate, internal
// wire the engine reconciles into these Checks before it reports.
package report

// Report is one distro's result.
type Report struct {
	Distro string            `json:"distro"`
	Info   map[string]string `json:"info"`   // recorded, not judged: versions, LSM mode, quirks, info records
	Checks []Check           `json:"checks"` // one rendered verdict per planned id, in plan order
}

// Check is one rendered verdict on one distro (spec 6.3). Status is one of
// pass|fail|skip|error|xfail|xpass|na ("na" = excluded by `when`, rendered "-").
type Check struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Config is what the host hands the guest: how to install on this distro, the
// subjects to provision, and the plan + entrypoint to run. Every source path is
// relative to the payload root the runner scps in (distrocheck -root <dir>);
// every guest path is absolute.
type Config struct {
	Distro   string            `json:"distro"`
	PM       string            `json:"pm"`       // dnf | apt | zypper | pacman
	Family   string            `json:"family"`   // rhel | debian | suse | arch; a check's when can test it
	Packages map[string]string `json:"packages"` // generic command name -> the package providing it here
	Proxy    string            `json:"proxy"`    // the host's proxy as the guest reaches it; "" for direct
	Subjects []Subject         `json:"subjects"` // the things under test, provisioned in order

	// The protocol run (docs/protocol.md). Entrypoint is the one script the
	// consumer provides; Plan is this distro's applicable ids (when-filtered by
	// the host, spec 4.3), in order. TestSugar marks the `test` single-script
	// shorthand (spec 4.5): the entrypoint's exit status is mapped, not records.
	Entrypoint string      `json:"entrypoint"`
	Plan       []PlanEntry `json:"plan"`
	TestSugar  bool        `json:"test_sugar"`

	// Provisioning knobs the engine owns (ordering a consumer script would get
	// wrong). TestUser, when set, is a provisioned unprivileged user exported as
	// MAYFLY_TEST_USER. Audit enables auditd, strips `-a task,never`, and loads a
	// subject's shipped rules after its files land.
	TestUser string `json:"test_user"`
	Audit    bool   `json:"audit"`
}

// PlanEntry is one planned check id (spec 4.2). XFail is the optional modifier
// (spec 6.2); mayfly/1 rejects it at eval until a consumer needs it, so it is
// carried but not yet acted on.
type PlanEntry struct {
	ID    string `json:"id"`
	XFail bool   `json:"xfail,omitempty"`
}

// Subject is one thing under test: a static binary, the files it needs, and how
// to start it. Provisioning only; a detect check names the subject's findings
// and scan command through `mayfly detect` flags, not here.
type Subject struct {
	Name        string            `json:"name"`
	Binary      string            `json:"binary"`       // payload-relative path to the static binary
	InstallPath string            `json:"install_path"` // absolute guest path to install it at
	Units       []string          `json:"units"`        // payload-relative unit files -> /etc/systemd/system/
	Files       map[string]string `json:"files"`        // absolute guest path -> payload-relative source
	Setup       string            `json:"setup"`        // payload-relative script, run once as root after files land
	Enable      []string          `json:"enable"`       // unit names to `systemctl enable --now`
	Packages    []string          `json:"packages"`     // generic package names installed first, translated through Packages (unmapped is an error)
}
