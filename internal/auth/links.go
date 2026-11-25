package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	mailer "github.com/Zagoman/meal-planner/internal/mail"
)

// LoginLinkTTL is how long an emailed sign-in link works (once).
const LoginLinkTTL = 15 * time.Minute

// WithMail sets how sign-in links are sent and the public origin they point
// to. Without it, links are not sent.
func (s *Service) WithMail(m mailer.Mailer, baseURL string) *Service {
	s.mail, s.baseURL = m, strings.TrimRight(baseURL, "/")
	return s
}

func validEmail(email string) bool {
	a, err := mail.ParseAddress(email)
	return err == nil && a.Address == email && len(email) <= 254
}

// RequestLoginLink emails a sign-in link to a member. It answers the same
// way whether or not the address has an account, so it never tells who has
// one; at most one link a minute is sent to a member.
func (s *Service) RequestLoginLink(ctx context.Context, email string) error {
	email = NormalizeEmail(email)
	if !validEmail(email) {
		return &domain.ValidationError{Problems: []string{"email: not a valid address"}}
	}
	m, err := s.q.GetMemberByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("login link: %w", err)
	}
	hm, err := s.q.LatestMembership(ctx, m.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // no household to sign in to
	}
	if err != nil {
		return fmt.Errorf("login link: %w", err)
	}
	return s.sendLink(ctx, m, hm.HouseholdID, uuid.Nil)
}

// sendLink stores a new link and emails it, unless one was sent to the
// member in the last minute. A send failure is logged, not returned: the
// person sees "check your inbox" either way.
func (s *Service) sendLink(ctx context.Context, m db.Member, householdID, inviteID uuid.UUID) error {
	n, err := s.q.RecentLoginTokens(ctx, m.ID)
	if err != nil {
		return fmt.Errorf("login link: %w", err)
	}
	if n > 0 {
		return nil
	}
	token, hash := NewToken()
	if _, err := s.q.CreateLoginToken(ctx, db.CreateLoginTokenParams{
		HouseholdID: householdID, MemberID: m.ID, TokenHash: hash, ExpiresAt: time.Now().Add(LoginLinkTTL),
		InviteID: uuid.NullUUID{UUID: inviteID, Valid: inviteID != uuid.Nil},
	}); err != nil {
		return fmt.Errorf("login link: %w", err)
	}
	if s.mail == nil {
		slog.WarnContext(ctx, "sign-in link not sent: no mailer configured")
		return nil
	}
	msg := mailer.Simple(m.Email, "Your sign-in link",
		"Tap the button to sign in.", "Sign in", s.baseURL+"/auth/"+token,
		"The link works once and expires in 15 minutes. If you didn't ask for it, ignore this email.")
	if err := s.mail.Send(ctx, msg); err != nil {
		slog.ErrorContext(ctx, "send sign-in link", "member_id", m.ID, "err", err)
	}
	return nil
}

// SignInWithLink uses an emailed link: it works once, within 15 minutes.
// A link sent from an invite adds the membership now (the invite is used up
// then). It returns a new session token.
func (s *Service) SignInWithLink(ctx context.Context, token string) (string, domain.Scope, error) {
	if token == "" {
		return "", domain.Scope{}, fmt.Errorf("sign-in link: %w", domain.ErrNotFound)
	}
	session, sessionHash := NewToken()
	var sc domain.Scope
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		lt, err := q.UseLoginToken(ctx, HashToken(token))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("this sign-in link was already used or has expired: %w", domain.ErrNotFound)
		}
		if err != nil {
			return err
		}
		if lt.InviteID.Valid {
			inv, err := q.GetInvite(ctx, db.GetInviteParams{ID: lt.InviteID.UUID, HouseholdID: lt.HouseholdID})
			if err != nil {
				return fmt.Errorf("invite: %w", domain.ErrNotFound)
			}
			n, err := q.AcceptInvite(ctx, db.AcceptInviteParams{ID: inv.ID, HouseholdID: inv.HouseholdID, MemberID: uuidNull(lt.MemberID)})
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("this invite was already used or has expired: %w", domain.ErrConflict)
			}
			if err := q.AddHouseholdMember(ctx, db.AddHouseholdMemberParams{
				HouseholdID: inv.HouseholdID, MemberID: lt.MemberID, Role: inv.Role,
			}); err != nil {
				return err
			}
		}
		hm, err := q.GetMembership(ctx, db.GetMembershipParams{HouseholdID: lt.HouseholdID, MemberID: lt.MemberID})
		if err != nil {
			return unauthorized("membership", err)
		}
		m, err := q.GetMember(ctx, lt.MemberID)
		if err != nil {
			return unauthorized("member", err)
		}
		if _, err := q.CreateSession(ctx, db.CreateSessionParams{
			HouseholdID: lt.HouseholdID, MemberID: m.ID, TokenHash: sessionHash, ExpiresAt: time.Now().Add(SessionTTL),
		}); err != nil {
			return err
		}
		sc = s.scope(m, lt.HouseholdID, domain.Role(hm.Role))
		return nil
	})
	if err != nil {
		return "", domain.Scope{}, fmt.Errorf("sign in: %w", err)
	}
	return session, sc, nil
}

// usableInvite finds an invite that is neither used nor expired.
func (s *Service) usableInvite(ctx context.Context, token string) (db.Invite, error) {
	inv, err := s.q.GetInviteByTokenHash(ctx, HashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Invite{}, fmt.Errorf("invite: %w", domain.ErrNotFound)
	}
	if err != nil {
		return db.Invite{}, fmt.Errorf("invite: %w", err)
	}
	if inv.AcceptedAt != nil || !time.Now().Before(inv.ExpiresAt) {
		return db.Invite{}, fmt.Errorf("this invite was already used or has expired: %w", domain.ErrNotFound)
	}
	return inv, nil
}

// InvitePreview is what the join page shows: the household and who
// invited. Used or expired invites are domain.ErrNotFound.
func (s *Service) InvitePreview(ctx context.Context, token string) (domain.InvitePreview, error) {
	inv, err := s.usableInvite(ctx, token)
	if err != nil {
		return domain.InvitePreview{}, err
	}
	h, err := s.q.GetHousehold(ctx, inv.HouseholdID)
	if err != nil {
		return domain.InvitePreview{}, fmt.Errorf("invite: %w", err)
	}
	p := domain.InvitePreview{HouseholdName: h.Name, ExpiresAt: inv.ExpiresAt}
	if inv.CreatedBy.Valid {
		if m, err := s.q.GetMember(ctx, inv.CreatedBy.UUID); err == nil {
			p.InviterName = m.Name
			if p.InviterName == "" {
				p.InviterName, _, _ = strings.Cut(m.Email, "@")
			}
		}
	}
	return p, nil
}

// Join answers an invite with a name and an email: it creates the member if
// the address is new and emails a sign-in link. The membership is added
// only when that link is used, so only the owner of the address can join.
// It answers the same whether or not the address already has an account.
func (s *Service) Join(ctx context.Context, inviteToken, name, email string) error {
	name = strings.Join(strings.Fields(name), " ")
	email = NormalizeEmail(email)
	var problems []string
	if name == "" || len(name) > 100 {
		problems = append(problems, "name: 1 to 100 characters")
	}
	if !validEmail(email) {
		problems = append(problems, "email: not a valid address")
	}
	if len(problems) > 0 {
		return &domain.ValidationError{Problems: problems}
	}
	inv, err := s.usableInvite(ctx, inviteToken)
	if err != nil {
		return err
	}
	m, err := s.q.GetMemberByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		m, err = s.q.CreateMember(ctx, db.CreateMemberParams{
			Email: email, Name: name, IsServerAdmin: s.adminEmail != "" && email == s.adminEmail,
		})
	}
	if err != nil {
		return fmt.Errorf("join: %w", err)
	}
	if err := s.q.SetMemberName(ctx, db.SetMemberNameParams{ID: m.ID, Name: name}); err != nil {
		return fmt.Errorf("join: %w", err)
	}
	return s.sendLink(ctx, m, inv.HouseholdID, inv.ID)
}
