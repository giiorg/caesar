#!/bin/sh
# The container is the permission boundary, so --dangerously-skip-permissions
# is safe HERE and only here: Plan 06's isolation already holds around this
# process — no capabilities, no-new-privileges, a read-only rootfs, one bind
# (the workspace), and an internal network whose sole route out is the
# allowlisted egress gateway. There is no interactive operator to approve
# anything, so asking would only stall an attempt nobody is watching.
#
# The credential arrives in this image's environment as the CLI's OWN
# variables; see ../README.md for why nothing here reads CAESAR_BROKER_*.
set -eu
exec claude -p "$(cat /workspace/.caesar/in/instructions.md)" --dangerously-skip-permissions
