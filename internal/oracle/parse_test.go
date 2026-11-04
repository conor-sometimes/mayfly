package oracle

import "testing"

func TestPinDNFRepo(t *testing.T) {
	in := "[baseos]\nmirrorlist=https://mirrors.example/rocky\n#baseurl=https://dl.rockylinux.org/pub/rocky/9/BaseOS\nmetalink=https://metalink.example\n"
	want := "[baseos]\n#mirrorlist=https://mirrors.example/rocky\nbaseurl=https://dl.rockylinux.org/pub/rocky/9/BaseOS\n#metalink=https://metalink.example\n"
	if got := PinDNFRepo(in); got != want {
		t.Errorf("PinDNFRepo:\n got: %q\nwant: %q", got, want)
	}
}

func TestExposure(t *testing.T) {
	out := "  NAME  DESCRIPTION  EXPOSURE\n...\nOverall exposure level for banken.service: 2.1 OK :-)\n"
	if s, l, ok := Exposure(out); !ok || s != "2.1" || l != "OK" {
		t.Errorf("Exposure = %q %q %v", s, l, ok)
	}
	if _, _, ok := Exposure("Failed to parse"); ok {
		t.Errorf("Exposure parsed garbage")
	}
}

func TestStatusAndOSRelease(t *testing.T) {
	if got := StatusField("enabled 1\nfailure 1\npid 612\n", "enabled"); got != "1" {
		t.Errorf("StatusField = %q", got)
	}
	if got := OSRelease("NAME=\"Rocky Linux\"\nPRETTY_NAME=\"Rocky Linux 9.8 (Blue Onyx)\"\n"); got != "Rocky Linux 9.8 (Blue Onyx)" {
		t.Errorf("OSRelease = %q", got)
	}
}

func TestFrom(t *testing.T) {
	if got := From([]string{"a", "b"}, 1); len(got) != 1 || got[0] != "b" {
		t.Errorf("From = %v", got)
	}
	if got := From([]string{"a"}, 5); got != nil {
		t.Errorf("From past end = %v, want nil", got)
	}
}

func TestTaskNever(t *testing.T) {
	rules := "-a task,never\n-a always,exit -F arch=b64 -S execve -k x\n-a never,task\n"
	if got := LinesMatching(rules, TaskNever); len(got) != 2 {
		t.Errorf("LinesMatching(TaskNever) = %v, want 2", got)
	}
}

func TestAnalyticName(t *testing.T) {
	line := `{"class_uid":2004,"finding_info":{"analytic":{"name":"reverse_shell"}},"severity_id":5}`
	if name, ok := AnalyticName(line); !ok || name != "reverse_shell" {
		t.Errorf("AnalyticName = %q %v", name, ok)
	}
	if sev := Severity(line); sev != 5 {
		t.Errorf("Severity = %d, want 5", sev)
	}
	if _, ok := AnalyticName("not json"); ok {
		t.Errorf("AnalyticName parsed garbage")
	}
	if _, ok := AnalyticName(`{"class_uid":2004}`); ok {
		t.Errorf("AnalyticName accepted a finding with no rule")
	}
}
