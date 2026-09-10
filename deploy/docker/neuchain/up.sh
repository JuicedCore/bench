#!/usr/bin/env bash
# NeuChain deploy - PHASE 4 (not yet implemented).
#
# Plan: build github.com/iDC-NEU/NeuChain@ev in an Ubuntu 20.04 container
# (cmake 3.16.3, gcc 9.4.0), producing block_server / epoch_server / user; run
# 4 block_servers + 1 epoch_server as containers; the neuchain adapter talks to
# them over gRPC (client strategy decided by the proto spike - see
# docs/platforms/neuchain-client-implementation.md and
# docs/decisions/adr-002-neuchain-client-spike.md).
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
die "neuchain deploy is Phase 4 - not implemented yet"
