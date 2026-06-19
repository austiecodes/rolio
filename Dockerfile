# Multi-stage build against musl: the server becomes a static binary, so the
# runtime image needs no packages at all (busybox wget serves the healthcheck).
# The pinned embedding model is NOT baked in; mount it read-only at /artifacts.

FROM rust:1.96.0-alpine AS build
WORKDIR /rolio
# build-base/cmake/clang build llama.cpp; clang-dev provides libclang for
# bindgen; perl is needed by aws-lc-sys under the sqlx rustls feature.
# Fully-static musl build scripts cannot dlopen libclang, so link musl
# dynamically instead; the alpine runtime provides the musl dynamic loader.
RUN apk add --no-cache build-base clang-dev cmake linux-headers perl
ENV RUSTFLAGS="-C target-feature=-crt-static"
COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY crates ./crates
COPY scripts ./scripts
RUN --mount=type=cache,target=/rolio/target \
    cargo build --release -p rolio-server --all-features --locked \
    && cp target/release/rolio-server /rolio-server

FROM alpine:3.22
# llama.cpp brings C++: the dynamic musl binary needs the C++ runtime.
RUN apk add --no-cache libgcc libstdc++
COPY --from=build /rolio-server /usr/local/bin/rolio-server
ENV ROLO_ADDR=0.0.0.0:8080 \
    ROLO_ARTIFACTS=/artifacts
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=60s --retries=12 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/rolio-server"]
