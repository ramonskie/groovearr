package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/user"
)

// minPasswordLength is the minimum accepted password length for account
// creation and password changes. Deliberately small and explicit so the
// boundary is easy to audit and test; it lives here, not in the store, because
// it is an input-validation rule (AGENTS §4 validate at the boundary).
const minPasswordLength = 8

// maxPasswordBytes is bcrypt's hard input limit. bcrypt.GenerateFromPassword
// returns an error for input longer than 72 bytes, so an over-long password
// must be rejected at the boundary as a client error (400) instead of falling
// through as a confusing 500. It is a byte limit, not a rune limit, because
// that is the unit bcrypt enforces.
const maxPasswordBytes = 72

// validatePassword enforces both account-password boundaries at the API edge.
// The minimum is counted in runes so a multibyte password is judged by how many
// characters the user typed, not how many bytes it occupies; the maximum is
// counted in bytes to match bcrypt's limit.
func validatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < minPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}
	if len(pw) > maxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes", maxPasswordBytes)
	}
	return nil
}

// userResponse is the explicit wire shape for an account. It is a dedicated
// DTO rather than user.User so the bcrypt hash can never be serialized: the
// field simply does not exist here. User.PasswordHash's json:"-" tag is the
// second layer, not the only one (security-patterns: never expose secrets).
type userResponse struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Role      user.Role `json:"role"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// newUserResponse projects a stored account onto the safe wire shape.
func newUserResponse(u user.User) userResponse {
	return userResponse{
		ID:        u.ID,
		Username:  u.Username,
		Role:      u.Role,
		Disabled:  u.Disabled,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

// createUserRequest is the POST /api/users body.
type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// updateUserRequest is the PATCH /api/users/{id} body. Pointer fields
// distinguish an omitted field from its zero value, so a partial update never
// clears a field the caller did not mention (e.g. disabled=false).
type updateUserRequest struct {
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
	Password *string `json:"password"`
}

// validRole reports whether r is one of the supported roles. Callers resolve
// the documented "empty means user" default before calling it.
func validRole(r user.Role) bool {
	return r == user.RoleAdmin || r == user.RoleUser
}

// parseUserID parses a positive numeric path ID, returning a boundary
// validation error otherwise.
func parseUserID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid user id %q", raw)
	}
	return id, nil
}

// requireUserStore reports whether a user store is wired, writing a 500 and
// returning false when it is not. A nil store is a server misconfiguration,
// not a client error, so it fails closed rather than dereferencing nil
// (mirrors handleLogin).
func (s *Server) requireUserStore(w http.ResponseWriter) bool {
	if s.userStore == nil {
		s.log.Error("users: request without a user store", "component", "api")
		writeError(w, http.StatusInternalServerError, errors.New("user store unavailable"))
		return false
	}
	return true
}

// invalidateUserSessions drops every session belonging to userID after a
// security-relevant change, and terminates that user's live SSE stream. The
// in-memory session carries a role copy, so a demoted or disabled account
// would otherwise keep acting with its old role until expiry; a password
// change likewise retires sessions issued under the old credential. The SSE
// stream is closed too because it resolves the caller's role once at connect
// and would otherwise keep delivering admin-only events to a demoted user.
// A nil store (tests) is a no-op.
//
// Decision — self password change logs the user out (plan Phase 5.1). The only
// password-change path is PATCH /api/users/{id}, which is admin-only; there is
// no separate self-service endpoint. When an admin targets their own account
// the change is security-relevant like any other, so their own live session is
// dropped too and the next request 401s. We deliberately do NOT re-issue a
// fresh session: a deliberate credential change is exactly the moment to force
// re-authentication with the new password as confirmation it took effect. The
// wire response is unchanged (a userResponse), so the SPA simply sees the next
// request as 401 and returns to the login screen.
func (s *Server) invalidateUserSessions(userID int64) {
	if s.sessions != nil {
		s.sessions.DeleteByUserID(userID)
	}
	if s.sseHub != nil {
		s.sseHub.UnregisterByUserID(userID)
	}
}

// writeUserMutationError maps the user store's exported sentinels to HTTP
// statuses (AGENTS §10). It never echoes internal DB text to the client.
//
// Status choices:
//   - ErrUserNotFound       -> 404
//   - ErrLastAdmin          -> 409 (well-formed request, invariant conflict)
//   - ErrDuplicateUsername  -> 409
//   - anything else         -> 500 "internal error"
func (s *Server) writeUserMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, user.ErrUserNotFound):
		writeError(w, http.StatusNotFound, errors.New("user not found"))
	case errors.Is(err, user.ErrLastAdmin):
		writeError(w, http.StatusConflict, errors.New("cannot remove the last active admin"))
	case errors.Is(err, user.ErrDuplicateUsername):
		writeError(w, http.StatusConflict, errors.New("username already exists"))
	default:
		s.log.Error("users: mutation failed", "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
	}
}

// writeUserInternalError is the sanitizer for read-path failures (list, read
// before update). It logs the underlying cause server-side and returns a
// generic 500 so raw store/DB text is never serialized to the client
// (AGENTS §10). Every unexpected user-store failure must go through here or
// writeUserMutationError; writeError(..., err) must never carry a store error.
func (s *Server) writeUserInternalError(w http.ResponseWriter, err error) {
	s.log.Error("users: unexpected failure", "error", err, "component", "api")
	writeError(w, http.StatusInternalServerError, errors.New("internal error"))
}

// handleListUsers returns every account as a userResponse. It never returns a
// password hash: the response is built from an explicit DTO, not user.User.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireUserStore(w) {
		return
	}
	users, err := s.userStore.ListUsers(r.Context())
	if err != nil {
		s.writeUserInternalError(w, err)
		return
	}
	out := make([]userResponse, 0, len(users))
	for _, u := range users {
		out = append(out, newUserResponse(u))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateUser creates an account from {username, password, role}. An
// empty role defaults to user; a duplicate username (case-insensitive) is 409.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireUserStore(w) {
		return
	}
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid request body"))
		return
	}

	username := strings.TrimSpace(req.Username)
	if username == "" {
		writeError(w, http.StatusBadRequest, errors.New("username is required"))
		return
	}
	if err := validatePassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	role := user.Role(strings.TrimSpace(req.Role))
	if role == "" {
		role = user.RoleUser
	}
	if !validRole(role) {
		writeError(w, http.StatusBadRequest, errors.New("role must be one of admin|user"))
		return
	}

	hash, err := config.HashPassword(req.Password)
	if err != nil {
		// Never log the plaintext or the hash.
		s.log.Error("users: hash password failed", "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	id, err := s.userStore.CreateUser(r.Context(), &user.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
	})
	if err != nil {
		if errors.Is(err, user.ErrDuplicateUsername) {
			writeError(w, http.StatusConflict, errors.New("username already exists"))
			return
		}
		s.log.Error("users: create failed", "username", username, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	// Read back so the caller sees the persisted timestamps and the exact
	// stored username. The account exists either way, so a read-back miss
	// still returns 201 with the known fields rather than a misleading 500.
	if created, err := s.userStore.GetUser(r.Context(), id); err == nil && created != nil {
		writeJSON(w, http.StatusCreated, newUserResponse(*created))
		return
	}
	writeJSON(w, http.StatusCreated, userResponse{ID: id, Username: username, Role: role})
}

// handleUpdateUser applies a partial update from {role?, disabled?, password?}.
// Last-admin protection is enforced by the store inside a single transaction;
// the target's sessions (and live SSE stream) are invalidated when a
// security-relevant field actually changes.
//
// The store is handed a UserPatch, not a full row, so a concurrent update to a
// field this request did not mention can never be reverted. The patch is built
// after a read only to detect the no-op case and to decide whether a session
// invalidation is warranted; the write itself re-reads inside the transaction.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireUserStore(w) {
		return
	}
	id, err := parseUserID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid request body"))
		return
	}

	// Validate and hash at the boundary before touching the store.
	var patch user.UserPatch
	if req.Role != nil {
		role := user.Role(strings.TrimSpace(*req.Role))
		if !validRole(role) {
			writeError(w, http.StatusBadRequest, errors.New("role must be one of admin|user"))
			return
		}
		patch.Role = &role
	}
	if req.Disabled != nil {
		patch.Disabled = req.Disabled
	}
	if req.Password != nil {
		if err := validatePassword(*req.Password); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		hash, err := config.HashPassword(*req.Password)
		if err != nil {
			s.log.Error("users: hash password failed", "error", err, "component", "api")
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		patch.PasswordHash = &hash
	}

	// A fully empty patch is a no-op: return the current row with 200 and
	// issue no write, so a client that sends {} does not bump updated_at.
	if patch.Role == nil && patch.Disabled == nil && patch.PasswordHash == nil {
		existing, err := s.userStore.GetUser(r.Context(), id)
		if err != nil {
			s.writeUserInternalError(w, err)
			return
		}
		if existing == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("user %d not found", id))
			return
		}
		writeJSON(w, http.StatusOK, newUserResponse(*existing))
		return
	}

	// Read once to decide the invalidation trigger. Presence of a field is not
	// a change: PATCHing the role to its current value must not retire a live
	// session. The store performs its own transactional read for the write.
	existing, err := s.userStore.GetUser(r.Context(), id)
	if err != nil {
		s.writeUserInternalError(w, err)
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("user %d not found", id))
		return
	}
	securityRelevantChange := (patch.Role != nil && *patch.Role != existing.Role) ||
		(patch.Disabled != nil && *patch.Disabled != existing.Disabled) ||
		(patch.PasswordHash != nil && *patch.PasswordHash != existing.PasswordHash)

	updated, err := s.userStore.UpdateUserGuarded(r.Context(), id, patch)
	if err != nil {
		s.writeUserMutationError(w, err)
		return
	}

	// Retire the target's sessions and live SSE stream only when a
	// security-relevant field actually changed: a demotion, disable, or
	// password change must not leave a stale role/window live. If the target
	// is the acting admin (self password change), this logs them out on their
	// next request; see invalidateUserSessions for the rationale.
	if securityRelevantChange {
		s.invalidateUserSessions(id)
	}
	writeJSON(w, http.StatusOK, newUserResponse(*updated))
}

// handleDeleteUser removes an account. Self-deletion is refused with 403; the
// last-admin guard is enforced by the store inside a single transaction.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireUserStore(w) {
		return
	}
	id, err := parseUserID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// Self-delete prevention: an admin may not remove their own account. A
	// missing identity cannot reach here (adminOnly fails closed first), but
	// the ok check keeps the handler safe when invoked directly.
	if id0, ok := identityFrom(r.Context()); ok && id0.UserID == id {
		writeError(w, http.StatusForbidden, errors.New("cannot delete your own account"))
		return
	}

	if err := s.userStore.DeleteUserGuarded(r.Context(), id); err != nil {
		s.writeUserMutationError(w, err)
		return
	}
	s.invalidateUserSessions(id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
