package kube

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntOrStringUnmarshal(t *testing.T) {
	cases := []struct {
		in   string
		want IntOrString
	}{
		{`8080`, IntOrString{IsInt: true, IntVal: 8080}},
		{`"8080"`, IntOrString{IsInt: true, IntVal: 8080}},
		{`"http"`, IntOrString{StrVal: "http"}},
		{`""`, IntOrString{}},
		{`null`, IntOrString{}},
	}
	for _, tc := range cases {
		var got IntOrString
		if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("%s -> %+v, want %+v", tc.in, got, tc.want)
		}
	}
	var bad IntOrString
	if err := json.Unmarshal([]byte(`{"a":1}`), &bad); err == nil {
		t.Error("an object should not decode as a targetPort")
	}
}

func TestServicePortResolvedDefaultsToPort(t *testing.T) {
	p := ServicePort{Port: 443}
	if r := p.Resolved(); !r.IsInt || r.IntVal != 443 {
		t.Errorf("unset targetPort should resolve to the service port, got %v", r)
	}
	p.TargetPort = IntOrString{StrVal: "https"}
	if r := p.Resolved(); r.IsInt || r.StrVal != "https" {
		t.Errorf("named targetPort lost: %v", r)
	}
}

func TestDecodeMultiDocAndListWrapper(t *testing.T) {
	c := &Cluster{}
	stream := `
apiVersion: v1
kind: Service
metadata: {name: a}
spec: {ports: [{port: 80}]}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: ignored}
data: {k: v}
---
apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: Pod
    metadata: {name: p1, labels: {app: x}}
    spec: {containers: [{name: c, ports: [{containerPort: 80}]}]}
  - apiVersion: apps/v1
    kind: StatefulSet
    metadata: {name: s1}
    spec:
      template:
        metadata: {labels: {app: x}}
        spec: {containers: [{name: c}]}
`
	if err := DecodeStream(c, strings.NewReader(stream), "inline"); err != nil {
		t.Fatal(err)
	}
	if len(c.Services) != 1 || len(c.Pods) != 1 || len(c.Workloads) != 1 {
		t.Fatalf("services=%d pods=%d workloads=%d", len(c.Services), len(c.Pods), len(c.Workloads))
	}
	if c.Workloads[0].WorkloadKind != "StatefulSet" {
		t.Errorf("workload kind = %q", c.Workloads[0].WorkloadKind)
	}
	if c.Workloads[0].TemplateLabels["app"] != "x" {
		t.Errorf("template labels lost: %v", c.Workloads[0].TemplateLabels)
	}
}

func TestDecodeCronJobReachesTheNestedTemplate(t *testing.T) {
	c := &Cluster{}
	stream := `
apiVersion: batch/v1
kind: CronJob
metadata: {name: nightly, namespace: jobs}
spec:
  schedule: "0 3 * * *"
  jobTemplate:
    spec:
      template:
        metadata:
          labels: {app: nightly}
        spec:
          containers:
            - name: run
              ports: [{name: debug, containerPort: 6060}]
`
	if err := DecodeStream(c, strings.NewReader(stream), "inline"); err != nil {
		t.Fatal(err)
	}
	if len(c.Workloads) != 1 {
		t.Fatalf("want 1 workload, got %d", len(c.Workloads))
	}
	w := c.Workloads[0]
	if w.TemplateLabels["app"] != "nightly" {
		t.Errorf("labels = %v", w.TemplateLabels)
	}
	ports := w.Template.DeclaredPorts()
	if len(ports) != 1 || ports[0].Name != "debug" || ports[0].ContainerPort != 6060 {
		t.Errorf("declared ports = %+v", ports)
	}
}

func TestDecodeRejectsBrokenYAML(t *testing.T) {
	c := &Cluster{}
	err := DecodeStream(c, strings.NewReader("kind: Service\n  bad: [indent"), "inline")
	if err == nil {
		t.Fatal("broken YAML should be an error, not a silent skip")
	}
	if !strings.Contains(err.Error(), "inline") {
		t.Errorf("error should name the source, got %v", err)
	}
}

func TestMatchesSelector(t *testing.T) {
	labels := map[string]string{"app": "web", "tier": "front", "extra": "x"}
	if !MatchesSelector(map[string]string{"app": "web"}, labels) {
		t.Error("subset selector should match")
	}
	if MatchesSelector(map[string]string{"app": "web", "tier": "back"}, labels) {
		t.Error("mismatched value must not match")
	}
	if MatchesSelector(nil, labels) {
		t.Error("an empty selector selects no pods")
	}
}

func TestPodReadyAndTerminating(t *testing.T) {
	notReady := Pod{Status: PodStatus{Phase: "Running", Conditions: []PodCondition{{Type: "Ready", Status: "False"}}}}
	if notReady.Ready() {
		t.Error("Ready=False pod reported ready")
	}
	ready := Pod{Status: PodStatus{Phase: "Running", Conditions: []PodCondition{{Type: "Ready", Status: "True"}}}}
	if !ready.Ready() {
		t.Error("Ready=True pod reported not ready")
	}
	manifest := Pod{}
	if !manifest.Ready() {
		t.Error("a status-less manifest pod should count as a candidate")
	}
	if !(Pod{Meta: Meta{DeletionTimestamp: "now"}}).Terminating() {
		t.Error("deletionTimestamp should mean terminating")
	}
}

func TestEndpointSliceCounts(t *testing.T) {
	yes, no := true, false
	es := EndpointSlice{
		Meta: Meta{Labels: map[string]string{"kubernetes.io/service-name": "web"}},
		Endpoints: []EndpointSliceEndpoint{
			{Addresses: []string{"10.0.0.1"}},
			{Addresses: []string{"10.0.0.2"}},
			{Addresses: []string{"10.0.0.3"}},
		},
	}
	es.Endpoints[1].Conditions.Ready = &yes
	es.Endpoints[2].Conditions.Ready = &no
	if es.ServiceName() != "web" {
		t.Errorf("service name = %q", es.ServiceName())
	}
	if got := es.ReadyAddresses(); got != 2 {
		t.Errorf("ready = %d, want 2 (absent condition means ready)", got)
	}
	if got := es.TotalAddresses(); got != 3 {
		t.Errorf("total = %d, want 3", got)
	}
}

func TestLoadManifestsWalksTreeAndSkipsHiddenDirs(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	svc := func(name string) string {
		return fmt.Sprintf("apiVersion: v1\nkind: Service\nmetadata: {name: %s}\nspec: {ports: [{port: 80}]}\n", name)
	}
	write("base/svc.yaml", svc("a"))
	write("overlays/prod/svc.yml", svc("b"))
	write("notes.md", "not a manifest")
	write(".git/objects/svc.yaml", svc("hidden"))
	write("charts/dep/svc.yaml", svc("vendored"))

	c, err := LoadManifests(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != "manifests" {
		t.Errorf("mode = %q", c.Mode)
	}
	var names []string
	for _, s := range c.Services {
		names = append(names, s.Meta.Name)
	}
	if len(names) != 2 {
		t.Fatalf("want 2 services (a, b), got %v", names)
	}
}

func TestLoadManifestsAcceptsASingleFile(t *testing.T) {
	c, err := LoadManifests(filepath.Join("testdata", "single.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Services) != 1 || c.Services[0].Meta.Name != "single" {
		t.Fatalf("unexpected load: %+v", c.Services)
	}
}

func TestLoadManifestsMissingPathErrors(t *testing.T) {
	if _, err := LoadManifests(filepath.Join("testdata", "nope")); err == nil {
		t.Fatal("missing path should error")
	}
}

func TestLoadLiveUsesTheRunnerAndToleratesMissingAPIs(t *testing.T) {
	var calls []string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[1] {
		case "services":
			return []byte(`{"kind":"List","items":[
				{"kind":"Service","metadata":{"name":"web","namespace":"shop"},
				 "spec":{"selector":{"app":"web"},"ports":[{"port":80,"targetPort":8080}]}}]}`), nil
		case "pods":
			return []byte(`{"kind":"List","items":[
				{"kind":"Pod","metadata":{"name":"web-1","namespace":"shop","labels":{"app":"web"}},
				 "spec":{"containers":[{"name":"c","ports":[{"containerPort":8080}]}]},
				 "status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`), nil
		case "endpointslices":
			return nil, fmt.Errorf("the server doesn't have a resource type \"endpointslices\"")
		default:
			return []byte(`{"kind":"List","items":[]}`), nil
		}
	}
	c, err := LoadLive(run, LiveOptions{AllNamespaces: true})
	if err != nil {
		t.Fatalf("a missing EndpointSlice API should degrade, not fail: %v", err)
	}
	if c.Mode != "live" {
		t.Errorf("mode = %q", c.Mode)
	}
	if len(c.Services) != 1 || len(c.Pods) != 1 {
		t.Fatalf("services=%d pods=%d", len(c.Services), len(c.Pods))
	}
	if !strings.Contains(calls[0], "--all-namespaces") || !strings.Contains(calls[0], "-o json") {
		t.Errorf("unexpected kubectl invocation: %q", calls[0])
	}
}

func TestLoadLiveFailsWhenServicesCannotBeRead(t *testing.T) {
	run := func(args ...string) ([]byte, error) { return nil, fmt.Errorf("Unauthorized") }
	if _, err := LoadLive(run, LiveOptions{Namespace: "shop"}); err == nil {
		t.Fatal("an unauthorized cluster must be a hard error")
	}
}

func TestLoadLivePassesNamespaceAndContext(t *testing.T) {
	var first string
	run := func(args ...string) ([]byte, error) {
		if first == "" {
			first = strings.Join(args, " ")
		}
		return []byte(`{"kind":"List","items":[]}`), nil
	}
	if _, err := LoadLive(run, LiveOptions{Namespace: "shop", Context: "staging"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--namespace shop", "--context staging"} {
		if !strings.Contains(first, want) {
			t.Errorf("missing %q in %q", want, first)
		}
	}
}

func TestIsWorkloadKind(t *testing.T) {
	if !IsWorkloadKind("Deployment") || !IsWorkloadKind("CronJob") {
		t.Error("workload kinds not recognised")
	}
	if IsWorkloadKind("ConfigMap") {
		t.Error("ConfigMap is not a workload")
	}
}
