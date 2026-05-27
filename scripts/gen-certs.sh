#!/usr/bin/env bash
# Generates a self-signed CA + webhook server cert for local development.
# Outputs files to scripts/certs/ and creates the kovern-webhook-tls Secret.
# Usage: bash scripts/gen-certs.sh [namespace]
set -euo pipefail

NAMESPACE="${1:-kovern-system}"
SERVICE="kovern-webhook"
CERT_DIR="scripts/certs"

mkdir -p "$CERT_DIR"

echo "Generating self-signed CA..."
openssl genrsa -out "$CERT_DIR/ca.key" 2048 2>/dev/null
openssl req -new -x509 -days 3650 -key "$CERT_DIR/ca.key" \
  -out "$CERT_DIR/ca.crt" \
  -subj "/CN=kovern-ca/O=kovern" 2>/dev/null

echo "Generating webhook server cert (SAN: $SERVICE.$NAMESPACE.svc)..."
openssl genrsa -out "$CERT_DIR/tls.key" 2048 2>/dev/null

cat > "$CERT_DIR/san.cnf" <<EOF
[req]
req_extensions = v3_req
distinguished_name = req_distinguished_name
[req_distinguished_name]
[v3_req]
basicConstraints = CA:FALSE
keyUsage = nonRepudiation, digitalSignature, keyEncipherment
subjectAltName = @alt_names
[alt_names]
DNS.1 = ${SERVICE}.${NAMESPACE}.svc
DNS.2 = ${SERVICE}.${NAMESPACE}.svc.cluster.local
EOF

openssl req -new -key "$CERT_DIR/tls.key" \
  -out "$CERT_DIR/tls.csr" \
  -subj "/CN=${SERVICE}.${NAMESPACE}.svc" \
  -config "$CERT_DIR/san.cnf" 2>/dev/null

openssl x509 -req -days 3650 \
  -in "$CERT_DIR/tls.csr" \
  -CA "$CERT_DIR/ca.crt" \
  -CAkey "$CERT_DIR/ca.key" \
  -CAcreateserial \
  -out "$CERT_DIR/tls.crt" \
  -extensions v3_req \
  -extfile "$CERT_DIR/san.cnf" 2>/dev/null

echo "Creating namespace $NAMESPACE..."
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace "$NAMESPACE" kovern.io/exclude=true --overwrite

echo "Creating kovern-webhook-tls Secret..."
kubectl create secret tls kovern-webhook-tls \
  --cert="$CERT_DIR/tls.crt" \
  --key="$CERT_DIR/tls.key" \
  --namespace="$NAMESPACE" \
  --dry-run=client -o yaml | kubectl apply -f -

CA_BUNDLE=$(base64 -i "$CERT_DIR/ca.crt" | tr -d '\n')
echo ""
echo "CA bundle (base64):"
echo "$CA_BUNDLE"
echo ""
echo "Done. Use --set webhook.caBundle=$CA_BUNDLE in helm install."
echo "CA_BUNDLE=$CA_BUNDLE" > "$CERT_DIR/ca-bundle.env"
