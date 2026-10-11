// Package audit resolves what every Service actually points at and reports the
// ones that point at nothing.
package audit

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/moveeeax/k8s-service-endpoint-audit/internal/kube"
)

// Severity orders findings. High means traffic is being black-holed right now.
type Severity int

const (
	Low Severity = iota
	Medium
	High
)

func (s Severity) String() string {
	switch s {
	case High:
		return "high"
	case Medium:
		return "medium"
	default:
		return "low"
	}
}

// ParseSeverity accepts low/medium/high.
func ParseSeverity(s string) (Severity, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low", "":
		return Low, nil
	case "medium", "med":
		return Medium, nil
	case "high":
		return High, nil
	}
	return Low, fmt.Errorf("unknown severity %q (want low, medium or high)", s)
}

// Rule ids, stable so --skip-rules and downstream JSON consumers can rely on them.
const (
	RuleDeadSelector     = "dead-selector"
	RuleNoReadyEndpoints = "no-ready-endpoints"
	RuleTargetPort       = "target-port-undeclared"
	RuleEmptySelector    = "empty-selector"
	RuleExternalName     = "external-name-empty"
	RuleTerminatingOnly  = "only-terminating-pods"
)

// AllRules is the full rule set, in reporting order.
var AllRules = []string{
	RuleDeadSelector,
	RuleNoReadyEndpoints,
	RuleTerminatingOnly,
	RuleTargetPort,
	RuleEmptySelector,
	RuleExternalName,
}

// Finding is one problem with one Service.
type Finding struct {
	Rule      string `json:"rule"`
	Severity  string `json:"severity"`
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Type      string `json:"type"`
	Message   string `json:"message"`
	Hint      string `json:"hint,omitempty"`

	sev Severity
}

// Report is the whole result, shaped for --json.
type Report struct {
	Mode     string    `json:"mode"`
	Findings []Finding `json:"findings"`
	Scanned  Scanned   `json:"scanned"`
}

// Scanned records what was loaded, so an empty report can be told apart from an
// empty input.
type Scanned struct {
	Services  int `json:"services"`
	Pods      int `json:"pods"`
	Workloads int `json:"workloads"`
	Skipped   int `json:"skipped"`
}

// Options are the CLI knobs.
type Options struct {
	IgnoreServices   []string
	IgnoreNamespaces []string
	SkipKinds        []string
	SkipRules        []string
	MinSeverity      Severity
}

// target is an endpoint candidate: a real pod, or a workload's pod template
// when auditing manifests that have never been applied.
type target struct {
	kind        string
	name        string
	spec        kube.PodSpec
	ready       bool
	terminating bool
	live        bool
}

func ns(m kube.Meta) string {
	if m.Namespace == "" {
		return "default"
	}
	return m.Namespace
}

func matchAny(patterns []string, value string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if ok, err := path.Match(p, value); err == nil && ok {
			return true
		}
		if strings.EqualFold(p, value) {
			return true
		}
	}
	return false
}

// Run audits every Service in c.
func Run(c *kube.Cluster, opt Options) Report {
	rep := Report{Mode: c.Mode, Findings: []Finding{}}
	rep.Scanned.Pods = len(c.Pods)
	rep.Scanned.Workloads = len(c.Workloads)

	skipRule := func(rule string) bool { return matchAny(opt.SkipRules, rule) }

	for _, svc := range c.Services {
		namespace := ns(svc.Meta)
		if matchAny(opt.IgnoreNamespaces, namespace) ||
			matchAny(opt.IgnoreServices, svc.Meta.Name) ||
			matchAny(opt.IgnoreServices, namespace+"/"+svc.Meta.Name) ||
			matchAny(opt.SkipKinds, svc.Kind()) {
			rep.Scanned.Skipped++
			continue
		}
		rep.Scanned.Services++
		rep.Findings = append(rep.Findings, auditService(c, svc, namespace, skipRule)...)
	}

	kept := rep.Findings[:0]
	for _, f := range rep.Findings {
		if f.sev >= opt.MinSeverity {
			kept = append(kept, f)
		}
	}
	rep.Findings = kept
	sortFindings(rep.Findings)
	return rep
}

func auditService(c *kube.Cluster, svc kube.Service, namespace string, skipRule func(string) bool) []Finding {
	var out []Finding
	add := func(rule string, sev Severity, msg, hint string) {
		if skipRule(rule) {
			return
		}
		out = append(out, Finding{
			Rule: rule, Severity: sev.String(), Namespace: namespace,
			Service: svc.Meta.Name, Type: svc.Kind(), Message: msg, Hint: hint, sev: sev,
		})
	}

	// ExternalName is a DNS alias; selectors and endpoints do not apply to it.
	if svc.Kind() == "ExternalName" {
		if strings.TrimSpace(svc.Spec.ExternalName) == "" {
			add(RuleExternalName, High,
				"type ExternalName with an empty spec.externalName",
				"every lookup returns NXDOMAIN; set externalName or change the Service type")
		}
		return out
	}

	ready, total, haveEndpointData := endpointCounts(c, svc, namespace)

	if len(svc.Spec.Selector) == 0 {
		// An empty selector is legitimate only when something else fills the
		// endpoints: a headless record, or a hand-maintained Endpoints object.
		if svc.Headless() || (haveEndpointData && total > 0) {
			return out
		}
		hint := "add a selector, or create a matching Endpoints object if the backend lives outside the cluster"
		msg := "no selector and no endpoints: nothing will ever answer on this Service"
		add(RuleEmptySelector, Medium, msg, hint)
		return out
	}

	matched := selectTargets(c, svc, namespace)
	if len(matched) == 0 {
		add(RuleDeadSelector, High,
			fmt.Sprintf("selector %s matches no %s", formatSelector(svc.Spec.Selector), subject(c)),
			"the labels drifted or the workload is gone; "+nearestHint(c, svc, namespace))
		return out
	}

	terminating := 0
	readyTargets := 0
	for _, t := range matched {
		if t.terminating {
			terminating++
			continue
		}
		if t.ready {
			readyTargets++
		}
	}
	if terminating == len(matched) {
		add(RuleTerminatingOnly, High,
			fmt.Sprintf("all %d matched pods are Terminating", len(matched)),
			"the Service will have zero endpoints as soon as they finish shutting down")
		return out
	}

	switch {
	case haveEndpointData && ready == 0:
		add(RuleNoReadyEndpoints, High,
			fmt.Sprintf("%d pods match the selector but 0 of %d endpoints are Ready", len(matched), total),
			"readiness probes are failing; the selector is fine, the backend is not")
	case !haveEndpointData && readyTargets == 0 && hasLivePods(matched):
		add(RuleNoReadyEndpoints, High,
			fmt.Sprintf("%d pods match the selector and none report Ready", len(matched)),
			"readiness probes are failing; the selector is fine, the backend is not")
	}

	out = append(out, auditPorts(svc, namespace, matched, skipRule)...)
	return out
}

// auditPorts flags a targetPort no matched container declares. A numeric
// targetPort is only reported when the containers declare ports at all:
// containerPort is optional and informational, so silence there would be a
// false positive factory. A *named* targetPort is different - Kubernetes
// resolves it through the container port name, so a missing name always breaks.
func auditPorts(svc kube.Service, namespace string, matched []target, skipRule func(string) bool) []Finding {
	if skipRule(RuleTargetPort) {
		return nil
	}
	declaredAny := false
	var names []string
	numbers := map[int]bool{}
	for _, t := range matched {
		for _, p := range t.spec.DeclaredPorts() {
			declaredAny = true
			if p.Name != "" {
				names = append(names, p.Name)
			}
			numbers[p.ContainerPort] = true
		}
	}

	var out []Finding
	for _, sp := range svc.Spec.Ports {
		tp := sp.Resolved()
		portLabel := sp.Name
		if portLabel == "" {
			portLabel = fmt.Sprintf("port %d", sp.Port)
		}
		if !tp.IsInt {
			found := false
			for _, n := range names {
				if n == tp.StrVal {
					found = true
					break
				}
			}
			if !found {
				out = append(out, Finding{
					Rule: RuleTargetPort, Severity: Medium.String(), Namespace: namespace,
					Service: svc.Meta.Name, Type: svc.Kind(), sev: Medium,
					Message: fmt.Sprintf("%s targets the named port %q, which no matched container declares", portLabel, tp.StrVal),
					Hint:    "named targetPorts must exist as a containerPort name; declared names: " + orNone(names),
				})
			}
			continue
		}
		if declaredAny && !numbers[tp.IntVal] {
			out = append(out, Finding{
				Rule: RuleTargetPort, Severity: Medium.String(), Namespace: namespace,
				Service: svc.Meta.Name, Type: svc.Kind(), sev: Medium,
				Message: fmt.Sprintf("%s targets container port %d, which no matched container declares", portLabel, tp.IntVal),
				Hint:    "declared container ports: " + orNone(numberList(numbers)),
			})
		}
	}
	return out
}

func selectTargets(c *kube.Cluster, svc kube.Service, namespace string) []target {
	var out []target
	for _, p := range c.Pods {
		if ns(p.Meta) != namespace {
			continue
		}
		if !kube.MatchesSelector(svc.Spec.Selector, p.Meta.Labels) {
			continue
		}
		out = append(out, target{
			kind: "Pod", name: p.Meta.Name, spec: p.Spec,
			ready: p.Ready(), terminating: p.Terminating(), live: true,
		})
	}
	if len(out) > 0 {
		return out
	}
	// Manifest mode (or a workload that has not rolled out yet): fall back to
	// pod templates, which is what the Service will select once applied.
	for _, w := range c.Workloads {
		if ns(w.Meta) != namespace {
			continue
		}
		if !kube.MatchesSelector(svc.Spec.Selector, w.TemplateLabels) {
			continue
		}
		if w.Replicas != nil && *w.Replicas == 0 {
			continue
		}
		out = append(out, target{
			kind: w.WorkloadKind, name: w.Meta.Name, spec: w.Template, ready: true,
		})
	}
	return out
}

func hasLivePods(ts []target) bool {
	for _, t := range ts {
		if t.live {
			return true
		}
	}
	return false
}

// endpointCounts returns ready and total backing addresses for a Service and
// whether any endpoint object was present at all. EndpointSlice wins over the
// legacy Endpoints object when both exist.
func endpointCounts(c *kube.Cluster, svc kube.Service, namespace string) (ready, total int, have bool) {
	for _, es := range c.EndpointSlices {
		if ns(es.Meta) != namespace || es.ServiceName() != svc.Meta.Name {
			continue
		}
		have = true
		ready += es.ReadyAddresses()
		total += es.TotalAddresses()
	}
	if have {
		return ready, total, true
	}
	for _, ep := range c.Endpoints {
		if ns(ep.Meta) != namespace || ep.Meta.Name != svc.Meta.Name {
			continue
		}
		have = true
		ready += ep.ReadyAddresses()
		total += ep.TotalAddresses()
	}
	return ready, total, have
}

// nearestHint names the closest label set in the namespace, which is almost
// always the typo the author made.
func nearestHint(c *kube.Cluster, svc kube.Service, namespace string) string {
	best, bestScore := "", 0
	consider := func(kind, name string, labels map[string]string) {
		if len(labels) == 0 {
			return
		}
		score := 0
		for k, v := range svc.Spec.Selector {
			if lv, ok := labels[k]; ok {
				score++
				if lv == v {
					score += 2
				}
			}
		}
		if score > bestScore {
			bestScore, best = score, fmt.Sprintf("%s/%s %s", kind, name, formatSelector(labels))
		}
	}
	for _, p := range c.Pods {
		if ns(p.Meta) == namespace {
			consider("Pod", p.Meta.Name, p.Meta.Labels)
		}
	}
	for _, w := range c.Workloads {
		if ns(w.Meta) == namespace {
			consider(w.WorkloadKind, w.Meta.Name, w.TemplateLabels)
		}
	}
	if best == "" {
		return "nothing in this namespace carries any of those labels"
	}
	return "closest label set is " + best
}

func subject(c *kube.Cluster) string {
	if c.Mode == "live" {
		return "pods"
	}
	return "pods or workload templates"
}

func formatSelector(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func numberList(m map[int]bool) []string {
	ns := make([]int, 0, len(m))
	for n := range m {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, fmt.Sprint(n))
	}
	return out
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	seen := map[string]bool{}
	uniq := make([]string, 0, len(xs))
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			uniq = append(uniq, x)
		}
	}
	sort.Strings(uniq)
	return strings.Join(uniq, ", ")
}

// ruleOrder gives findings a stable, meaningful sort position.
func ruleOrder(rule string) int {
	for i, r := range AllRules {
		if r == rule {
			return i
		}
	}
	return len(AllRules)
}

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.sev != b.sev {
			return a.sev > b.sev
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Rule != b.Rule {
			return ruleOrder(a.Rule) < ruleOrder(b.Rule)
		}
		return a.Message < b.Message
	})
}

// Worst returns the highest severity present, useful for exit-code policy.
func (r Report) Worst() Severity {
	worst := Low
	for _, f := range r.Findings {
		if f.sev > worst {
			worst = f.sev
		}
	}
	return worst
}
