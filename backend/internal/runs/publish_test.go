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

// There is deliberately no unit test on the halt DECISION any more.
//
// It used to be an allow-list of three codes, and this file asserted its
// negative half — that invalid_output, base_sha_unavailable and the ordinary
// transients stayed out of a human's inbox. Both halves are now gone, for
// different reasons. The positive half was always unfalsifiable, and said so:
// asserting the predicate against the same constants its switch names passes
// for any implementation that compiles the same list. The negative half was
// worse than unfalsifiable — it was WRONG about the permanents, and its
// reasoning ("the run is entitled to die on these") answered the question
// "is this a person's problem?" when the question maybeHalt's position forces
// is "must this publication's row be closed?". Nine permanent gitops exits
// left the intent 'pending' forever on a dead run behind that argument.
//
// maybeHalt now decides on the CLASS, so a predicate test would restate one
// comparison. The property is earned end to end instead, where it can fail:
// the conflict tests and TestPublish_APermanentCommitFailureOnAResumed... drive
// a real state machine and assert the intent row, the halt reason, the event
// and the audit row. What this file still owes the halt is the assertion just
// below — that a permanent answer reaches maybeHalt STILL permanent, since a
// downgrade would mean the halt never happens at all.

// What the forge adapter cannot PROVE permanent must come back
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
	// And the permanent ones are deliberately NOT downgraded: they have to
	// arrive at maybeHalt still permanent, or the halt never happens.
	//
	// invalid_output and base_sha_unavailable are on this list because they are
	// what gitops raises — nine of its exits between them — and they are the
	// codes the old allow-list left stranded. A downgrade here would put them
	// straight back to a 'pending' row that no sweep revisits.
	for _, code := range []string{
		CodeAuthExpired, CodeRepoUnavailable, CodeInvalidOutput, CodeBaseSHAUnavailable,
	} {
		perm := OutcomeError{Outcome: Outcome{Class: OutcomePermanent, Code: code}}
		if got := ClassifyOutcome(unchargedWhileUnfinished(perm)); got.Class != OutcomePermanent || got.Code != code {
			t.Errorf("%s = %+v; a proven-permanent forge answer must reach maybeHalt intact", code, got)
		}
	}
}

// The DELAY half of the same contract, which the class assertions above cannot
// see: what a failure is typed as also decides how long the publication waits
// before asking again, and one of the four answers a probe can carry waits for
// nothing at all.
//
// publish_not_started is scheduled {0, 0, 0} (retry.go) — an immediate requeue,
// which is right for what that code was minted for: a worker that died before
// an intent existed, where the successor authorizes from scratch. Both forge
// ports also produce it from their OWN pre-flight (runsports.go: an unsupported
// forge string, or assets.UseCredential failing because the credential row is
// gone or its DEK will not unwrap), and by the time a probe runs, an intent
// exists and every one of those causes is PERSISTENT. Uncharged and exempt from
// both terminalization doors, it would then claim → authorize → probe → fail →
// requeue at now+0 until the wall deadline, burning the run's execution budget
// at the maximum rate and writing a credential.used audit row per lap.
//
// So all four rows must wait. The table is the whole point: three of them are
// unchanged behaviour and only the second is the fix, and a version that
// downgraded everything, or nothing, fails a different row.
func TestPublish_AProbeFailureOnALivePublicationAlwaysWaitsBeforeAskingAgain(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	noJitter := func() float64 { return 0.5 } // multiplier 1.0
	reset := now.Add(90 * time.Second)

	for _, tc := range []struct {
		name      string
		in        error
		wantClass OutcomeClass
		wantCode  string
		// wantDelay is what the successor parks for; zero means this answer is
		// never retried at all, which only the halting permanent may be.
		wantDelay time.Duration
		// wantRelabelled says whether unchargedWhileUnfinished should have
		// CHANGED this code — its message marker is the only evidence of that,
		// and it must appear exactly when a code was replaced. It is what
		// separates this fix from the over-broad version that relabels every
		// transient it is handed, including ones already correct.
		wantRelabelled bool
	}{
		{
			name:      "a forge that failed in a way nobody has taught us to read",
			in:        errors.New("connection reset by peer"),
			wantClass: OutcomeTransient, wantCode: CodePublishResumable, wantDelay: 15 * time.Second,
			wantRelabelled: true,
		},
		{
			name: "the port's own non-start: a credential row that is gone, or a DEK that will not unwrap",
			in: OutcomeError{Outcome: Outcome{
				Class: OutcomeTransient, Code: CodePublishNotStarted,
				Message: "the publication could not be prepared: no such credential",
			}},
			wantClass: OutcomeTransient, wantCode: CodePublishResumable, wantDelay: 15 * time.Second,
			wantRelabelled: true,
		},
		{
			name: "a credential the forge itself rejected",
			in: OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodeAuthExpired,
				Message: "probe branch caesar/x: the forge rejected the credential",
			}},
			wantClass: OutcomePermanent, wantCode: CodeAuthExpired,
		},
		{
			name: "a rate limit that named its own reset",
			in: OutcomeError{Outcome: Outcome{
				Class: OutcomeTransient, Code: CodeForgeUnavailable,
				Message: "probe branch caesar/x: 429", RetryAt: &reset,
			}},
			wantClass: OutcomeTransient, wantCode: CodePublishResumable, wantDelay: 90 * time.Second,
			wantRelabelled: true,
		},
		{
			// Not a probe answer but the same chain: resumable() and
			// receiptFailure type the reads and writes AROUND the probes, and
			// their output passes through the same downgrade. It is already the
			// right code with the right schedule, so it must arrive untouched.
			name:      "an answer that is already resumable",
			in:        resumable("reload publication position: %v", errors.New("pool timeout")),
			wantClass: OutcomeTransient, wantCode: CodePublishResumable, wantDelay: 15 * time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The real chain a probe failure travels: the port types it,
			// probeFailure keeps or invents a class, and runPublish hands the
			// result to unchargedWhileUnfinished before evaluate sees it.
			out := ClassifyOutcome(unchargedWhileUnfinished(probeFailure("branch caesar/x", tc.in)))
			if out.Class != tc.wantClass || out.Code != tc.wantCode {
				t.Fatalf("outcome = %s/%s, want %s/%s", out.Class, out.Code, tc.wantClass, tc.wantCode)
			}
			if out.Chargeable() {
				t.Errorf("%s must not spend the failure budget of a publication that is still unfinished", out.Code)
			}
			// "while a publication is unfinished" is the relabel's own marker.
			if got := strings.Contains(out.Message, "while a publication is unfinished"); got != tc.wantRelabelled {
				t.Errorf("relabelled = %v, want %v (message %q): a code that was already right must "+
					"pass through untouched, and one that was replaced must say so", got, tc.wantRelabelled, out.Message)
			}
			if tc.wantDelay == 0 {
				if out.Retryable() {
					t.Fatalf("%s must not be retryable: it is the answer that halts", out.Code)
				}
				return
			}
			if d := retryDelay(out, now, 0, noJitter); d != tc.wantDelay {
				t.Errorf("the successor parks %v out, want %v: a publication that re-asks with no "+
					"delay hot-loops to the wall deadline on a cause no retry can fix", d, tc.wantDelay)
			}
		})
	}
}

// The same lie arriving from the PUSH rather than a probe, which is where it
// could always arrive: gitPublisher.PushPatch types its scratch and prepare
// failures with the same notStarted (runsports.go), and prepare opens the same
// credential. This leg is older than the probe pass-through and reaches
// unchargedWhileUnfinished by a different route, so it is asserted separately —
// a fix that lived in probeFailure alone would leave it at zero.
func TestPublish_APublisherThatNeverStartedStillWaitsWhileAnIntentIsLive(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	noJitter := func() float64 { return 0.5 }
	in := OutcomeError{Outcome: Outcome{
		Class: OutcomeTransient, Code: CodePublishNotStarted,
		Message: "the publication could not be prepared: open credential: cipher: message authentication failed",
	}}
	out := ClassifyOutcome(unchargedWhileUnfinished(in))
	if out.Class != OutcomeTransient || out.Code != CodePublishResumable || out.Chargeable() {
		t.Fatalf("outcome = %+v; want a transient, uncharged publish_resumable", out)
	}
	if d := retryDelay(out, now, 0, noJitter); d != 15*time.Second {
		t.Errorf("the successor parks %v out, want 15s", d)
	}
	if !strings.Contains(out.Message, "authentication failed") || !strings.Contains(out.Message, CodePublishNotStarted) {
		t.Errorf("message = %q; both the original code and the port's own words must survive, or an "+
			"operator cannot tell which pre-flight failed", out.Message)
	}
	// The honest non-start is the one that reaches evaluate WITHOUT passing
	// through here: authorizePublish's exit types itself with
	// notStartedUnlessTyped and returns directly, so a publication that truly
	// never began keeps its code and its immediate requeue.
	if got := ClassifyOutcome(notStartedUnlessTyped(errors.New("dial timeout"))); got.Code != CodePublishNotStarted {
		t.Fatalf("outcome = %+v; want publish_not_started to survive where nothing has begun", got)
	}
	if d := retryDelay(Outcome{Code: CodePublishNotStarted}, now, 0, noJitter); d != 0 {
		t.Errorf("a publication that never began parks %v out, want an immediate requeue", d)
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
