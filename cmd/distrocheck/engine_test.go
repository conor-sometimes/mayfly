package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/conor-sometimes/mayfly/internal/report"
)

// chk builds a checker over a config, for the pure engine functions (no shell).
func chk(cfg report.Config) *checker {
	return &checker{cfg: cfg, rep: report.Report{Distro: cfg.Distro, Info: map[string]string{}}}
}

// TestSubjectPackages: generic names map through the distro's spelling, in
// subject-then-name order, across more than one install.
func TestSubjectPackages(t *testing.T) {
	c := chk(report.Config{
		Distro:   "fedora-44",
		Packages: map[string]string{"auditd": "audit", "python3": "python3", "curl": "curl"},
		Subjects: []report.Subject{
			{Name: "a", Packages: []string{"auditd", "python3"}},
			{Name: "b", Packages: []string{"curl"}},
		},
	})
	got, err := c.subjectPackages()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"audit", "python3", "curl"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("subjectPackages = %v, want %v", got, want)
	}
}

// TestSubjectPackagesUnmapped: an unmapped generic name is a hard error (never a
// silent skip), and the message names both the distro and the package so the
// consumer knows exactly which distro attr to extend.
func TestSubjectPackagesUnmapped(t *testing.T) {
	c := chk(report.Config{
		Distro:   "rocky-9",
		Packages: map[string]string{"curl": "curl"},
		Subjects: []report.Subject{{Name: "a", Packages: []string{"nmap"}}},
	})
	_, err := c.subjectPackages()
	if err == nil {
		t.Fatal("expected an error for an unmapped package, got nil")
	}
	for _, want := range []string{"rocky-9", "nmap"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestInstallDst: a binary lands at its explicit installPath, else on PATH at
// /usr/local/bin/<name>.
func TestInstallDst(t *testing.T) {
	if got := installDst(report.Subject{Name: "mytool"}); got != "/usr/local/bin/mytool" {
		t.Errorf("default dst = %q, want /usr/local/bin/mytool", got)
	}
	if got := installDst(report.Subject{Name: "mytool", InstallPath: "/opt/x/mytool"}); got != "/opt/x/mytool" {
		t.Errorf("explicit dst = %q, want /opt/x/mytool", got)
	}
}

// TestInstalledBinary: $MAYFLY_BINARY is the first install that ships a binary,
// and "" when none does.
func TestInstalledBinary(t *testing.T) {
	c := chk(report.Config{Subjects: []report.Subject{
		{Name: "a"}, // no binary, skipped
		{Name: "b", Binary: "subjects/b/bin"},
		{Name: "c", Binary: "subjects/c/bin", InstallPath: "/opt/c"},
	}})
	if got := c.installedBinary(); got != "/usr/local/bin/b" {
		t.Errorf("installedBinary = %q, want /usr/local/bin/b", got)
	}
	if got := chk(report.Config{Subjects: []report.Subject{{Name: "a"}}}).installedBinary(); got != "" {
		t.Errorf("no-binary installedBinary = %q, want empty", got)
	}
}

// TestDedup: dedup removes repeats and sorts.
func TestDedup(t *testing.T) {
	got := dedup([]string{"b", "a", "b", "c", "a"})
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dedup = %v, want %v", got, want)
	}
	if got := dedup(nil); len(got) != 0 {
		t.Errorf("dedup(nil) = %v, want empty", got)
	}
}
