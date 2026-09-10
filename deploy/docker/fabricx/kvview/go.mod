// Phase 3: the custom Fabric-X "kv-write" FSC view service.
//
// This module currently builds a STUB HTTP server (stdlib only) so the container
// runs and the fabricx adapter's health check passes. The real implementation
// adds the Fabric Smart Client + Panurus token deps and registers the KV views -
// see README.md for the exact call sequence, transcribed from the
// fabric-x-samples owner service.
module github.com/juicedcore/bench/deploy/docker/fabricx/kvview

go 1.23
