package audit

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moveeeax/k8s-service-endpoint-audit/internal/kube"
)

func load(t *testing.T, fixture string) *kube.Cluster {
	t.Helper()
	c, err := kube.LoadManifests(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	return c
}

func rules(r Report) []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.Service+":"+f.Rule)
	}
	return out
}

func TestFixtures(t *testing.T) {
	cases := []struct {
		fixture string
		want    []string
	}{
		{"healthy", nil},
		{"dead-selector", []string{"api:" + RuleDeadSelector}},
		{"targetport", []string{
			"metrics:" + RuleTargetPort,
			"metrics:" + RuleTargetPort,
		}},
		{"headless", nil},
		{"empty-selector", []string{"orphan:" + RuleEmptySelector}},
		{"externalname", []string{"payments:" + RuleExternalName}},
		{"live-notready", []string{"web:" + RuleNoReadyEndpoints}},
		{"terminating", []string{"worker:" + RuleTerminatingOnly}},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			rep := Run(load(t, tc.fixture), Options{})
			got := rules(rep)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("finding %d: got %q, want %q (all: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

func TestDeadSelectorIsHighAndNamesTheClosestLabelSet(t *testing.T) {
	rep := Run(load(t, "dead-selector"), Options{})
	if len(rep.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(rep.Findings))
	}
	f := rep.Findings[0]
	if f.Severity != "high" {
		t.Errorf("severity = %q, want high", f.Severity)
	}
	if !strings.Contains(f.Message, "app=api-server") {
		t.Errorf("message does not quote the selector: %q", f.Message)
	}
	if !strings.Contains(f.Hint, "Deployment/api") {
		t.Errorf("hint should point at the closest label set, got %q", f.Hint)
	}
}

func TestNamedTargetPortMismatchIsReportedPerPort(t *testing.T) {
	rep := Run(load(t, "targetport"), Options{})
	if len(rep.Findings) != 2 {
		t.Fatalf("want 2 findings (named + numeric), got %d: %v", len(rep.Findings), rules(rep))
	}
	var sawNamed, sawNumeric bool
	for _, f := range rep.Findings {
		if strings.Contains(f.Message, `"telemetry"`) {
			sawNamed = true
		}
		if strings.Contains(f.Message, "container port 9999") {
			sawNumeric = true
		}
	}
	if !sawNamed {
		t.Error("named targetPort telemetry was not reported")
	}
	if !sawNumeric {
		t.Error("numeric targetPort 9999 was not reported")
	}
}

// A numeric targetPort must not be flagged when the container declares no ports
// at all - containerPort is optional, so that would be noise on most charts.
func TestNumericTargetPortIgnoredWhenNoPortsDeclared(t *testing.T) {
	c := &kube.Cluster{Mode: "manifests"}
	if err := kube.DecodeStream(c, strings.NewReader(`
apiVersion: v1
kind: Service
metadata: {name: quiet, namespace: shop}
spec:
  selector: {app: quiet}
  ports: [{port: 80, targetPort: 8080}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: quiet, namespace: shop}
spec:
  replicas: 1
  template:
    metadata:
      labels: {app: quiet}
    spec:
      containers: [{name: app, image: busybox}]
`), "inline"); err != nil {
		t.Fatal(err)
	}
	rep := Run(c, Options{})
	if len(rep.Findings) != 0 {
		t.Fatalf("want no findings, got %v", rules(rep))
	}
}

func TestZeroReplicaWorkloadDoesNotRescueASelector(t *testing.T) {
	c := &kube.Cluster{Mode: "manifests"}
	if err := kube.DecodeStream(c, strings.NewReader(`
apiVersion: v1
kind: Service
metadata: {name: scaled-down, namespace: shop}
spec:
  selector: {app: batch}
  ports: [{port: 80}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: batch, namespace: shop}
spec:
  replicas: 0
  template:
    metadata:
      labels: {app: batch}
    spec:
      containers: [{name: app, image: busybox}]
`), "inline"); err != nil {
		t.Fatal(err)
	}
	rep := Run(c, Options{})
	if len(rep.Findings) != 1 || rep.Findings[0].Rule != RuleDeadSelector {
		t.Fatalf("want one dead-selector finding, got %v", rules(rep))
	}
}

func TestSeverityThresholdFiltersMediumFindings(t *testing.T) {
	c := load(t, "targetport")
	if n := len(Run(c, Options{}).Findings); n != 2 {
		t.Fatalf("baseline want 2, got %d", n)
	}
	if n := len(Run(c, Options{MinSeverity: High}).Findings); n != 0 {
		t.Fatalf("--severity high should drop the medium findings, got %d", n)
	}
}

func TestIgnoreAndSkipFlags(t *testing.T) {
	c := load(t, "dead-selector")
	if n := len(Run(c, Options{IgnoreServices: []string{"ap*"}}).Findings); n != 0 {
		t.Errorf("glob ignore did not match, got %d findings", n)
	}
	if n := len(Run(c, Options{IgnoreNamespaces: []string{"shop"}}).Findings); n != 0 {
		t.Errorf("namespace ignore did not match, got %d findings", n)
	}
	if n := len(Run(c, Options{SkipKinds: []string{"ClusterIP"}}).Findings); n != 0 {
		t.Errorf("skip-kinds did not match the default Service type, got %d findings", n)
	}
	if n := len(Run(c, Options{SkipRules: []string{RuleDeadSelector}}).Findings); n != 0 {
		t.Errorf("skip-rules did not drop the rule, got %d findings", n)
	}
	rep := Run(c, Options{IgnoreServices: []string{"shop/api"}})
	if len(rep.Findings) != 0 {
		t.Errorf("namespace/name ignore did not match, got %v", rules(rep))
	}
	if rep.Scanned.Skipped != 1 {
		t.Errorf("skipped counter = %d, want 1", rep.Scanned.Skipped)
	}
}

func TestOrderingIsDeterministicAndSeverityFirst(t *testing.T) {
	c := &kube.Cluster{Mode: "manifests"}
	for _, dir := range []string{"dead-selector", "targetport", "externalname"} {
		part, err := kube.LoadManifests(filepath.Join("testdata", dir))
		if err != nil {
			t.Fatal(err)
		}
		c.Services = append(c.Services, part.Services...)
		c.Workloads = append(c.Workloads, part.Workloads...)
		c.Pods = append(c.Pods, part.Pods...)
		c.Endpoints = append(c.Endpoints, part.Endpoints...)
		c.EndpointSlices = append(c.EndpointSlices, part.EndpointSlices...)
	}
	first := Run(c, Options{})
	second := Run(c, Options{})
	if strings.Join(rules(first), "|") != strings.Join(rules(second), "|") {
		t.Fatal("ordering is not stable across runs")
	}
	if first.Findings[0].Severity != "high" {
		t.Errorf("high severity findings must sort first, got %q", first.Findings[0].Severity)
	}
	if first.Worst() != High {
		t.Errorf("Worst() = %v, want high", first.Worst())
	}
}

func TestJSONOutputShape(t *testing.T) {
	var buf bytes.Buffer
	rep := Run(load(t, "dead-selector"), Options{})
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var back struct {
		Mode     string `json:"mode"`
		Findings []struct {
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
			Service  string `json:"service"`
		} `json:"findings"`
		Scanned struct {
			Services int `json:"services"`
		} `json:"scanned"`
	}
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if back.Mode != "manifests" || back.Scanned.Services != 1 {
		t.Errorf("unexpected envelope: %+v", back)
	}
	if len(back.Findings) != 1 || back.Findings[0].Rule != RuleDeadSelector {
		t.Errorf("unexpected findings: %+v", back.Findings)
	}
}

func TestEmptyReportRendersWithoutATable(t *testing.T) {
	var buf bytes.Buffer
	rep := Run(load(t, "healthy"), Options{})
	if err := rep.WriteTable(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "no findings") {
		t.Errorf("unexpected table output: %q", buf.String())
	}
	buf.Reset()
	if err := rep.WriteMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no findings") {
		t.Errorf("unexpected markdown output: %q", buf.String())
	}
}

func TestMarkdownTableListsEveryFinding(t *testing.T) {
	var buf bytes.Buffer
	rep := Run(load(t, "targetport"), Options{})
	if err := rep.WriteMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "| medium |") != 2 {
		t.Errorf("want 2 medium rows, got:\n%s", out)
	}
	if !strings.Contains(out, "| Severity | Service | Rule | Detail |") {
		t.Errorf("missing header row:\n%s", out)
	}
}

func TestParseSeverity(t *testing.T) {
	for in, want := range map[string]Severity{"low": Low, "": Low, "MEDIUM": Medium, "high": High} {
		got, err := ParseSeverity(in)
		if err != nil {
			t.Fatalf("ParseSeverity(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseSeverity(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseSeverity("critical"); err == nil {
		t.Error("ParseSeverity(critical) should fail")
	}
}
