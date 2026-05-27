# hack/

Shell scripts used exclusively for **local development**. These are not run in
production — the production Helm chart uses cert-manager for TLS instead.

## Scripts

### `gen-certs.sh`

Generates a self-signed CA and webhook server certificate for use in a local
cluster (Docker Desktop or kind). The admission webhook requires TLS; in production
cert-manager handles this automatically. Locally we need to generate the certs
ourselves because cert-manager is not a prerequisite for local dev.

Outputs:
- `hack/certs/ca.crt` / `ca.key` — self-signed CA
- `hack/certs/tls.crt` / `tls.key` — webhook server cert (SAN: `kovern-webhook.<namespace>.svc`)
- `hack/certs/ca-bundle.env` — base64-encoded CA bundle (consumed by `gen-local-values.sh`)
- `kovern-webhook-tls` Secret in the target namespace

Usage:
```bash
bash hack/gen-certs.sh [namespace]   # default namespace: kovern-system
```

Called automatically by `make local-deploy`.

---

### `gen-local-values.sh`

Reads the CA bundle written by `gen-certs.sh` and generates
`/tmp/kovern-local-values.yaml` — a Helm values override file that:
- Points the chart at the local `kovern:local` image (`pullPolicy: Never`)
- Disables the self-signed cert generator inside the chart (we already have certs)
- Injects the CA bundle so the Kubernetes API server trusts the webhook TLS cert

Usage:
```bash
bash hack/gen-local-values.sh
```

Called automatically by `make local-deploy` (after `gen-certs.sh`).

---

## `certs/`

Generated at runtime by `gen-certs.sh`. Contains private keys — **never commit this
directory**. It is listed in `.gitignore`.
