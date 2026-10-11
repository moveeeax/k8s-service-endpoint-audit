// Command k8s-service-endpoint-audit finds Kubernetes Services that route nowhere.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/moveeeax/k8s-service-endpoint-audit/internal/audit"
	"github.com/moveeeax/k8s-service-endpoint-audit/internal/kube"
)

// version is overridden at release time with -ldflags.
var version = "0.1.0"

const usage = `k8s-service-endpoint-audit - find Services that route nowhere

Usage:
  k8s-service-endpoint-audit --manifests DIR
  k8s-service-endpoint-audit --namespace NS
  k8s-service-endpoint-audit --all-namespaces --json

Exit codes: 0 no findings, 1 findings, 2 load or usage error.

Flags:
`

type config struct {
	manifests       string
	namespace       string
	allNamespaces   bool
	kubeContext     string
	jsonOut         bool
	markdownOut     bool
	ignore          string
	ignoreNamespace string
	skipKinds       string
	skipRules       string
	severity        string
	showVersion     bool
}

func main() {
	code, err := run(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	os.Exit(code)
}

func run(args []string, stdout, stderr io.Writer) (int, error) {
	var c config
	fs := flag.NewFlagSet("k8s-service-endpoint-audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	fs.StringVar(&c.manifests, "manifests", "", "audit a directory or file of YAML/JSON manifests instead of a cluster")
	fs.StringVar(&c.namespace, "namespace", "", "namespace to audit in live mode (default: the kubeconfig's current namespace)")
	fs.BoolVar(&c.allNamespaces, "all-namespaces", false, "audit every namespace in live mode")
	fs.StringVar(&c.kubeContext, "context", "", "kubeconfig context to use in live mode")
	fs.BoolVar(&c.jsonOut, "json", false, "emit JSON instead of a table")
	fs.BoolVar(&c.markdownOut, "markdown", false, "emit a Markdown table for a PR comment")
	fs.StringVar(&c.ignore, "ignore", "", "comma-separated glob(s) on service name or namespace/name to skip")
	fs.StringVar(&c.ignoreNamespace, "ignore-namespace", "", "comma-separated glob(s) on namespace to skip")
	fs.StringVar(&c.skipKinds, "skip-kinds", "", "comma-separated Service types to skip (ClusterIP, NodePort, LoadBalancer, ExternalName)")
	fs.StringVar(&c.skipRules, "skip-rules", "", "comma-separated rule ids to skip ("+strings.Join(audit.AllRules, ", ")+")")
	fs.StringVar(&c.severity, "severity", "low", "report findings at this severity or above (low, medium, high)")
	fs.BoolVar(&c.showVersion, "version", false, "print the version and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, nil
	}
	if c.showVersion {
		fmt.Fprintln(stdout, version)
		return 0, nil
	}
	if c.manifests != "" && (c.allNamespaces || c.namespace != "") {
		return 2, errors.New("--manifests cannot be combined with --namespace or --all-namespaces")
	}
	if c.jsonOut && c.markdownOut {
		return 2, errors.New("--json and --markdown are mutually exclusive")
	}

	minSev, err := audit.ParseSeverity(c.severity)
	if err != nil {
		return 2, err
	}

	var cluster *kube.Cluster
	if c.manifests != "" {
		cluster, err = kube.LoadManifests(c.manifests)
	} else {
		cluster, err = kube.LoadLive(kube.ExecKubectl, kube.LiveOptions{
			Namespace:     c.namespace,
			AllNamespaces: c.allNamespaces,
			Context:       c.kubeContext,
		})
	}
	if err != nil {
		return 2, err
	}

	rep := audit.Run(cluster, audit.Options{
		IgnoreServices:   splitList(c.ignore),
		IgnoreNamespaces: splitList(c.ignoreNamespace),
		SkipKinds:        splitList(c.skipKinds),
		SkipRules:        splitList(c.skipRules),
		MinSeverity:      minSev,
	})

	switch {
	case c.jsonOut:
		err = rep.WriteJSON(stdout)
	case c.markdownOut:
		err = rep.WriteMarkdown(stdout)
	default:
		err = rep.WriteTable(stdout)
	}
	if err != nil {
		return 2, err
	}
	if len(rep.Findings) > 0 {
		return 1, nil
	}
	return 0, nil
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
