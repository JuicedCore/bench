#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
need docker
cd "$(dirname "${BASH_SOURCE[0]}")" || exit 1
log "starting monitoring stack (Prometheus :9090, Grafana :3000, cAdvisor :8080, node_exporter :9100)"
docker compose up -d
log "Grafana:    http://localhost:3000  (admin / bench, anonymous viewer enabled)"
log "Prometheus: http://localhost:9090"
