FROM node:22-bookworm-slim
# Pinned at plan authoring (2026-08-06): npm view @anthropic-ai/claude-code version
ARG CLAUDE_CODE_VERSION=2.1.223
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}
COPY entrypoint.sh /usr/local/bin/harness-entry
RUN chmod 0755 /usr/local/bin/harness-entry
ENTRYPOINT ["/usr/local/bin/harness-entry"]
