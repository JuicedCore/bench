#!/usr/bin/env python3
"""Point the sidecar's orderer identity at Arma's own generated org1 identity
instead of the committer test-node samples' self-test peer-org-0 identity -
Arma's genesis material is the identity actually trusted by the routers this
sidecar delivers from. Adapted from a proven working reference (see
docs/platforms/fabricx-comparability.md)."""
from pathlib import Path

p = Path("/root/config/sidecar.yaml")
t = p.read_text()
t = t.replace("    msp-id: peer-org-0", "    msp-id: org1", 1)
t = t.replace(
    "    msp-dir: /root/artifacts/peerOrganizations/peer-org-0.com/users/client@peer-org-0.com/msp",
    "    msp-dir: /root/arma/crypto/ordererOrganizations/org1/users/client@org1/msp",
    1,
)
p.write_text(t)
