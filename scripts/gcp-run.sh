#!/usr/bin/env bash
# Drive a full GCP benchmark for ONE platform (adr-005: one at a time).
#
#   scripts/gcp-run.sh <platform> <config.yaml> [profile] [terraform-var ...]
#
# Steps:
#   1. terraform apply  (deploy/terraform) -> node VMs + loadgen VM + monitoring VM
#   2. rsync repo + built benchrunner to the loadgen VM
#   3. on the node VM(s): run deploy/docker/<platform>/up.sh
#   4. copy the platform's connection.env from a node VM to the loadgen VM,
#      rewriting localhost -> the node's internal IP
#   5. on the loadgen VM: benchrunner run --config <config> --platform <platform>
#   6. rsync results/ back into ./results/
#   7. terraform destroy   (unless KEEP=1)
#
# Requires: terraform, gcloud (for `gcloud compute ssh/scp`).
# Pass the project and any extra settings as full terraform tokens, e.g.:
#   scripts/gcp-run.sh fabric-cft configs/normalized/probe-sweep.yaml gcp-full -var project=my-proj -var node_count=4
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PLATFORM="${1:?usage: gcp-run.sh <platform> <config.yaml> [profile] [tf-var ...]}"
CONFIG="${2:?config path required}"
PROFILE="${3:-gcp-full}"
shift $(( $# >= 3 ? 3 : 2 )) || true
TF_VARS=("$@")

TF_DIR="$ROOT/deploy/terraform"
ZONE="${GCP_ZONE:-us-central1-a}"
SSH_USER="${SSH_USER:-bench}"
KEEP="${KEEP:-0}"
# Set IAP=1 to tunnel SSH/SCP through Identity-Aware Proxy instead of the public IP.
IAP_FLAG=""; [ "${IAP:-0}" = "1" ] && IAP_FLAG="--tunnel-through-iap"

need() { command -v "$1" >/dev/null || { echo "missing $1"; exit 1; }; }
need terraform; need gcloud; need go

echo "== build benchrunner (linux/amd64) =="
GOOS=linux GOARCH=amd64 go build -o "$ROOT/bin/benchrunner.linux" ./cmd/benchrunner

echo "== terraform apply ($PLATFORM / $PROFILE) =="
terraform -chdir="$TF_DIR" init -input=false >/dev/null
terraform -chdir="$TF_DIR" apply -auto-approve -input=false \
  -var "platform=$PLATFORM" -var "profile=$PROFILE" "${TF_VARS[@]}"

tf() { terraform -chdir="$TF_DIR" output -raw "$1"; }
tfjson() { terraform -chdir="$TF_DIR" output -json "$1"; }

LOADGEN_IP="$(tf loadgen_ip)"
NODE0_NAME="bench-${PLATFORM}-node-0"
LOADGEN_NAME="bench-${PLATFORM}-loadgen"
NODE0_INTERNAL="$(tfjson node_internal_ips | python3 -c 'import json,sys;print(json.load(sys.stdin)[0])')"

gssh()  { gcloud compute ssh "$1" --zone "$ZONE" $IAP_FLAG --command "$2"; }
gscp()  { gcloud compute scp --zone "$ZONE" $IAP_FLAG --recurse "$@"; }

echo "== ship repo + binary to loadgen ($LOADGEN_NAME) =="
gssh "$LOADGEN_NAME" "mkdir -p ~/bench"
tar --exclude=.git --exclude=bin --exclude=results --exclude='deploy/docker/*/.cache' -czf - . \
  | gcloud compute ssh "$LOADGEN_NAME" --zone "$ZONE" $IAP_FLAG --command "tar -xzf - -C ~/bench"
gscp "$ROOT/bin/benchrunner.linux" "$LOADGEN_NAME":~/bench/bin/benchrunner

echo "== ship repo to node-0 ($NODE0_NAME) and deploy $PLATFORM =="
gssh "$NODE0_NAME" "mkdir -p ~/bench"
tar --exclude=.git --exclude=bin --exclude=results --exclude='deploy/docker/*/.cache' -czf - . \
  | gcloud compute ssh "$NODE0_NAME" --zone "$ZONE" $IAP_FLAG --command "tar -xzf - -C ~/bench"
gssh "$NODE0_NAME" "cd ~/bench && bash deploy/docker/$PLATFORM/up.sh $PROFILE"

echo "== pull connection.env, rewrite localhost -> $NODE0_INTERNAL =="
gssh "$NODE0_NAME" "cat ~/bench/deploy/docker/$PLATFORM/connection.env" \
  | sed "s/localhost/$NODE0_INTERNAL/g; s/127\\.0\\.0\\.1/$NODE0_INTERNAL/g" \
  | gcloud compute ssh "$LOADGEN_NAME" --zone "$ZONE" $IAP_FLAG \
      --command "cat > ~/bench/deploy/docker/$PLATFORM/connection.env"

echo "== run benchmark on loadgen =="
gssh "$LOADGEN_NAME" "cd ~/bench && set -a && . deploy/docker/$PLATFORM/connection.env && set +a && \
  ./bin/benchrunner run --config $CONFIG --platform $PLATFORM --profile $PROFILE"

echo "== pull results =="
gscp "$LOADGEN_NAME":'~/bench/results/*' "$ROOT/results/" || true

if [ "$KEEP" != "1" ]; then
  echo "== terraform destroy =="
  gssh "$NODE0_NAME" "cd ~/bench && bash deploy/docker/$PLATFORM/down.sh $PROFILE" || true
  terraform -chdir="$TF_DIR" destroy -auto-approve -input=false \
    -var "platform=$PLATFORM" -var "profile=$PROFILE" "${TF_VARS[@]}" </dev/null
else
  echo "KEEP=1 - leaving infra up. Destroy with:"
  echo "  terraform -chdir=$TF_DIR destroy -var platform=$PLATFORM -var profile=$PROFILE"
fi

echo "done. results under $ROOT/results/$PLATFORM/"
