package httpx

import (
	"net/http"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// CheckOrigin is the CSRF belt over SameSite=Lax braces (spec §4): on
// unsafe methods, a present Origin header must equal the app origin.
// Absent Origin passes — non-browser clients don't send it, and Lax
// cookies already don't ride cross-site POSTs.
func CheckOrigin(appOrigin string) Middleware {
	unsafe := map[string]bool{
		http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if unsafe[r.Method] {
				if o := r.Header.Get("Origin"); o != "" && o != appOrigin {
					Problem(w, r, xerrors.E(xerrors.ClassForbidden, "cross-origin request rejected"))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
