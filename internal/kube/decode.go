package kube

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

type typeMeta struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
}

// workloadKinds are the kinds whose pod template can back a Service.
var workloadKinds = map[string]bool{
	"Deployment":            true,
	"StatefulSet":           true,
	"DaemonSet":             true,
	"ReplicaSet":            true,
	"ReplicationController": true,
	"Job":                   true,
	"CronJob":               true,
	"Rollout":               true,
}

// IsWorkloadKind reports whether a kind carries a pod template this tool reads.
func IsWorkloadKind(kind string) bool { return workloadKinds[kind] }

// DecodeStream reads a YAML or JSON stream of Kubernetes objects (multi-document
// YAML and `kind: List` wrappers included) and adds everything recognised to c.
// Unknown kinds are skipped silently: a manifest directory is full of ConfigMaps.
func DecodeStream(c *Cluster, r io.Reader, source string) error {
	dec := yaml.NewDecoder(r)
	for i := 0; ; i++ {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: document %d: %w", source, i, err)
		}
		if doc == nil {
			continue
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("%s: document %d: %w", source, i, err)
		}
		if err := addObject(c, raw, fmt.Sprintf("%s document %d", source, i)); err != nil {
			return err
		}
	}
}

func addObject(c *Cluster, raw []byte, source string) error {
	var tm typeMeta
	if err := json.Unmarshal(raw, &tm); err != nil {
		// Not an object (a bare scalar or list in some random yaml file).
		return nil
	}
	if strings.HasSuffix(tm.Kind, "List") && tm.Kind != "List" || tm.Kind == "List" {
		var wrapper struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		for _, item := range wrapper.Items {
			if err := addObject(c, item, source); err != nil {
				return err
			}
		}
		return nil
	}

	switch tm.Kind {
	case "Service":
		var s Service
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("%s: Service: %w", source, err)
		}
		c.Services = append(c.Services, s)
	case "Pod":
		var p Pod
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%s: Pod: %w", source, err)
		}
		c.Pods = append(c.Pods, p)
	case "EndpointSlice":
		var e EndpointSlice
		if err := json.Unmarshal(raw, &e); err != nil {
			return fmt.Errorf("%s: EndpointSlice: %w", source, err)
		}
		c.EndpointSlices = append(c.EndpointSlices, e)
	case "Endpoints":
		var e Endpoints
		if err := json.Unmarshal(raw, &e); err != nil {
			return fmt.Errorf("%s: Endpoints: %w", source, err)
		}
		c.Endpoints = append(c.Endpoints, e)
	default:
		if !workloadKinds[tm.Kind] {
			return nil
		}
		w, err := decodeWorkload(tm.Kind, raw)
		if err != nil {
			return fmt.Errorf("%s: %s: %w", source, tm.Kind, err)
		}
		c.Workloads = append(c.Workloads, w)
	}
	return nil
}

// podTemplate is the shape shared by every workload except CronJob, which hides
// the template one level deeper under jobTemplate.
type podTemplate struct {
	Meta Meta    `json:"metadata"`
	Spec PodSpec `json:"spec"`
}

func decodeWorkload(kind string, raw []byte) (Workload, error) {
	var obj struct {
		Meta Meta `json:"metadata"`
		Spec struct {
			Replicas    *int        `json:"replicas"`
			Template    podTemplate `json:"template"`
			JobTemplate struct {
				Spec struct {
					Template podTemplate `json:"template"`
				} `json:"spec"`
			} `json:"jobTemplate"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Workload{}, err
	}
	tpl := obj.Spec.Template
	if kind == "CronJob" {
		tpl = obj.Spec.JobTemplate.Spec.Template
	}
	return Workload{
		WorkloadKind:   kind,
		Meta:           obj.Meta,
		TemplateLabels: tpl.Meta.Labels,
		Template:       tpl.Spec,
		Replicas:       obj.Spec.Replicas,
	}, nil
}
