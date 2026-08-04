# Contributing to Kovern

Thanks for taking a look at Kovern. This project is early — issues, design
pushback, and PRs are all genuinely welcome.

By participating, you're expected to follow the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Before you start

- **Small fix or bug?** Just open a PR.
- **New feature or anything that changes behavior/CRD shape?** Open an issue
  first so we can agree on the approach before you put time into it.
- **Structural/architectural change** (package layout, reconciler design,
  webhook behavior)? Read the [ADRs](docs/adr/) first — they capture
  constraints and trade-offs that aren't obvious from the code alone. If your
  change conflicts with one, say so in the issue/PR rather than silently
  working around it.
- Check [`docs/roadmap.md`](docs/roadmap.md) for current phase status so you
  know whether something is intentionally unimplemented vs. actually broken.

## Development setup

See [`docs/development.md`](docs/development.md) for prerequisites, running
tests, linting, and deploying to a local cluster. The short version:

```bash
git clone https://github.com/plate-platform/kovern.git
cd kovern
make dev-setup   # one-time: installs golangci-lint, govulncheck, trivy, kubeconform
make test        # unit tests — no cluster needed, runs in seconds
```

## Key invariants to know before touching webhook/ledger code

- The admission webhook **must never** call the Kubernetes API on the hot
  path — it reads only from `internal/ledger.Cache`.
- Fail-open: no `TokenQuota` for a namespace+ServiceAccount → the webhook
  allows the pod. Fail-open on ledger read errors too — a ledger bug must
  never block a legitimate workload.
- See [`docs/adr/`](docs/adr/) for the reasoning behind these and other
  structural decisions.

## Making changes

1. Fork and branch from `main`.
2. Write tests for what you change. `internal/otelreceiver`, `internal/pricing`,
   and `internal/remediation` are examples of packages that went from zero to
   full coverage — that's the bar.
3. Run before opening a PR:
   ```bash
   make fmt
   make lint
   make vet
   make test
   ```
4. Commit messages: `<type>(<scope>): <summary>`, types are `feat`, `fix`,
   `test`, `docs`, `refactor`, `chore`. Examples:
   - `feat(webhook): deny pod creates when quota exceeded`
   - `fix(ledger): fix race condition in concurrent RecordSpend`
   - `test(webhook): add soft-limit warning assertion`
5. Open the PR against `main`. Describe *why*, not just *what* — the diff
   already shows what changed.

## Adding a new CRD

If your change introduces a new CRD, follow the pattern in
[`CLAUDE.md`](CLAUDE.md#adding-a-new-crd):

1. Type file in `api/v1alpha1/<name>_types.go`
2. `DeepCopyInto`/`DeepCopy`/`DeepCopyObject` in `zz_generated_deepcopy.go`
3. Register in `init()` via `SchemeBuilder.Register`
4. Reconciler in `internal/controller/`
5. Helm template in `charts/kovern/templates/crd-<name>.yaml`
6. RBAC update in `charts/kovern/templates/rbac.yaml`

## Reporting bugs

Open a GitHub issue with:
- What you expected vs. what happened
- Kovern version / Helm chart version
- Kubernetes version and distribution (kind, Docker Desktop, EKS, etc.)
- Relevant `TokenQuota`/`LivelockPolicy` spec if applicable

For **security vulnerabilities**, do not open a public issue — see
[SECURITY.md](SECURITY.md).

## Questions

Open an [issue](https://github.com/plate-platform/kovern/issues) tagged
`question`.
