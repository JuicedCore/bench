# Bench — Hardware Requirements

> Minimum host specifications required to run a proper benchmark with each
> shipped profile. Numbers are derived from
> [`deploy/profiles/`](../deploy/profiles/) and
> [`scripts/preflight.sh`](../scripts/preflight.sh).

---

## Quick Reference

| Profile | CPU Cores | RAM | Disk | Load Generator | Target Host |
|---------|-----------|-----|------|----------------|-------------|
| `local-small` | 11 | 13 GB | 40 GB free | Co-located | ≥14 GB laptop / desktop |
| `local` | 16 | 16 GB | 40 GB free | Co-located | 16-core / 16 GB workstation |
| `gcp-small` | 16 vCPU (SUT) + 8 vCPU (loadgen) | 64 GB (SUT) | 200 GB (SUT) | Separate VM | `n2-standard-16` + `n2-standard-8` |
| `gcp-full` | 32 vCPU (SUT) + 16 vCPU (loadgen) | 128 GB (SUT) | 300 GB (SUT) | Separate VM | `n2-standard-32` + `n2-standard-16` |

> [!IMPORTANT]
> These are **minimum** requirements. The host must have this much **total RAM**
> — not just "available" — because the benchmark disables swap
> (`--memory-swap == --memory`) and the kernel OOM-kills any container that
> exceeds its ceiling.

---

## Budget Breakdown per Profile

Every profile splits host resources into four buckets:

```
Total Host = Platform Budget + Load Generator + Monitoring Stack + Host OS
```

### `local-small` — Small Local Machine

| Bucket | CPU | RAM |
|--------|-----|-----|
| Platform under test | 8 cores | 8 GB |
| Load generator | 2 cores | 2 GB |
| Monitoring (Prometheus, Grafana, cAdvisor, node_exporter) | 1 core | 1 GB |
| Host OS + Docker daemon | — | ~2 GB |
| **Total** | **11 cores** | **~13 GB** |

- Results on this profile are comparable **only to other `local-small` runs**, not to `local` or GCP runs.
- Fabric-X is "heavily starved" at this size (see `docs/architecture/fairness-guarantees.md`).

### `local` — Standard Local Benchmark

| Bucket | CPU | RAM |
|--------|-----|-----|
| Platform under test | 11 cores | 11 GB |
| Load generator | 2–3 cores (platform-dependent) | 2 GB |
| Monitoring | 1 core | 1 GB |
| Host OS + Docker daemon | — | ~2 GB |
| **Total** | **~16 cores** | **~16 GB** |

- This is the **reference local profile** for publishable results.

### `gcp-small` — Mid-Size Cloud

| Bucket | CPU | RAM | Machine |
|--------|-----|-----|---------|
| Platform under test | 13 vCPU | 52 GB | `n2-standard-16` |
| Monitoring + OS headroom | 3 vCPU | 12 GB | (same VM) |
| Load generator | 8 vCPU | — | `n2-standard-8` (separate VM) |

### `gcp-full` — Full Cloud Benchmark

| Bucket | CPU | RAM | Machine |
|--------|-----|-----|---------|
| Platform under test | 28 vCPU | 112 GB | `n2-standard-32` |
| Monitoring + OS headroom | 4 vCPU | 16 GB | (same VM) |
| Load generator | 16 vCPU | — | `n2-standard-16` (separate VM) |

---

## Monitoring Stack Footprint

The monitoring stack runs alongside every benchmark and is **not optional** for
proper runs (it records per-container CPU/memory and host-level metrics).

| Service | CPU Limit | Memory Limit |
|---------|-----------|--------------|
| Prometheus | 0.50 cores | 512 MB |
| Grafana | 0.25 cores | 256 MB |
| cAdvisor | 0.25 cores | 256 MB |
| node_exporter | 0.10 cores | 128 MB |
| **Total** | **~1.1 cores** | **~1.1 GB** |

---

## Software Prerequisites

| Tool | Required | Notes |
|------|----------|-------|
| Go | ✅ | Must match `go.mod` version |
| Docker + Compose plugin | ✅ | Daemon must be running; user in `docker` group |
| git | ✅ | Clones pinned upstream sources |
| curl | ✅ | Downloads Fabric binaries and yq |
| jq | ✅ | JSON processing in scripts |
| Python 3 + PyYAML | ✅ | Profile parsing, orderer-batch patching |
| matplotlib | Optional | Per-run report charts |
| Passwordless sudo | Optional | Page-cache drop between runs for isolation |

---

## Preflight Check

Run before any benchmark to verify the host meets the chosen profile:

```bash
scripts/preflight.sh <profile>        # e.g. local, local-small
scripts/preflight.sh --remote-loadgen <profile>   # GCP: loadgen on separate VM
```

Exit codes:
- `0` — Ready
- `1` — Hard blocker (not enough RAM, missing tools)
- `2` — Runnable but results will be suspect (over-committed or under-resourced)

---

## Why Under-Provisioned Hosts OOM

Bench enforces **hard memory ceilings with no swap** on every container
(`--memory-swap == --memory` via `docker update`). This is deliberate: swap
inflates latency percentiles, so the benchmark would measure the OS paging
subsystem rather than the platform.

On a host with less RAM than the profile requires:
1. The platform budget exceeds what the kernel can back with physical pages.
2. Containers hit their ceiling under load.
3. The kernel OOM-kills the offending container — typically Arma (16 Go
   processes) or the pipeline (sidecar + verifier + coordinator).

The same Fabric-X deployment runs fine in the NeuChain harness because that
harness is single-purpose (no monitoring overhead), gives each container
1576 MB on pinned CPU cores, and totals only ~6.3 GB — well within an 8.7 GB
host.

---

## Platform Scaling Behavior

Understanding how each platform scales informs hardware sizing decisions: more
hardware helps Fabric-X but does not help NeuChain in the same way.

### Fabric-X — Horizontal Scale-Out ✅

Fabric-X was architected for horizontal scaling. It decomposes the monolithic
Fabric peer into independently scalable microservices:

| Component | How it scales |
|-----------|--------------|
| **Arma ordering** | Sharded — add more shards (router/batcher/consenter/assembler quartets). Consenters order compact **digests**, not full payloads. |
| **Endorsers** | Independent services — add instances behind a load balancer. |
| **Validators / Committers** | Separate tier — scale independently from ordering. |

Published evaluation: **>200,000 TPS** on a large cluster (CBDC benchmark).

In Bench's single-host 4-container setup, Fabric-X is heavily starved — all 16
Arma processes (4 parties × 4 roles) are packed into one container. On a real
cluster each party runs on its own machine, and throughput scales linearly with
additional shards.

### NeuChain — Vertical Scale-Up Only ❌

NeuChain's architecture works **against** horizontal scaling:

| Bottleneck | Why it hurts at scale |
|------------|----------------------|
| **All-to-all batch exchange** | Every node sends its epoch's batch to every other node (port 7001). Network traffic grows **O(n²)** with node count. |
| **Global epoch synchronization** | `EpochTransactionBuffer` blocks until **every** node has delivered its epoch-*e* batch. More nodes = more stragglers = higher epoch latency. |
| **Raft-based epoch server** | Epoch assignment uses Raft consensus. More Raft members = more quorum messages per epoch. |
| **Cross-node block verification** | Every node's `BlockBroadcaster` cross-checks its block signature against all peers. More nodes = more signatures to verify per block. |

NeuChain is designed for high throughput at a **fixed node count** (typically 4)
by scaling **vertically** — more CPU cores per server, not more servers. Its
VLDB paper numbers come from high core counts, not large clusters.

> [!IMPORTANT]
> On Bench's `local` profile (4 block servers + 1 epoch server in ~6 cores),
> NeuChain runs well below its paper numbers. Quotable numbers require
> `gcp-full`.

### Comparison Summary

| | Fabric-X | NeuChain |
|---|---|---|
| **Scale-out model** | Horizontal (add shards, endorsers, validators) | Vertical (add cores per node) |
| **Throughput with more nodes** | ↑ Increases | ↓ Degrades (O(n²) all-to-all, epoch sync barrier) |
| **Bottleneck at scale** | Per-shard consenter throughput (add more shards) | All-to-all network + epoch synchronization |
| **Paper headline** | >200k TPS on a large cluster | ~2400 TPS on 4 nodes with high core count |
| **Same-host lab (4 containers)** | 996 TPS (starved) | 2391 TPS (near-paper) |

> [!NOTE]
> NeuChain outperforming Fabric-X in the same-host lab does **not** mean it is
> the faster system in general. Fabric-X is constrained to a fraction of its
> design capacity (16 Arma processes in one container, no sharding), while
> NeuChain is running close to its optimal topology (4 nodes, dedicated cores).
> The comparison is valid for "what fits on one machine" but not for
> production-scale deployments.
