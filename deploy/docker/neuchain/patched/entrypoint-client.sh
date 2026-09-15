#!/bin/bash
set -euo pipefail
cd /data
export LD_LIBRARY_PATH=/usr/local/lib:${LD_LIBRARY_PATH:-}
WORKERS="${1:-4}"
TPS="${2:-250}"
DURATION="${3:-150}"
if [ "${1:-}" = "-b" ]; then
  exec /opt/neuchain/bin/user "$@"
fi
if [ "${1:-}" = "--heartbeat" ]; then
  exec /opt/neuchain/bin/user
fi
exec /opt/neuchain/bin/user -b "${WORKERS}" "${TPS}" "${DURATION}"
