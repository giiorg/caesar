package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/egress"
)

// The agent's world, pinned: the workspace is the only path it shares with
// the control plane, and the gateway is the only host it can name.
const (
	workspaceDir = "/workspace"
	// gatewayAlias is the gateway's DNS name on the attempt network. The
	// broker URL is built from it so the alias and the address the agent is
	// handed can never drift apart.
	gatewayAlias = "egress"
	brokerURL    = "http://" + gatewayAlias + ":8080"
	agentHome    = "/tmp" // the only writable path on a read-only rootfs
	agentPath    = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// RunSpec is one attempt resolved down to facts: the image it will actually
// run, the environment it will see, and the credentials the gateway holds on
// its behalf. Everything is computed before a container exists, so no step of
// the pipeline reads ambient state.
type RunSpec struct {
	Ref         runs.AttemptRef
	ImageDigest string // registry digest when the image has one; a locally built dev image was never pushed and carries none, so this falls back to the ref
	Harness     HarnessSpec
	User        string   // "<uid>:<gid>" of this process
	Env         []string // the pinned allowlist — never inherited from the control plane
	// BrokerToken goes to the agent; only BrokerTokenSHA goes to the
	// gateway, so the gateway's config leaks nothing replayable.
	BrokerToken                        string
	BrokerTokenSHA                     string
	ProviderOps                        []egress.Op
	CredentialHeader, CredentialPrefix string
	// ProviderCredential is the opened provider secret. It is filled in by
	// Execute (buildSpec never touches the credential store) and travels
	// exactly one place: the gateway container's environment. Nothing may
	// log a RunSpec.
	ProviderCredential string
	BaseSHA            string
	Deadline           time.Time
}

// buildSpec resolves everything an attempt needs before anything starts:
// unknown harness kinds and unavailable images fail here, where the failure
// is still cheap and typed.
func (r *Runner) buildSpec(ctx context.Context, req runs.ExecutionRequest, hs HarnessSpec) (RunSpec, error) {
	header, prefix, ops, err := providerProfile(hs.Kind)
	if err != nil {
		return RunSpec{}, err
	}
	digest, err := r.resolveImage(ctx, hs.ImageRef)
	if err != nil {
		return RunSpec{}, err
	}
	token, tokenSHA, err := newBrokerToken()
	if err != nil {
		return RunSpec{}, err
	}
	return RunSpec{
		Ref:         req.Ref,
		ImageDigest: digest,
		Harness:     hs,
		User:        r.runAs(),
		Env: []string{
			"CAESAR_ATTEMPT=" + req.Ref.String(),
			"CAESAR_WORKSPACE=" + workspaceDir,
			"CAESAR_BROKER_URL=" + brokerURL,
			"CAESAR_BROKER_TOKEN=" + token,
			"HTTPS_PROXY=" + brokerURL,
			"HTTP_PROXY=" + brokerURL,
			"NO_PROXY=",
			"HOME=" + agentHome,
			"PATH=" + agentPath,
		},
		BrokerToken:      token,
		BrokerTokenSHA:   tokenSHA,
		ProviderOps:      ops,
		CredentialHeader: header,
		CredentialPrefix: prefix,
		BaseSHA:          req.BaseSHA,
		Deadline:         req.Deadline,
	}, nil
}

// resolveImage pins the image an attempt runs. Any inspect error is treated
// as "not here yet": pull once, then inspect again. That keeps the code free
// of daemon-error-shape guessing — a genuinely broken daemon surfaces on the
// second inspect, classified the same way.
func (r *Runner) resolveImage(ctx context.Context, ref string) (string, error) {
	info, err := r.engine.ImageInspect(ctx, ref)
	if err != nil {
		if pullErr := r.engine.ImagePull(ctx, ref); pullErr != nil {
			return "", imageUnavailable(ref, pullErr)
		}
		if info, err = r.engine.ImageInspect(ctx, ref); err != nil {
			return "", imageUnavailable(ref, err)
		}
	}
	if strings.Contains(ref, "@sha256:") {
		return ref, nil
	}
	if len(info.RepoDigests) > 0 {
		return info.RepoDigests[0], nil
	}
	return ref, nil // built locally, never pushed: the ref is all the identity there is
}

func imageUnavailable(ref string, err error) error {
	return runs.OutcomeError{Outcome: runs.Outcome{
		Class:   runs.OutcomeTransient,
		Code:    runs.CodeImageUnavailable,
		Message: fmt.Sprintf("image %s unavailable: %v", ref, err),
	}}
}

// providerProfile is the pinned per-harness allowlist: which header carries
// the credential and which operations the broker will forward. An unlisted
// kind has no allowlist, so it is refused rather than defaulted — a default
// here would be an open proxy onto a provider account.
func providerProfile(kind string) (header, prefix string, ops []egress.Op, err error) {
	switch kind {
	case "claude-code":
		return "x-api-key", "", []egress.Op{
			{Method: "POST", PathPrefix: "/v1/messages"},
			{Method: "POST", PathPrefix: "/v1/messages/count_tokens"},
			{Method: "GET", PathPrefix: "/v1/models"},
		}, nil
	case "codex":
		return "authorization", "Bearer ", []egress.Op{
			{Method: "POST", PathPrefix: "/v1/responses"},
			{Method: "POST", PathPrefix: "/v1/chat/completions"},
			{Method: "GET", PathPrefix: "/v1/models"},
		}, nil
	}
	return "", "", nil, fmt.Errorf("runner: unknown harness kind %q", kind)
}
