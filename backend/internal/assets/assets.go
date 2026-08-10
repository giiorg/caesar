// Package assets is the assets bounded context (spec §2): projects, repos,
// encrypted credentials, and the harness registry. Peers never import it
// except through app wiring; it owns its transactions end to end.
package assets

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

type ForgeKind string

const (
	ForgeGitHub ForgeKind = "github"
	ForgeGitLab ForgeKind = "gitlab"
)

func (f ForgeKind) Valid() bool { return f == ForgeGitHub || f == ForgeGitLab }

type CredentialKind string

const (
	CredentialForgeToken        CredentialKind = "forge_token"
	CredentialModelKey          CredentialKind = "model_key"
	CredentialSubscriptionToken CredentialKind = "subscription_token"
	CredentialAgentAuthFile     CredentialKind = "agent_auth_file" //nolint:gosec // G101 matches the name: this is a kind label, not a secret
)

func (k CredentialKind) Valid() bool {
	switch k {
	case CredentialForgeToken, CredentialModelKey, CredentialSubscriptionToken, CredentialAgentAuthFile:
		return true
	}
	return false
}

// credentialKinds is the operator-facing list, used in validation
// messages so a rejected kind names every accepted one.
var credentialKinds = []CredentialKind{
	CredentialForgeToken, CredentialModelKey, CredentialSubscriptionToken, CredentialAgentAuthFile,
}

// joinKinds renders a kind set for a validation message.
func joinKinds(kinds []CredentialKind) string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}

type HarnessKind string

const (
	HarnessClaudeCode HarnessKind = "claude-code"
	HarnessCodex      HarnessKind = "codex"
)

func (k HarnessKind) Valid() bool { return k == HarnessClaudeCode || k == HarnessCodex }

// Project groups repos, policies, and grants (spec §2).
type Project struct {
	ID          uuid.UUID
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Repo is one forge repository inside a project.
type Repo struct {
	ID            uuid.UUID
	ProjectID     uuid.UUID
	Forge         ForgeKind
	Slug          string
	DefaultBranch string
	CredentialID  uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Credential is the METADATA view of an encrypted secret — plaintext
// never rides this struct; use-time decryption goes through UseCredential.
// SecretVersion counts operator-supplied secrets: it advances on every
// rotation and lets a consumer prove which bytes it was handed. The two
// stamps are the freshness view (spec §5) — a subscription credential
// that was last used long after it last died is healthy, one that died
// after its last use needs re-import.
type Credential struct {
	ID                uuid.UUID
	Name              string
	Kind              CredentialKind
	KeyID             string
	SecretVersion     int64
	LastUsedAt        *time.Time
	LastAuthExpiredAt *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Harness is one admin-registered agent harness (spec §2).
type Harness struct {
	ID               uuid.UUID
	Name             string
	Kind             HarnessKind
	ImageRef         string
	ProviderEndpoint string
	CredentialID     uuid.UUID
	Defaults         map[string]any
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Validation pins (plan header). BYTE counts, the house convention — every
// check below is a `len()`, the columns behind them are unconstrained `text`,
// and the field errors say "bytes" so a caller counting graphemes is not
// surprised by a name it thinks is 60 characters long.
const (
	maxNameLen        = 100
	maxDescriptionLen = 500
	maxSlugLen        = 200
	maxBranchLen      = 200
	maxImageRefLen    = 300
	maxSecretLen      = 4096
	maxAuthFileLen    = 64 << 10
	maxDefaultsLen    = 4096
)

var slugPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+$`)

func validateName(field, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxNameLen {
		return "", xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: field, Message: fmt.Sprintf("must be 1-%d bytes", maxNameLen),
		})
	}
	return name, nil
}

func validateDescription(desc string) error {
	if len(desc) > maxDescriptionLen {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "description", Message: fmt.Sprintf("must be at most %d bytes", maxDescriptionLen),
		})
	}
	return nil
}
