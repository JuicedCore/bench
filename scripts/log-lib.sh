#!/usr/bin/env bash
# shellcheck shell=bash
# Logging and failure helpers shared by every script in this repo. Source it; it
# sets no shell options of its own.
#
#   LOG_TAG=deploy . scripts/log-lib.sh
#   log  "bringing up network"          # [deploy 12:00:01] bringing up network
#   warn "retrying"                     # [deploy 12:00:01] WARN: retrying
#   die  "no profile" "see deploy/profiles/"   # [deploy 12:00:01] ERROR: no profile
#                                              #   hint: see deploy/profiles/   (exit 1)
#   need docker "install it with scripts/install-deps.sh"
#   wait_for "orderer port 7050" 60 nc -z localhost 7050
#
# Everything goes to stderr, so a function whose stdout is captured
# (VAR="$(fn)") can still log. Colour is used only when stderr is a terminal, so
# campaign log files stay free of escape codes. The literal markers "WARN:" and
# "ERROR:" are what scripts/campaign-lib.sh reason_of looks for when it pulls a
# one-line failure reason out of a step log.

: "${LOG_TAG:=bench}"

if [ -t 2 ] && [ -z "${NO_COLOR:-}" ]; then
  _LOG_C_TAG=$'\033[1;34m' _LOG_C_WARN=$'\033[1;33m' _LOG_C_ERR=$'\033[1;31m' _LOG_C_OFF=$'\033[0m'
else
  _LOG_C_TAG='' _LOG_C_WARN='' _LOG_C_ERR='' _LOG_C_OFF=''
fi

_log_prefix() { printf '%s[%s %s]%s' "$_LOG_C_TAG" "$LOG_TAG" "$(date +%H:%M:%S)" "$_LOG_C_OFF"; }

log()  { printf '%s %s\n' "$(_log_prefix)" "$*" >&2; }
warn() { printf '%s %sWARN:%s %s\n' "$(_log_prefix)" "$_LOG_C_WARN" "$_LOG_C_OFF" "$*" >&2; }

# die <message> [hint...] - print an error (and optional hint lines) and exit 1.
die() {
  printf '%s %sERROR:%s %s\n' "$(_log_prefix)" "$_LOG_C_ERR" "$_LOG_C_OFF" "$1" >&2
  shift || true
  local h
  for h in "$@"; do printf '  hint: %s\n' "$h" >&2; done
  exit 1
}

# need <tool> [hint] - die unless <tool> is on PATH.
need() {
  command -v "$1" >/dev/null 2>&1 && return 0
  die "missing required tool: $1" "${2:-run scripts/install-deps.sh (or: make deps), then scripts/preflight.sh}"
}

# wait_for <description> <timeout-seconds> <command...>
#
# Run <command> every 2s until it succeeds or the timeout passes. Prints a
# progress line every 10s so a long wait is never silent. On timeout it runs the
# function named in $WAIT_FOR_ON_TIMEOUT, if set (e.g. to dump container logs),
# and returns 1 - the caller decides whether that is fatal.
wait_for() {
  local what="$1" timeout="$2"; shift 2
  local start now last=0 elapsed
  start="$(date +%s)"
  log "waiting up to ${timeout}s for ${what}"
  while :; do
    if "$@" >/dev/null 2>&1; then
      now="$(date +%s)"
      log "${what}: ready after $(( now - start ))s"
      return 0
    fi
    now="$(date +%s)"; elapsed=$(( now - start ))
    if [ "$elapsed" -ge "$timeout" ]; then
      warn "${what}: still not ready after ${timeout}s (check: $*)"
      if [ -n "${WAIT_FOR_ON_TIMEOUT:-}" ]; then "$WAIT_FOR_ON_TIMEOUT" || true; fi
      return 1
    fi
    if [ $(( elapsed - last )) -ge 10 ]; then
      log "  ...still waiting for ${what} (${elapsed}s)"
      last="$elapsed"
    fi
    sleep 2
  done
}

# dump_containers [name-regex] [lines] - print `docker ps -a` for matching
# containers and the log tail of every one that is not running. Used when a
# deploy fails, so the reason is on screen (and in the campaign's deploy.log)
# without a separate `docker logs` hunt.
dump_containers() {
  local pattern="${1:-.}" lines="${2:-60}" c
  command -v docker >/dev/null 2>&1 || return 0
  printf '\n----- docker ps -a (matching /%s/) -----\n' "$pattern" >&2
  docker ps -a --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}' 2>&1 | { head -1; grep -E "$pattern" || true; } >&2
  while read -r c; do
    [ -n "$c" ] || continue
    printf '\n----- last %s log lines of exited container %s -----\n' "$lines" "$c" >&2
    docker logs --tail "$lines" "$c" 2>&1 | sed 's/^/  /' >&2 || true
  done < <(docker ps -a --filter status=exited --filter status=dead --format '{{.Names}}' 2>/dev/null | grep -E "$pattern" || true)
  printf '\n(full capture of a live network: scripts/capture.sh <out-dir>)\n\n' >&2
}

# on_error_dump <name-regex> - install an ERR/EXIT hook that, when the script
# fails, reports the failing command and line and dumps matching containers.
on_error_dump() {
  _LOG_DUMP_PATTERN="$1"
  trap '_log_on_exit $? "${BASH_SOURCE[0]:-$0}" "${LINENO}" "${BASH_COMMAND}"' EXIT
}

_log_on_exit() {
  local rc="$1" src="$2" line="$3" cmd="$4"
  [ "$rc" -eq 0 ] && return 0
  printf '%s %sERROR:%s %s failed (exit %s) near line %s: %s\n' \
    "$(_log_prefix)" "$_LOG_C_ERR" "$_LOG_C_OFF" "$(basename "$src")" "$rc" "$line" "$cmd" >&2
  dump_containers "${_LOG_DUMP_PATTERN:-.}" 40
  return "$rc"
}
