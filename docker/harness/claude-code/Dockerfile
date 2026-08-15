FROM node:22-bookworm-slim
# The base image carries NO trust store — /etc/ssl/certs does not exist. This
# CLI happens not to need one (Node compiles its own root bundle in, which is
# how it reached api.anthropic.com in the row 07 smoke run), so this line buys
# nothing today. It is here because that is luck, not design: the day this
# image gains a helper that reads the system store — a Rust or Go sidecar, a
# curl in a hook — the failure is an opaque `UnknownIssuer` at TLS time. Its
# sibling image had exactly that failure.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
# Pinned at plan authoring (2026-08-06): npm view @anthropic-ai/claude-code version
ARG CLAUDE_CODE_VERSION=2.1.223
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}
COPY entrypoint.sh /usr/local/bin/harness-entry
RUN chmod 0755 /usr/local/bin/harness-entry
ENTRYPOINT ["/usr/local/bin/harness-entry"]
