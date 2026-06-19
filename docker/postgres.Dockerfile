# Official PostgreSQL image plus the pgvector extension, compiled from a
# pinned release. v0.8.0 is the minimum that provides iterative scan. Bitcode
# generation is skipped: the PGXS LLVM compiler is version-pinned to the
# postgres package build and the extension does not rely on JIT.

FROM postgres:17-alpine

RUN apk add --no-cache build-base curl postgresql17-dev \
    && curl -fsSL https://github.com/pgvector/pgvector/archive/refs/tags/v0.8.0.tar.gz \
        | tar xz -C /tmp \
    && make -C /tmp/pgvector-0.8.0 with_llvm=no \
    && make -C /tmp/pgvector-0.8.0 install with_llvm=no \
    && apk del build-base curl postgresql17-dev \
    && rm -rf /tmp/pgvector-0.8.0
