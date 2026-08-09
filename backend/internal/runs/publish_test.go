package runs

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/giiorg/caesar/backend/internal/runs/store"
)

func TestPublish_TheUnfinishedPublicationInvariantHolds(t *testing.T) {
	// A plain unit test, deliberately: the invariant is a property of three
	// small functions, and pinning it here means it cannot be broken without
	// a red test — including by a later task that adds a fourth exit path.
	//
	// The invariant: while an intent is 'pending' or 'pushed', every error
	// leaving runPublish is TRANSIENT and UNCHARGED. If it is ever permanent,
	// evaluate terminalizes the run; terminalization supersedes every phase,
	// no sweep revisits a failed run, and a branch already on the forge
	// becomes an orphan nobody looks for.
	intent := store.PublishIntent{Branch: "caesar/abc", Status: "pushed"}

	t.Run("a receipt transaction that fails after its effect", func(t *testing.T) {
		// The raw pgx error is the dangerous one: default-deny would make it
		// a permanent internal_error.
		out := ClassifyOutcome(receiptFailure(intent, "push", errors.New("connection reset")))
		if out.Class != OutcomeTransient || out.Chargeable() {
			t.Fatalf("outcome = %+v; want a transient, uncharged failure", out)
		}
	})

	t.Run("a lost receipt CAS still reloads", func(t *testing.T) {
		// errIntentAdvanced is not a failure and must pass through
		// untouched, or the state machine loses its adopt path.
		if err := receiptFailure(intent, "push", errIntentAdvanced); !errors.Is(err, errIntentAdvanced) {
			t.Fatalf("err = %v; want errIntentAdvanced unwrapped", err)
		}
	})

	t.Run("a chargeable transient stops being chargeable", func(t *testing.T) {
		for _, code := range []string{CodeForgeUnavailable, CodePushRejected} {
			in := OutcomeError{Outcome: Outcome{Class: OutcomeTransient, Code: code}}
			out := ClassifyOutcome(unchargedWhileUnfinished(in))
			if out.Class != OutcomeTransient || out.Chargeable() {
				t.Errorf("%s: outcome = %+v; want transient and uncharged — an outage must not "+
					"exhaust a publication whose branch is already pushed", code, out)
			}
		}
	})

	t.Run("a stated reset survives the downgrade", func(t *testing.T) {
		at := time.Now().Add(time.Hour)
		in := OutcomeError{Outcome: Outcome{
			Class: OutcomeTransient, Code: CodeForgeUnavailable, RetryAt: &at,
		}}
		out := ClassifyOutcome(unchargedWhileUnfinished(in))
		if out.RetryAt == nil || !out.RetryAt.Equal(at) {
			t.Fatalf("RetryAt = %v; a forge that stated its own reset must still win", out.RetryAt)
		}
	})

	t.Run("a permanent outcome is left alone", func(t *testing.T) {
		// Converting these to retries would spin until the wall deadline
		// instead of telling the truth now.
		in := OutcomeError{Outcome: Outcome{Class: OutcomePermanent, Code: CodePublishConflict}}
		if out := ClassifyOutcome(unchargedWhileUnfinished(in)); out.Class != OutcomePermanent {
			t.Fatalf("outcome = %+v; want the permanent conflict unchanged", out)
		}
	})

	t.Run("every read and write on a live intent", func(t *testing.T) {
		// resumable is the type every non-receipt exit wears: the artifact
		// read, the position reload, the re-probe after a create conflict.
		out := ClassifyOutcome(resumable("read %s: %v", "artifact", errors.New("connection reset")))
		if out.Class != OutcomeTransient || out.Code != CodePublishResumable || out.Chargeable() {
			t.Fatalf("outcome = %+v; want a transient, uncharged publish_resumable", out)
		}
		if !strings.Contains(out.Message, "connection reset") {
			t.Errorf("message = %q; the underlying cause must survive", out.Message)
		}
	})

	t.Run("the run's execution budget is a separate door", func(t *testing.T) {
		// Chargeable is the NODE's failure budget. This is the RUN's execution
		// budget, pre-checked in evaluateFailure, and a breach there is a
		// permanent budget_exhausted — a terminalized run no sweep revisits,
		// reached with an intent that may be 'pushed'.
		for _, code := range []string{CodeCredentialBusy, CodePublishNotStarted, CodePublishResumable} {
			if !exemptFromBudgetPreCheck(code) {
				t.Errorf("%s is not exempt: it charges nothing, so demoting its requeue to "+
					"budget_exhausted kills a run for time it never spent", code)
			}
		}
		// rate_limited is uncharged too and deliberately NOT exempt: the
		// attempt did run before the window shut.
		for _, code := range []string{CodeRateLimited, CodeForgeUnavailable, CodePushRejected, CodeTimeout} {
			if exemptFromBudgetPreCheck(code) {
				t.Errorf("%s must keep the budget pre-check", code)
			}
		}
	})

	t.Run("the wall deadline is a third door, and parking past it is the safe answer", func(t *testing.T) {
		// evaluateFailure refuses to park a retry that would wake past the run's
		// wall deadline and terminalizes instead — with publish_resumable's ~15s
		// backoff, that kills a run whose branch is on the forge while its
		// deadline is still in the future, which no sweep revisits.
		for _, code := range []string{CodePublishNotStarted, CodePublishResumable} {
			if !exemptFromParkPastDeadline(code) {
				t.Errorf("%s is not exempt: terminalizing a live publication strands it, while "+
					"parking it only makes a row ClaimPhase already refuses", code)
			}
		}
		// credential_busy is uncharged and budget-exempt, and deliberately NOT
		// exempt here: it leaves nothing behind, so a run out of wall clock
		// waiting for a lease is honestly dead. The two sets differ on purpose.
		for _, code := range []string{CodeCredentialBusy, CodeRateLimited, CodeForgeUnavailable, CodeTimeout} {
			if exemptFromParkPastDeadline(code) {
				t.Errorf("%s must keep the park-past-deadline refusal", code)
			}
		}
	})
}

// The halt set's NEGATIVE half: what must stay out of a human's inbox.
//
// Only this direction is asserted here. The positive direction — that
// publish_conflict, auth_expired and repo_unavailable each halt — is a list this
// file cannot falsify, because asserting haltsPublication(x) for the same three
// constants the switch names passes for any implementation that compiles the
// same list. That half is earned end to end instead, by the three conflict
// tests and by TestPublish_APermanentForgeRefusalHalts..., which drive a real
// state machine and assert the intent row and the run's outcome CODE.
//
// This half does carry information: every code below really does reach
// maybeHalt — the gitops permanents through the push call site, the transients
// through all four, the two probes included — and each is a failure the run is
// entitled to die on or to retry. Summoning a person for one of them empties
// the inbox of meaning.
func TestHaltsPublication_KeepsOrdinaryFailuresOutOfAHumansInbox(t *testing.T) {
	for _, code := range []string{
		CodeForgeUnavailable, CodePushRejected, CodePublishResumable, CodePublishNotStarted,
		CodeInvalidOutput, CodeBaseSHAUnavailable, CodeInternalError, CodeTimeout,
	} {
		if haltsPublication(code) {
			t.Errorf("%s must not halt: halting is a human's inbox, and filling it with "+
				"answers that retry or that honestly end the run empties it of meaning", code)
		}
	}
}

// The other half: what the forge adapter cannot PROVE permanent must come back
// transient, so unchargedWhileUnfinished downgrades it to an uncharged,
// resumable retry rather than terminalizing a run whose branch is on the forge.
func TestPublish_AForgeAnswerWeCannotProvePermanentResumesUncharged(t *testing.T) {
	// The exact outcome app's forgeAdapter produces for an answer it cannot
	// classify: a 400 with a message nobody has taught us to read.
	in := OutcomeError{Outcome: Outcome{
		Class: OutcomeTransient, Code: CodeForgeUnavailable,
		Message: "open pull request: forge returned 418: I'm a teapot",
	}}
	out := ClassifyOutcome(unchargedWhileUnfinished(in))
	if out.Class != OutcomeTransient || out.Code != CodePublishResumable || out.Chargeable() {
		t.Fatalf("outcome = %+v; want a transient, uncharged publish_resumable — an answer we "+
			"could not read must be asked again, not terminalize a run with its branch pushed", out)
	}
	if !strings.Contains(out.Message, "teapot") {
		t.Errorf("message = %q; the forge's own words must survive the downgrade", out.Message)
	}
	// And the proven-permanent ones are deliberately NOT downgraded: they have
	// to arrive at maybeHalt still permanent, or the halt never happens.
	for _, code := range []string{CodeAuthExpired, CodeRepoUnavailable} {
		perm := OutcomeError{Outcome: Outcome{Class: OutcomePermanent, Code: code}}
		if got := ClassifyOutcome(unchargedWhileUnfinished(perm)); got.Class != OutcomePermanent || got.Code != code {
			t.Errorf("%s = %+v; a proven-permanent forge answer must reach maybeHalt intact", code, got)
		}
	}
}

// notStartedUnlessTyped is the only thing standing between the nine untyped
// statement errors inside authorizePublish — the COMMIT included — and a
// permanent internal_error that terminalizes a run which has caused nothing.
func TestNotStartedUnlessTyped(t *testing.T) {
	t.Run("an untyped failure becomes an uncharged non-start", func(t *testing.T) {
		out := ClassifyOutcome(notStartedUnlessTyped(errors.New("connection reset")))
		if out.Class != OutcomeTransient || out.Code != CodePublishNotStarted || out.Chargeable() {
			t.Fatalf("outcome = %+v; want a transient, uncharged publish_not_started", out)
		}
	})
	t.Run("a deliberate answer survives", func(t *testing.T) {
		// A stale authorization is a real permanent verdict. Retyping it
		// would spin until the wall deadline instead of failing honestly.
		in := OutcomeError{Outcome: Outcome{
			Class: OutcomePermanent, Code: CodeAuthorizationStale, Message: "spent",
		}}
		out := ClassifyOutcome(notStartedUnlessTyped(in))
		if out.Class != OutcomePermanent || out.Code != CodeAuthorizationStale {
			t.Fatalf("outcome = %+v; want the permanent stale authorization unchanged", out)
		}
	})
}

// matchPublication decides whether a pull request sitting on our marker IS
// this publication's. Every refusal below is a place where adopting would
// record a stranger's request as this run's output, and none of them is
// reachable from the integration suite: the fake forge answers truthfully, so
// only the happy leg is ever exercised there.
func TestMatchPublication(t *testing.T) {
	const (
		base = "main"
		head = "aaaa111"
	)
	ours := store.PublishIntent{
		TargetBranch: base,
		HeadSha:      pgtype.Text{String: head, Valid: true},
	}
	exact := PullRequestRef{Number: 7, Base: base, HeadSHA: head}

	cases := []struct {
		name          string
		intent        store.PublishIntent
		candidates    []PullRequestRef
		wantNumber    int64
		wantFound     bool
		wantAmbiguous bool
	}{
		{
			name: "no candidates is not a match and not a problem",
		},
		{
			name: "an exact agreement on base and head is ours", intent: ours,
			candidates: []PullRequestRef{exact}, wantFound: true, wantNumber: 7,
		},
		{
			// A human can open a second request from our marker to a
			// different target. Adopting it would report a publication that
			// merges somewhere nobody approved.
			name: "a request to another base is definitively not ours", intent: ours,
			candidates: []PullRequestRef{{Number: 8, Base: "release", HeadSHA: head}},
		},
		{
			name: "a request at another commit is definitively not ours", intent: ours,
			candidates: []PullRequestRef{{Number: 9, Base: base, HeadSHA: "bbbb222"}},
		},
		{
			// Absent evidence must not become permission: an empty field
			// cannot be confirmed OR ruled out, so it is a human's call.
			name: "a candidate with no base is unprovable either way", intent: ours,
			candidates: []PullRequestRef{{Number: 10, HeadSHA: head}}, wantAmbiguous: true,
		},
		{
			name: "a candidate with no head sha is unprovable either way", intent: ours,
			candidates: []PullRequestRef{{Number: 11, Base: base}}, wantAmbiguous: true,
		},
		{
			name: "two exact matches cannot both be ours, so neither is chosen", intent: ours,
			candidates:    []PullRequestRef{exact, {Number: 12, Base: base, HeadSHA: head}},
			wantAmbiguous: true,
		},
		{
			// Before the push receipt commits there is no head sha to compare
			// against, so nothing on the marker is provably ours.
			name:       "an intent with no push receipt matches nothing",
			intent:     store.PublishIntent{TargetBranch: base},
			candidates: []PullRequestRef{exact},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr, found, ambiguous := matchPublication(tc.candidates, tc.intent)
			if found != tc.wantFound || ambiguous != tc.wantAmbiguous {
				t.Fatalf("found=%v ambiguous=%v, want found=%v ambiguous=%v",
					found, ambiguous, tc.wantFound, tc.wantAmbiguous)
			}
			if found && pr.Number != tc.wantNumber {
				t.Errorf("adopted pull request %d, want %d", pr.Number, tc.wantNumber)
			}
		})
	}
}

func TestPublicationBranchIsDerivedFromTheApprovedArtifact(t *testing.T) {
	// Recovery reconstructs the marker from the authorization row alone, so
	// it may depend on nothing else.
	art := store.PublishIntent{}.ID // the zero uuid is enough to pin the shape
	if got, want := PublicationBranch(art), "caesar/00000000-0000-0000-0000-000000000000"; got != want {
		t.Fatalf("PublicationBranch = %q, want %q", got, want)
	}
}

func TestEmailLocalPartIsTheCommitAuthorsName(t *testing.T) {
	for in, want := range map[string]string{
		"ada@example.com": "ada",
		"ada":             "ada", // no @: the address is the closest thing to a name
		"@example.com":    "@example.com",
		"":                "",
	} {
		if got := emailLocalPart(in); got != want {
			t.Errorf("emailLocalPart(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateNeverSplitsARune(t *testing.T) {
	// A forge rejecting a request for size surfaces as an unexplained
	// permanent failure, and a half rune is worse than a short title.
	s := strings.Repeat("é", 10) // two bytes each
	if got := truncate(s, 5); got != strings.Repeat("é", 2) {
		t.Fatalf("truncate = %q, want two whole runes", got)
	}
	if got := truncate("short", 100); got != "short" {
		t.Fatalf("truncate = %q, want the input untouched", got)
	}
}

func TestCommitMessageForIsAPureFunctionOfDurableInputs(t *testing.T) {
	// Task 10's settlement rebuilds this exact commit to prove a receiptless
	// branch is ours. Any drift changes the commit sha and every ownership
	// check fails open, which is why the rendered string is stored on the
	// intent rather than re-rendered — and why the attempt ordinal, the one
	// input that differs between attempts, may never appear in it.
	auth := authorization{ApproverEmail: "ada@example.com"}
	ref := AttemptRef{NodeID: "publish", Attempt: 4}
	got := commitMessageFor("ship it", ref, auth)
	for _, want := range []string{
		"ship it\n\n", "caesar-run: ", "caesar-node: publish\n",
		"caesar-artifact: ", "caesar-approved-by: ada@example.com\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("commit message %q is missing %q", got, want)
		}
	}
	if strings.Contains(got, "4") {
		t.Error("the attempt ordinal must not reach the message: it differs between attempts of one publication")
	}
}
