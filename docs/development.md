# Development Guide

## Prerequisites

| Tool | Version | Install |
|---|---|---|
| Go | 1.26+ | [go.dev/dl](https://go.dev/dl) |
| Docker Desktop | any | [docker.com](https://www.docker.com/products/docker-desktop) — enable Kubernetes in Settings |
| Helm | 3+ | `brew install helm` |
| kubectl | any | bundled with Docker Desktop |
| git | any | `brew install git` |

> Docker Desktop's built-in Kubernetes is used for the local cluster. Kind or a remote cluster also work — see [Using a different cluster](#using-a-different-cluster).

---

## First-time setup

```bash
git clone https://github.com/plate-platform/kovern.git
cd kovern

# Install lint, vuln, and scan tools (one-time — skips anything already installed)
make dev-setup
```

`make dev-setup` installs:
- **govulncheck** — Go vulnerability scanner (`go install`)
- **golangci-lint** — aggregated linter (`brew install`)
- **trivy** — container and filesystem vulnerability scanner (`brew install`)
- **kubeconform** — Kubernetes manifest schema validator (`brew install`)

---

## Running tests

```bash
# Unit tests — no cluster, no API keys, runs in seconds
make test

# Unit tests with HTML coverage report → coverage.html
make test-coverage

# Integration tests — spins up a real K8s API server + etcd via envtest
# Downloads envtest binaries on first run (~30s), cached after that
make test-integration
```

---

## Code quality

```bash
# Lint (golangci-lint with .golangci.yml config)
make lint

# Vulnerability scan (fails only if your code calls a vulnerable function)
make vuln

# Trivy: scan the source tree for secrets and misconfigurations
make scan-fs

# Trivy: scan the Helm chart for K8s misconfigurations
make scan-helm

# kubeconform: validate rendered Helm templates against K8s JSON schemas
make helm-validate

# Run all of the above in one shot
make security
```

---

## Local cluster deployment (Docker Desktop)

This path builds the operator image, generates self-signed TLS certs for the
admission webhook, and installs the Helm chart on your local cluster.

```bash
# Full first-time deploy (build image → generate certs → helm install)
make local-deploy

# Verify the operator is running
kubectl get pods -n kovern-system
kubectl get crd | grep kovern.io

# Watch operator logs
kubectl logs -n kovern-system -l app.kubernetes.io/name=kovern -f
```

### Re-deploying after code changes

```bash
make local-image-load                                          # rebuild + import image
kubectl rollout restart deployment/kovern -n kovern-system    # restart pods
```

### Tear down

```bash
make local-clean
```

---

## Running the operator locally (no cluster webhook)

Useful for rapid iteration on controller logic. The admission webhook is
disabled in this mode since it requires in-cluster TLS.

```bash
# Runs against your current kubeconfig context
make run
```

---

## Environment variables

| Variable | Required | Purpose |
|---|---|---|
| `ANTHROPIC_API_KEY` | Only for semantic loop detection | Enables `LivelockPolicy.spec.detection.semantic` |
| `KUBECONFIG` | No | Defaults to `~/.kube/config` |

To enable semantic detection locally:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
make run
```

Then set `spec.detection.semantic.enabled: true` in a `LivelockPolicy`.

---

## E2e tests (requires Kovern deployed)

```bash
# Deploy first
make local-deploy

# Then run
make e2e
```

---

## Using a different cluster

Set `KUBECONFIG` or switch context before running deploy/e2e targets:

```bash
kubectl config use-context my-other-cluster
make local-deploy   # deploys to the active context
```

For kind:
```bash
kind create cluster --name kovern-dev
make local-deploy   # skips the Docker Desktop containerd import step — update local-image-load if needed
```

---

## Useful make targets

```
make help          # full list of targets with descriptions
make build         # compile binary to bin/kovern
make fmt           # check gofmt compliance
make vet           # run go vet
make tidy          # run go mod tidy
make docker-build  # build Docker image (IMG=ghcr.io/plate-platform/kovern:latest)
make helm-template # render chart templates without installing
```
