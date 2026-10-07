package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/ramonskie/groovearr/internal/user"
)

// errForbidden is the sentinel returned by adminOnly for callers that are not
// admins. Its message is the stable wire value for the 403 body.
var errForbidden = errors.New("forbidden")

// Identity is the authenticated caller resolved by withAuth and carried on the
// request context for the rest of the handler chain.
//
// It is the single source of caller identity and role for authorization
// decisions (see adminOnly). ViaAPIKey records that the request
// authenticated with the global API key rather than a session — the key
// itself is never stored or exposed, only whether the request used it.
type Identity struct {
	UserID    int64
	Username  string
	Role      user.Role
	ViaAPIKey bool
}

// identityKey is the unexported context key type for Identity values. Using an
// unexported struct type prevents collisions with keys defined in other
// packages (Go context keys compare by type and value).
type identityKey struct{}

// contextWithIdentity returns a copy of ctx carrying id.
func contextWithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// identityFrom returns the Identity stored in ctx and whether one was present.
// A missing identity yields the zero Identity and false, so callers can
// distinguish "no caller" from an authenticated admin.
func identityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// requesterID returns the authenticated caller's user id for DB-only
// attribution, or 0 when no identity is present. 0 means system/unknown:
// API-key requests, auth.method="none", and background/system callers all
// carry no user id. This grants no authorization — it only labels who queued.
func requesterID(ctx context.Context) int64 {
	id, _ := identityFrom(ctx)
	return id.UserID
}

// requesterUsername returns the authenticated caller's username for the
// DB-only attribution snapshot taken at queue time, or "" when no identity is
// present. Paired with requesterID; grants no authorization.
func requesterUsername(ctx context.Context) string {
	id, _ := identityFrom(ctx)
	return id.Username
}

// adminOnly gates h to admins. It reads the Identity injected by withAuth and
// rejects anything that is not an admin with 403 {"error":"forbidden"}.
//
// A missing identity is treated as forbidden too, so a route wrapped without
// withAuth fails closed instead of allowing access. Authentication is
// withAuth's job; this is authorization only, and it performs no credential
// checks of its own.
func (s *Server) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := identityFrom(r.Context())
		if !ok || id.Role != user.RoleAdmin {
			writeError(w, http.StatusForbidden, errForbidden)
			return
		}
		h(w, r)
	}
}
