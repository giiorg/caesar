#!/bin/sh
# fakecli — the scripted stand-in for a real agent CLI, and the ONLY agent CI
# ever runs: no lane may place a live subscription call. It reads the phase's
# instructions from exactly where the real harness entrypoints do
# (docker/harness/*/entrypoint.sh) and executes one `fakecli:` directive per
# line; every other line is prose and is ignored, so the same instructions can
# read as a task to a human and as a script to this file.
#
# It is also the executable specification of the codex driver's PROVISIONAL
# exit-marker table: `fakecli:rate-limit-until-refreshed` prints the cap marker
# verbatim, so an edit to driver.go's regex breaks the e2e here rather than
# silently reclassifying a real cap hit as a generic harness error.
set -eu

instructions=/workspace/.caesar/in/instructions.md
# CODEX_HOME is what the codex driver's authMount sets, and there is
# deliberately no default: the mount's containerPath is a value this fixture
# must never know. A default equal to it would read the same bytes with the env
# half of the delivery unwired, so print-auth-sha would keep passing while the
# variable the real CLI needs was never emitted. Unset dies here, loudly.
auth="${CODEX_HOME:?the codex driver's authMount did not deliver CODEX_HOME}/auth.json"

# The values mutate-auth writes. Both clear the runner's 16-byte exact-match
# redaction floor, and the refresh token carries the "rt-refreshed" marker that
# rate-limit-until-refreshed keys its cross-attempt state off: that state lives
# in the credential store, never in this process.
refreshed_access="at-refreshed-0123456789abcdef"
refreshed_refresh="rt-refreshed-0123456789abcdef"

# The account poison-auth substitutes. account_id and the id_token's sub/email
# claims are the whole of what the runner fingerprints an auth file by
# (driver.go codexIdentity), so poison_auth moves all three at once: a
# different account, and an id_token that yields no claims at all.
intruder_account="acct-intruder"

# write_auth rewrites the auth file the way a real CLI does: truncate in place
# and rewrite, never rename into position. A single-file bind would survive
# only the rename form, which is exactly why the runner binds the DIRECTORY
# (spec §2.4) — this is the harsher of the two write strategies.
write_auth() {
    tmp=/tmp/fakecli-auth.json
    cat > "$tmp"
    : > "$auth"
    cat "$tmp" >> "$auth"
    rm -f "$tmp"
}

# A CLI token refresh: new access and refresh values, SAME account identity.
mutate_auth() {
    jq --arg at "$refreshed_access" --arg rt "$refreshed_refresh" \
        '.tokens.access_token = $at | .tokens.refresh_token = $rt' "$auth" | write_auth
}

# An agent-authored substitution: a different subscription behind an otherwise
# well-formed file (spec §2.5).
poison_auth() {
    jq --arg acct "$intruder_account" \
        '.tokens.account_id = $acct | .tokens.id_token = ""' "$auth" | write_auth
}

# The torn write (spec §2.7): a CLI died mid-rewrite, and what is on disk can
# no longer say what the live credential is.
garble_auth() {
    printf '{"tokens":{"access_to' > "$auth"
}

line=""
while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
    fakecli:*) ;;
    *) continue ;;
    esac
    # Deliberate word splitting: a directive is a space-separated command line,
    # and every one of them is control-plane authored.
    # shellcheck disable=SC2086
    set -- $line
    directive=$1
    shift

    case "$directive" in
    fakecli:assert-no-broker)
        # Under materialized custody spec.go never mints a broker token, so a
        # token in this environment means the custody derivation broke and a
        # brokered secret reached an attempt that holds the credential itself.
        if [ -n "${CAESAR_BROKER_TOKEN:-}" ]; then
            echo "fakecli:broker-token-present"
            exit 42
        fi
        echo "fakecli:no-broker-token"
        ;;
    fakecli:print-auth-sha)
        # Proves BOTH halves of file delivery at once: the env var pointed
        # somewhere, and the bytes there are the ones the store granted.
        sha=$(sha256sum "$auth" | cut -d' ' -f1)
        echo "fakecli:auth-sha $sha"
        printf '%s\n' "$sha" > /workspace/auth-sha.txt
        ;;
    fakecli:mutate-auth)
        mutate_auth
        echo "fakecli:auth-mutated"
        ;;
    fakecli:poison-auth)
        poison_auth
        echo "fakecli:auth-poisoned"
        ;;
    fakecli:garble-auth)
        garble_auth
        echo "fakecli:auth-garbled"
        ;;
    fakecli:print-secret)
        # Redaction bait. A real CLI prints its own token often enough — in a
        # stack trace, in a debug dump — that a published log may never be
        # trusted to be free of one by accident.
        echo "fakecli:secret $(jq -r '.tokens.access_token' "$auth")"
        ;;
    fakecli:write)
        file=$1
        shift
        printf '%s\n' "$*" > "/workspace/$file"
        echo "fakecli:wrote $file"
        ;;
    fakecli:rate-limit-until-refreshed)
        if grep -q "$refreshed_refresh" "$auth"; then
            # Attempt 2 and later: the store handed back the credential this
            # attempt's predecessor refreshed, so the cap window is over.
            echo "fakecli:credential-already-refreshed"
        else
            # The codex driver's epoch-bearing cap marker, verbatim:
            # `(?i)usage limit reached\|(\d{9,12})` in driver.go.
            echo "usage limit reached|$1"
            mutate_auth
            exit 1
        fi
        ;;
    fakecli:auth-death)
        # The codex driver's auth-expired marker, in the shape the row now
        # requires: the CLI's own uppercase ERROR level marker, then the
        # message. A bare `401 Unauthorized` no longer classifies, because an
        # agent pasting a log excerpt puts exactly that at column 0.
        echo "ERROR: 401 Unauthorized: token expired"
        exit 1
        ;;
    fakecli:exit)
        exit "$1"
        ;;
    *)
        # A mistyped directive must never read as a passing attempt.
        echo "fakecli: unknown directive $directive" >&2
        exit 43
        ;;
    esac
done < "$instructions"

echo "fakecli:done"
exit 0
