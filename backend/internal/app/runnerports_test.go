package app

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestCloneURL_BuildsHostedURLsAndRejectsUnknownForge(t *testing.T) {
	tests := []struct {
		name    string
		forge   string
		slug    string
		want    string
		wantErr bool
	}{
		{name: "github", forge: "github", slug: "acme/widgets", want: "https://github.com/acme/widgets.git"},
		{name: "gitlab", forge: "gitlab", slug: "acme/widgets", want: "https://gitlab.com/acme/widgets.git"},
		{name: "unknown forge errors", forge: "bitbucket", slug: "acme/widgets", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cloneURL(tt.forge, tt.slug)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("cloneURL(%q, %q) = %q, want error", tt.forge, tt.slug, got)
				}
				if !strings.Contains(err.Error(), tt.forge) {
					t.Errorf("error %v should name the offending forge %q", err, tt.forge)
				}
				return
			}
			if err != nil {
				t.Fatalf("cloneURL(%q, %q): %v", tt.forge, tt.slug, err)
			}
			if got != tt.want {
				t.Errorf("cloneURL(%q, %q) = %q, want %q", tt.forge, tt.slug, got, tt.want)
			}
		})
	}
}

func TestCloneAuthHeader_IsBasicXAccessToken(t *testing.T) {
	const token = "ghs_exampletoken123"
	want := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	if got := cloneAuthHeader(token); got != want {
		t.Errorf("cloneAuthHeader(%q) = %q, want %q", token, got, want)
	}
}
