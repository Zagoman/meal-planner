package webfake

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// loginLink is an emailed sign-in link in the fake: the link itself is
// kept for tests (LastLink) instead of being sent.
type loginLink struct {
	memberID    uuid.UUID
	householdID uuid.UUID
	inviteToken string
	used        bool
	expires     time.Time
}

// LastLink returns the newest sign-in link token "sent" to email, or "".
func (s *Store) LastLink(email string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastLink[strings.ToLower(email)]
}

func (s *Store) addLink(m *member, householdID uuid.UUID, inviteToken string) {
	if s.links == nil {
		s.links, s.lastLink = map[string]*loginLink{}, map[string]string{}
	}
	t := randToken()
	s.links[t] = &loginLink{memberID: m.ID, householdID: householdID, inviteToken: inviteToken, expires: time.Now().Add(15 * time.Minute)}
	s.lastLink[m.Email] = t
}

func (s *Store) RequestLoginLink(_ context.Context, email string) error {
	if !strings.Contains(email, "@") {
		return &domain.ValidationError{Problems: []string{"email: not a valid address"}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.memberByEmail(email)
	if m == nil || len(m.joined) == 0 {
		return nil // same answer either way
	}
	s.addLink(m, m.joined[len(m.joined)-1], "")
	return nil
}

func (s *Store) SignInWithLink(_ context.Context, token string) (string, domain.Scope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.links[token]
	if !ok || l.used || time.Now().After(l.expires) {
		return "", domain.Scope{}, fmt.Errorf("this sign-in link was already used or has expired: %w", domain.ErrNotFound)
	}
	l.used = true
	m := s.members[l.memberID]
	if l.inviteToken != "" {
		inv, err := s.usableInvite(l.inviteToken)
		if err != nil {
			return "", domain.Scope{}, fmt.Errorf("this invite was already used or has expired: %w", domain.ErrConflict)
		}
		now := time.Now()
		inv.AcceptedAt = &now
		if _, ok := m.roles[l.householdID]; !ok {
			m.roles[l.householdID] = inv.Role
			m.joined = append(m.joined, l.householdID)
		}
	}
	role, ok := m.roles[l.householdID]
	if !ok {
		return "", domain.Scope{}, fmt.Errorf("sign in: %w", domain.ErrUnauthorized)
	}
	t := randToken()
	s.sessions[t] = session{m.ID, l.householdID}
	return t, domain.Scope{HouseholdID: l.householdID, MemberID: m.ID, Role: role}, nil
}

func (s *Store) InvitePreview(_ context.Context, token string) (domain.InvitePreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, err := s.usableInvite(token)
	if err != nil {
		return domain.InvitePreview{}, err
	}
	return domain.InvitePreview{HouseholdName: s.households[inv.householdID].Name, ExpiresAt: inv.ExpiresAt}, nil
}

func (s *Store) Join(_ context.Context, token, name, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	var problems []string
	if strings.TrimSpace(name) == "" {
		problems = append(problems, "name: 1 to 100 characters")
	}
	if !strings.Contains(email, "@") {
		problems = append(problems, "email: not a valid address")
	}
	if len(problems) > 0 {
		return &domain.ValidationError{Problems: problems}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, err := s.usableInvite(token)
	if err != nil {
		return err
	}
	m := s.memberByEmail(email)
	if m == nil {
		m = &member{Member: domain.Member{ID: uuid.New(), Email: email, Name: strings.TrimSpace(name)}, roles: map[uuid.UUID]domain.Role{}}
		s.members[m.ID] = m
		s.byEmail[email] = m.ID
	}
	s.addLink(m, inv.householdID, token)
	return nil
}

func (s *Store) Setup(_ context.Context, sc domain.Scope, in domain.SetupInput) (domain.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.Profile{}, err
	}
	total := 0
	for _, n := range in.MealTargets {
		total += n
	}
	if total == 0 || in.People < 1 || in.People > 12 || !in.ShoppingDay.Valid() {
		return domain.Profile{}, &domain.ValidationError{Problems: []string{"answer all four questions"}}
	}
	h.profile.MealTargets, h.profile.People, h.profile.ShoppingDay = in.MealTargets, in.People, in.ShoppingDay
	for _, a := range in.Avoid {
		h.addAvoid(a, domain.AvoidFromSetup)
	}
	h.profile.SetupDone = true
	return h.profileWithAvoid(), nil
}

// NewHousehold adds a household that has not answered the setup questions
// and returns a one-time admin invite token for it.
func (s *Store) NewHousehold(name string) (uuid.UUID, string) {
	id := s.AddHousehold(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.households[id].profile.SetupDone = false
	t := randToken()
	s.invites[t] = &invite{Invite: domain.Invite{ID: uuid.New(), Role: domain.RoleAdmin, ExpiresAt: time.Now().Add(7 * 24 * time.Hour)}, householdID: id}
	return id, t
}
