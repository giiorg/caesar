#!/bin/sh
# The container is the permission boundary: Plan 06's isolation already holds
# around this process — no capabilities, no-new-privileges, a read-only
# rootfs, the workspace and the auth directory as the only binds, and an
# internal network whose sole route out is the allowlisted egress gateway.
#
# --sandbox danger-full-access because of that, not in spite of it. The flag
# selects which sandbox CODEX builds for itself, and any mode but this one
# makes it spawn bubblewrap, which needs unprivileged user namespaces — which
# the posture above denies. The result is not a weaker sandbox but a broken
# agent: `bwrap: No permissions to create a new namespace`, every file write
# refused, and the CLI exiting 0 having changed nothing. Codex's own help for
# the neighbouring bypass flag names this case exactly — "intended solely for
# running in environments that are externally sandboxed". Read the name as
# "no SECOND sandbox", not "no sandbox". Row 07's smoke run found this.
#
# --skip-git-repo-check because the staged workspace is a real clone detached
# at the run's pinned base SHA; the check is an interactive-use guard against
# running the CLI in some arbitrary directory, and nothing about the isolation
# depends on it.
#
# The credential arrives as a materialized auth.json under $CODEX_HOME, which
# the runner binds; see ../README.md for why nothing here reads CAESAR_BROKER_*.
set -eu
exec codex exec --skip-git-repo-check --sandbox danger-full-access "$(cat /workspace/.caesar/in/instructions.md)"
