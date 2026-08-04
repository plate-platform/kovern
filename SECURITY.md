# Security Policy

Kovern runs as a `ValidatingAdmissionWebhook` and a set of Kubernetes
controllers with RBAC access across namespaces you configure it for. A
vulnerability here can mean bypassed budget enforcement, bypassed loop
remediation, or broader cluster access than intended — please report
responsibly rather than filing a public issue.

## Supported versions

Kovern is pre-1.0 and moving fast. Only the latest released version (see
[Releases](https://github.com/plate-platform/kovern/releases)) and `main`
are supported with security fixes.

| Version | Supported |
|---|---|
| Latest release | ✅ |
| `main` | ✅ |
| Older releases | ❌ |

## Reporting a vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Report privately using GitHub's
[private vulnerability reporting](https://github.com/plate-platform/kovern/security/advisories/new)
(Security tab → "Report a vulnerability"). This reaches maintainers directly
and keeps the report confidential until a fix is available.

Please include:
- The affected component (webhook, controller, OTLP receiver, remediation
  executor, Helm chart, etc.)
- Kovern version / commit SHA and Kubernetes version
- Steps to reproduce, or a minimal `TokenQuota`/`LivelockPolicy` + workload
  that demonstrates the issue
- What you'd expect to happen vs. what actually happens

## What's in scope

- Budget enforcement bypass (a pod scheduled despite an `Exceeded`/`Suspended`
  `TokenQuota`)
- Loop detection / remediation bypass or unintended remediation of the wrong
  workload
- RBAC/privilege escalation via the operator's ServiceAccount
- Admission webhook denial-of-service (e.g. a request that hangs or crashes
  the webhook, since `failurePolicy` determines whether that fails open or
  closed cluster-wide)
- Injection via CRD fields (`TokenQuota`, `LivelockPolicy` spec values) into
  logs, events, or the semantic detector's LLM prompt

## What's out of scope

- Vulnerabilities that require already having cluster-admin or equivalent
  access to the namespaces Kovern manages
- The optional semantic loop detector's LLM output being wrong/hallucinated
  (a quality issue, not a security one — file a regular issue)
- Issues in upstream dependencies without a demonstrated Kovern-specific
  impact (report those upstream; `govulncheck` already runs in CI)

## Response

We aim to acknowledge reports within a few days and will keep you updated as
a fix is developed. Credit is given in the release notes unless you'd prefer
otherwise.
