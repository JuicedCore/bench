#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
need docker; need curl
cd "$(dirname "${BASH_SOURCE[0]}")" || exit 1
log "starting monitoring stack (Prometheus :9090, Grafana :3000, cAdvisor :8080, node_exporter :9100)"
docker compose up -d || { compose_dump 40; die "monitoring stack failed to start"; }
WAIT_FOR_ON_TIMEOUT=compose_dump
wait_for "Prometheus ready" 60 curl -fsS http://localhost:9090/-/ready \
  || die "Prometheus did not become ready on :9090" "port already in use? ss -ltnp | grep 9090"
wait_for "Grafana healthy" 60 curl -fsS http://localhost:3000/api/health \
  || warn "Grafana is not answering on :3000 yet; dashboards may be unavailable (benchmark numbers are unaffected)"
unset WAIT_FOR_ON_TIMEOUT
log "Grafana:    http://localhost:3000  (admin / bench, anonymous viewer enabled)"
log "Prometheus: http://localhost:9090"
