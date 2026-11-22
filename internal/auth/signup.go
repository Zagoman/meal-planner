package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
)

// SignUpWithInvite creates a new member from an invite: it registers the
// member, uses up the invite, adds the membership and starts a session in
// that household, all in one transaction, so a bad invite never leaves an
// orphan account behind. It implements web.InviteSignup.
//
// Errors: unknown or expired invite = domain.ErrNotFound, already used =
// domain.ErrConflict, email already registered = domain.ErrConflict (the
// person should log in and accept instead), bad input = ValidationError.
func (s *Service) SignUpWithInvite(ctx context.Context, inviteToken, email, name, password string) (string, domain.Scope, error) {
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
		return "", domain.Scope{}, &domain.ValidationError{Problems: problems}
	}

	inv, err := s.q.GetInviteByTokenHash(ctx, HashToken(inviteToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.Scope{}, fmt.Errorf("sign up: invite: %w", domain.ErrNotFound)
	}
	if err != nil {
		return "", domain.Scope{}, fmt.Errorf("sign up: invite: %w", err)
	}
	if inv.AcceptedAt != nil {
		return "", domain.Scope{}, fmt.Errorf("sign up: invite already used: %w", domain.ErrConflict)
	}
	if !time.Now().Before(inv.ExpiresAt) {
		return "", domain.Scope{}, fmt.Errorf("sign up: invite expired: %w", domain.ErrNotFound)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", domain.Scope{}, fmt.Errorf("sign up: %w", err)
	}

	token, tokenHash := NewToken()
	var sc domain.Scope
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		m, err := q.CreateMember(ctx, db.CreateMemberParams{
			Email: email, Name: name, PasswordHash: string(hash),
			IsServerAdmin: s.adminEmail != "" && email == s.adminEmail,
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return fmt.Errorf("email already registered: %w", domain.ErrConflict)
			}
			return err
		}
		// Matches only an unused, unexpired invite: the single-use guard
		// under concurrent sign-ups.
		n, err := q.AcceptInvite(ctx, db.AcceptInviteParams{
			ID: inv.ID, HouseholdID: inv.HouseholdID, MemberID: uuidNull(m.ID),
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("invite already used: %w", domain.ErrConflict)
		}
		if err := q.AddHouseholdMember(ctx, db.AddHouseholdMemberParams{
			HouseholdID: inv.HouseholdID, MemberID: m.ID, Role: inv.Role,
		}); err != nil {
			return err
		}
		if _, err := q.CreateSession(ctx, db.CreateSessionParams{
			HouseholdID: inv.HouseholdID, MemberID: m.ID, TokenHash: tokenHash,
			ExpiresAt: time.Now().Add(SessionTTL),
		}); err != nil {
			return err
		}
		sc = s.scope(m, inv.HouseholdID, domain.Role(inv.Role))
		return nil
	})
	if err != nil {
		return "", domain.Scope{}, fmt.Errorf("sign up: %w", err)
	}
	return token, sc, nil
}

func uuidNull(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: true} }
