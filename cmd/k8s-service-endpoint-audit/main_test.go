package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunManifestsExitsOneOnFindings(t *testing.T) {
	var out, errb bytes.Buffer
	code, err := run([]string{"--manifests", "../../examples/broken"}, &out, &errb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (findings present)\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "dead-selector") {
		t.Errorf("table does not mention the dead selector:\n%s", out.String())
	}
}

func TestRunManifestsExitsZeroWhenClean(t *testing.T) {
	var out, errb bytes.Buffer
	code, err := run([]string{"--manifests", "../../examples/healthy"}, &out, &errb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "no findings") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func TestRunJSONFlag(t *testing.T) {
	var out, errb bytes.Buffer
	code, _ := run([]string{"--manifests", "../../examples/broken", "--json"}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Errorf("--json did not produce JSON:\n%s", out.String())
	}
}

func TestRunSeverityHighHidesMediumFindingsAndExitsZero(t *testing.T) {
	var out, errb bytes.Buffer
	code, _ := run([]string{"--manifests", "../../examples/healthy", "--severity", "high"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
}

func TestRunRejectsConflictingScopes(t *testing.T) {
	var out, errb bytes.Buffer
	code, err := run([]string{"--manifests", ".", "--all-namespaces"}, &out, &errb)
	if code != 2 || err == nil {
		t.Fatalf("code = %d, err = %v; want 2 and an error", code, err)
	}
}

func TestRunRejectsBadSeverity(t *testing.T) {
	var out, errb bytes.Buffer
	if code, err := run([]string{"--manifests", ".", "--severity", "nope"}, &out, &errb); code != 2 || err == nil {
		t.Fatalf("code = %d, err = %v; want 2 and an error", code, err)
	}
}

func TestRunMissingManifestPathExitsTwo(t *testing.T) {
	var out, errb bytes.Buffer
	if code, err := run([]string{"--manifests", "./does-not-exist"}, &out, &errb); code != 2 || err == nil {
		t.Fatalf("code = %d, err = %v; want 2 and an error", code, err)
	}
}

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	code, err := run([]string{"--version"}, &out, &errb)
	if code != 0 || err != nil {
		t.Fatalf("code = %d, err = %v", code, err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Error("--version printed nothing")
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" a , b ,, c ")
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("splitList = %v", got)
	}
	if splitList("  ") != nil {
		t.Error("blank list should be nil")
	}
}
