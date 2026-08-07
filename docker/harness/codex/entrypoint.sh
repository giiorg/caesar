#!/bin/sh
# The container is the permission boundary, so --sandbox workspace-write is
# a convenience rather than the barrier: Plan 06's isolation already holds
# around this process — no capabilities, no-new-privileges, a read-only
# rootfs, the workspace and the auth directory as the only binds, and an
# internal network whose sole route out is the allowlisted egress gateway.
#
# --skip-git-repo-check because the staged workspace is a real clone detached
# at the run's pinned base SHA; the check is an interactive-use guard against
# running the CLI in some arbitrary directory, and nothing about the isolation
# depends on it.
#
# The credential arrives as a materialized auth.json under $CODEX_HOME, which
# the runner binds; see ../README.md for why nothing here reads CAESAR_BROKER_*.
set -eu
exec codex exec --skip-git-repo-check --sandbox workspace-write "$(cat /workspace/.caesar/in/instructions.md)"
