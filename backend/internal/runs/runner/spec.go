package runner

import (
	"context"
	"fmt"
	"slices"
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
	// Custody is how this attempt's credential reaches the harness, derived
	// from the (harness kind, credential kind) pair by the driver table. It
	// decides both halves of the pipeline: what the agent's env carries, and
	// whether the gateway holds a provider credential at all.
	Custody custody
	// BrokerToken goes to the agent; only BrokerTokenSHA goes to the
	// gateway, so the gateway's config leaks nothing replayable. Both are
	// empty under materialized custody: there is nobody to authenticate,
	// because the gateway brokers nothing.
	BrokerToken    string
	BrokerTokenSHA string
	// ProviderCredential is the opened provider secret. It is filled in by
	// Execute (buildSpec never touches the credential store) for both
	// custodies — redaction needs it either way — but it is handed to the
	// gateway only when brokered. Nothing may log a RunSpec.
	ProviderCredential string
	BaseSHA            string
	Deadline           time.Time
}

// buildSpec resolves everything an attempt needs before anything starts:
// unknown harness kinds and unavailable images fail here, where the failure
// is still cheap and typed.
func (r *Runner) buildSpec(ctx context.Context, req runs.ExecutionRequest, hs HarnessSpec) (RunSpec, error) {
	d, err := driverFor(hs.Kind)
	if err != nil {
		return RunSpec{}, permanentf(runs.CodeHarnessError, "%v", err)
	}
	cust, err := d.custodyOf(req.Credential.Kind)
	if err != nil {
		return RunSpec{}, permanentf(runs.CodeHarnessError, "%v", err)
	}
	digest, err := r.resolveImage(ctx, hs.ImageRef)
	if err != nil {
		return RunSpec{}, err
	}
	spec := RunSpec{
		Ref:         req.Ref,
		ImageDigest: digest,
		Harness:     hs,
		User:        r.runAs(),
		Custody:     cust,
		// The pinned allowlist: the agent sees exactly these, plus whatever
		// its driver adds below. Every addition is explicit in the driver
		// table — nothing here is inherited from the control plane.
		Env: []string{
			"CAESAR_ATTEMPT=" + req.Ref.String(),
			"CAESAR_WORKSPACE=" + workspaceDir,
			"HTTPS_PROXY=" + brokerURL,
			"HTTP_PROXY=" + brokerURL,
			"NO_PROXY=",
			"HOME=" + agentHome,
			"PATH=" + agentPath,
		},
		BaseSHA:  req.BaseSHA,
		Deadline: req.Deadline,
	}
	// Only a brokered attempt has anything to authenticate: the agent presents
	// this token and the gateway swaps it for the provider credential. A
	// materialized attempt's gateway brokers nothing, so minting a token for
	// it would be one more secret in the agent's environment for no reason.
	if cust == custodyBrokered {
		token, tokenSHA, err := newBrokerToken()
		if err != nil {
			return RunSpec{}, err
		}
		spec.BrokerToken, spec.BrokerTokenSHA = token, tokenSHA
		spec.Env = append(spec.Env,
			"CAESAR_BROKER_URL="+brokerURL,
			"CAESAR_BROKER_TOKEN="+token)
		spec.Env = append(spec.Env, d.env(envInput{custody: custodyBrokered, brokerToken: token})...)
	}
	// The materialized half of the env needs the secret, which buildSpec never
	// touches; Execute appends it once the credential is open.
	return spec, nil
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

// gatewayConfig picks the gateway's shape from custody (spec §3). Materialized
// is the open-CONNECT configuration the validation gateway already runs: empty
// token digest, zero Provider — the gateway holds nothing worth stealing, and
// the allowlist is the sole barrier. Brokered keeps the token-authenticated
// header injection unchanged. Both get the driver's FQDNs ∪ the operator's
// configured list; for a brokered attempt the driver's hosts are reachable
// over CONNECT too, which is harmless — the agent holds no provider credential
// to spend there — and one composition point beats two shapes.
func (r *Runner) gatewayConfig(spec RunSpec, d driver) egress.Config {
	allow := append(slices.Clone(d.connectAllow), r.opts.ConnectAllow...)
	cfg := egress.Config{Attempt: spec.Ref.String(), ConnectAllow: allow}
	if spec.Custody == custodyBrokered {
		cfg.TokenSHA256 = spec.BrokerTokenSHA
		cfg.Provider = egress.Provider{
			Endpoint:         spec.Harness.ProviderEndpoint,
			Credential:       spec.ProviderCredential,
			CredentialHeader: d.brokered.header,
			CredentialPrefix: d.brokered.prefix,
			Ops:              d.brokered.ops,
		}
	}
	return cfg
}
