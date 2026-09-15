#!/bin/bash
set -euo pipefail
cd /data
export LD_LIBRARY_PATH=/usr/local/lib:${LD_LIBRARY_PATH:-}
cp /opt/neuchain/config-dbinit.yaml /data/config.yaml
/opt/neuchain/bin/db_init
mkdir -p /snapshot
if [ -d /data/small_bank ]; then
  rm -rf /snapshot/small_bank
  cp -a /data/small_bank /snapshot/small_bank
fi
echo "db_init complete"
