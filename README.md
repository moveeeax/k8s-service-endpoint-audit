# k8s-service-endpoint-audit

[![ci](https://github.com/moveeeax/k8s-service-endpoint-audit/actions/workflows/ci.yml/badge.svg)](https://github.com/moveeeax/k8s-service-endpoint-audit/actions/workflows/ci.yml)
[![go](https://img.shields.io/badge/go-1.23%2B-00ADD8)](https://go.dev)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Find Kubernetes Services that route nowhere.

A Service with a typo in its selector does not fail. The API server accepts any
map of labels, creates the Service with an empty endpoint list, and reports
nothing wrong. The only symptom is a caller getting connection refused, usually
from another team, usually at 3am, usually after an unrelated deploy renamed a
label. This walks every Service, resolves what it actually points at, and names
the ones pointing at nothing.

Works on a live cluster or on a directory of manifests, so it runs in CI before
the broken Service is ever applied.

## Rules

| Rule | Severity | What it catches |
|---|---|---|
| `dead-selector` | high | The selector matches no pod (live) and no workload pod template (manifests). |
| `no-ready-endpoints` | high | Pods match, but zero endpoints are Ready — readiness is failing, the selector is fine. |
| `only-terminating-pods` | high | Every matched pod has a `deletionTimestamp`; the Service empties out as they finish. |
| `target-port-undeclared` | medium | `targetPort` names or numbers a port no matched container declares. |
| `empty-selector` | medium | No selector, not headless, no manual `Endpoints` — nothing can ever answer. |
| `external-name-empty` | high | `type: ExternalName` with an empty `spec.externalName`; every lookup is NXDOMAIN. |

Intentional silence: a headless Service (`clusterIP: None`) with no selector is
fine, and so is a selector-less Service backed by a hand-maintained `Endpoints`
object pointing outside the cluster.

## Install

```sh
go install github.com/moveeeax/k8s-service-endpoint-audit/cmd/k8s-service-endpoint-audit@latest
```

Or build from a clone:

```sh
go build -o k8s-service-endpoint-audit ./cmd/k8s-service-endpoint-audit
```

## Usage

Audit manifests on disk:

```console
$ k8s-service-endpoint-audit --manifests examples/broken
SEVERITY  NAMESPACE  SERVICE         RULE                    DETAIL
HIGH      shop       checkout        dead-selector           selector {app=checkout-service} matches no pods or workload templates
HIGH      shop       payments        external-name-empty     type ExternalName with an empty spec.externalName
MEDIUM    shop       inventory       target-port-undeclared  grpc targets the named port "grpc-api", which no matched container declares
MEDIUM    shop       legacy-reports  empty-selector          no selector and no endpoints: nothing will ever answer on this Service

shop/checkout (dead-selector)
  selector {app=checkout-service} matches no pods or workload templates
  hint: the labels drifted or the workload is gone; closest label set is Deployment/checkout {app=checkout-api}
...

4 finding(s) across 4 service(s) (manifests mode)
```

Audit a live cluster:

```sh
k8s-service-endpoint-audit --namespace shop
k8s-service-endpoint-audit --all-namespaces --json | jq '.findings[] | select(.severity=="high")'
```

Gate a pull request on high-severity findings only:

```sh
k8s-service-endpoint-audit --manifests ./k8s --severity high --markdown
```

### Flags

```
--manifests DIR       audit a directory or file of YAML/JSON manifests instead of a cluster
--namespace NS        namespace to audit in live mode
--all-namespaces      audit every namespace in live mode
--context NAME        kubeconfig context to use in live mode
--json                emit JSON instead of a table
--markdown            emit a Markdown table for a PR comment
--ignore GLOB,...     skip service names or namespace/name pairs (glob)
--ignore-namespace G  skip namespaces (glob)
--skip-kinds T,...    skip Service types (ClusterIP, NodePort, LoadBalancer, ExternalName)
--skip-rules R,...    skip rule ids
--severity LEVEL      report at this severity or above (low, medium, high; default low)
--version             print the version
```

Exit codes: `0` no findings, `1` findings, `2` load or usage error.

## How it works

Manifest mode walks the tree (skipping dotted directories, `charts/` and
`node_modules/`), decodes every YAML document, and keeps Services, Pods,
EndpointSlices, Endpoints and the pod template of every workload kind including
the one CronJob buries under `jobTemplate.spec.template`.

For each Service it applies equality-based label selection against pods first,
and falls back to workload pod templates when no live pod matches — that is what
makes a never-applied manifest auditable. Readiness comes from EndpointSlices
when present (an absent `ready` condition means ready, per the spec), with the
legacy `Endpoints` object as a fallback. A dead selector also gets a hint naming
the closest label set in the namespace, scored by shared keys and values, which
is nearly always the typo.

Live mode shells out to `kubectl get ... -o json` rather than linking
`client-go`. That keeps the dependency tree at a single YAML parser and means the
tool inherits whatever kubeconfig, context and auth plugin already work on the
machine. Each resource group is a separate call, so a cluster without the
EndpointSlice API degrades to Endpoints instead of failing.

## In CI

```yaml
- name: audit services
  run: |
    go install github.com/moveeeax/k8s-service-endpoint-audit/cmd/k8s-service-endpoint-audit@latest
    k8s-service-endpoint-audit --manifests ./k8s --severity high
```

The non-zero exit on findings is the gate. There is no cluster access involved,
so it runs on a plain `ubuntu-latest` runner.

## Development

```sh
go test ./...
go vet ./...
```

Fixtures live in `internal/*/testdata/`; `examples/healthy` and
`examples/broken` double as the smoke test in CI.

## License

MIT
