package kube

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// manifestExts are the files worth opening in a manifest tree.
var manifestExts = map[string]bool{".yaml": true, ".yml": true, ".json": true}

// LoadManifests walks a directory (or reads a single file) and returns
// everything it recognises. Hidden directories and vendored chart caches are
// skipped so pointing it at a repo root does something sane.
func LoadManifests(root string) (*Cluster, error) {
	c := &Cluster{Mode: "manifests"}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("read manifests: %w", err)
	}
	if !info.IsDir() {
		return c, loadFile(c, root)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "charts" || name == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		if !manifestExts[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		return loadFile(c, path)
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

func loadFile(c *Cluster, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return DecodeStream(c, f, path)
}

// Runner executes kubectl. It is a variable so tests can replay canned JSON
// instead of needing a cluster.
type Runner func(args ...string) ([]byte, error)

// ExecKubectl is the default Runner.
func ExecKubectl(args ...string) ([]byte, error) {
	cmd := exec.Command("kubectl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("kubectl %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// LiveOptions selects the scope of a live read.
type LiveOptions struct {
	Namespace     string
	AllNamespaces bool
	Context       string
}

// liveGroups are fetched in separate calls so a missing API (EndpointSlice on an
// ancient cluster) degrades instead of killing the whole read.
var liveGroups = [][]string{
	{"services"},
	{"pods"},
	{"endpointslices"},
	{"endpoints"},
	{"deployments", "statefulsets", "daemonsets", "replicasets", "jobs", "cronjobs"},
}

// LoadLive reads the cluster through kubectl. Shelling out keeps the dependency
// tree at one YAML parser and means the tool inherits whatever kubeconfig,
// context and auth plugin the operator already has working.
func LoadLive(run Runner, opt LiveOptions) (*Cluster, error) {
	if run == nil {
		run = ExecKubectl
	}
	c := &Cluster{Mode: "live"}
	var softErrs []string
	for _, group := range liveGroups {
		args := []string{"get", strings.Join(group, ",")}
		if opt.AllNamespaces {
			args = append(args, "--all-namespaces")
		} else if opt.Namespace != "" {
			args = append(args, "--namespace", opt.Namespace)
		}
		if opt.Context != "" {
			args = append(args, "--context", opt.Context)
		}
		args = append(args, "-o", "json")
		out, err := run(args...)
		if err != nil {
			if group[0] == "services" {
				// No Services means no audit; that is fatal.
				return nil, err
			}
			softErrs = append(softErrs, err.Error())
			continue
		}
		if err := DecodeStream(c, bytes.NewReader(out), "kubectl "+strings.Join(group, ",")); err != nil {
			return nil, err
		}
	}
	if len(c.Services) == 0 && len(softErrs) > 0 {
		return nil, fmt.Errorf("live read failed: %s", strings.Join(softErrs, "; "))
	}
	return c, nil
}
