# syntax=docker/dockerfile:1
# Self-built Fabric-X backend: Arma (4-party/1-shard) + all 5 committer
# services, packed in one container, replacing fabric-x-samples' Ansible-
# deployed orderer+committer. See docs/platforms/fabricx-comparability.md for
# why: that deployment pins fabric-x-committer:0.1.7, which predates the
# committerpb.QueryService RPC tokens/'s go.mod-pinned client expects.
# Building committer+orderer together from one consistent source snapshot
# (adapted from a proven working reference) avoids that skew entirely.
#
#   docker build -t bench/fabricx-backend:latest \
#     --build-arg COMMITTER_SRC=.cache/fabric-x-committer-src \
#     --build-arg ORDERER_SRC=.cache/fabric-x-orderer-src \
#     -f deploy/docker/fabricx/backend/Dockerfile deploy/docker/fabricx

FROM golang:1.27-bookworm AS build
ARG COMMITTER_SRC=.cache/fabric-x-committer-src
ARG ORDERER_SRC=.cache/fabric-x-orderer-src
WORKDIR /src

COPY ${COMMITTER_SRC} /src/committer
WORKDIR /src/committer
ENV CGO_ENABLED=0
RUN go build -o /out/committer ./cmd/committer

COPY ${ORDERER_SRC} /src/orderer
WORKDIR /src/orderer
RUN go build -o /out/arma ./cmd/arma \
 && go build -o /out/armageddon ./cmd/armageddon

# testutil sample configs + the deployment topology, staged for the final
# stage (armageddon must generate directly at its final runtime path -
# /root/arma - since it bakes absolute paths into the generated YAMLs; doing
# this in a separate stage and COPYing the result elsewhere breaks every
# baked-in path, including the TLS key/cert paths).
RUN mkdir -p /out/sampleconfig \
 && cp -r /src/orderer/testutil/fabric/sampleconfig/. /out/sampleconfig/
COPY backend/arma-deployment.yaml /out/arma-deployment.yaml
COPY backend/generate-arma.sh /out/generate-arma.sh

FROM postgres:16-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
 && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /root/bin /root/config /root/artifacts /root/arma /data/arma /data/ledger
COPY --from=build /out/committer /out/arma /out/armageddon /root/bin/
COPY --from=build /out/sampleconfig /tmp/sampleconfig
COPY --from=build /out/arma-deployment.yaml /tmp/arma-deployment.yaml
COPY --from=build /out/generate-arma.sh /tmp/generate-arma.sh
COPY --from=build /src/committer/cmd/config/samples /root/config
COPY backend/patch-sidecar-identity.py /root/patch-sidecar-identity.py
COPY backend/run.sh /opt/fabricx-backend/run.sh
RUN chmod a+x /root/bin/* /tmp/generate-arma.sh /opt/fabricx-backend/run.sh \
 && ARMA_OUT=/root/arma DEPLOY=/tmp/arma-deployment.yaml \
    SAMPLE=/tmp/sampleconfig ARMAGEDDON=/root/bin/armageddon \
    /tmp/generate-arma.sh \
 && cp /root/arma/bootstrap/bootstrap.block /root/artifacts/config-block.pb.bin \
 && python3 /root/patch-sidecar-identity.py

ENV BINS_PATH=/root/bin \
    CONFIGS_PATH=/root/config \
    ARTIFACTS_PATH=/root/artifacts \
    ARMA_DIR=/root/arma \
    PATH=/root/bin:/opt/fabricx-backend:$PATH \
    POSTGRES_PASSWORD=postgres \
    POSTGRES_USER=postgres \
    POSTGRES_DB=postgres \
    SC_VC_DATABASE_USERNAME=postgres \
    SC_VC_DATABASE_PASSWORD=postgres \
    SC_VC_DATABASE_DATABASE=postgres \
    SC_QUERY_DATABASE_USERNAME=postgres \
    SC_QUERY_DATABASE_PASSWORD=postgres \
    SC_QUERY_DATABASE_DATABASE=postgres \
    SC_VC_DATABASE_TLS_MODE=none \
    SC_QUERY_DATABASE_TLS_MODE=none \
    SC_VC_DATABASE_ENDPOINTS=localhost:5433 \
    SC_QUERY_DATABASE_ENDPOINTS=localhost:5433 \
    SC_SIDECAR_COMMITTER_ENDPOINT=localhost:9001 \
    SC_COORDINATOR_VERIFIER_ENDPOINTS=localhost:5001 \
    SC_COORDINATOR_VALIDATOR_COMMITTER_ENDPOINTS=localhost:6001

# 4001 = sidecar (client-facing, tokens/'s core.yaml: peers/notificationService)
# 7001 = query-service (client-facing, tokens/'s core.yaml: queryService)
# Everything else (coordinator/verifier/vc, Arma's internal ports) is only
# reached from inside this container.
EXPOSE 4001 7001

ENTRYPOINT ["/opt/fabricx-backend/run.sh"]
