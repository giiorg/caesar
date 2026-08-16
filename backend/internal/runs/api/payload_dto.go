package api

// The five types below exist to be REFLECTED. No handler constructs one — an
// artifact payload travels as ArtifactPayloadResponse.Payload, a map, because
// the same endpoint serves patch and log_ref payloads that have no Go shape
// here. What these declare is the CONTRACT the client renders against and the
// runner validates to, and contracts/payload_schema_test.go is what stops the
// two descriptions drifting. They reach the document through
// internal/contracts' extraSchemas list; nothing else references them, so an
// "unused type" cleanup would silently delete the published contract.

// PlanPayload is a `plan` artifact's payload at document schema 2.
type PlanPayload struct {
	Summary string     `json:"summary" required:"true"`
	Steps   []PlanStep `json:"steps" required:"true"`
}

type PlanStep struct {
	Title string `json:"title" required:"true"`
	// Detail is optional. Absent and empty mean the same thing and the renderer
	// treats them the same; there is no third state worth a null here.
	Detail string `json:"detail"`
}

// ReviewVerdictPayload is a `review_verdict` artifact's payload at schema 2.
type ReviewVerdictPayload struct {
	// One of approve | request_changes | reject — the same vocabulary a human
	// gate decision uses, so a reader is not learning two.
	Verdict  string          `json:"verdict" required:"true"`
	Findings []ReviewFinding `json:"findings" required:"true"`
	// Tests is required BY THE VALIDATOR and nullable: null is "this harness
	// did not report test results", which the approval surface renders as
	// unknown rather than as a tick. A stub never fakes success.
	//
	// It is deliberately NOT marked required HERE, and the two statements are
	// not in conflict — the second is the only shape this toolchain can
	// express. Its schema is a $ref, and this generator gives a $ref property
	// no null branch (jsonschema-go envelops nullability only when the
	// reflector asks it to; contracts.go builds a bare one). `required:"true"`
	// would render `tests: ApiTestOutcome` in TypeScript — not optional, not
	// nullable — on the one key whose whole purpose is that null is a legal,
	// meaningful value. Optional is the honest half-truth, because it makes the
	// client narrow before it reads. The same trade is recorded on
	// PhaseResponse.Evidence and RunResponse.Outcome, and
	// contracts/payload_schema_test.go pins this exception by name so a future
	// `required:"true"` here fails loudly rather than quietly lying.
	Tests *TestOutcome `json:"tests"`
}

type ReviewFinding struct {
	// One of info | minor | major | blocker.
	Severity string `json:"severity" required:"true"`
	Message  string `json:"message" required:"true"`
	// File and Line are empty/zero when the finding is not file- or
	// line-scoped, which is ordinary for a design comment. Both are scalars,
	// so the $ref caveat above does not reach them — they are simply optional.
	File string `json:"file"`
	Line int32  `json:"line"`
}

type TestOutcome struct {
	// One of passed | failed | not_run. `not_run` is a harness saying it did
	// not run tests; a null TestOutcome is a harness saying nothing at all.
	Status  string `json:"status" required:"true"`
	Passed  int32  `json:"passed"`
	Failed  int32  `json:"failed"`
	Details string `json:"details"`
}
