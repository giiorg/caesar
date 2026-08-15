FROM node:22-bookworm-slim
# The base image carries NO trust store — /etc/ssl/certs does not exist. Node
# compiles its own root bundle in, so an npm install and a JS CLI both work
# without one, which is why this was invisible until a live run. The codex CLI
# is a Rust binary that reads the SYSTEM store, so without this every TLS
# connection it makes fails `invalid peer certificate: UnknownIssuer` — the
# WebSocket transport and the HTTPS fallback alike. Found by the row 07 smoke
# run; no CI lane can reach a real endpoint to catch it.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
# Pinned at plan authoring (2026-08-06): npm view @openai/codex version
ARG CODEX_VERSION=0.146.1
RUN npm install -g @openai/codex@${CODEX_VERSION}
COPY entrypoint.sh /usr/local/bin/harness-entry
RUN chmod 0755 /usr/local/bin/harness-entry
ENTRYPOINT ["/usr/local/bin/harness-entry"]
