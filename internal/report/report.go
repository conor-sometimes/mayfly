// Package report is the distro matrix's wire format: the JSON config the host
// runner (cmd/matrix) hands the in-guest oracle (cmd/distrocheck), and the JSON
// report the oracle hands back. Typed both ways, so neither side scrapes text.
//
// The report travels on SSH stdout, not a raw disk, so there is no size cap to
// fit: a report is whatever JSON the oracle prints on its last line.
package report

// Report is one distro's result.
type Report struct {
	Distro string            `json:"distro"`
	Info   map[string]string `json:"info"`   // recorded, not judged: versions, LSM mode, quirks
	Checks []Check           `json:"checks"` // one per check, in run order
}

// Check is one check's verdict on one distro. ID is the check's attr name
// ("revshell", "sandbox", "sysmon"); for a script check that reports several
// ids, the oracle emits one Check per id ("sysmon/S1").
type Check struct {
	ID     string `json:"id"`
	Pass   bool   `json:"pass"`
	Skip   bool   `json:"skip,omitempty"` // recorded, not a failure: a missing optional tool, an unserved distro
	Detail string `json:"detail,omitempty"`
}

// Config is what the host hands the guest: how to install on this distro, the
// subjects to provision, and the checks to run. Every source path is relative
// to the payload root the runner scps in (distrocheck -root <dir>); every guest
// path is absolute.
type Config struct {
	Distro   string            `json:"distro"`
	PM       string            `json:"pm"`       // dnf | apt | zypper | pacman
	Family   string            `json:"family"`   // rhel | debian | suse | arch; a check's when can test it
	Packages map[string]string `json:"packages"` // generic command name -> the package providing it here
	Proxy    string            `json:"proxy"`    // the host's proxy as the guest reaches it; "" for direct
	Subjects []Subject         `json:"subjects"` // the things under test, provisioned in order
	Checks   []CheckSpec       `json:"checks"`   // the checks, already sorted by order then name
}

// Subject is one thing under test: a static binary, the files it needs, and how
// to start and drive it.
type Subject struct {
	Name        string            `json:"name"`
	Binary      string            `json:"binary"`       // payload-relative path to the static binary
	InstallPath string            `json:"install_path"` // absolute guest path to install it at
	Units       []string          `json:"units"`        // payload-relative unit files -> /etc/systemd/system/
	Files       map[string]string `json:"files"`        // absolute guest path -> payload-relative source
	Setup       string            `json:"setup"`        // payload-relative script, run once as root after files land
	Enable      []string          `json:"enable"`       // unit names to `systemctl enable --now`
	Packages    []string          `json:"packages"`     // generic package names installed first, translated through the distro's Packages map (unmapped is an error)
	OCSF        string            `json:"ocsf"`         // glob of OCSF jsonl findings this subject writes (detect checks read it)
	Scan        []string          `json:"scan"`         // command that drives a batch subject once (banken's oneshot scan); nil = a running daemon
}

// CheckSpec is one check. Kind is detect | script | sandbox | lsm.
type CheckSpec struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Run      string   `json:"run"`      // payload-relative script (detect, script)
	As       string   `json:"as"`       // detect: "user" (default) or "root"
	Expect   []string `json:"expect"`   // detect: the exact rule set, sorted; [] means none
	IDs      []string `json:"ids"`      // script: the ids the script must report
	Unit     string   `json:"unit"`     // sandbox: the unit to rate
	Match    string   `json:"match"`    // lsm: the subject name denials are searched for
	Tools    []string `json:"tools"`    // commands the check needs, resolved through Packages
	Optional bool     `json:"optional"` // a missing tool skips the check instead of failing the distro
}
