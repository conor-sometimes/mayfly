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

// TestPkg: a command maps to its package, or falls back to the command name when
// the distro attr does not name it.
func TestPkg(t *testing.T) {
	c := chk(report.Config{Packages: map[string]string{"nc": "ncat"}})
	if got := c.pkg("nc"); got != "ncat" {
		t.Errorf(`pkg("nc") = %q, want "ncat"`, got)
	}
	if got := c.pkg("curl"); got != "curl" {
		t.Errorf(`pkg("curl") = %q, want "curl" (fallback)`, got)
	}
}

// TestCheckTools: required is the union of non-optional checks' tools; optional
// drops anything also required (a tool any required check needs must be present,
// so it can never merely skip). Both are sorted and deduped.
func TestCheckTools(t *testing.T) {
	c := chk(report.Config{Checks: []report.CheckSpec{
		{ID: "a", Tools: []string{"nmap"}, Optional: true},
		{ID: "b", Tools: []string{"tcpdump", "nmap"}},
		{ID: "c", Tools: []string{"jq"}, Optional: true},
	}})
	required, optional := c.checkTools()
	if want := []string{"nmap", "tcpdump"}; !reflect.DeepEqual(required, want) {
		t.Errorf("required = %v, want %v", required, want)
	}
	// nmap is required (check b), so it is not optional even though check a is;
	// jq is the only purely-optional tool.
	if want := []string{"jq"}; !reflect.DeepEqual(optional, want) {
		t.Errorf("optional = %v, want %v", optional, want)
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
