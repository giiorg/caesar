package egress

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLoadConfig_AppliesDefaultsAndKeepsWhatIsGiven(t *testing.T) {
	got, err := LoadConfig(`{
		"attempt": "caesar-018f-implement-1",
		"token_sha256": "` + shaOf(tok) + `",
		"provider": {
			"endpoint": "https://api.anthropic.com",
			"credential": "sk-ant-secret",
			"credential_header": "x-api-key",
			"credential_prefix": "",
			"ops": [{"method": "POST", "path_prefix": "/v1/messages"}]
		},
		"connect_allow": ["registry.npmjs.org"]
	}`)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := Config{
		Attempt:     "caesar-018f-implement-1",
		ListenAddr:  ":8080",
		TokenSHA256: shaOf(tok),
		Provider: Provider{
			Endpoint: "https://api.anthropic.com", Credential: "sk-ant-secret", CredentialHeader: "x-api-key",
			Ops: []Op{{Method: "POST", PathPrefix: "/v1/messages"}},
		},
		ConnectAllow: []string{"registry.npmjs.org"},
		MeterSeconds: 30,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("config mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadConfig_RoundTripsAMarshalledConfig(t *testing.T) {
	// The runner configures a gateway container with exactly json.Marshal of
	// this struct, so the tags and the strict decoder have to agree.
	want := Config{
		Attempt: "caesar-018f-implement-1", ListenAddr: ":8080", TokenSHA256: shaOf(tok),
		Provider: Provider{
			Endpoint: "https://api.openai.com", Credential: "sk-secret", CredentialHeader: "authorization",
			CredentialPrefix: "Bearer ", Ops: []Op{{Method: "POST", PathPrefix: "/v1/responses"}},
		},
		ConnectAllow: []string{"registry.npmjs.org"}, MeterSeconds: 30,
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(string(raw))
	if err != nil {
		t.Fatalf("LoadConfig(%s): %v", raw, err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("round trip mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadConfig_TreatsExplicitZerosAsAbsent(t *testing.T) {
	got, err := LoadConfig(`{"attempt": "a", "listen_addr": "", "meter_seconds": 0}`)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.ListenAddr != ":8080" || got.MeterSeconds != 30 {
		t.Fatalf("config = %+v, want the defaults", got)
	}
}

func TestLoadConfig_AcceptsTheOpenConnectPosture(t *testing.T) {
	got, err := LoadConfig(`{"attempt": "a", "connect_allow": ["registry.npmjs.org"], "listen_addr": "127.0.0.1:9000", "meter_seconds": 5}`)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.TokenSHA256 != "" || got.Provider.Endpoint != "" {
		t.Fatalf("config = %+v, want the credential-free posture", got)
	}
	if got.ListenAddr != "127.0.0.1:9000" || got.MeterSeconds != 5 {
		t.Errorf("config = %+v, want the given listen addr and meter interval", got)
	}
}

func TestLoadConfig_RejectsMalformedConfigs(t *testing.T) {
	cases := []struct {
		name, raw, wantErr string
	}{
		{"empty", ``, "parse"},
		{"not an object", `[]`, "parse"},
		{"unknown field", `{"attempt": "a", "listen_port": 8080}`, "listen_port"},
		{"unknown provider field", `{"attempt": "a", "provider": {"api_key": "x"}}`, "api_key"},
		{"trailing content", `{"attempt": "a"} {"attempt": "b"}`, "parse"},
		{"no attempt", `{"connect_allow": []}`, "attempt"},
		{"token digest too short", `{"attempt": "a", "token_sha256": "abcd"}`, "token_sha256"},
		{"token digest is not hex", `{"attempt": "a", "token_sha256": "` + strings.Repeat("z", 64) + `"}`, "token_sha256"},
		{"endpoint is not a url", `{"attempt": "a", "token_sha256": "` + shaOf(tok) + `", "provider": {"endpoint": "::nope", "credential_header": "x-api-key"}}`, "endpoint"},
		{"endpoint is not http", `{"attempt": "a", "token_sha256": "` + shaOf(tok) + `", "provider": {"endpoint": "ftp://api.example", "credential_header": "x-api-key"}}`, "endpoint"},
		{"endpoint without a host", `{"attempt": "a", "token_sha256": "` + shaOf(tok) + `", "provider": {"endpoint": "https://", "credential_header": "x-api-key"}}`, "endpoint"},
		{"provider without a credential header", `{"attempt": "a", "token_sha256": "` + shaOf(tok) + `", "provider": {"endpoint": "https://api.example"}}`, "credential_header"},
		{"an op without a rooted prefix would not be an allowlist", `{"attempt": "a", "token_sha256": "` + shaOf(tok) + `", "provider": {"endpoint": "https://api.example", "credential_header": "x-api-key", "ops": [{"method": "POST", "path_prefix": "v1/messages"}]}}`, "provider.ops[0]"},
		{"an op without a method", `{"attempt": "a", "token_sha256": "` + shaOf(tok) + `", "provider": {"endpoint": "https://api.example", "credential_header": "x-api-key", "ops": [{"path_prefix": "/v1/messages"}]}}`, "provider.ops[0]"},
		{"negative meter interval", `{"attempt": "a", "meter_seconds": -1}`, "meter_seconds"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadConfig(c.raw)
			if err == nil {
				t.Fatalf("LoadConfig(%s) = nil error, want one naming %q", c.raw, c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error = %v, want it to name %q", err, c.wantErr)
			}
		})
	}
}

func TestLoadConfig_NeverEchoesTheCredentialInAnError(t *testing.T) {
	const credential = "sk-ant-secret-value"
	_, err := LoadConfig(`{"attempt": "", "token_sha256": "` + shaOf(tok) + `", "provider": {"endpoint": "ftp://api.example", "credential": "` + credential + `", "credential_header": "x-api-key"}}`)
	if err == nil {
		t.Fatal("LoadConfig accepted an invalid config")
	}
	if strings.Contains(err.Error(), credential) {
		t.Fatalf("error leaks the credential: %v", err)
	}
}
