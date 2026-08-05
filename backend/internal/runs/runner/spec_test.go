package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/egress"
)

type fakeEngine struct {
	Engine  // embed nil; only override what the test needs — calls to the rest panic loudly
	inspect func(string) (dockerapi.ImageInspect, error)
	pull    func(string) error
	pulled  []string
}

func (f *fakeEngine) ImageInspect(_ context.Context, ref string) (dockerapi.ImageInspect, error) {
	return f.inspect(ref)
}

func (f *fakeEngine) ImagePull(_ context.Context, ref string) error {
	f.pulled = append(f.pulled, ref)
	if f.pull != nil {
		return f.pull(ref)
	}
	return nil
}

func newSpecRunner(t *testing.T, eng Engine) *Runner {
	t.Helper()
	r, err := New(eng, nil, nil, nil, nil, nil,
		Options{CaesarImage: "caesar:dev", GitImage: "caesar-git:dev"},
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}

func testRequest() runs.ExecutionRequest {
	return runs.ExecutionRequest{
		Ref:       testRef(),
		Kind:      runs.KindImplement,
		HarnessID: uuid.New(),
		Repo:      runs.RepoRef{Forge: "github", Slug: "giiorg/demo"},
		BaseSHA:   "abc",
	}
}

func TestBuildSpec_PinsDigestAndConstructsEnv(t *testing.T) {
	missOnce := true
	eng := &fakeEngine{inspect: func(string) (dockerapi.ImageInspect, error) {
		if missOnce { // absent locally: buildSpec pulls, then re-inspects
			missOnce = false
			return dockerapi.ImageInspect{}, errors.New("no such image")
		}
		return dockerapi.ImageInspect{RepoDigests: []string{"ghcr.io/x/claude@sha256:beef"}}, nil
	}}
	r := newSpecRunner(t, eng)
	spec, err := r.buildSpec(t.Context(), testRequest(),
		HarnessSpec{Kind: "claude-code", ImageRef: "ghcr.io/x/claude:latest", ProviderEndpoint: "https://api.anthropic.com"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ImageDigest != "ghcr.io/x/claude@sha256:beef" {
		t.Fatalf("digest = %s", spec.ImageDigest)
	}
	if len(eng.pulled) != 1 {
		t.Fatal("image should have been pulled once")
	}
	envMap := envToMap(spec.Env)
	for _, k := range []string{"CAESAR_ATTEMPT", "CAESAR_WORKSPACE", "CAESAR_BROKER_URL", "CAESAR_BROKER_TOKEN", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "HOME", "PATH"} {
		if _, ok := envMap[k]; !ok {
			t.Errorf("env missing %s", k)
		}
	}
	if len(spec.Env) != 9 {
		t.Fatalf("env allowlist must be EXACTLY the pinned nine, got %v", spec.Env)
	}
	if spec.CredentialHeader != "x-api-key" || spec.CredentialPrefix != "" {
		t.Fatal("claude-code credential profile wrong")
	}
}

func TestBuildSpec_EnvCarriesTheAttemptIdentityAndBrokerReachability(t *testing.T) {
	eng := &fakeEngine{inspect: func(string) (dockerapi.ImageInspect, error) {
		return dockerapi.ImageInspect{RepoDigests: []string{"ghcr.io/x/claude@sha256:beef"}}, nil
	}}
	r := newSpecRunner(t, eng)
	req := testRequest()
	spec, err := r.buildSpec(t.Context(), req, HarnessSpec{Kind: "claude-code", ImageRef: "ghcr.io/x/claude:latest"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"CAESAR_ATTEMPT":      req.Ref.String(),
		"CAESAR_WORKSPACE":    "/workspace",
		"CAESAR_BROKER_URL":   "http://egress:8080",
		"CAESAR_BROKER_TOKEN": spec.BrokerToken,
		"HTTPS_PROXY":         "http://egress:8080",
		"HTTP_PROXY":          "http://egress:8080",
		"NO_PROXY":            "",
		"HOME":                "/tmp",
		"PATH":                "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	if diff := cmp.Diff(want, envToMap(spec.Env)); diff != "" {
		t.Errorf("env mismatch (-want +got):\n%s", diff)
	}
	if spec.BrokerToken == "" || spec.BrokerTokenSHA == "" {
		t.Error("spec must carry both the token and its digest")
	}
	if !strings.Contains(strings.Join(spec.Env, " "), spec.BrokerToken) {
		t.Error("the agent reaches the broker with the token, so it must be in its env")
	}
	if strings.Contains(strings.Join(spec.Env, " "), spec.BrokerTokenSHA) {
		t.Error("the digest is the gateway's half and must never reach the agent")
	}
}

func TestBuildSpec_AlreadyDigestPinnedSkipsPull(t *testing.T) {
	const ref = "ghcr.io/x/claude@sha256:beef"
	eng := &fakeEngine{inspect: func(string) (dockerapi.ImageInspect, error) {
		return dockerapi.ImageInspect{RepoDigests: []string{"ghcr.io/x/other@sha256:cafe"}}, nil
	}}
	r := newSpecRunner(t, eng)
	spec, err := r.buildSpec(t.Context(), testRequest(), HarnessSpec{Kind: "codex", ImageRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	if len(eng.pulled) != 0 {
		t.Errorf("a locally present image must not be pulled, pulled = %v", eng.pulled)
	}
	if spec.ImageDigest != ref {
		t.Errorf("digest = %s, want the already-pinned ref %s", spec.ImageDigest, ref)
	}
}

func TestBuildSpec_LocallyBuiltImageFallsBackToItsRef(t *testing.T) {
	eng := &fakeEngine{inspect: func(string) (dockerapi.ImageInspect, error) {
		return dockerapi.ImageInspect{ID: "sha256:local"}, nil // never pushed: no repo digest
	}}
	r := newSpecRunner(t, eng)
	spec, err := r.buildSpec(t.Context(), testRequest(), HarnessSpec{Kind: "claude-code", ImageRef: "caesar-harness:dev"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ImageDigest != "caesar-harness:dev" {
		t.Errorf("digest = %s, want the ref itself", spec.ImageDigest)
	}
}

func TestBuildSpec_PullFailureClassifiesImageUnavailable(t *testing.T) {
	cases := []struct {
		name string
		pull func(string) error
	}{
		{"pull fails", func(string) error { return errors.New("registry unreachable") }},
		{"pull succeeds but the image is still absent", func(string) error { return nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng := &fakeEngine{
				inspect: func(string) (dockerapi.ImageInspect, error) {
					return dockerapi.ImageInspect{}, errors.New("no such image")
				},
				pull: c.pull,
			}
			r := newSpecRunner(t, eng)
			_, err := r.buildSpec(t.Context(), testRequest(), HarnessSpec{Kind: "claude-code", ImageRef: "ghcr.io/x/claude:latest"})
			if err == nil {
				t.Fatal("want an error when the image cannot be resolved")
			}
			got := runs.ClassifyOutcome(err)
			if got.Class != runs.OutcomeTransient || got.Code != runs.CodeImageUnavailable {
				t.Errorf("outcome = %+v, want transient/%s", got, runs.CodeImageUnavailable)
			}
		})
	}
}

func TestBuildSpec_UnknownHarnessKindRejected(t *testing.T) {
	eng := &fakeEngine{inspect: func(string) (dockerapi.ImageInspect, error) {
		return dockerapi.ImageInspect{RepoDigests: []string{"x@sha256:beef"}}, nil
	}}
	r := newSpecRunner(t, eng)
	if _, err := r.buildSpec(t.Context(), testRequest(), HarnessSpec{Kind: "mystery", ImageRef: "x:1"}); err == nil {
		t.Fatal("want rejection of a harness kind with no pinned provider profile")
	}
}

func TestProviderProfile_PinnedPerKind(t *testing.T) {
	cases := []struct {
		kind       string
		wantHeader string
		wantPrefix string
		wantOps    []egress.Op
	}{
		{"claude-code", "x-api-key", "", []egress.Op{
			{Method: "POST", PathPrefix: "/v1/messages"},
			{Method: "POST", PathPrefix: "/v1/messages/count_tokens"},
			{Method: "GET", PathPrefix: "/v1/models"},
		}},
		{"codex", "authorization", "Bearer ", []egress.Op{
			{Method: "POST", PathPrefix: "/v1/responses"},
			{Method: "POST", PathPrefix: "/v1/chat/completions"},
			{Method: "GET", PathPrefix: "/v1/models"},
		}},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			header, prefix, ops, err := providerProfile(c.kind)
			if err != nil {
				t.Fatal(err)
			}
			if header != c.wantHeader || prefix != c.wantPrefix {
				t.Errorf("credential = %q/%q, want %q/%q", header, prefix, c.wantHeader, c.wantPrefix)
			}
			if diff := cmp.Diff(c.wantOps, ops); diff != "" {
				t.Errorf("ops mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestProviderProfile_UnknownKindRejected(t *testing.T) {
	if _, _, _, err := providerProfile("mystery"); err == nil {
		t.Fatal("an unlisted kind has no allowlist, so it must be refused")
	}
}
