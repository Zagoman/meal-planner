// Package webfake is an in-memory implementation of every service interface,
// used by the web UI's handler tests and by the e2e test server. It keeps the
// real services' rules that the UI depends on: everything is scoped by
// Scope.HouseholdID, another household's row is domain.ErrNotFound, list
// writes publish events, and a failed generation keeps the current plan.
//
// It never calls Claude: plans are built from a fixed set of recipes.
package webfake

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/service"
	"github.com/Zagoman/meal-planner/internal/shopping"
)

// Store implements service.Auth, Households, Profiles, Plans, Lists and
// Feedback (their method names do not overlap).
type Store struct {
	// GenerateDelay simulates Claude's latency in Generate and SwapMeal.
	GenerateDelay time.Duration

	mu         sync.Mutex
	hub        *events.Hub
	members    map[uuid.UUID]*member
	byEmail    map[string]uuid.UUID
	households map[uuid.UUID]*household
	sessions   map[string]session
	apiTokens  map[string]session
	invites    map[string]*invite
	links      map[string]*loginLink // links.go
	lastLink   map[string]string     // email -> newest link token
	imports    map[uuid.UUID]*fakeDraft
	tickAt     map[uuid.UUID]time.Time // item -> time of the tick applied
}

type member struct {
	domain.Member
	password string
	roles    map[uuid.UUID]domain.Role
	joined   []uuid.UUID // households in join order
}

type session struct{ memberID, householdID uuid.UUID }

type invite struct {
	domain.Invite
	householdID uuid.UUID
}

type household struct {
	domain.Household
	profile   domain.Profile
	sections  []domain.Section
	pantry    []domain.PantryItem
	avoid     []domain.AvoidItem
	plan      *domain.Plan  // active plan, nil if none
	draft     *draftState   // next week's draft (drafts.go)
	library   *libraryState // outcomes and recipes (library.go)
	importIDs []uuid.UUID   // recipe drafts (imports.go)
	meals     map[uuid.UUID]domain.Meal
	items     []domain.ListItem
	cleared   map[uuid.UUID][]domain.ListItem // ClearChecked batches, for Undo
	feedback  []domain.Feedback
	recipes   []domain.Recipe
	next      int // recipe rotation
}

var (
	_ service.Auth       = (*Store)(nil)
	_ service.Households = (*Store)(nil)
	_ service.Profiles   = (*Store)(nil)
	_ service.Plans      = (*Store)(nil)
	_ service.Lists      = (*Store)(nil)
	_ service.Feedback   = (*Store)(nil)
)

// New returns an empty store publishing list changes to hub.
func New(hub *events.Hub) *Store {
	return &Store{
		hub:        hub,
		members:    map[uuid.UUID]*member{},
		byEmail:    map[string]uuid.UUID{},
		households: map[uuid.UUID]*household{},
		sessions:   map[string]session{},
		apiTokens:  map[string]session{},
		invites:    map[string]*invite{},
	}
}

// Services returns the store as every service.
func (s *Store) Services() service.Services {
	return service.Services{Auth: s, Households: s, Profiles: s, Plans: s, Drafts: s, Outcomes: s, Library: s, Lists: s, Feedback: s}
}

// --- fixtures --------------------------------------------------------------

// AddHousehold creates a household with the default profile and sections.
func (s *Store) AddHousehold(name string) uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &household{
		Household: domain.Household{ID: uuid.New(), Name: name, CreatedAt: time.Now()},
		profile: domain.Profile{
			MealTargets: map[domain.MealType]int{domain.MealBreakfast: 0, domain.MealLunch: 0, domain.MealDinner: 5, domain.MealBaking: 0},
			People:      2, SpiceLevel: domain.SpiceMild,
			ProteinMix: map[domain.Protein]int{domain.ProteinChicken: 2, domain.ProteinFish: 1, domain.ProteinVegetarian: 2},
			Avoid:      []string{}, ShoppingDay: domain.Saturday, AutoConfirmTime: "08:00", Language: "en", SetupDone: true,
		},
		sections: slices.Clone(domain.DefaultSections),
		meals:    map[uuid.UUID]domain.Meal{},
	}
	s.households[h.ID] = h
	return h.ID
}

// AddMember creates (or reuses) a member by email and adds them to the
// household with the role. It returns the member's scope there.
func (s *Store) AddMember(householdID uuid.UUID, email, name, password string, role domain.Role) domain.Scope {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.memberByEmail(email)
	if m == nil {
		m = &member{Member: domain.Member{ID: uuid.New(), Email: strings.ToLower(email), Name: name},
			password: password, roles: map[uuid.UUID]domain.Role{}}
		s.members[m.ID] = m
		s.byEmail[m.Email] = m.ID
	}
	if _, ok := m.roles[householdID]; !ok {
		m.joined = append(m.joined, householdID)
	}
	m.roles[householdID] = role
	return domain.Scope{HouseholdID: householdID, MemberID: m.ID, Role: role}
}

// NewSession returns a session token for the scope (tests skip the login form).
func (s *Store) NewSession(sc domain.Scope) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := randToken()
	s.sessions[t] = session{sc.MemberID, sc.HouseholdID}
	return t
}

func (s *Store) memberByEmail(email string) *member {
	id, ok := s.byEmail[strings.ToLower(strings.TrimSpace(email))]
	if !ok {
		return nil
	}
	return s.members[id]
}

// hh returns the caller's household after checking the member belongs to it.
// Callers hold s.mu.
func (s *Store) hh(sc domain.Scope) (*household, error) {
	m, ok := s.members[sc.MemberID]
	if !ok {
		return nil, domain.ErrUnauthorized
	}
	if _, ok := m.roles[sc.HouseholdID]; !ok {
		return nil, domain.ErrUnauthorized
	}
	h, ok := s.households[sc.HouseholdID]
	if !ok {
		return nil, domain.ErrUnauthorized
	}
	return h, nil
}

func (s *Store) publish(householdID uuid.UUID, e events.Event) {
	if s.hub != nil {
		s.hub.Publish(householdID, e)
	}
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// --- Auth -------------------------------------------------------------------

func (s *Store) Login(_ context.Context, email, password string) (string, domain.Scope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.memberByEmail(email)
	if m == nil || m.password == "" || m.password != password || len(m.joined) == 0 {
		return "", domain.Scope{}, fmt.Errorf("login: %w", domain.ErrUnauthorized)
	}
	hid := m.joined[0]
	t := randToken()
	s.sessions[t] = session{m.ID, hid}
	return t, domain.Scope{HouseholdID: hid, MemberID: m.ID, Role: m.roles[hid]}, nil
}

func (s *Store) Logout(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
	return nil
}

func (s *Store) scopeFor(sess session, ok bool) (domain.Scope, error) {
	if !ok {
		return domain.Scope{}, domain.ErrUnauthorized
	}
	m := s.members[sess.memberID]
	role, ok := m.roles[sess.householdID]
	if !ok {
		return domain.Scope{}, domain.ErrUnauthorized
	}
	return domain.Scope{HouseholdID: sess.householdID, MemberID: m.ID, Role: role}, nil
}

func (s *Store) SessionScope(_ context.Context, token string) (domain.Scope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	return s.scopeFor(sess, ok)
}

func (s *Store) BearerScope(_ context.Context, token string) (domain.Scope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.apiTokens[token]
	return s.scopeFor(sess, ok)
}

func (s *Store) SwitchHousehold(_ context.Context, token string, householdID uuid.UUID) (domain.Scope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok {
		return domain.Scope{}, domain.ErrUnauthorized
	}
	if _, ok := s.members[sess.memberID].roles[householdID]; !ok {
		return domain.Scope{}, fmt.Errorf("switch household: %w", domain.ErrNotFound)
	}
	sess.householdID = householdID
	s.sessions[token] = sess
	return s.scopeFor(sess, true)
}

func (s *Store) CreateAPIToken(_ context.Context, sc domain.Scope, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.hh(sc); err != nil {
		return "", err
	}
	if strings.TrimSpace(name) == "" {
		return "", &domain.ValidationError{Problems: []string{"name: required"}}
	}
	t := randToken()
	s.apiTokens[t] = session{sc.MemberID, sc.HouseholdID}
	return t, nil
}

// SignUpWithInvite creates a member from an invite and logs them in. It is
// the optional web.InviteSignup interface (not yet in service.Auth).
func (s *Store) SignUpWithInvite(_ context.Context, token, email, name, password string) (string, domain.Scope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var problems []string
	if !strings.Contains(email, "@") {
		problems = append(problems, "email: must be an email address")
	}
	if strings.TrimSpace(name) == "" {
		problems = append(problems, "name: required")
	}
	if len(password) < 8 {
		problems = append(problems, "password: at least 8 characters")
	}
	if len(problems) > 0 {
		return "", domain.Scope{}, &domain.ValidationError{Problems: problems}
	}
	if s.memberByEmail(email) != nil {
		return "", domain.Scope{}, fmt.Errorf("sign up: email already registered, log in instead: %w", domain.ErrConflict)
	}
	inv, err := s.usableInvite(token)
	if err != nil {
		return "", domain.Scope{}, err
	}
	m := &member{Member: domain.Member{ID: uuid.New(), Email: strings.ToLower(email), Name: name},
		password: password, roles: map[uuid.UUID]domain.Role{inv.householdID: inv.Role}, joined: []uuid.UUID{inv.householdID}}
	s.members[m.ID] = m
	s.byEmail[m.Email] = m.ID
	now := time.Now()
	inv.AcceptedAt = &now
	t := randToken()
	s.sessions[t] = session{m.ID, inv.householdID}
	return t, domain.Scope{HouseholdID: inv.householdID, MemberID: m.ID, Role: inv.Role}, nil
}

// ChangePassword mirrors auth.Service.ChangePassword: the current password
// must match, and every other session of the member is signed out.
func (s *Store) ChangePassword(_ context.Context, sc domain.Scope, keepToken, current, next string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.members[sc.MemberID]
	if m == nil {
		return fmt.Errorf("change password: %w", domain.ErrUnauthorized)
	}
	if m.password == "" || m.password != current {
		return &domain.ValidationError{Problems: []string{"current password: not correct"}}
	}
	if len(next) < 8 {
		return &domain.ValidationError{Problems: []string{"new password: at least 8 characters"}}
	}
	m.password = next
	for t, ss := range s.sessions {
		if ss.memberID == m.ID && t != keepToken {
			delete(s.sessions, t)
		}
	}
	return nil
}

// --- Households -------------------------------------------------------------

func (s *Store) Create(context.Context, domain.Scope, string, string) (domain.Household, error) {
	return domain.Household{}, domain.ErrNotImplemented
}

func (s *Store) Members(_ context.Context, sc domain.Scope) ([]domain.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.hh(sc); err != nil {
		return nil, err
	}
	var out []domain.Member
	for _, m := range s.members {
		if r, ok := m.roles[sc.HouseholdID]; ok {
			mm := m.Member
			mm.Role = r
			out = append(out, mm)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) Memberships(_ context.Context, sc domain.Scope) ([]domain.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.hh(sc); err != nil {
		return nil, err
	}
	m := s.members[sc.MemberID]
	var out []domain.Membership
	for _, hid := range m.joined {
		out = append(out, domain.Membership{HouseholdID: hid, HouseholdName: s.households[hid].Name, Role: m.roles[hid]})
	}
	return out, nil
}

func (s *Store) CreateInvite(_ context.Context, sc domain.Scope, role domain.Role) (domain.Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.hh(sc); err != nil {
		return domain.Invite{}, err
	}
	if !sc.IsHouseholdAdmin() {
		return domain.Invite{}, fmt.Errorf("create invite: %w", domain.ErrForbidden)
	}
	if role != domain.RoleAdmin && role != domain.RoleMember {
		return domain.Invite{}, &domain.ValidationError{Problems: []string{"role: must be admin or member"}}
	}
	t := randToken()
	inv := &invite{Invite: domain.Invite{ID: uuid.New(), Role: role, ExpiresAt: time.Now().Add(7 * 24 * time.Hour)}, householdID: sc.HouseholdID}
	s.invites[t] = inv
	out := inv.Invite
	out.Token = t
	return out, nil
}

func (s *Store) usableInvite(token string) (*invite, error) {
	inv, ok := s.invites[token]
	if !ok || inv.AcceptedAt != nil || time.Now().After(inv.ExpiresAt) {
		return nil, fmt.Errorf("invite: %w", domain.ErrNotFound)
	}
	return inv, nil
}

func (s *Store) AcceptInvite(_ context.Context, memberID uuid.UUID, token string) (domain.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.members[memberID]
	if !ok {
		return domain.Membership{}, domain.ErrUnauthorized
	}
	inv, err := s.usableInvite(token)
	if err != nil {
		return domain.Membership{}, err
	}
	if _, already := m.roles[inv.householdID]; !already {
		m.joined = append(m.joined, inv.householdID)
		m.roles[inv.householdID] = inv.Role
	}
	now := time.Now()
	inv.AcceptedAt = &now
	return domain.Membership{HouseholdID: inv.householdID, HouseholdName: s.households[inv.householdID].Name, Role: m.roles[inv.householdID]}, nil
}

func (s *Store) Usage(context.Context, domain.Scope, time.Time) ([]domain.UsageRow, error) {
	return nil, domain.ErrNotImplemented
}

// --- Profiles ---------------------------------------------------------------

func (s *Store) Get(_ context.Context, sc domain.Scope) (domain.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.Profile{}, err
	}
	return h.profileWithAvoid(), nil
}

func (h *household) profileWithAvoid() domain.Profile {
	p := h.profile
	p.Avoid = make([]string, len(h.avoid))
	for i, a := range h.avoid {
		p.Avoid[i] = a.Name
	}
	return p
}

func (s *Store) Put(_ context.Context, sc domain.Scope, p domain.Profile) (domain.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.Profile{}, err
	}
	var problems []string
	if p.TotalMeals() < 1 {
		problems = append(problems, "meal_targets: at least one meal a week")
	}
	for t, n := range p.MealTargets {
		if n < 0 || n > domain.MaxMealsPerType {
			problems = append(problems, fmt.Sprintf("meal_targets.%s: 0 to 14", t))
		}
	}
	if p.People < 1 || p.People > 12 {
		problems = append(problems, "people: must be 1 to 12")
	}
	if p.ShoppingDay != "" && !p.ShoppingDay.Valid() {
		problems = append(problems, "shopping_day: not a weekday")
	}
	if len(problems) > 0 {
		return domain.Profile{}, &domain.ValidationError{Problems: problems}
	}
	if p.Avoid != nil {
		h.avoid = nil
		for _, name := range p.Avoid {
			h.addAvoid(name, domain.AvoidFromProfile)
		}
	}
	if p.AutoConfirmTime == "" {
		p.AutoConfirmTime = "08:00"
	}
	h.profile = p
	return h.profileWithAvoid(), nil
}

func (h *household) addAvoid(name string, source domain.AvoidSource) domain.AvoidItem {
	name = strings.TrimSpace(name)
	key := shopping.Normalize(name)
	for _, a := range h.avoid {
		if a.NormalizedName == key {
			return a
		}
	}
	a := domain.AvoidItem{ID: uuid.New(), Name: name, NormalizedName: key, Source: source, AddedAt: time.Now()}
	h.avoid = append(h.avoid, a)
	return a
}

func (s *Store) AvoidItems(_ context.Context, sc domain.Scope) ([]domain.AvoidItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return nil, err
	}
	return slices.Clone(h.avoid), nil
}

func (s *Store) AddAvoid(_ context.Context, sc domain.Scope, name string, source domain.AvoidSource) (domain.AvoidItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.AvoidItem{}, err
	}
	if normalize(name) == "" {
		return domain.AvoidItem{}, &domain.ValidationError{Problems: []string{"name is required"}}
	}
	return h.addAvoid(name, source), nil
}

func (s *Store) DeleteAvoid(_ context.Context, sc domain.Scope, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(h.avoid, func(a domain.AvoidItem) bool { return a.ID == id })
	if i < 0 {
		return fmt.Errorf("delete avoid: %w", domain.ErrNotFound)
	}
	h.avoid = slices.Delete(h.avoid, i, i+1)
	return nil
}

func (s *Store) Sections(_ context.Context, sc domain.Scope) ([]domain.Section, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return nil, err
	}
	return slices.Clone(h.sections), nil
}

func (s *Store) PutSections(_ context.Context, sc domain.Scope, sections []domain.Section) ([]domain.Section, error) {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	out := slices.Clone(sections)
	sort.SliceStable(out, func(i, j int) bool { return out[i].SortOrder < out[j].SortOrder })
	h.sections = out
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.ListRebuilt})
	return slices.Clone(out), nil
}

func (s *Store) Pantry(_ context.Context, sc domain.Scope) ([]domain.PantryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return nil, err
	}
	return slices.Clone(h.pantry), nil
}

func (s *Store) PutPantry(_ context.Context, sc domain.Scope, names []string) ([]domain.PantryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return nil, err
	}
	h.pantry = nil
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		key := normalize(n)
		if n == "" || seen[key] {
			continue
		}
		seen[key] = true
		h.pantry = append(h.pantry, domain.PantryItem{Name: n, NormalizedName: key})
	}
	return slices.Clone(h.pantry), nil
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// --- Plans ------------------------------------------------------------------

func (s *Store) wait(ctx context.Context) error {
	if s.GenerateDelay <= 0 {
		return nil
	}
	select {
	case <-time.After(s.GenerateDelay):
		return nil
	case <-ctx.Done():
		return fmt.Errorf("generate: %w: %w", domain.ErrGeneration, ctx.Err())
	}
}

// Generate builds a plan from the fixed recipes. A prompt containing "fail"
// fails like a Claude error would, leaving the current plan active.
func (s *Store) Generate(ctx context.Context, sc domain.Scope, prompt string) (domain.Plan, error) {
	s.mu.Lock()
	if _, err := s.hh(sc); err != nil {
		s.mu.Unlock()
		return domain.Plan{}, err
	}
	s.mu.Unlock()
	if err := s.wait(ctx); err != nil {
		return domain.Plan{}, err
	}
	if strings.Contains(strings.ToLower(prompt), "fail") {
		return domain.Plan{}, fmt.Errorf("generate: model returned invalid meals twice: %w", domain.ErrGeneration)
	}
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.Plan{}, err
	}
	p := domain.Plan{ID: uuid.New(), WeekStart: weekStart(time.Now()), Status: domain.PlanActive, Prompt: prompt, CreatedAt: time.Now()}
	n := max(1, min(7, h.profile.MealTargets[domain.MealDinner]))
	for i := range n {
		m := h.nextMeal(domain.Weekdays[i], nil)
		p.Meals = append(p.Meals, m)
	}
	h.plan = &p
	h.rebuild(true)
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.ListRebuilt})
	return clonePlan(p), nil
}

func weekStart(t time.Time) time.Time {
	t = t.Truncate(24 * time.Hour)
	for t.Weekday() != time.Monday {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func (h *household) nextMeal(day domain.Weekday, exclude map[string]bool) domain.Meal {
	for range recipes {
		r := recipes[h.next%len(recipes)]
		h.next++
		if exclude[r.Title] {
			continue
		}
		m := r.instantiate(day, h.profile.People)
		h.meals[m.ID] = m
		return m
	}
	m := recipes[0].instantiate(day, h.profile.People)
	h.meals[m.ID] = m
	return m
}

func clonePlan(p domain.Plan) domain.Plan {
	p.Meals = slices.Clone(p.Meals)
	return p
}

func (s *Store) Current(_ context.Context, sc domain.Scope) (domain.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.Plan{}, err
	}
	if h.plan == nil {
		return domain.Plan{}, fmt.Errorf("current plan: %w", domain.ErrNotFound)
	}
	return clonePlan(*h.plan), nil
}

func (s *Store) SwapMeal(ctx context.Context, sc domain.Scope, planID uuid.UUID, day domain.Weekday, _ string) (domain.Meal, error) {
	if err := s.wait(ctx); err != nil {
		return domain.Meal{}, err
	}
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.Meal{}, err
	}
	if h.plan == nil || h.plan.ID != planID {
		s.mu.Unlock()
		return domain.Meal{}, fmt.Errorf("swap meal: plan %w", domain.ErrNotFound)
	}
	idx := slices.IndexFunc(h.plan.Meals, func(m domain.Meal) bool { return m.Day == day })
	if idx < 0 {
		s.mu.Unlock()
		return domain.Meal{}, fmt.Errorf("swap meal: no meal on %s: %w", day, domain.ErrNotFound)
	}
	exclude := map[string]bool{}
	for _, m := range h.plan.Meals {
		exclude[m.Title] = true
	}
	m := h.nextMeal(day, exclude)
	h.plan.Meals[idx] = m
	h.rebuild(false)
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.ListRebuilt})
	return m, nil
}

func (s *Store) Substitute(context.Context, domain.Scope, uuid.UUID, string, string) (domain.Meal, error) {
	return domain.Meal{}, domain.ErrNotImplemented
}

func (s *Store) Meal(_ context.Context, sc domain.Scope, mealID uuid.UUID) (domain.Meal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.Meal{}, err
	}
	m, ok := h.meals[mealID]
	if !ok {
		return domain.Meal{}, fmt.Errorf("meal: %w", domain.ErrNotFound)
	}
	if r := h.recipeFor(m.Title); r != nil {
		id := r.ID
		m.RecipeID, m.InLibrary = &id, r.InLibrary
	}
	return m, nil
}

// --- Lists ------------------------------------------------------------------

// rebuild regenerates generated items from the plan's meals, keeping ticks
// by normalized name and every manual item. Callers hold s.mu.
func (h *household) rebuild(newPlan bool) {
	type agg struct {
		item domain.ListItem
		qty  map[string]float64
	}
	pantry := map[string]bool{}
	for _, p := range h.pantry {
		pantry[p.NormalizedName] = true
	}
	old := map[string]domain.ListItem{}
	var manual []domain.ListItem
	for _, it := range h.items {
		if it.IsManual {
			if newPlan {
				it.PlanID = h.plan.ID
			}
			manual = append(manual, it)
			continue
		}
		old[normalize(it.Name)] = it
	}
	byName := map[string]*agg{}
	var order []string
	for _, m := range h.plan.Meals {
		for _, ing := range m.Ingredients {
			key := ing.NormalizedName
			a, ok := byName[key]
			if !ok {
				a = &agg{item: domain.ListItem{Name: key, Section: ing.Section, PlanID: h.plan.ID, CheckHome: pantry[key]}, qty: map[string]float64{}}
				byName[key] = a
				order = append(order, key)
			}
			a.qty[ing.Unit] += ing.Qty
		}
	}
	items := make([]domain.ListItem, 0, len(order)+len(manual))
	for _, key := range order {
		a := byName[key]
		var parts []string
		for unit, q := range a.qty {
			parts = append(parts, strings.TrimSpace(fmt.Sprintf("%g %s", q, unit)))
		}
		sort.Strings(parts)
		it := a.item
		it.NeedText = "need " + strings.Join(parts, " + ")
		if prev, ok := old[key]; ok && !newPlan {
			it.ID, it.Checked, it.CheckedBy, it.HaveIt = prev.ID, prev.Checked, prev.CheckedBy, prev.HaveIt
		} else {
			it.ID = uuid.New()
		}
		items = append(items, it)
	}
	h.items = append(items, manual...)
}

func (s *Store) list(h *household) domain.List {
	return shopping.Group(h.plan.ID, h.items, h.sections)
}

func (s *Store) Active(_ context.Context, sc domain.Scope) (domain.List, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.List{}, err
	}
	if h.plan == nil {
		return domain.List{}, fmt.Errorf("active list: %w", domain.ErrNotFound)
	}
	return s.list(h), nil
}

func (s *Store) Rebuild(_ context.Context, sc domain.Scope, planID uuid.UUID) (domain.List, error) {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.List{}, err
	}
	if h.plan == nil || h.plan.ID != planID {
		s.mu.Unlock()
		return domain.List{}, fmt.Errorf("rebuild: %w", domain.ErrNotFound)
	}
	h.rebuild(false)
	l := s.list(h)
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.ListRebuilt})
	return l, nil
}

func (s *Store) AddItem(_ context.Context, sc domain.Scope, name, section string) (domain.ListItem, error) {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.ListItem{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		s.mu.Unlock()
		return domain.ListItem{}, &domain.ValidationError{Problems: []string{"name: required"}}
	}
	if h.plan == nil {
		s.mu.Unlock()
		return domain.ListItem{}, fmt.Errorf("add item: no active plan: %w", domain.ErrNotFound)
	}
	if !slices.ContainsFunc(h.sections, func(x domain.Section) bool { return x.Key == section }) {
		section = "other"
	}
	it := domain.ListItem{ID: uuid.New(), PlanID: h.plan.ID, Name: name, Section: section, IsManual: true}
	h.items = append(h.items, it)
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.ItemChanged, ItemID: it.ID})
	return it, nil
}

func (s *Store) SetChecked(_ context.Context, sc domain.Scope, itemID uuid.UUID, checked bool) (domain.ListItem, error) {
	s.mu.Lock()
	if s.tickAt != nil {
		s.tickAt[itemID] = time.Now()
	}
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.ListItem{}, err
	}
	i := slices.IndexFunc(h.items, func(it domain.ListItem) bool { return it.ID == itemID })
	if i < 0 {
		s.mu.Unlock()
		return domain.ListItem{}, fmt.Errorf("set checked: %w", domain.ErrNotFound)
	}
	h.items[i].Checked = checked
	h.items[i].CheckedBy = nil
	if checked {
		by := sc.MemberID
		h.items[i].CheckedBy = &by
	}
	it := h.items[i]
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.ItemChanged, ItemID: itemID})
	return it, nil
}

func (s *Store) SetHaveIt(_ context.Context, sc domain.Scope, itemID uuid.UUID, haveIt *bool) (domain.ListItem, error) {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.ListItem{}, err
	}
	i := slices.IndexFunc(h.items, func(it domain.ListItem) bool { return it.ID == itemID && it.CheckHome })
	if i < 0 {
		s.mu.Unlock()
		return domain.ListItem{}, fmt.Errorf("have it: %w", domain.ErrNotFound)
	}
	h.items[i].HaveIt = nil
	if haveIt != nil {
		v := *haveIt
		h.items[i].HaveIt = &v
	}
	it := h.items[i]
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.ItemChanged, ItemID: itemID})
	return it, nil
}

func (s *Store) DeleteItem(_ context.Context, sc domain.Scope, itemID uuid.UUID) error {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	i := slices.IndexFunc(h.items, func(it domain.ListItem) bool { return it.ID == itemID && it.IsManual })
	if i < 0 {
		s.mu.Unlock()
		return fmt.Errorf("delete item: %w", domain.ErrNotFound)
	}
	h.items = slices.Delete(h.items, i, i+1)
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.ItemDeleted, ItemID: itemID})
	return nil
}

// --- Feedback ---------------------------------------------------------------

func (s *Store) Rate(_ context.Context, sc domain.Scope, mealID uuid.UUID, rating int, note string) (domain.Feedback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.Feedback{}, err
	}
	if _, ok := h.meals[mealID]; !ok {
		return domain.Feedback{}, fmt.Errorf("rate: meal %w", domain.ErrNotFound)
	}
	if rating < 1 || rating > 5 {
		return domain.Feedback{}, &domain.ValidationError{Problems: []string{"rating: must be 1 to 5"}}
	}
	f := domain.Feedback{ID: uuid.New(), MealID: mealID, Rating: rating, Note: note, CreatedAt: time.Now()}
	h.feedback = append(h.feedback, f)
	h.lib().outcomes[mealID] = domain.MealOutcome{MealID: mealID, Status: domain.Cooked, Rating: rating, Note: note, At: f.CreatedAt}
	h.putInLibrary(h.meals[mealID].Title)
	return f, nil
}

func (s *Store) SaveRecipe(_ context.Context, sc domain.Scope, mealID uuid.UUID) (domain.Recipe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.Recipe{}, err
	}
	m, ok := h.meals[mealID]
	if !ok {
		return domain.Recipe{}, fmt.Errorf("save recipe: meal %w", domain.ErrNotFound)
	}
	r := domain.Recipe{ID: uuid.New(), SourceMealID: &mealID, Meal: m, CreatedAt: time.Now()}
	h.recipes = append(h.recipes, r)
	return r, nil
}

func (s *Store) Recipes(_ context.Context, sc domain.Scope) ([]domain.Recipe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return nil, err
	}
	return slices.Clone(h.recipes), nil
}

// Feedback returns the household's ratings (for tests).
func (s *Store) FeedbackFor(householdID uuid.UUID) []domain.Feedback {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.households[householdID].feedback)
}
