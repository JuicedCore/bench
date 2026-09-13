#!/usr/bin/env bash
# Run a benchmark campaign on GCP: every platform on its own freshly created,
# identical pair of VMs, strictly one platform at a time (adr-005).
#
#   scripts/gcp-run.sh --project my-proj [options]
#
# Options:
#   --project ID          GCP project (or GCP_PROJECT, or `project` in terraform.tfvars)
#   --profile NAME        deploy/profiles/NAME.yaml with a `gcp:` block   (default: gcp-small)
#   --platforms "A B"     platforms, run in this order        (default: fabric-cft fabric-bft drunix)
#   --configs "X Y"       run configs, each on a fresh deploy
#                         (default: configs/normalized/quick-smoke.yaml configs/normalized/probe-sweep.yaml)
#   --results-bucket B    also archive results to gs://B/<campaign>/
#   --local-state         keep Terraform state on this machine instead of GCS
#   --keep                leave the last platform's VMs up (you pay for them until destroyed)
#   --tf-var k=v          extra Terraform variable; repeatable
#   --dry-run             print the plan and exit, touching nothing
#
# Per platform:
#   1. terraform apply (deploy/terraform, workspace = platform): platform VM + load-generator VM,
#      no public IPs, sized from the profile's `gcp:` block
#   2. wait for both VMs to finish scripts/install-deps.sh (startup script)
#   3. ship the working tree to both; preflight the platform VM against the profile
#   4. mutual-TLS Docker API on the platform VM, so benchrunner on the load generator
#      can sample containers and detect OOM kills there; monitoring stack on the platform VM
#   5. for each config: deploy the platform, copy connection.env and the crypto it names
#      to the load generator, run benchrunner, tear the platform down
#   6. pull results into ./results, record the hardware, archive to GCS if asked
#   7. terraform destroy - also on failure or Ctrl-C, unless --keep
#
# Needs on this machine: terraform (>= 1.5), gcloud (authenticated), openssl, python3
# with PyYAML. Needs in GCP: see docs/guides/gcp-deployment.md (APIs, IAM roles).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PROJECT="${GCP_PROJECT:-}"
PROFILE="gcp-small"
PLATFORMS="fabric-cft fabric-bft drunix"
CONFIGS="configs/normalized/quick-smoke.yaml configs/normalized/probe-sweep.yaml"
BUCKET=""
LOCAL_STATE=0
KEEP=0
DRY_RUN=0
EXTRA_TF=()

usage() { sed -n '2,34p' "$0"; exit "${1:-0}"; }
while [ $# -gt 0 ]; do
  case "$1" in
    --project)        PROJECT="$2"; shift 2 ;;
    --profile)        PROFILE="$2"; shift 2 ;;
    --platforms)      PLATFORMS="$2"; shift 2 ;;
    --configs)        CONFIGS="$2"; shift 2 ;;
    --results-bucket) BUCKET="${2#gs://}"; shift 2 ;;
    --local-state)    LOCAL_STATE=1; shift ;;
    --keep)           KEEP=1; shift ;;
    --dry-run)        DRY_RUN=1; shift ;;
    --tf-var)         EXTRA_TF+=(-var "$2"); shift 2 ;;
    -h|--help)        usage ;;
    *) echo "unknown argument: $1" >&2; usage 64 ;;
  esac
done

log()  { printf '\033[1;34m[gcp %s]\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
warn() { printf '\033[1;33m[gcp %s]\033[0m %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die()  { printf '\033[1;31m[gcp]\033[0m %s\n' "$*" >&2; exit 1; }

command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' 2>/dev/null \
  || die "python3 with PyYAML required (pip install pyyaml, or sudo scripts/install-deps.sh)"

TF_DIR="$ROOT/deploy/terraform"
# The VMs get the working tree without .git, so the manifest's harness_git_sha
# comes from here. "-dirty" marks uncommitted changes in what was shipped.
HARNESS_SHA="$(git -C "$ROOT" describe --always --dirty --abbrev=12 2>/dev/null || echo unknown)"
PROFILE_FILE="$ROOT/deploy/profiles/${PROFILE}.yaml"
[ -f "$PROFILE_FILE" ] || die "no profile $PROFILE_FILE"
for c in $CONFIGS; do [ -f "$c" ] || die "no config $c"; done
for p in $PLATFORMS; do [ -f "deploy/docker/$p/up.sh" ] || die "no deploy script for platform $p"; done

gcp_field() {
  python3 - "$PROFILE_FILE" "$1" <<'PY'
import sys, yaml
g = (yaml.safe_load(open(sys.argv[1])) or {}).get("gcp") or {}
v = g.get(sys.argv[2])
if v is None:
    sys.exit(f"profile {sys.argv[1]} has no gcp.{sys.argv[2]}")
print(v)
PY
}
SUT_TYPE="$(gcp_field sut_machine_type)"
LOADGEN_TYPE="$(gcp_field loadgen_machine_type)"
SUT_DISK="$(gcp_field sut_disk_gb)"

TF_ARGS=(-var "profile=$PROFILE" -var "sut_machine_type=$SUT_TYPE"
         -var "loadgen_machine_type=$LOADGEN_TYPE" -var "sut_disk_gb=$SUT_DISK")
[ -n "$PROJECT" ] && TF_ARGS+=(-var "project=$PROJECT")
TF_ARGS+=("${EXTRA_TF[@]+"${EXTRA_TF[@]}"}")

if [ "$DRY_RUN" = 1 ]; then
  echo "profile:    $PROFILE  (platform VM $SUT_TYPE, ${SUT_DISK} GB; load generator $LOADGEN_TYPE)"
  echo "project:    ${PROJECT:-<from terraform.tfvars>}"
  echo "state:      $([ "$LOCAL_STATE" = 1 ] && echo "local" || echo "GCS (deploy/terraform/backend.hcl)")"
  echo "platforms:  $PLATFORMS"
  echo "configs:    $CONFIGS"
  echo "archive:    ${BUCKET:+gs://$BUCKET/}${BUCKET:-none}"
  echo "keep VMs:   $([ "$KEEP" = 1 ] && echo yes || echo "no - destroyed after each platform")"
  echo "harness:    $HARNESS_SHA"
  echo "terraform:  ${TF_ARGS[*]}"
  exit 0
fi

for t in terraform gcloud openssl tar; do
  command -v "$t" >/dev/null 2>&1 || die "missing $t on this machine"
done

CAMPAIGN="$(date -u +%Y%m%dT%H%M%SZ)-${PROFILE}"
CAMPAIGN_START="$(date +%Y-%m-%dT%H:%M:%S%:z)"
CAMPAIGN_DIR="$ROOT/results/_campaigns/$CAMPAIGN"
mkdir -p "$CAMPAIGN_DIR"
exec > >(tee -a "$CAMPAIGN_DIR/gcp-run.log") 2>&1
log "campaign ${CAMPAIGN}: platforms [${PLATFORMS}] configs [${CONFIGS}]"
log "hardware: platform ${SUT_TYPE} (${SUT_DISK} GB), load generator ${LOADGEN_TYPE}"

# --- Terraform backend ---------------------------------------------------------
BACKEND_OVERRIDE="$TF_DIR/local_backend_override.tf"
if [ "$LOCAL_STATE" = 1 ]; then
  printf 'terraform {\n  backend "local" {}\n}\n' > "$BACKEND_OVERRIDE"
  terraform -chdir="$TF_DIR" init -input=false -reconfigure >/dev/null
else
  rm -f "$BACKEND_OVERRIDE"
  [ -f "$TF_DIR/backend.hcl" ] || die "no deploy/terraform/backend.hcl - copy backend.hcl.example, or pass --local-state"
  terraform -chdir="$TF_DIR" init -input=false -reconfigure -backend-config=backend.hcl >/dev/null
fi

tfo() { terraform -chdir="$TF_DIR" output -raw "$1"; }

# --- remote helpers --------------------------------------------------------------
ZONE=""
# rsh VM CMD: run CMD in a login shell on VM through IAP (PATH includes Go).
rsh() {
  gcloud compute ssh "$1" --zone "$ZONE" --tunnel-through-iap --quiet \
    --command "bash -lc $(printf '%q' "$2")" -- -o ServerAliveInterval=30 -o LogLevel=ERROR </dev/null
}
# rsh_in VM CMD: as rsh, with this script's stdin forwarded.
rsh_in() {
  gcloud compute ssh "$1" --zone "$ZONE" --tunnel-through-iap --quiet \
    --command "bash -lc $(printf '%q' "$2")" -- -o LogLevel=ERROR
}

wait_ready() {
  local vm="$1" i=0
  log "waiting for ${vm} to finish provisioning"
  while [ "$i" -lt 90 ]; do
    if state="$(rsh "$vm" 'if [ -f /var/lib/bench/ready ]; then echo ready; elif [ -f /var/lib/bench/failed ]; then echo failed; else echo pending; fi' 2>/dev/null)"; then
      case "$state" in
        *ready*)  log "${vm} ready"; return 0 ;;
        *failed*) rsh "$vm" 'sudo tail -40 /var/log/bench-install.log' || true
                  die "${vm}: install-deps failed" ;;
      esac
    fi
    i=$((i+1)); sleep 20
  done
  die "${vm} did not finish provisioning within 30 minutes"
}

ship_tree() {
  local vm="$1"
  rsh "$vm" 'rm -rf ~/bench && mkdir -p ~/bench'
  tar -C "$ROOT" \
    --exclude=.git --exclude=./bin --exclude=./results --exclude='*/.cache' \
    --exclude='*/connection.env' --exclude='*/.terraform' --exclude='*.tfstate*' \
    --exclude='deploy/terraform/backend.hcl' --exclude='deploy/terraform/terraform.tfvars' \
    -czf - . | rsh_in "$vm" 'tar -xzf - -C ~/bench'
}

# Mutual-TLS Docker API on the platform VM, reachable only from the load generator
# (firewall) and only with a client certificate signed by a CA whose key never
# leaves this machine and is deleted when the script exits.
setup_docker_tls() {
  local sut="$1" sut_ip="$2" loadgen="$3" d
  d="$(mktemp -d)"
  (
    cd "$d"
    openssl genrsa -out ca-key.pem 3072 2>/dev/null
    openssl req -x509 -new -key ca-key.pem -sha256 -days 3 -subj "/CN=bench-docker-ca" -out ca.pem
    openssl genrsa -out server-key.pem 3072 2>/dev/null
    openssl req -new -key server-key.pem -subj "/CN=${sut}" -out server.csr
    printf 'subjectAltName=IP:%s,DNS:%s\nextendedKeyUsage=serverAuth\n' "$sut_ip" "$sut" > server.ext
    openssl x509 -req -in server.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial -days 3 -sha256 \
      -extfile server.ext -out server.pem 2>/dev/null
    openssl genrsa -out key.pem 3072 2>/dev/null
    openssl req -new -key key.pem -subj "/CN=bench-loadgen" -out client.csr
    printf 'extendedKeyUsage=clientAuth\n' > client.ext
    openssl x509 -req -in client.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial -days 3 -sha256 \
      -extfile client.ext -out cert.pem 2>/dev/null
  )
  tar -C "$d" -czf - ca.pem server.pem server-key.pem | rsh_in "$sut" "
    set -e
    sudo install -d -m 0700 /etc/docker/bench-tls
    sudo tar -xzf - -C /etc/docker/bench-tls
    sudo chmod 0600 /etc/docker/bench-tls/server-key.pem
    sudo install -d /etc/systemd/system/docker.service.d
    printf '%s\n' '[Service]' 'ExecStart=' \
      'ExecStart=/usr/bin/dockerd -H fd:// -H tcp://${sut_ip}:2376 --tlsverify --tlscacert=/etc/docker/bench-tls/ca.pem --tlscert=/etc/docker/bench-tls/server.pem --tlskey=/etc/docker/bench-tls/server-key.pem --containerd=/run/containerd/containerd.sock' \
      | sudo tee /etc/systemd/system/docker.service.d/bench-tls.conf >/dev/null
    sudo systemctl daemon-reload
    sudo systemctl restart docker"
  tar -C "$d" -czf - ca.pem cert.pem key.pem | rsh_in "$loadgen" '
    set -e
    install -d -m 0700 ~/.docker/bench
    tar -xzf - -C ~/.docker/bench
    chmod 0600 ~/.docker/bench/key.pem'
  rm -rf "$d"
}

# --- cleanup ------------------------------------------------------------------
CURRENT_PLATFORM=""
destroy_current() {
  [ -n "$CURRENT_PLATFORM" ] || return 0
  if [ "$KEEP" = 1 ]; then
    warn "--keep: ${CURRENT_PLATFORM} VMs left running. Destroy with:"
    warn "  terraform -chdir=$TF_DIR workspace select ${CURRENT_PLATFORM} && terraform -chdir=$TF_DIR destroy ${TF_ARGS[*]} -var platform=${CURRENT_PLATFORM}"
    return 0
  fi
  log "destroying ${CURRENT_PLATFORM} infrastructure"
  terraform -chdir="$TF_DIR" workspace select "$CURRENT_PLATFORM" >/dev/null
  terraform -chdir="$TF_DIR" destroy -auto-approve -input=false "${TF_ARGS[@]}" \
    -var "platform=$CURRENT_PLATFORM" </dev/null \
    || warn "terraform destroy failed for ${CURRENT_PLATFORM} - check the project for leftover VMs"
  CURRENT_PLATFORM=""
}
# shellcheck disable=SC2154  # rc is assigned inside the trap string
trap 'rc=$?; destroy_current; rm -f "$BACKEND_OVERRIDE"; exit $rc' EXIT
trap 'exit 130' INT TERM

FAILED=()

# --- per platform ------------------------------------------------------------------
for PLATFORM in $PLATFORMS; do
  log "==================== ${PLATFORM} ===================="
  terraform -chdir="$TF_DIR" workspace select -or-create "$PLATFORM" >/dev/null
  CURRENT_PLATFORM="$PLATFORM"
  terraform -chdir="$TF_DIR" apply -auto-approve -input=false "${TF_ARGS[@]}" -var "platform=$PLATFORM"

  ZONE="$(tfo zone)"
  SUT="$(tfo sut_name)"; SUT_IP="$(tfo sut_internal_ip)"
  LOADGEN="$(tfo loadgen_name)"

  wait_ready "$SUT"
  wait_ready "$LOADGEN"

  for vm in "$SUT" "$LOADGEN"; do
    # OS Login users are not in the docker group by default. A new SSH session
    # picks the group up.
    rsh "$vm" 'sudo usermod -aG docker "$(id -un)"'
    ship_tree "$vm"
  done

  log "recording hardware"
  {
    echo "campaign: $CAMPAIGN"; echo "platform: $PLATFORM"; echo "profile: $PROFILE"
    echo "sut: $SUT ($SUT_TYPE)"; echo "loadgen: $LOADGEN ($LOADGEN_TYPE)"; echo "zone: $ZONE"
    echo "--- sut lscpu"; rsh "$SUT" 'lscpu; free -g; uname -a; docker version --format "docker {{.Server.Version}}"'
    echo "--- loadgen lscpu"; rsh "$LOADGEN" 'lscpu | head -20; uname -a'
  } > "$CAMPAIGN_DIR/${PLATFORM}-hardware.txt" 2>&1 || warn "could not record hardware"

  rsh "$SUT" "cd ~/bench && scripts/preflight.sh --remote-loadgen $PROFILE" || {
    [ $? -eq 2 ] && warn "preflight warnings on ${SUT} (see above)" || die "preflight failed on ${SUT}"
  }

  log "docker API over mutual TLS on ${SUT}"
  setup_docker_tls "$SUT" "$SUT_IP" "$LOADGEN"
  rsh "$SUT" 'cd ~/bench && bash deploy/docker/monitoring/up.sh' \
    || warn "monitoring stack did not start; runs continue without Prometheus charts"

  log "building benchrunner on ${LOADGEN}"
  rsh "$LOADGEN" 'cd ~/bench && make build'

  for CONFIG in $CONFIGS; do
    log "---- ${PLATFORM}: ${CONFIG} ----"
    if ! rsh "$SUT" "cd ~/bench && bash deploy/docker/$PLATFORM/up.sh $PROFILE"; then
      warn "deploy failed: ${PLATFORM} ${CONFIG}"
      FAILED+=("${PLATFORM}:${CONFIG}:deploy")
      rsh "$SUT" "cd ~/bench && bash deploy/docker/$PLATFORM/down.sh $PROFILE" || true
      continue
    fi

    # connection.env names crypto material (TLS CA, client cert and key) by absolute
    # path on the platform VM. Ship those files to the load generator and rewrite the
    # paths and endpoints for it.
    rsh_in "$SUT" "cd ~/bench && python3 - deploy/docker/$PLATFORM/connection.env" <<'PY' > "$CAMPAIGN_DIR/.paths"
import os, sys
root = os.path.expanduser("~/bench")
for line in open(sys.argv[1]):
    if "=" not in line or line.lstrip().startswith("#"):
        continue
    v = line.split("=", 1)[1].strip().strip('"')
    if v.startswith(root + "/") and os.path.exists(v):
        print(os.path.relpath(v, root))
PY
    if [ -s "$CAMPAIGN_DIR/.paths" ]; then
      rsh "$SUT" "cd ~/bench && tar -czf - $(tr '\n' ' ' < "$CAMPAIGN_DIR/.paths")" \
        | rsh_in "$LOADGEN" 'cd ~/bench && tar -xzf -'
    fi
    rsh "$SUT" "cat ~/bench/deploy/docker/$PLATFORM/connection.env" \
      | sed -e "s/localhost/${SUT_IP}/g" -e "s/127\.0\.0\.1/${SUT_IP}/g" \
      | rsh_in "$LOADGEN" "cat > ~/bench/deploy/docker/$PLATFORM/connection.env"
    rm -f "$CAMPAIGN_DIR/.paths"

    if ! rsh "$LOADGEN" "cd ~/bench && set -a && . deploy/docker/$PLATFORM/connection.env && set +a && \
        export BENCH_HARNESS_GIT_SHA=${HARNESS_SHA} DOCKER_HOST=tcp://${SUT_IP}:2376 DOCKER_TLS_VERIFY=1 DOCKER_CERT_PATH=\$HOME/.docker/bench \
               BENCH_PROMETHEUS_URL=http://${SUT_IP}:9090 && \
        docker info --format 'remote docker: {{.Name}} ({{.NCPU}} CPU)' && \
        ./bin/benchrunner run --progress --config $CONFIG --platform $PLATFORM --profile $PROFILE"; then
      warn "run failed: ${PLATFORM} ${CONFIG}"
      FAILED+=("${PLATFORM}:${CONFIG}:run")
    fi
    rsh "$SUT" "cd ~/bench && bash deploy/docker/$PLATFORM/down.sh $PROFILE" || warn "teardown reported an error"
  done

  log "pulling results"
  mkdir -p "$ROOT/results"
  rsh "$LOADGEN" 'cd ~/bench && [ -d results ] && tar -czf - --exclude=.gitkeep results || true' \
    | tar -xzf - -C "$ROOT" || warn "no results pulled from ${LOADGEN}"

  destroy_current
done

# --- campaign wrap-up --------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  log "building the comparison report"
  go build -o "$ROOT/bin/benchrunner" ./cmd/benchrunner
  "$ROOT/bin/benchrunner" report --results-dir "$ROOT/results" \
    --output "$CAMPAIGN_DIR/comparison.html" --since "$CAMPAIGN_START" \
    && log "report: $CAMPAIGN_DIR/comparison.html"
else
  warn "go not installed here; build the report with: make report SINCE=$CAMPAIGN_START"
fi

if [ -n "$BUCKET" ]; then
  log "archiving to gs://${BUCKET}/${CAMPAIGN}/"
  gcloud storage rsync --recursive "$ROOT/results" "gs://${BUCKET}/${CAMPAIGN}/results" \
    || warn "archive upload failed"
fi

if [ ${#FAILED[@]} -gt 0 ]; then
  warn "campaign ${CAMPAIGN} finished with failures: ${FAILED[*]}"
  exit 1
fi
log "campaign ${CAMPAIGN} complete: results/ and ${CAMPAIGN_DIR}"
