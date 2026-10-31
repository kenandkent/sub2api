FROM oven/bun:1.4.2@sha256:9114c058aeae42162ee16dd5084b95fe9473970bb6bcb5b232ab1630f0546895
ARG VERSION=
ARG COMMIT=docker
ARG DATE=
ARG SOURCE_DIGEST=
ARG SOURCE_DIRTY=unknown
LABEL org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${DATE}" \
      io.sub2api.source.digest="${SOURCE_DIGEST}" \
      io.sub2api.source.dirty="${SOURCE_DIRTY}"
WORKDIR /app
COPY deploy/opencode/worker/ /app/worker/
COPY deploy/opencode/profiles/ /app/profiles/
USER bun
ENV LISTEN_HOST=127.0.0.1 PORT=19090
CMD ["bun", "/app/worker/index.ts"]
