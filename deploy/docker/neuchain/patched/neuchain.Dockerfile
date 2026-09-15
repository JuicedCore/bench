# syntax=docker/dockerfile:1
# Multi-stage: NeuChain official Ubuntu 20.04 deps + build + runtime.
# First stage follows upstream Dockerfile (install_deps.sh). Second stage compiles.
# Third stage is a slim runtime with binaries and .so files.

FROM ubuntu:20.04 AS deps
ENV HOME=/root
ARG DEBIAN_FRONTEND=noninteractive
SHELL ["/bin/bash", "-c"]
WORKDIR /root
COPY third_party/NeuChain /root/neuchain
RUN echo $'path-exclude /usr/share/doc/* \n\
path-exclude /usr/share/doc/*/copyright \n\
path-exclude /usr/share/man/* \n\
path-exclude /usr/share/groff/* \n\
path-exclude /usr/share/info/* \n\
path-exclude /usr/share/lintian/* \n\
path-exclude /usr/share/linda/*' > /etc/dpkg/dpkg.cfg.d/01_nodoc && \
    chmod +x /root/neuchain/install_deps.sh && \
    /root/neuchain/install_deps.sh && \
    apt-get clean && apt-get autoclean && rm -rf /var/lib/apt/lists/*

FROM deps AS build
WORKDIR /root/neuchain
# Optional source patches (latency CSV, epoch-gap CHECK). No-op if a hunk fails.
COPY docker/patches /root/patches
RUN for p in /root/patches/*.patch; do \
      echo "Applying $p"; \
      patch -p1 -d /root/neuchain < "$p" || true; \
    done
COPY docker/patches/workload_size_adapter.h \
     /root/neuchain/include/block_server/coordinator/workload_size_adapter.h
COPY docker/patches/crypto_sign.cpp \
     /root/neuchain/src/common/msp/crypto_sign.cpp
# Do not overlay aria_coordinator: a 20ms collect timeout made nodes commit
# different tx sets for the same epoch, so remote block signatures failed
# (crypto phase-3 CHECK → docker 139). Upstream coordinator + clientSize=1
# keeps epoch order; empty epochs still need generateBlockBody below.
COPY docker/patches/block_generator_impl.cpp \
     /root/neuchain/src/block_server/database/impl/block_generator_impl.cpp
COPY docker/patches/ev_consensus_manager.h \
     /root/neuchain/include/epoch_server/ev_consensus_manager.h
COPY docker/patches/ev_consensus_manager.cpp \
     /root/neuchain/src/epoch_server/ev_consensus_manager.cpp
COPY docker/patches/epoch_server.cpp \
     /root/neuchain/src/epoch_server/epoch_server.cpp
# Serialise epoch assignment: 10 async_recv threads assign epochs out of order.
RUN sed -i 's/const int clientSize = 10;/const int clientSize = 1;/' \
      include/block_server/comm/client_proxy/user_collector.h
# db_init binary: same sources with initDatabase() enabled. Restore server.cpp
# from a copy — the tree has no .git, so checkout is a no-op.
RUN cp src/block_server/server.cpp /tmp/server.cpp.bak && \
    sed -i 's|// initDatabase();|initDatabase();|' src/block_server/server.cpp && \
    sed -i 's|// return 0;|return 0;|' src/block_server/server.cpp && \
    cmake -B /tmp/build-init -D CMAKE_BUILD_TYPE=Release && \
    cmake --build /tmp/build-init --target block_server_test_comm -j"$(nproc)" && \
    cp /tmp/build-init/src/block_server/block_server_test_comm /tmp/db_init && \
    cp /tmp/server.cpp.bak src/block_server/server.cpp
RUN sed -i 's/const double retryDelay = 0;/const double retryDelay = 0.05;/' \
      src/user/block_bench/db_user_base.cpp
RUN cmake -B build -D CMAKE_BUILD_TYPE=Release && \
    cmake --build build --target block_server_test_comm -j"$(nproc)" && \
    cmake --build build --target epoch_server -j"$(nproc)" && \
    cmake --build build --target user -j"$(nproc)"

FROM ubuntu:20.04 AS runtime
ENV DEBIAN_FRONTEND=noninteractive \
    LD_LIBRARY_PATH=/usr/local/lib \
    LC_ALL=C.UTF-8
RUN apt-get update && apt-get install -y --no-install-recommends \
        libssl1.1 libunwind8 libpq5 ca-certificates iperf3 procps iproute2 python3 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /opt/neuchain
COPY --from=build /usr/local/lib /usr/local/lib
COPY --from=build /root/neuchain/build/src/block_server/block_server_test_comm /opt/neuchain/bin/
COPY --from=build /root/neuchain/build/src/epoch_server/epoch_server /opt/neuchain/bin/
COPY --from=build /root/neuchain/build/src/user/user /opt/neuchain/bin/
COPY --from=build /tmp/db_init /opt/neuchain/bin/db_init
COPY docker/entrypoint-node.sh docker/entrypoint-client.sh docker/entrypoint-db-init.sh docker/entrypoint-crypto.sh /opt/neuchain/
COPY configs/neuchain/config-dbinit.yaml configs/neuchain/config-crypto.yaml /opt/neuchain/
RUN chmod +x /opt/neuchain/*.sh /opt/neuchain/bin/* && ldconfig
ENV PATH=/opt/neuchain/bin:$PATH
WORKDIR /data
EXPOSE 5001 7003 8100 9002 9003
ENTRYPOINT ["/opt/neuchain/entrypoint-node.sh"]
