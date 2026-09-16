#!/usr/bin/env bash
# Install everything the benchmark needs on a fresh Linux host.
#
#   sudo scripts/install-deps.sh [--tune] [--no-service]
#
# Idempotent: anything already present at a sufficient version is left alone, so
# it is safe to re-run. Supported: Debian/Ubuntu (apt), Fedora/RHEL/Rocky/Alma/
# CentOS Stream (dnf), Arch (pacman). The GCP VMs run this same file as their
# startup script (deploy/terraform), so a laptop and a cloud host are provisioned
# identically.
#
# Installs:
#   git curl jq python3 + PyYAML, openssl, make, rsync   (distro packages)
#   matplotlib, python-pptx                               (per-run charts + campaign decks)
#   Docker Engine + compose plugin                        (Docker's own repo on
#                                                          apt/dnf; distro on Arch)
#   Go at the version go.mod names                        (go.dev tarball, sha256
#                                                          verified, /usr/local/go)
#
#   --tune        host settings for stable latency: raise file/inotify limits,
#                 disable transparent huge pages. Used on benchmark VMs; optional
#                 on a workstation.
#   --no-service  install packages but do not enable/start dockerd (containers
#                 without systemd, image builds).
#
# Environment:
#   BENCH_USER    user to add to the docker group (default: $SUDO_USER)
#   GO_VERSION    override the Go version (default: read from go.mod, else 1.26.5)
set -euo pipefail

TUNE=0
SERVICE=1
for a in "$@"; do
  case "$a" in
    --tune) TUNE=1 ;;
    --no-service) SERVICE=0 ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown argument: $a" >&2; exit 64 ;;
  esac
done

log()  { printf '\033[1;34m[install]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[install]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[install]\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root (sudo $0 $*)"
[ -r /etc/os-release ] || die "cannot identify the distribution (/etc/os-release missing)"
# shellcheck disable=SC1091
. /etc/os-release

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU architecture $(uname -m)" ;;
esac

# Go version: go.mod when this file sits in a checkout; the GCP startup script
# runs it standalone, so fall back to a pinned default kept in step with go.mod.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd || true)"
if [ -z "${GO_VERSION:-}" ] && [ -n "$HERE" ] && [ -f "${HERE}/../go.mod" ]; then
  GO_VERSION="$(awk '/^go /{print $2; exit}' "${HERE}/../go.mod")"
fi
GO_VERSION="${GO_VERSION:-1.26.5}"

family=""
for id in ${ID:-} ${ID_LIKE:-}; do
  case "$id" in
    debian|ubuntu) family=apt; break ;;
    fedora|rhel|centos|rocky|almalinux) family=dnf; break ;;
    arch) family=pacman; break ;;
  esac
done
[ -n "$family" ] || die "unsupported distribution '${ID:-unknown}'. Install manually: git curl jq python3 python3-yaml openssl make rsync, Docker Engine with the compose plugin, Go ${GO_VERSION}"
log "${PRETTY_NAME:-$ID} (${family}, ${ARCH}); Go ${GO_VERSION}"

have_compose() { command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; }

# --- distro packages and Docker ---------------------------------------------
case "$family" in
  apt)
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq git curl jq python3 python3-yaml openssl make rsync ca-certificates gnupg >/dev/null
    if ! have_compose; then
      log "installing Docker Engine from download.docker.com"
      distro="$ID"
      # Derivatives (Mint, Pop!_OS) use their parent's Docker repo.
      case "$distro" in debian|ubuntu) ;; *) case " ${ID_LIKE:-} " in *" ubuntu "*) distro=ubuntu ;; *) distro=debian ;; esac ;; esac
      codename="${UBUNTU_CODENAME:-${VERSION_CODENAME:-}}"
      [ -n "$codename" ] || die "cannot determine the release codename for Docker's apt repo"
      install -m 0755 -d /etc/apt/keyrings
      curl -fsSL "https://download.docker.com/linux/${distro}/gpg" -o /etc/apt/keyrings/docker.asc
      chmod a+r /etc/apt/keyrings/docker.asc
      echo "deb [arch=${ARCH} signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${distro} ${codename} stable" \
        > /etc/apt/sources.list.d/docker.list
      apt-get update -qq
      apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
    fi
    ;;
  dnf)
    # RHEL-family images ship curl-minimal, which conflicts with the curl package;
    # either provides the curl binary.
    pkgs="git jq python3 python3-pyyaml openssl make rsync tar gzip dnf-plugins-core"
    command -v curl >/dev/null 2>&1 || pkgs="$pkgs curl"
    # shellcheck disable=SC2086
    dnf install -y -q $pkgs >/dev/null
    if ! have_compose; then
      log "installing Docker Engine from download.docker.com"
      repo=centos; [ "$ID" = fedora ] && repo=fedora
      [ "$ID" = rhel ] && repo=rhel
      # dnf5 (Fedora 41+) changed the config-manager syntax.
      if dnf --version 2>/dev/null | head -1 | grep -q dnf5; then
        dnf config-manager addrepo --overwrite --from-repofile="https://download.docker.com/linux/${repo}/docker-ce.repo" >/dev/null
      else
        dnf config-manager --add-repo "https://download.docker.com/linux/${repo}/docker-ce.repo" >/dev/null
      fi
      dnf install -y -q docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
    fi
    ;;
  pacman)
    pacman -Sy --noconfirm --needed git curl jq python python-yaml openssl make rsync tar gzip >/dev/null
    have_compose || pacman -S --noconfirm --needed docker docker-compose docker-buildx >/dev/null
    ;;
esac
have_compose || die "docker compose is still unavailable after install"
log "docker: $(docker --version)"

if [ "$SERVICE" = 1 ]; then
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl enable --now docker >/dev/null
  else
    warn "no running systemd; start dockerd yourself"
  fi
  user="${BENCH_USER:-${SUDO_USER:-}}"
  if [ -n "$user" ] && [ "$user" != root ] && id "$user" >/dev/null 2>&1; then
    getent group docker >/dev/null || groupadd docker
    if ! id -nG "$user" | tr ' ' '\n' | grep -qx docker; then
      usermod -aG docker "$user"
      warn "added ${user} to the docker group - log out and back in (or run: newgrp docker)"
    fi
  fi
fi

# --- Go ------------------------------------------------------------------------
go_ok() {
  local bin="$1" have
  [ -x "$bin" ] || return 1
  have="$("$bin" env GOVERSION 2>/dev/null | sed 's/^go//')"
  [ -n "$have" ] && [ "$(printf '%s\n%s\n' "$GO_VERSION" "$have" | sort -V | head -1)" = "$GO_VERSION" ]
}
if go_ok "$(command -v go 2>/dev/null || true)" || go_ok /usr/local/go/bin/go; then
  log "go >= ${GO_VERSION} already installed"
else
  tarball="go${GO_VERSION}.linux-${ARCH}.tar.gz"
  log "installing Go ${GO_VERSION} from go.dev"
  want_sha="$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all' \
    | jq -r --arg f "$tarball" '.[].files[] | select(.filename == $f) | .sha256' | head -1)"
  [ -n "$want_sha" ] || die "go.dev does not list ${tarball}"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  curl -fsSL --retry 5 "https://go.dev/dl/${tarball}" -o "${tmp}/${tarball}"
  echo "${want_sha}  ${tmp}/${tarball}" | sha256sum -c --quiet - || die "checksum mismatch for ${tarball}"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "${tmp}/${tarball}"
  printf 'export PATH=/usr/local/go/bin:$PATH\n' > /etc/profile.d/go.sh
  log "go: $(/usr/local/go/bin/go version) (new shells pick up /etc/profile.d/go.sh)"
fi

# --- Python extras: per-run charts (matplotlib) and campaign decks (python-pptx)
py_ok() { python3 -c "import $1" 2>/dev/null; }
pip_install() {
  python3 -m pip install --break-system-packages "$1" >/dev/null 2>&1 \
    || python3 -m pip install "$1" >/dev/null 2>&1
}
case "$family" in
  apt)
    apt-get install -y -qq python3-matplotlib python3-pip >/dev/null || warn "apt matplotlib/pip failed"
    ;;
  dnf)
    dnf install -y -q python3-matplotlib python3-pip >/dev/null || warn "dnf matplotlib/pip failed"
    ;;
  pacman)
    pacman -S --noconfirm --needed python-matplotlib python-pip >/dev/null || warn "pacman matplotlib/pip failed"
    ;;
esac
if py_ok matplotlib; then
  log "python3 matplotlib already installed"
else
  log "installing matplotlib"
  pip_install matplotlib || warn "matplotlib missing - per-run reports render without charts"
fi
if py_ok pptx; then
  log "python3 python-pptx already installed"
else
  log "installing python-pptx"
  pip_install python-pptx || warn "python-pptx missing - campaign PPT decks cannot be generated"
fi

# --- optional host tuning -------------------------------------------------------
if [ "$TUNE" = 1 ]; then
  log "tuning host for benchmarking"
  cat > /etc/sysctl.d/90-bench.conf <<'EOF'
# deploy: blockchain benchmark host (scripts/install-deps.sh --tune)
fs.file-max = 2097152
fs.inotify.max_user_instances = 8192
fs.inotify.max_user_watches = 1048576
net.core.somaxconn = 65535
net.ipv4.ip_local_port_range = 10240 65535
vm.swappiness = 1
EOF
  sysctl --system >/dev/null 2>&1 || warn "sysctl --system failed (read-only /proc/sys in a container?)"
  if [ -w /sys/kernel/mm/transparent_hugepage/enabled ]; then
    echo never > /sys/kernel/mm/transparent_hugepage/enabled
    echo never > /sys/kernel/mm/transparent_hugepage/defrag 2>/dev/null || true
  fi
fi

log "done. Next: scripts/preflight.sh <profile>"
