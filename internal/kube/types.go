// Package kube holds the small slice of the Kubernetes API this tool actually
// reads. It is deliberately hand-written instead of pulling in k8s.io/api: the
// audit only needs selectors, container ports and endpoint readiness, and a
// stdlib-shaped struct keeps the binary small and the tests offline.
package kube

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// IntOrString mirrors intstr.IntOrString, the shape of Service.spec.ports[].targetPort.
type IntOrString struct {
	IsInt  bool
	IntVal int
	StrVal string
}

// UnmarshalJSON accepts 8080, "8080" and "http". A numeric string is normalised
// to an int, because Kubernetes resolves it as a port number, not a port name.
func (v *IntOrString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*v = IntOrString{}
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if s == "" {
			*v = IntOrString{}
			return nil
		}
		if n, err := strconv.Atoi(s); err == nil {
			*v = IntOrString{IsInt: true, IntVal: n}
			return nil
		}
		*v = IntOrString{StrVal: s}
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("targetPort %s is neither a number nor a string: %w", data, err)
	}
	*v = IntOrString{IsInt: true, IntVal: n}
	return nil
}

func (v IntOrString) MarshalJSON() ([]byte, error) {
	if v.IsInt {
		return json.Marshal(v.IntVal)
	}
	return json.Marshal(v.StrVal)
}

// Empty reports whether targetPort was omitted, in which case Kubernetes falls
// back to the Service port number.
func (v IntOrString) Empty() bool { return !v.IsInt && v.StrVal == "" }

func (v IntOrString) String() string {
	if v.IsInt {
		return strconv.Itoa(v.IntVal)
	}
	if v.StrVal == "" {
		return "<unset>"
	}
	return v.StrVal
}

// Meta is the subset of ObjectMeta the audit uses.
type Meta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	Labels            map[string]string `json:"labels"`
	DeletionTimestamp string            `json:"deletionTimestamp"`
}

type ContainerPort struct {
	Name          string `json:"name"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

type Container struct {
	Name  string          `json:"name"`
	Ports []ContainerPort `json:"ports"`
}

type PodSpec struct {
	Containers     []Container `json:"containers"`
	InitContainers []Container `json:"initContainers"`
}

// DeclaredPorts flattens every containerPort across the pod's containers.
func (s PodSpec) DeclaredPorts() []ContainerPort {
	out := make([]ContainerPort, 0, len(s.Containers))
	for _, c := range append(append([]Container{}, s.Containers...), s.InitContainers...) {
		out = append(out, c.Ports...)
	}
	return out
}

type PodCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type PodStatus struct {
	Phase      string         `json:"phase"`
	Conditions []PodCondition `json:"conditions"`
}

type Pod struct {
	Meta   Meta      `json:"metadata"`
	Spec   PodSpec   `json:"spec"`
	Status PodStatus `json:"status"`
}

// Ready follows the kubelet contract: the Ready condition is what puts a pod
// into a Service's endpoint list.
func (p Pod) Ready() bool {
	for _, c := range p.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	// A manifest on disk has no status at all; treat it as a candidate, not a failure.
	return len(p.Status.Conditions) == 0 && p.Status.Phase == ""
}

func (p Pod) Terminating() bool { return p.Meta.DeletionTimestamp != "" }

type ServicePort struct {
	Name       string      `json:"name"`
	Port       int         `json:"port"`
	TargetPort IntOrString `json:"targetPort"`
	Protocol   string      `json:"protocol"`
}

// Resolved returns the port the Service actually forwards to, applying the
// "targetPort defaults to port" rule.
func (p ServicePort) Resolved() IntOrString {
	if p.TargetPort.Empty() {
		return IntOrString{IsInt: true, IntVal: p.Port}
	}
	return p.TargetPort
}

type ServiceSpec struct {
	Selector     map[string]string `json:"selector"`
	Ports        []ServicePort     `json:"ports"`
	Type         string            `json:"type"`
	ClusterIP    string            `json:"clusterIP"`
	ExternalName string            `json:"externalName"`
}

type Service struct {
	Meta Meta        `json:"metadata"`
	Spec ServiceSpec `json:"spec"`
}

// Kind returns the effective Service type, defaulting to ClusterIP the way the
// API server does.
func (s Service) Kind() string {
	if s.Spec.Type == "" {
		return "ClusterIP"
	}
	return s.Spec.Type
}

func (s Service) Headless() bool { return s.Spec.ClusterIP == "None" }

// Workload is a flattened Deployment/StatefulSet/DaemonSet/ReplicaSet/Job/CronJob:
// the pod template is the only part that matters for selector resolution.
type Workload struct {
	WorkloadKind   string
	Meta           Meta
	TemplateLabels map[string]string
	Template       PodSpec
	Replicas       *int
}

type EndpointSliceEndpoint struct {
	Addresses  []string `json:"addresses"`
	Conditions struct {
		Ready       *bool `json:"ready"`
		Serving     *bool `json:"serving"`
		Terminating *bool `json:"terminating"`
	} `json:"conditions"`
}

type EndpointSlice struct {
	Meta      Meta                    `json:"metadata"`
	Endpoints []EndpointSliceEndpoint `json:"endpoints"`
}

// ServiceName reads the owning Service from the well-known label.
func (e EndpointSlice) ServiceName() string {
	return e.Meta.Labels["kubernetes.io/service-name"]
}

// ReadyAddresses counts addresses whose ready condition is true. An absent
// condition means ready, per the EndpointSlice spec.
func (e EndpointSlice) ReadyAddresses() int {
	n := 0
	for _, ep := range e.Endpoints {
		if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
			n += len(ep.Addresses)
		}
	}
	return n
}

func (e EndpointSlice) TotalAddresses() int {
	n := 0
	for _, ep := range e.Endpoints {
		n += len(ep.Addresses)
	}
	return n
}

// Endpoints is the legacy core/v1 object, still what hand-written manual
// endpoints use.
type Endpoints struct {
	Meta    Meta `json:"metadata"`
	Subsets []struct {
		Addresses         []map[string]any `json:"addresses"`
		NotReadyAddresses []map[string]any `json:"notReadyAddresses"`
	} `json:"subsets"`
}

func (e Endpoints) ReadyAddresses() int {
	n := 0
	for _, s := range e.Subsets {
		n += len(s.Addresses)
	}
	return n
}

func (e Endpoints) TotalAddresses() int {
	n := 0
	for _, s := range e.Subsets {
		n += len(s.Addresses) + len(s.NotReadyAddresses)
	}
	return n
}

// Cluster is everything the audit was able to load, from a directory of
// manifests or from a live cluster.
type Cluster struct {
	Mode           string
	Services       []Service
	Pods           []Pod
	Workloads      []Workload
	EndpointSlices []EndpointSlice
	Endpoints      []Endpoints
}

// MatchesSelector implements equality-based label selection: every selector
// key must be present on the object with the same value. An empty selector
// matches nothing here, because an empty Service selector selects no pods.
func MatchesSelector(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
