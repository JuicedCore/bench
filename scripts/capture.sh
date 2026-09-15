#!/usr/bin/env bash
# Snapshot every container's state and logs on the docker host BEFORE teardown
# removes them, so a crash cause (e.g. a Go panic in a peer) survives. Call this
# right after a run finishes, win or lose, and before down.sh. Operates against
# whatever DOCKER_HOST is already set (local daemon or a remote SUT).
#
#   scripts/capture.sh <out-dir> [since]
#
#   scripts/capture.sh results/_campaigns/.../drunix/quick-smoke/capture \
#     "$(date --rfc-3339=seconds)"
#
# <since>, if given, is a timestamp `docker events` accepts (e.g. the output of
# `date --rfc-3339=seconds` taken before the run started); die/oom/kill/restart
# events in that window are then captured too.
#
# This must never fail the caller mid-campaign - it is diagnostic best-effort,
# not part of the pass/fail path. Deliberately no -e/-o pipefail; every command
# is allowed to fail on its own and the script always exits 0.
set -u

OUT="${1:?usage: capture.sh <out-dir> [since]}"
SINCE="${2:-}"
CAPTURE_TAIL="${CAPTURE_TAIL:-5000}"

mkdir -p "$OUT/inspect" "$OUT/logs" || true

docker ps -a --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.RunningFor}}' \
  > "$OUT/ps.txt" 2>&1 || true

n=0
# Process substitution (not a pipe) so `n` survives the loop in this shell.
while IFS= read -r name; do
  [ -n "$name" ] || continue
  n=$((n + 1))
  docker inspect --format \
    '{"state":{{json .State}},"restart_count":{{.RestartCount}},"memory_limit":{{.HostConfig.Memory}},"nano_cpus":{{.HostConfig.NanoCpus}}}' \
    "$name" > "$OUT/inspect/${name}.json" 2>&1 || true
  docker logs --timestamps --tail "$CAPTURE_TAIL" "$name" \
    > "$OUT/logs/${name}.log" 2>&1 || true
done < <(docker ps -a --format '{{.Names}}' 2>/dev/null || true)

if [ -n "$SINCE" ]; then
  # docker events' timestamp parser rejects the space `date --rfc-3339=seconds`
  # puts between date and time (in a non-UTC zone it errors outright rather than
  # ignoring the events window); swap in a T, which it accepts, on both ends.
  # --until makes this return instead of streaming forever; timeout is a
  # second safety net in case the daemon is slow to answer.
  timeout 30 docker events --since "${SINCE/ /T}" --until "$(date --rfc-3339=seconds | tr ' ' T)" \
    --filter event=die --filter event=oom --filter event=kill --filter event=restart \
    > "$OUT/events.txt" 2>&1 || true
fi

{
  echo "=== date -u ==="
  date -u || true
  echo
  echo "=== free -m ==="
  free -m || true
  echo
  echo "=== df -h ==="
  df -h || true
  echo
  echo "=== docker system df ==="
  docker system df || true
  echo
  echo "=== dmesg (oom/killed/segfault) ==="
  if sudo -n true 2>/dev/null; then
    sudo -n dmesg -T 2>/dev/null | grep -iE 'oom|killed process|segfault' | tail -50
  else
    echo "(passwordless sudo not available; dmesg not captured)"
  fi
} > "$OUT/host.txt" 2>&1 || true

echo "capture: ${n} containers -> ${OUT}"
exit 0
