# syntax=docker/dockerfile:1
#
# One image, four roles. Postgres is the base and the Fabric-X binaries are
# layered on top; the role is chosen by argv[1] (see run.sh). This mirrors the
# working reference deployment - packing the four Arma parties into a single
# container keeps the whole stack inside the profile's memory budget, which a
# container-per-service layout does not on a laptop-sized host.
#
# Built by deploy/docker/fabricx/up.sh, which clones the pinned sources into
# .cache/ first. Build context is deploy/docker/fabricx/.

FROM golang:1.27-bookworm AS build
ENV CGO_ENABLED=0
WORKDIR /src

COPY .cache/fabric-x-committer-src /src/committer
WORKDIR /src/committer
RUN go build -o /out/committer ./cmd/committer \
 && go build -o /out/loadgen   ./cmd/loadgen

COPY .cache/fabric-x-orderer-src /src/orderer
WORKDIR /src/orderer
RUN go build -o /out/arma       ./cmd/arma \
 && go build -o /out/armageddon ./cmd/armageddon

FROM postgres:18.3-alpine3.23
RUN apk add --no-cache ca-certificates python3 \
 && mkdir -p /root/bin /root/config /root/artifacts /data/ledger /data/arma /opt/fabric-x

COPY --from=build /out/committer /out/loadgen /out/arma /out/armageddon /root/bin/
COPY .cache/fabric-x-committer-src/cmd/config/samples          /root/config
COPY .cache/fabric-x-orderer-src/testutil/fabric/sampleconfig  /root/arma-sampleconfig
COPY image/run.sh image/start-arma.sh image/start-node2.sh image/start-node3.sh \
     image/generate-arma.sh image/patch-configs.py image/entrypoint.sh /opt/fabric-x/
COPY image/arma-deployment.yaml /opt/fabric-x/arma-deployment.yaml

ENV BINS_PATH=/root/bin \
    CONFIGS_PATH=/root/config \
    ARTIFACTS_PATH=/root/artifacts \
    ARMA_DIR=/root/arma \
    PATH=/root/bin:/opt/fabric-x:$PATH \
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
    SC_LOADGEN_LOAD_PROFILE_POLICY_CHANNEL_ID=arma

# Block-cutting parameters are a fairness lever and must match every other
# platform in the comparison, so they are baked from the profile at build time
# rather than left at the sample defaults.
ARG BENCH_BATCH_TIMEOUT=1s
ARG BENCH_BATCH_MAX_MESSAGE_COUNT=100
ENV BENCH_BATCH_TIMEOUT=${BENCH_BATCH_TIMEOUT} \
    BENCH_BATCH_MAX_MESSAGE_COUNT=${BENCH_BATCH_MAX_MESSAGE_COUNT}

RUN chmod a+x /root/bin/* /opt/fabric-x/*.sh /opt/fabric-x/patch-configs.py

# The namespace signing keypair. Generated here so the public half can be
# registered as the namespace policy at deploy time and the private half handed
# to the adapter - both sides then provably use the same key. PKCS#8 for the
# private key and PKIX for the public, which is what the committer parses.
RUN apk add --no-cache openssl \
 && openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 \
      -outform PEM -out /root/artifacts/ns-signing-key.pem \
 && openssl pkey -in /root/artifacts/ns-signing-key.pem -pubout \
      -out /root/artifacts/ns-verification-key.pem \
 && chmod 600 /root/artifacts/ns-signing-key.pem

RUN python3 /opt/fabric-x/patch-configs.py
RUN ARMA_OUT=/root/arma DEPLOY=/opt/fabric-x/arma-deployment.yaml \
    SAMPLE=/root/arma-sampleconfig ARMAGEDDON=/root/bin/armageddon \
    /opt/fabric-x/generate-arma.sh \
 && loadgen make-artifacts -c /root/config/loadgen.yaml \
 && cp /root/arma/bootstrap/bootstrap.block /root/artifacts/config-block.pb.bin

WORKDIR /data
ENTRYPOINT ["/opt/fabric-x/entrypoint.sh"]
