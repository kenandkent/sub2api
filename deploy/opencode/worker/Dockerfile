FROM oven/bun:1.4.2@sha256:9114c058aeae42162ee16dd5084b95fe9473970bb6bcb5b232ab1630f0546895
WORKDIR /app
COPY deploy/opencode/worker/ /app/worker/
COPY deploy/opencode/balanced-text-profile.v2.json /app/balanced-text-profile.v2.json
USER bun
ENV LISTEN_HOST=127.0.0.1 PORT=19090
CMD ["bun", "/app/worker/index.ts"]
