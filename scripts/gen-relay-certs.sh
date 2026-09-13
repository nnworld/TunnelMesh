#!/usr/bin/env bash
# Generate the relay CA and per-node mTLS material consumed by
# server.relay.{ca,cert,key,server_name}.
#
# Output layout (deploy/certs is gitignored):
#   <out>/relay/relay-ca.pem            CA certificate, mounted into every Server
#   <out>/relay/<node-id>/relay.pem     node certificate
#   <out>/relay/<node-id>/relay-key.pem node private key, mode 0600
#   <out>/private/relay-ca-key.pem      CA private key, host only, mode 0600
#
# The CA private key is deliberately kept outside relay/ so a container that
# mounts its own node directory can never read it. Production deployments should
# keep it on the issuing host or in a secret manager instead of this repository.
set -euo pipefail
export LC_ALL=C

SERVER_NAME="relay.tunnelmesh.internal"
CA_DAYS="${CA_DAYS:-3650}"
NODE_DAYS="${NODE_DAYS:-365}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="$ROOT_DIR/deploy/certs"
FORCE=0

usage() {
  cat >&2 <<'EOF'
Usage: gen-relay-certs.sh [--server-name NAME] [--out DIR] [--ca-days N] [--node-days N] [--force] NODE_ID [NODE_ID...]

An existing relay CA is always reused, so this script is safe to re-run when a
node joins the fleet. --force reissues the node certificates listed on the
command line; it never replaces the CA.

NODE_ID must be the exact value of node.id / TUNNELMESH_NODE_ID for each Server
node, because it is written into the certificate subjectAltName. Run
`tunnelmesh-server --config <file> init-node-id` first when node.id is generated.

Example (docker-compose.cluster.yml):
  scripts/gen-relay-certs.sh server-1 server-2
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server-name) SERVER_NAME="${2:?missing server name}"; shift 2 ;;
    --out) OUT_DIR="${2:?missing output directory}"; shift 2 ;;
    --ca-days) CA_DAYS="${2:?missing CA validity days}"; shift 2 ;;
    --node-days) NODE_DAYS="${2:?missing node validity days}"; shift 2 ;;
    --force) FORCE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    --) shift; break ;;
    -*) echo "unknown argument: $1" >&2; usage; exit 2 ;;
    *) break ;;
  esac
done

NODE_IDS=("$@")
if [[ ${#NODE_IDS[@]} -eq 0 ]]; then
  echo "at least one NODE_ID is required" >&2
  usage
  exit 2
fi

if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl is required" >&2
  exit 1
fi

# A DNS label is the only form that can appear in subjectAltName=DNS:..., and the
# relay verifies the peer node id against it verbatim.
valid_dns_label() {
  [[ "$1" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]]
}

for node_id in "${NODE_IDS[@]}"; do
  if ! valid_dns_label "$node_id"; then
    echo "invalid node id '$node_id': must be a lowercase DNS label (a-z, 0-9, '-', 1-63 chars, no leading/trailing '-')" >&2
    exit 2
  fi
done
if ! valid_dns_label "$SERVER_NAME" && [[ ! "$SERVER_NAME" =~ ^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$ ]]; then
  echo "invalid server name '$SERVER_NAME': must be a lowercase DNS name" >&2
  exit 2
fi

RELAY_DIR="$OUT_DIR/relay"
PRIVATE_DIR="$OUT_DIR/private"
CA_CERT="$RELAY_DIR/relay-ca.pem"
CA_KEY="$PRIVATE_DIR/relay-ca-key.pem"

install -d -m 0755 "$RELAY_DIR"
install -d -m 0700 "$PRIVATE_DIR"

# Every node shares one CA, so an existing CA is always reused. That keeps
# "add server-3 to the fleet" a plain re-run. Rotating the CA invalidates every
# node certificate signed by it and is intentionally a manual, explicit act.
if [[ -f "$CA_KEY" || -f "$CA_CERT" ]]; then
  if [[ ! -f "$CA_KEY" || ! -f "$CA_CERT" ]]; then
    echo "found a partial relay CA under $OUT_DIR (certificate or key missing)." >&2
    echo "Restore the pair, or remove both to start a new CA:" >&2
    echo "  $CA_CERT" >&2
    echo "  $CA_KEY" >&2
    exit 1
  fi
  echo "reusing existing relay CA: $CA_CERT"
fi

if [[ ! -f "$CA_KEY" ]]; then
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$CA_KEY"
  chmod 0600 "$CA_KEY"
fi

if [[ ! -f "$CA_CERT" ]]; then
  openssl req -x509 -new -sha256 -days "$CA_DAYS" \
    -key "$CA_KEY" \
    -subj "/O=TunnelMesh/CN=TunnelMesh Relay CA" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -out "$CA_CERT"
  chmod 0644 "$CA_CERT"
  echo "created relay CA: $CA_CERT"
fi

for node_id in "${NODE_IDS[@]}"; do
  node_dir="$RELAY_DIR/$node_id"
  cert="$node_dir/relay.pem"
  key="$node_dir/relay-key.pem"
  csr="$node_dir/relay.csr"
  ext="$node_dir/relay.ext"

  if [[ -f "$cert" && "$FORCE" -ne 1 ]]; then
    echo "skipping $node_id: $cert already exists (use --force to reissue)"
    continue
  fi

  install -d -m 0755 "$node_dir"
  # Each node gets its own key pair; keys and certificates are never shared.
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$key"
  chmod 0600 "$key"
  openssl req -new -sha256 -key "$key" -subj "/O=TunnelMesh/CN=${node_id}-relay" -out "$csr"

  cat > "$ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth,clientAuth
subjectAltName=DNS:${node_id},DNS:${SERVER_NAME}
EOF

  openssl x509 -req -in "$csr" -CA "$CA_CERT" -CAkey "$CA_KEY" -CAcreateserial \
    -days "$NODE_DAYS" -sha256 -extfile "$ext" -out "$cert" 2>/dev/null
  chmod 0644 "$cert"

  # Fail here rather than at Server startup: the relay verifies the peer
  # certificate SAN against the claimed node id and the shared server name.
  openssl verify -CAfile "$CA_CERT" "$cert" >/dev/null
  san="$(openssl x509 -in "$cert" -noout -ext subjectAltName | tr -d ' ')"
  for required in "DNS:${node_id}" "DNS:${SERVER_NAME}"; do
    if [[ "$san" != *"$required"* ]]; then
      echo "issued certificate for $node_id is missing $required" >&2
      exit 1
    fi
  done

  rm -f "$csr" "$ext"
  echo "issued $node_id -> $cert"
done

cat <<EOF

Done. Wire these into each Server node:

  server.relay.ca          = $CA_CERT
  server.relay.cert        = $RELAY_DIR/<node-id>/relay.pem
  server.relay.key         = $RELAY_DIR/<node-id>/relay-key.pem
  server.relay.server_name = $SERVER_NAME

Keep $CA_KEY on the issuing host or in a secret manager; Server nodes only need
the CA certificate. deploy/certs/ is gitignored - verify with:

  git check-ignore -v deploy/certs/relay/relay-ca.pem
EOF
