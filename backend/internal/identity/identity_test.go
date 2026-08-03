package identity_test

import (
	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
)

var (
	_ authn.SessionAuthenticator = (*identity.Service)(nil)
	_ authn.Authorizer           = (*identity.Service)(nil)
)
