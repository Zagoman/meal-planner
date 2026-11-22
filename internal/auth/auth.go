// Package auth implements service.Auth: password login, web sessions, API
// tokens and household switching (package A).
//
// Every token (session, API, invite) is 32 random bytes sent to the client
// as base64url; only its SHA-256 is stored. Passwords are bcrypt hashes.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/service"
)

// SessionTTL is how long a web session (and the mp_session cookie) lives.
const SessionTTL = 30 * 24 * time.Hour

// Password length limits. bcrypt ignores everything after 72 bytes, so
// longer passwords are rejected rather than silently truncated.
const (
	MinPasswordLen = 8
	MaxPasswordLen = 72
)

// touchEvery limits how often an API token's last_used_at is written.
const touchEvery = time.Minute

// Service implements service.Auth on the database.
type Service struct {
	pool       *pgxpool.Pool
	q          *db.Queries
	adminEmail string
}

var _ service.Auth = (*Service)(nil)

// New returns the auth service. adminEmail (Config.AdminEmail) is the
// server admin's email; that member always gets Scope.ServerAdmin.
func New(pool *pgxpool.Pool, adminEmail string) *Service {
	return &Service{pool: pool, q: db.New(pool), adminEmail: NormalizeEmail(adminEmail)}
}

// NormalizeEmail lowercases and trims an email address.
func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// dummyHash is compared against when the email is unknown so a failed login
// takes the same time whether or not the member exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcrypt.DefaultCost)

func (s *Service) Login(ctx context.Context, email, password string) (string, domain.Scope, error) {
	m, err := s.q.GetMemberByEmail(ctx, NormalizeEmail(email))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", domain.Scope{}, fmt.Errorf("login: %w", err)
		}
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return "", domain.Scope{}, fmt.Errorf("login: %w", domain.ErrUnauthorized)
	}
	if m.PasswordHash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return "", domain.Scope{}, fmt.Errorf("login: no password set: %w", domain.ErrUnauthorized)
	}
	if bcrypt.CompareHashAndPassword([]byte(m.PasswordHash), []byte(password)) != nil {
		return "", domain.Scope{}, fmt.Errorf("login: %w", domain.ErrUnauthorized)
	}
	hs, err := s.q.ListMemberHouseholds(ctx, m.ID)
	if err != nil {
		return "", domain.Scope{}, fmt.Errorf("login: households: %w", err)
	}
	if len(hs) == 0 {
		return "", domain.Scope{}, fmt.Errorf("login: member has no household: %w", domain.ErrUnauthorized)
	}
	if err := s.q.DeleteExpiredSessions(ctx); err != nil {
		slog.WarnContext(ctx, "delete expired sessions", "err", err)
	}
	token, hash := NewToken()
	if _, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		HouseholdID: hs[0].ID, MemberID: m.ID, TokenHash: hash, ExpiresAt: time.Now().Add(SessionTTL),
	}); err != nil {
		return "", domain.Scope{}, fmt.Errorf("login: create session: %w", err)
	}
	return token, s.scope(m, hs[0].ID, domain.Role(hs[0].Role)), nil
}

func (s *Service) Logout(ctx context.Context, sessionToken string) error {
	sess, err := s.q.GetSessionByTokenHash(ctx, HashToken(sessionToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already gone
	}
	if err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	if err := s.q.DeleteSession(ctx, sess.ID); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}

func (s *Service) SessionScope(ctx context.Context, sessionToken string) (domain.Scope, error) {
	if sessionToken == "" {
		return domain.Scope{}, fmt.Errorf("session: %w", domain.ErrUnauthorized)
	}
	sess, err := s.q.GetSessionByTokenHash(ctx, HashToken(sessionToken))
	if err != nil {
		return domain.Scope{}, unauthorized("session", err)
	}
	return s.scopeFor(ctx, sess.HouseholdID, sess.MemberID)
}

func (s *Service) BearerScope(ctx context.Context, apiToken string) (domain.Scope, error) {
	if apiToken == "" {
		return domain.Scope{}, fmt.Errorf("api token: %w", domain.ErrUnauthorized)
	}
	t, err := s.q.GetAPITokenByHash(ctx, HashToken(apiToken))
	if err != nil {
		return domain.Scope{}, unauthorized("api token", err)
	}
	sc, err := s.scopeFor(ctx, t.HouseholdID, t.MemberID)
	if err != nil {
		return domain.Scope{}, err
	}
	if t.LastUsedAt == nil || time.Since(*t.LastUsedAt) > touchEvery {
		if err := s.q.TouchAPIToken(ctx, t.ID); err != nil {
			slog.WarnContext(ctx, "touch api token", "err", err)
		}
	}
	return sc, nil
}

func (s *Service) SwitchHousehold(ctx context.Context, sessionToken string, householdID uuid.UUID) (domain.Scope, error) {
	sess, err := s.q.GetSessionByTokenHash(ctx, HashToken(sessionToken))
	if err != nil {
		return domain.Scope{}, unauthorized("switch household", err)
	}
	sc, err := s.scopeFor(ctx, householdID, sess.MemberID)
	if errors.Is(err, domain.ErrUnauthorized) {
		// Not a member of that household: it does not exist for this caller.
		return domain.Scope{}, fmt.Errorf("switch household: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.Scope{}, err
	}
	if err := s.q.SwitchSessionHousehold(ctx, db.SwitchSessionHouseholdParams{ID: sess.ID, HouseholdID: householdID}); err != nil {
		return domain.Scope{}, fmt.Errorf("switch household: %w", err)
	}
	return sc, nil
}

func (s *Service) CreateAPIToken(ctx context.Context, sc domain.Scope, name string) (string, error) {
	name = strings.TrimSpace(name)
	if len(name) > 100 {
		return "", &domain.ValidationError{Problems: []string{"name: at most 100 characters"}}
	}
	token, hash := NewToken()
	if _, err := s.q.CreateAPIToken(ctx, db.CreateAPITokenParams{
		HouseholdID: sc.HouseholdID, MemberID: sc.MemberID, Name: name, TokenHash: hash,
	}); err != nil {
		return "", fmt.Errorf("create api token: %w", err)
	}
	return token, nil
}

// APIToken is an API token as listed on the Household screen (never the
// token itself).
type APIToken struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// APITokens lists the caller's tokens in the current household. Not part
// of service.Auth; offered for the web UI.
func (s *Service) APITokens(ctx context.Context, sc domain.Scope) ([]APIToken, error) {
	rows, err := s.q.ListAPITokens(ctx, db.ListAPITokensParams{HouseholdID: sc.HouseholdID, MemberID: sc.MemberID})
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	out := make([]APIToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, APIToken{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt, RevokedAt: r.RevokedAt})
	}
	return out, nil
}

// RevokeAPIToken revokes one of the caller's tokens in the current
// household; other members' or households' tokens are untouched.
func (s *Service) RevokeAPIToken(ctx context.Context, sc domain.Scope, id uuid.UUID) error {
	if err := s.q.RevokeAPIToken(ctx, db.RevokeAPITokenParams{ID: id, HouseholdID: sc.HouseholdID, MemberID: sc.MemberID}); err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}
	return nil
}

// Register creates a member with a password and no household (they join
// one by accepting an invite). Not part of service.Auth; the web invite page
// calls it for people who have no account yet. An existing email is
// domain.ErrConflict.
func (s *Service) Register(ctx context.Context, email, name, password string) (domain.Member, error) {
	email = NormalizeEmail(email)
	name = strings.TrimSpace(name)
	var problems []string
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
		problems = append(problems, "email: not a valid address")
	}
	if len(name) > 100 {
		problems = append(problems, "name: at most 100 characters")
	}
	if p := passwordProblem(password); p != "" {
		problems = append(problems, p)
	}
	if len(problems) > 0 {
		return domain.Member{}, &domain.ValidationError{Problems: problems}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return domain.Member{}, fmt.Errorf("register: %w", err)
	}
	m, err := s.q.CreateMember(ctx, db.CreateMemberParams{
		Email: email, Name: name, PasswordHash: string(hash), IsServerAdmin: s.adminEmail != "" && email == s.adminEmail,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Member{}, fmt.Errorf("register: email already registered: %w", domain.ErrConflict)
		}
		return domain.Member{}, fmt.Errorf("register: %w", err)
	}
	return domain.Member{ID: m.ID, Email: m.Email, Name: m.Name, ServerAdmin: s.isServerAdmin(m)}, nil
}

// SetPassword replaces a member's password. Not part of service.Auth.
func (s *Service) SetPassword(ctx context.Context, memberID uuid.UUID, password string) error {
	if p := passwordProblem(password); p != "" {
		return &domain.ValidationError{Problems: []string{p}}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	if err := s.q.UpdateMemberPassword(ctx, db.UpdateMemberPasswordParams{ID: memberID, PasswordHash: string(hash)}); err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return nil
}

// ResetPassword gives the member with this email a new random password and
// returns it. Used by the `server admin password` command; the caller prints
// it to the terminal, never to the log.
func (s *Service) ResetPassword(ctx context.Context, email string) (string, error) {
	m, err := s.q.GetMemberByEmail(ctx, NormalizeEmail(email))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("reset password: no member with that email: %w", domain.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("reset password: %w", err)
	}
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	pw := base64.RawURLEncoding.EncodeToString(b) // 20 characters
	if err := s.SetPassword(ctx, m.ID, pw); err != nil {
		return "", err
	}
	return pw, nil
}

func passwordProblem(pw string) string {
	switch {
	case len(pw) < MinPasswordLen:
		return fmt.Sprintf("password: at least %d characters", MinPasswordLen)
	case len(pw) > MaxPasswordLen:
		return fmt.Sprintf("password: at most %d bytes", MaxPasswordLen)
	}
	return ""
}

// scopeFor builds the scope of a member in a household, checking the
// membership still exists. No membership is ErrUnauthorized.
func (s *Service) scopeFor(ctx context.Context, householdID, memberID uuid.UUID) (domain.Scope, error) {
	hm, err := s.q.GetMembership(ctx, db.GetMembershipParams{HouseholdID: householdID, MemberID: memberID})
	if err != nil {
		return domain.Scope{}, unauthorized("membership", err)
	}
	m, err := s.q.GetMember(ctx, memberID)
	if err != nil {
		return domain.Scope{}, unauthorized("member", err)
	}
	return s.scope(m, householdID, domain.Role(hm.Role)), nil
}

func (s *Service) scope(m db.Member, householdID uuid.UUID, role domain.Role) domain.Scope {
	return domain.Scope{HouseholdID: householdID, MemberID: m.ID, Role: role, ServerAdmin: s.isServerAdmin(m)}
}

func (s *Service) isServerAdmin(m db.Member) bool {
	return m.IsServerAdmin || (s.adminEmail != "" && NormalizeEmail(m.Email) == s.adminEmail)
}

func unauthorized(what string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, domain.ErrUnauthorized)
	}
	return fmt.Errorf("%s: %w", what, err)
}
