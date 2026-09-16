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
#      to the load generator, run benchrunner, tear the platform down; per-step deploy/run/
#      teardown logs and a container capture (state + logs from the platform VM) are pulled
#      back before the VM is destroyed - see docs/guides/running-benchmarks.md#logs-and-failure-captures
#   6. pull results into ./results, record the hardware, archive to GCS if asked
#   7. terraform destroy - also on failure or Ctrl-C, unless --keep
#
# Needs on this machine: terraform (>= 1.5), gcloud (authenticated), openssl, python3
# with PyYAML. Needs in GCP: see docs/guides/gcp-deployment.md (APIs, IAM roles).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=scripts/campaign-lib.sh
source "$ROOT/scripts/campaign-lib.sh"
LOG_TAG=gcp
# shellcheck source=scripts/log-lib.sh
source "$ROOT/scripts/log-lib.sh"

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
    *) warn "unknown argument: $1"; usage 64 ;;
  esac
done

command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' 2>/dev/null \
  || die "python3 with PyYAML required (pip install pyyaml, or sudo scripts/install-deps.sh)"

TF_DIR="$ROOT/deploy/terraform"
# The VMs get the working tree without .git, so the manifest's harness_git_sha
# comes from here. "-dirty" marks uncommitted changes in what was shipped.
HARNESS_SHA="$(git -C "$ROOT" describe --always --dirty --abbrev=12 2>/dev/null || echo unknown)"
PROFILE_FILE="$ROOT/deploy/profiles/${PROFILE}.yaml"
[ -f "$PROFILE_FILE" ] || die "no profile $PROFILE_FILE" "available: $(ls deploy/profiles/*.yaml | xargs -n1 basename | tr '\n' ' ')"
for c in $CONFIGS; do [ -f "$c" ] || die "no config $c" "configs live in configs/normalized/ and configs/native/"; done
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
  need "$t" "see docs/guides/gcp-deployment.md#one-time-setup"
done
# Terraform >= 1.5 (workspace select -or-create, the backend config used here).
TF_VER="$(terraform version -json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["terraform_version"])' 2>/dev/null || echo 0)"
python3 -c 'import sys; v=[int(x) for x in sys.argv[1].split(".")[:2]]; sys.exit(0 if v >= [1,5] else 1)' "$TF_VER" 2>/dev/null \
  || die "terraform ${TF_VER} is too old; need >= 1.5"
# Nothing below works without credentials, and the failure otherwise shows up
# as a silent 30 minute wait_ready or a terraform provider error.
ACCOUNT="$(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null | head -1)"
[ -n "$ACCOUNT" ] || die "gcloud has no active account" "gcloud auth login && gcloud auth application-default login"
gcloud auth application-default print-access-token >/dev/null 2>&1 \
  || die "no application-default credentials for terraform" "gcloud auth application-default login"
if [ -z "$PROJECT" ] && ! grep -qs '^ *project *=' "$ROOT/deploy/terraform/terraform.tfvars"; then
  die "no GCP project: pass --project, set GCP_PROJECT, or set project in deploy/terraform/terraform.tfvars"
fi

CAMPAIGN="$(date -u +%Y%m%dT%H%M%SZ)-${PROFILE}"
CAMPAIGN_START="$(date +%Y-%m-%dT%H:%M:%S%:z)"
CAMPAIGN_DIR="$ROOT/results/_campaigns/$CAMPAIGN"
mkdir -p "$CAMPAIGN_DIR"
exec > >(tee -a "$CAMPAIGN_DIR/gcp-run.log") 2>&1
log "campaign ${CAMPAIGN}: platforms [${PLATFORMS}] configs [${CONFIGS}] as ${ACCOUNT}"
log "hardware: platform ${SUT_TYPE} (${SUT_DISK} GB), load generator ${LOADGEN_TYPE}"

# --- Terraform backend ---------------------------------------------------------
BACKEND_OVERRIDE="$TF_DIR/local_backend_override.tf"
if [ "$LOCAL_STATE" = 1 ]; then
  printf 'terraform {\n  backend "local" {}\n}\n' > "$BACKEND_OVERRIDE"
  terraform -chdir="$TF_DIR" init -input=false -reconfigure >"$CAMPAIGN_DIR/terraform-init.log" 2>&1 \
    || { tail -20 "$CAMPAIGN_DIR/terraform-init.log" >&2; die "terraform init failed (full log: $CAMPAIGN_DIR/terraform-init.log)"; }
else
  rm -f "$BACKEND_OVERRIDE"
  [ -f "$TF_DIR/backend.hcl" ] || die "no deploy/terraform/backend.hcl - copy backend.hcl.example, or pass --local-state"
  terraform -chdir="$TF_DIR" init -input=false -reconfigure -backend-config=backend.hcl >"$CAMPAIGN_DIR/terraform-init.log" 2>&1 \
    || { tail -20 "$CAMPAIGN_DIR/terraform-init.log" >&2; die "terraform init failed (full log: $CAMPAIGN_DIR/terraform-init.log)" "does the state bucket in backend.hcl exist? (deploy/terraform/shared)"; }
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

# wait_ready VM: wait for the startup script's ready/failed marker. Returns 1
# (with the install log saved) instead of exiting, so the caller can record the
# failure and still destroy the VMs.
wait_ready() {
  local vm="$1" i=0 state errf
  errf="$(mktemp)"
  log "waiting for ${vm} to finish provisioning (install-deps.sh; typically 3-8 minutes)"
  while [ "$i" -lt 90 ]; do
    if state="$(rsh "$vm" 'if [ -f /var/lib/bench/ready ]; then echo ready; elif [ -f /var/lib/bench/failed ]; then echo failed; else echo pending; fi' 2>"$errf")"; then
      case "$state" in
        *ready*)  log "${vm} ready"; rm -f "$errf"; return 0 ;;
        *failed*) rsh "$vm" 'sudo cat /var/lib/bench/failed; sudo tail -40 /var/log/bench-install.log' >&2 || true
                  rsh "$vm" 'sudo cat /var/log/bench-install.log' > "$CAMPAIGN_DIR/${vm}-install.log" 2>&1 || true
                  warn "${vm}: install-deps failed - see $CAMPAIGN_DIR/${vm}-install.log"
                  rm -f "$errf"; return 1 ;;
      esac
    fi
    i=$((i+1))
    if [ $((i % 3)) -eq 0 ]; then
      # Every minute: say we are still here, and why the last probe did not answer.
      log "  ...${vm} not ready after $((i * 20))s (last probe: $(tail -1 "$errf" 2>/dev/null | cut -c1-160 || true)${state:+ state=$state})"
    fi
    sleep 20
  done
  warn "${vm} did not finish provisioning within 30 minutes; last ssh error: $(tail -3 "$errf" | tr '\n' ' ')"
  warn "  IAP/SSH problems: check the firewall allows 35.235.240.0/20 on :22 and you have roles/iap.tunnelResourceAccessor"
  rsh "$vm" 'sudo cat /var/log/bench-install.log' > "$CAMPAIGN_DIR/${vm}-install.log" 2>&1 || true
  rm -f "$errf"
  return 1
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

# capture_sut DIR SINCE: snapshot every container's state and log on the platform VM
# before teardown removes them, and pull it back - the VM is destroyed afterwards.
capture_sut() {
  local dir="$1" since="$2"
  mkdir -p "$dir/capture"
  rsh "$SUT" "cd ~/bench && rm -rf /tmp/bench-capture && bash scripts/capture.sh /tmp/bench-capture '$since' >&2 && tar -czf - -C /tmp/bench-capture ." \
    | tar -xzf - -C "$dir/capture" || warn "could not pull container capture from ${SUT}"
}

# Mutual-TLS Docker API on the platform VM, reachable only from the load generator
# (firewall) and only with a client certificate signed by a CA whose key never
# leaves this machine and is deleted when the script exits.
TLS_TMP=""
setup_docker_tls() {
  local sut="$1" sut_ip="$2" loadgen="$3" d
  d="$(mktemp -d)"
  TLS_TMP="$d"   # removed by the EXIT trap if anything below fails
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
  ) || { warn "generating Docker TLS certificates with openssl failed"; return 1; }
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
    sudo systemctl restart docker" || { warn "configuring the Docker TLS listener on ${sut} failed (sudo journalctl -u docker there)"; return 1; }
  tar -C "$d" -czf - ca.pem cert.pem key.pem | rsh_in "$loadgen" '
    set -e
    install -d -m 0700 ~/.docker/bench
    tar -xzf - -C ~/.docker/bench
    chmod 0600 ~/.docker/bench/key.pem' || { warn "installing Docker client certificates on ${loadgen} failed"; return 1; }
  rm -rf "$d"; TLS_TMP=""
  # dockerd restarts asynchronously; the first run would otherwise fail its
  # docker info and be recorded as run-failed.
  local i
  for i in $(seq 1 30); do
    if rsh "$loadgen" "DOCKER_HOST=tcp://${sut_ip}:2376 DOCKER_TLS_VERIFY=1 DOCKER_CERT_PATH=\$HOME/.docker/bench docker info --format '{{.Name}}'" >/dev/null 2>&1; then
      log "remote Docker API on ${sut_ip}:2376 answers from ${loadgen}"
      return 0
    fi
    sleep 4
  done
  warn "remote Docker API on ${sut_ip}:2376 not reachable from ${loadgen} after 2 minutes"
  warn "  check: firewall rule *-loadgen-to-sut (tcp:2376), and 'sudo systemctl status docker' on ${sut}"
  return 1
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
  # Runs from the EXIT trap too, so it must not be cut short by errexit: every
  # step is checked by hand, and destroy is retried, because a skipped destroy
  # leaves billed VMs running.
  local n ok=0
  set +e
  for n in 1 2 3; do
    if terraform -chdir="$TF_DIR" workspace select "$CURRENT_PLATFORM" >/dev/null \
       && terraform -chdir="$TF_DIR" destroy -auto-approve -input=false "${TF_ARGS[@]}" \
            -var "platform=$CURRENT_PLATFORM" </dev/null; then
      ok=1; break
    fi
    warn "terraform destroy for ${CURRENT_PLATFORM} failed (attempt ${n}/3)"
    sleep 15
  done
  set -e
  if [ "$ok" != 1 ]; then
    warn "=================================================================="
    warn "LEFTOVER VMs - ${CURRENT_PLATFORM} infrastructure was NOT destroyed and is still billed."
    warn "  destroy: terraform -chdir=$TF_DIR workspace select ${CURRENT_PLATFORM} && terraform -chdir=$TF_DIR destroy ${TF_ARGS[*]} -var platform=${CURRENT_PLATFORM}"
    warn "  inspect: gcloud compute instances list ${PROJECT:+--project $PROJECT} --filter='labels.platform=${CURRENT_PLATFORM} AND labels.purpose=blockchain-benchmark'"
    warn "=================================================================="
  fi
  CURRENT_PLATFORM=""
}
# shellcheck disable=SC2154  # rc is assigned inside the trap string
trap 'rc=$?; destroy_current; rm -f "$BACKEND_OVERRIDE"; [ -n "$TLS_TMP" ] && rm -rf "$TLS_TMP"; exit $rc' EXIT
trap 'exit 130' INT TERM

FAILED=()

# platform_failed <stage> <reason>: record a failure that ends this platform
# (every config for it is skipped) and destroy its VMs. The campaign continues
# with the next platform instead of aborting - the summary shows what happened.
platform_failed() {
  local stage="$1" reason="$2" c
  warn "${PLATFORM}: ${stage} failed - ${reason}; skipping its configs"
  for c in $CONFIGS; do
    record "$CAMPAIGN_DIR" "$PLATFORM" "$c" "$stage" infra-failed "$reason" "$CAMPAIGN_DIR"
  done
  FAILED+=("${PLATFORM}:${stage}")
  destroy_current
}

# --- per platform ------------------------------------------------------------------
for PLATFORM in $PLATFORMS; do
  log "==================== ${PLATFORM} ===================="
  terraform -chdir="$TF_DIR" workspace select -or-create "$PLATFORM" >/dev/null \
    || { platform_failed infra "terraform workspace select failed"; continue; }
  CURRENT_PLATFORM="$PLATFORM"
  if ! terraform -chdir="$TF_DIR" apply -auto-approve -input=false "${TF_ARGS[@]}" -var "platform=$PLATFORM" 2>&1 \
       | tee "$CAMPAIGN_DIR/${PLATFORM}-terraform-apply.log"; then
    platform_failed infra "terraform apply failed ($(grep -E '^(│ )?Error' "$CAMPAIGN_DIR/${PLATFORM}-terraform-apply.log" | head -1 | cut -c1-200)); quota/API/IAM - see ${PLATFORM}-terraform-apply.log"
    continue
  fi

  ZONE="$(tfo zone)"
  SUT="$(tfo sut_name)"; SUT_IP="$(tfo sut_internal_ip)"
  LOADGEN="$(tfo loadgen_name)"

  wait_ready "$SUT" || { platform_failed infra "${SUT} provisioning failed (${SUT}-install.log)"; continue; }
  wait_ready "$LOADGEN" || { platform_failed infra "${LOADGEN} provisioning failed (${LOADGEN}-install.log)"; continue; }

  shipped=1
  for vm in "$SUT" "$LOADGEN"; do
    # OS Login users are not in the docker group by default. A new SSH session
    # picks the group up.
    rsh "$vm" 'sudo usermod -aG docker "$(id -un)"' && ship_tree "$vm" || { shipped=0; break; }
  done
  [ "$shipped" = 1 ] || { platform_failed infra "could not prepare ${vm} (docker group / shipping the tree over IAP)"; continue; }

  log "recording hardware"
  {
    echo "campaign: $CAMPAIGN"; echo "platform: $PLATFORM"; echo "profile: $PROFILE"
    echo "sut: $SUT ($SUT_TYPE)"; echo "loadgen: $LOADGEN ($LOADGEN_TYPE)"; echo "zone: $ZONE"
    echo "--- sut lscpu"; rsh "$SUT" 'lscpu; free -g; uname -a; docker version --format "docker {{.Server.Version}}"'
    echo "--- loadgen lscpu"; rsh "$LOADGEN" 'lscpu | head -20; uname -a'
  } > "$CAMPAIGN_DIR/${PLATFORM}-hardware.txt" 2>&1 || warn "could not record hardware"

  pf=0
  rsh "$SUT" "cd ~/bench && scripts/preflight.sh --remote-loadgen $PROFILE" || pf=$?
  case "$pf" in
    0) ;;
    2) warn "preflight warnings on ${SUT} (see above)" ;;
    *) platform_failed infra "preflight failed on ${SUT} (exit ${pf}; output above)"; continue ;;
  esac

  log "docker API over mutual TLS on ${SUT}"
  setup_docker_tls "$SUT" "$SUT_IP" "$LOADGEN" \
    || { platform_failed infra "remote Docker API setup failed - container failure detection would be blind"; continue; }
  rsh "$SUT" 'cd ~/bench && bash deploy/docker/monitoring/up.sh' \
    || warn "monitoring stack did not start; runs continue without Prometheus charts"

  log "building benchrunner on ${LOADGEN}"
  rsh "$LOADGEN" 'cd ~/bench && make build' || { platform_failed infra "make build failed on ${LOADGEN} (output above)"; continue; }

  for CONFIG in $CONFIGS; do
    cfg_norm="$(yaml_top "$CONFIG" normalized)"
    cfg_plat="$(yaml_top "$CONFIG" platform)"
    if [ "$cfg_norm" = "false" ] && [ -n "$cfg_plat" ] && [ "$cfg_plat" != "$PLATFORM" ]; then
      continue
    fi
    if [ "$cfg_norm" = "false" ]; then
      export BENCH_NORMALIZED=false
    else
      export BENCH_NORMALIZED=true
    fi
    log "---- ${PLATFORM}: ${CONFIG} ----"
    DIR="$(step_dir "$CAMPAIGN_DIR" "$PLATFORM" "$CONFIG")"
    SINCE="$(date --rfc-3339=seconds)"
    if ! rsh "$SUT" "cd ~/bench && BENCH_NORMALIZED=${BENCH_NORMALIZED} bash deploy/docker/$PLATFORM/up.sh $PROFILE" 2>&1 | tee "$DIR/deploy.log"; then
      warn "deploy failed: ${PLATFORM} ${CONFIG}"
      FAILED+=("${PLATFORM}:${CONFIG}:deploy")
      capture_sut "$DIR" "$SINCE"
      record "$CAMPAIGN_DIR" "$PLATFORM" "$CONFIG" deploy deploy-failed "$(reason_of "$DIR/deploy.log")" "$DIR"
      rsh "$SUT" "cd ~/bench && bash deploy/docker/$PLATFORM/down.sh $PROFILE" 2>&1 | tee "$DIR/teardown.log" || true
      continue
    fi

    # connection.env names crypto material (TLS CA, client cert and key) by absolute
    # path on the platform VM. Ship those files to the load generator and rewrite the
    # paths and endpoints for it.
    if ! rsh "$SUT" "test -s ~/bench/deploy/docker/$PLATFORM/connection.env"; then
      warn "deploy for ${PLATFORM} exited 0 but wrote no connection.env"
      FAILED+=("${PLATFORM}:${CONFIG}:deploy")
      capture_sut "$DIR" "$SINCE"
      record "$CAMPAIGN_DIR" "$PLATFORM" "$CONFIG" deploy deploy-failed "deploy did not emit connection.env" "$DIR"
      rsh "$SUT" "cd ~/bench && bash deploy/docker/$PLATFORM/down.sh $PROFILE" 2>&1 | tee "$DIR/teardown.log" || true
      continue
    fi
    rsh_in "$SUT" "cd ~/bench && python3 - deploy/docker/$PLATFORM/connection.env" <<'PY' > "$CAMPAIGN_DIR/.paths" \
      || warn "could not list connection material on ${SUT}; the run may fail to find certificates"
import os, sys
root = os.path.expanduser("~/bench")
for line in open(sys.argv[1]):
    if "=" not in line or line.lstrip().startswith("#"):
        continue
    v = line.split("=", 1)[1].strip().strip('"')
    if v.startswith(root + "/") and os.path.exists(v):
        print(os.path.relpath(v, root))
PY
    copied=1
    if [ -s "$CAMPAIGN_DIR/.paths" ]; then
      rsh "$SUT" "cd ~/bench && tar -czf - $(tr '\n' ' ' < "$CAMPAIGN_DIR/.paths")" \
        | rsh_in "$LOADGEN" 'cd ~/bench && tar -xzf -' || copied=0
    fi
    rsh "$SUT" "cat ~/bench/deploy/docker/$PLATFORM/connection.env" \
      | sed -e "s/localhost/${SUT_IP}/g" -e "s/127\.0\.0\.1/${SUT_IP}/g" \
      | rsh_in "$LOADGEN" "cat > ~/bench/deploy/docker/$PLATFORM/connection.env" || copied=0
    rm -f "$CAMPAIGN_DIR/.paths"
    if [ "$copied" != 1 ]; then
      warn "copying connection.env / crypto material to ${LOADGEN} failed"
      FAILED+=("${PLATFORM}:${CONFIG}:run")
      record "$CAMPAIGN_DIR" "$PLATFORM" "$CONFIG" run run-failed "could not copy connection material to the load generator" "$DIR"
      capture_sut "$DIR" "$SINCE"
      rsh "$SUT" "cd ~/bench && bash deploy/docker/$PLATFORM/down.sh $PROFILE" 2>&1 | tee "$DIR/teardown.log" || true
      continue
    fi

    set +e
    rsh "$LOADGEN" "cd ~/bench && set -a && . deploy/docker/$PLATFORM/connection.env && set +a && \
        export BENCH_HARNESS_GIT_SHA=${HARNESS_SHA} DOCKER_HOST=tcp://${SUT_IP}:2376 DOCKER_TLS_VERIFY=1 DOCKER_CERT_PATH=\$HOME/.docker/bench \
               BENCH_PROMETHEUS_URL=http://${SUT_IP}:9090 && \
        docker info --format 'remote docker: {{.Name}} ({{.NCPU}} CPU)' && \
        ./bin/benchrunner run --progress --config $CONFIG --platform $PLATFORM --profile $PROFILE" 2>&1 | tee "$DIR/run.log"
    rc=${PIPESTATUS[0]}
    set -e
    case "$rc" in
      0) if grep -q 'FAILED RUN' "$DIR/run.log"; then
           warn "run completed but is not a measurement: ${PLATFORM} ${CONFIG}"
           FAILED+=("${PLATFORM}:${CONFIG}:no-measurement")
           record "$CAMPAIGN_DIR" "$PLATFORM" "$CONFIG" run no-measurement "$(reason_of "$DIR/run.log")" "$DIR"
         else
           record "$CAMPAIGN_DIR" "$PLATFORM" "$CONFIG" run ok "" "$DIR"
         fi ;;
      3) warn "platform container failed: ${PLATFORM} ${CONFIG}"
         FAILED+=("${PLATFORM}:${CONFIG}:container")
         record "$CAMPAIGN_DIR" "$PLATFORM" "$CONFIG" run container-failed "$(reason_of "$DIR/run.log")" "$DIR" ;;
      *) warn "run failed: ${PLATFORM} ${CONFIG} (exit $rc)"
         FAILED+=("${PLATFORM}:${CONFIG}:run")
         record "$CAMPAIGN_DIR" "$PLATFORM" "$CONFIG" run run-failed "$(reason_of "$DIR/run.log")" "$DIR" ;;
    esac

    capture_sut "$DIR" "$SINCE"
    rsh "$SUT" "cd ~/bench && bash deploy/docker/$PLATFORM/down.sh $PROFILE" 2>&1 | tee "$DIR/teardown.log" || warn "teardown reported an error"
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
  if go build -o "$ROOT/bin/benchrunner" ./cmd/benchrunner; then
    "$ROOT/bin/benchrunner" report --results-dir "$ROOT/results" \
      --output "$CAMPAIGN_DIR/comparison.html" --since "$CAMPAIGN_START" \
      && log "report: $CAMPAIGN_DIR/comparison.html" \
      || warn "comparison report failed; build it later with: make report SINCE=$CAMPAIGN_START"
  else
    warn "go build failed here; build the report later with: make report SINCE=$CAMPAIGN_START"
  fi
else
  warn "go not installed here; build the report with: make report SINCE=$CAMPAIGN_START"
fi

if [ -n "$BUCKET" ]; then
  log "archiving to gs://${BUCKET}/${CAMPAIGN}/"
  gcloud storage rsync --recursive "$ROOT/results" "gs://${BUCKET}/${CAMPAIGN}/results" \
    || warn "archive upload failed"
fi

print_summary "$CAMPAIGN_DIR"

if [ ${#FAILED[@]} -gt 0 ]; then
  warn "campaign ${CAMPAIGN} finished with failures: ${FAILED[*]}"
  exit 1
fi
log "campaign ${CAMPAIGN} complete: results/ and ${CAMPAIGN_DIR}"
