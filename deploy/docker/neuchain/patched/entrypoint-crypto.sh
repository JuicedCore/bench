#!/bin/bash
# Generate peer keys via ./user with init_crypto config. Safe to interrupt after "all worker started".
set -euo pipefail
cd /data
export LD_LIBRARY_PATH=/usr/local/lib:${LD_LIBRARY_PATH:-}
cp /opt/neuchain/config-crypto.yaml /data/config.yaml
mkdir -p /data/crypto
timeout 25 /opt/neuchain/bin/user -b 1 1 1 || true
ls -la /data/crypto || true
echo "crypto-init finished (keys in /data/crypto if the binary wrote them)"
