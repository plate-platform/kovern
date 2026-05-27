#!/usr/bin/env bash
# Generates /tmp/kovern-local-values.yaml for make local-deploy.
# Usage: bash scripts/gen-local-values.sh
set -euo pipefail

CA_BUNDLE=$(sed 's/^CA_BUNDLE=//' scripts/certs/ca-bundle.env)

cat > /tmp/kovern-local-values.yaml <<EOF
image:
  repository: kovern
  tag: local
  pullPolicy: Never
tls:
  selfSigned: false
  secretName: kovern-webhook-tls
  certDir: /tmp/k8s-webhook-server/serving-certs
webhook:
  caBundle: "${CA_BUNDLE}"
  failurePolicy: Ignore
EOF

echo "Written /tmp/kovern-local-values.yaml"
