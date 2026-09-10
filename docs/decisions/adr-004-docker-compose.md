# ADR-004: Docker Compose for local deployment, not Kubernetes

## Context

Local host is 16 cores / 16 GB. The platform under test gets ~11 cores / 11 GB
after host OS, load generator and monitoring. Each platform is a multi-container
topology.

## Decision

Local deployment is Docker Compose with per-container `--cpus` / `--memory`
limits derived from `deploy/profiles/<profile>.yaml`. Kubernetes is not used
locally. (GCP multi-VM is a separate concern — [adr-005](adr-005-sequential-runs.md),
guides/gcp-deployment.md.)

## Rationale

- A local control plane (k3s/kind/minikube) costs 1–2 GB and a core that the
  platform under test needs.
- Compose resource limits are predictable and easy to verify (`docker stats`).
- The official Fabric/Fabric-X sample networks are Compose-based; reusing them
  avoids re-authoring topologies and drifting from upstream.
- One `ResourceAllocator` reads the profile YAML and emits Compose overrides;
  "local" scales node counts and limits down, "gcp" scales up, same base files.

## Alternatives considered

- **kind / k3s locally** — more realistic for the GCP story but too heavy for
  16 GB and adds a scheduler variable to every measurement.
- **Bare processes, no containers** — loses resource isolation and the cAdvisor
  per-container metrics.

## Consequences

- GCP deployment (Phase 5) uses a different mechanism (Terraform + VMs or GKE);
  the adapter and configs are unchanged, only `deploy/` differs.
- Resource-limit enforcement must be spot-checked with `docker stats` as part of
  run validation.
