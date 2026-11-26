// Package household implements service.Households, service.Profiles and
// service.Feedback (package A): households, members, invites, the profile,
// shop sections, pantry, ratings and saved recipes.
package household

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Zagoman/meal-planner/internal/auth"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/service"
)

// InviteTTL is how long an invite link works.
const InviteTTL = 7 * 24 * time.Hour

// Service implements Households, Profiles and Feedback.
type Service struct {
	pool       *pgxpool.Pool
	q          *db.Queries
	baseURL    string
	adminEmail string
}

var (
	_ service.Households = (*Service)(nil)
	_ service.Profiles   = (*Service)(nil)
	_ service.Feedback   = (*Service)(nil)
)

// New returns the household service. baseURL (Config.BaseURL, no trailing
// slash) builds invite links; adminEmail marks the server admin.
func New(pool *pgxpool.Pool, baseURL, adminEmail string) *Service {
	return &Service{
		pool: pool, q: db.New(pool),
		baseURL:    strings.TrimRight(baseURL, "/"),
		adminEmail: auth.NormalizeEmail(adminEmail),
	}
}

func (s *Service) Create(ctx context.Context, sc domain.Scope, name, adminEmail string) (domain.Household, error) {
	if !sc.ServerAdmin {
		return domain.Household{}, fmt.Errorf("create household: %w", domain.ErrForbidden)
	}
	name = strings.TrimSpace(name)
	adminEmail = auth.NormalizeEmail(adminEmail)
	var problems []string
	if name == "" || len(name) > 100 {
		problems = append(problems, "name: 1 to 100 characters")
	}
	if adminEmail != "" && (!strings.Contains(adminEmail, "@") || len(adminEmail) > 254) {
		problems = append(problems, "admin_email: not a valid address")
	}
	if len(problems) > 0 {
		return domain.Household{}, &domain.ValidationError{Problems: problems}
	}
	var h db.Household
	var inv domain.Invite
	err := s.inTx(ctx, func(q *db.Queries) error {
		var err error
		h, _, err = createHousehold(ctx, q, name, adminEmail, s.adminEmail)
		if err != nil {
			return err
		}
		// The first admin joins with this one-time link.
		inv, err = s.createInvite(ctx, q, h.ID, uuid.Nil, domain.RoleAdmin)
		return err
	})
	if err != nil {
		return domain.Household{}, fmt.Errorf("create household: %w", err)
	}
	return domain.Household{ID: h.ID, Name: h.Name, CreatedAt: h.CreatedAt, Invite: &inv}, nil
}

// createHousehold makes the household with its default profile and
// sections and adds adminEmail as admin, creating the member (without a
// password) if needed.
func createHousehold(ctx context.Context, q *db.Queries, name, adminEmail, serverAdminEmail string) (db.Household, db.Member, error) {
	h, err := q.CreateHousehold(ctx, name)
	if err != nil {
		return db.Household{}, db.Member{}, err
	}
	if err := createDefaults(ctx, q, h.ID); err != nil {
		return db.Household{}, db.Member{}, err
	}
	if adminEmail == "" {
		return h, db.Member{}, nil
	}
	m, err := q.GetMemberByEmail(ctx, adminEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		m, err = q.CreateMember(ctx, db.CreateMemberParams{
			Email: adminEmail, IsServerAdmin: serverAdminEmail != "" && adminEmail == serverAdminEmail,
		})
	}
	if err != nil {
		return db.Household{}, db.Member{}, err
	}
	if err := q.AddHouseholdMember(ctx, db.AddHouseholdMemberParams{
		HouseholdID: h.ID, MemberID: m.ID, Role: string(domain.RoleAdmin),
	}); err != nil {
		return db.Household{}, db.Member{}, err
	}
	return h, m, nil
}

// createDefaults gives a new household its profile (not set up yet) and
// the default shop sections.
func createDefaults(ctx context.Context, q *db.Queries, householdID uuid.UUID) error {
	if err := q.CreateProfile(ctx, householdID); err != nil {
		return err
	}
	for _, sec := range domain.DefaultSections {
		if err := q.InsertSection(ctx, db.InsertSectionParams{
			HouseholdID: householdID, Key: sec.Key, Name: sec.Name, SortOrder: int32(sec.SortOrder), //nolint:gosec // small constant
		}); err != nil {
			return err
		}
	}
	return nil
}

// Bootstrap makes sure the server admin exists. If no member has
// adminEmail, it creates a household "Home" with that member as admin and
// server admin, without a password: the operator then runs
// `server admin password` to get one. If the member exists it is (re)marked
// server admin. Empty adminEmail does nothing. Returns whether it created.
func Bootstrap(ctx context.Context, pool *pgxpool.Pool, adminEmail string) (bool, error) {
	adminEmail = auth.NormalizeEmail(adminEmail)
	if adminEmail == "" {
		return false, nil
	}
	q := db.New(pool)
	m, err := q.GetMemberByEmail(ctx, adminEmail)
	switch {
	case err == nil:
		if !m.IsServerAdmin {
			if err := q.SetServerAdmin(ctx, db.SetServerAdminParams{ID: m.ID, IsServerAdmin: true}); err != nil {
				return false, fmt.Errorf("bootstrap: %w", err)
			}
		}
		return false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return false, fmt.Errorf("bootstrap: %w", err)
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, _, err := createHousehold(ctx, q.WithTx(tx), "Home", adminEmail, adminEmail)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("bootstrap: %w", err)
	}
	slog.WarnContext(ctx, "created server admin and household \"Home\"; set its password with: server admin password")
	return true, nil
}

func (s *Service) Members(ctx context.Context, sc domain.Scope) ([]domain.Member, error) {
	rows, err := s.q.ListHouseholdMembers(ctx, sc.HouseholdID)
	if err != nil {
		return nil, fmt.Errorf("members: %w", err)
	}
	out := make([]domain.Member, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.Member{ID: r.ID, Email: r.Email, Name: r.Name, Role: domain.Role(r.Role)})
	}
	return out, nil
}

// SetMyName sets the scope's own member name, 1 to 100 characters.
func (s *Service) SetMyName(ctx context.Context, sc domain.Scope, name string) (domain.Member, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || len(name) > maxText {
		return domain.Member{}, &domain.ValidationError{Problems: []string{"name: 1 to 100 characters"}}
	}
	m, err := s.q.SetOwnMemberName(ctx, db.SetOwnMemberNameParams{Name: name, MemberID: sc.MemberID, HouseholdID: sc.HouseholdID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Member{}, fmt.Errorf("set name: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.Member{}, fmt.Errorf("set name: %w", err)
	}
	hm, err := s.q.GetMembership(ctx, db.GetMembershipParams{HouseholdID: sc.HouseholdID, MemberID: sc.MemberID})
	if err != nil {
		return domain.Member{}, fmt.Errorf("set name: %w", err)
	}
	return domain.Member{ID: m.ID, Email: m.Email, Name: m.Name, Role: domain.Role(hm.Role)}, nil
}

func (s *Service) Memberships(ctx context.Context, sc domain.Scope) ([]domain.Membership, error) {
	rows, err := s.q.ListMemberHouseholds(ctx, sc.MemberID)
	if err != nil {
		return nil, fmt.Errorf("memberships: %w", err)
	}
	out := make([]domain.Membership, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.Membership{HouseholdID: r.ID, HouseholdName: r.Name, Role: domain.Role(r.Role)})
	}
	return out, nil
}

func (s *Service) CreateInvite(ctx context.Context, sc domain.Scope, role domain.Role) (domain.Invite, error) {
	if !sc.IsHouseholdAdmin() {
		return domain.Invite{}, fmt.Errorf("create invite: %w", domain.ErrForbidden)
	}
	if role == "" {
		role = domain.RoleMember
	}
	if role != domain.RoleMember && role != domain.RoleAdmin {
		return domain.Invite{}, &domain.ValidationError{Problems: []string{"role: must be admin or member"}}
	}
	inv, err := s.createInvite(ctx, s.q, sc.HouseholdID, sc.MemberID, role)
	if err != nil {
		return domain.Invite{}, fmt.Errorf("create invite: %w", err)
	}
	return inv, nil
}

// createInvite stores a one-time invite (valid InviteTTL) and returns its
// link, /join/{token}, shown once.
func (s *Service) createInvite(ctx context.Context, q *db.Queries, householdID, by uuid.UUID, role domain.Role) (domain.Invite, error) {
	token, hash := auth.NewToken()
	inv, err := q.CreateInvite(ctx, db.CreateInviteParams{
		HouseholdID: householdID, TokenHash: hash, Role: string(role),
		CreatedBy: uuid.NullUUID{UUID: by, Valid: by != uuid.Nil}, ExpiresAt: time.Now().Add(InviteTTL),
	})
	if err != nil {
		return domain.Invite{}, err
	}
	return domain.Invite{
		ID: inv.ID, Role: domain.Role(inv.Role), ExpiresAt: inv.ExpiresAt,
		Token: token, URL: s.baseURL + "/join/" + token,
	}, nil
}

// AcceptInvite: unknown or expired token is ErrNotFound, an already used
// one ErrConflict. A member who already belongs to the household keeps
// their current role (the invite is still used up).
func (s *Service) AcceptInvite(ctx context.Context, memberID uuid.UUID, token string) (domain.Membership, error) {
	inv, err := s.q.GetInviteByTokenHash(ctx, auth.HashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Membership{}, fmt.Errorf("accept invite: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.Membership{}, fmt.Errorf("accept invite: %w", err)
	}
	var out domain.Membership
	err = s.inTx(ctx, func(q *db.Queries) error {
		n, err := q.AcceptInvite(ctx, db.AcceptInviteParams{ID: inv.ID, HouseholdID: inv.HouseholdID, MemberID: uuid.NullUUID{UUID: memberID, Valid: true}})
		if err != nil {
			return err
		}
		if n == 0 {
			// The UPDATE only matches an unused, unexpired invite, so it is
			// the single-use guard even under concurrent accepts.
			if inv.AcceptedAt == nil && !time.Now().Before(inv.ExpiresAt) {
				return fmt.Errorf("invite expired: %w", domain.ErrNotFound)
			}
			return fmt.Errorf("invite already used: %w", domain.ErrConflict)
		}
		if err := q.AddHouseholdMember(ctx, db.AddHouseholdMemberParams{
			HouseholdID: inv.HouseholdID, MemberID: memberID, Role: inv.Role,
		}); err != nil {
			return err
		}
		hm, err := q.GetMembership(ctx, db.GetMembershipParams{HouseholdID: inv.HouseholdID, MemberID: memberID})
		if err != nil {
			return err
		}
		h, err := q.GetHousehold(ctx, inv.HouseholdID)
		if err != nil {
			return err
		}
		out = domain.Membership{HouseholdID: h.ID, HouseholdName: h.Name, Role: domain.Role(hm.Role)}
		return nil
	})
	if err != nil {
		return domain.Membership{}, fmt.Errorf("accept invite: %w", err)
	}
	return out, nil
}

func (s *Service) Usage(ctx context.Context, sc domain.Scope, since time.Time) ([]domain.UsageRow, error) {
	if !sc.ServerAdmin {
		return nil, fmt.Errorf("usage: %w", domain.ErrForbidden)
	}
	rows, err := s.q.UsageByHousehold(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	out := make([]domain.UsageRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.UsageRow{
			HouseholdID: r.HouseholdID, Household: r.Name,
			Calls: r.Calls, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
		})
	}
	return out, nil
}

func (s *Service) inTx(ctx context.Context, fn func(q *db.Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return fn(s.q.WithTx(tx)) })
}
