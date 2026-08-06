FROM node:22-bookworm-slim
# Pinned at plan authoring (2026-08-06): npm view @openai/codex version
ARG CODEX_VERSION=0.146.1
RUN npm install -g @openai/codex@${CODEX_VERSION}
COPY entrypoint.sh /usr/local/bin/harness-entry
RUN chmod 0755 /usr/local/bin/harness-entry
ENTRYPOINT ["/usr/local/bin/harness-entry"]
