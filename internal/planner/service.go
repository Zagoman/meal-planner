package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Zagoman/meal-planner/internal/claude"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/service"
	"github.com/Zagoman/meal-planner/internal/shopping"
)

// Usage kinds logged with InsertUsage.
const (
	kindPlan  = "plan"
	kindSwap  = "swap"
	kindRetry = "retry"
)

// MaxPromptLen is the longest request text accepted, in characters.
const MaxPromptLen = 2000

// feedbackWindow is how far back ratings are sent with the prompt.
const feedbackWindow = 8 * 7 * 24 * time.Hour

// Options tune the service; zero values get defaults.
type Options struct {
	// Timeout bounds a whole Generate or SwapMeal (Config.GenerateTimeout).
	Timeout   time.Duration
	MaxTokens int
	Now       func() time.Time
	Logger    *slog.Logger
}

// Service implements service.Plans.
type Service struct {
	pool   *pgxpool.Pool
	client claude.Client
	events *events.Hub
	opts   Options
}

var _ service.Plans = (*Service)(nil)

// New returns the Plans service. client may be nil when no API key is
// configured: reads work and Generate/SwapMeal fail with domain.ErrGeneration.
func New(pool *pgxpool.Pool, client claude.Client, hub *events.Hub, opts Options) *Service {
	if opts.Timeout <= 0 {
		opts.Timeout = 55 * time.Second
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 16000
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Service{pool: pool, client: client, events: hub, opts: opts}
}

var errNoClient = fmt.Errorf("%w: plan generation is disabled (no ANTHROPIC_API_KEY)", domain.ErrGeneration)

// Generate asks Claude for a new week. The previous plan stays active until
// the new one is validated and saved; then both switch in one transaction
// together with the list rebuild.
func (s *Service) Generate(ctx context.Context, sc domain.Scope, prompt string) (domain.Plan, error) {
	if err := checkPrompt(prompt); err != nil {
		return domain.Plan{}, err
	}
	if s.client == nil {
		return domain.Plan{}, errNoClient
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()

	q := db.New(s.pool)
	hh, err := s.loadHousehold(ctx, q, sc.HouseholdID)
	if err != nil {
		return domain.Plan{}, err
	}
	in := PromptInput{Profile: hh.profile, Sections: hh.sections, Feedback: hh.feedback, Request: prompt}
	mp, err := s.ask(ctx, sc, BuildSystem(hh.profile), BuildPlanPrompt(in), hh.rules(""), kindPlan)
	if err != nil {
		return domain.Plan{}, err
	}

	draft, err := s.saveDraft(ctx, sc, prompt, mp)
	if err != nil {
		return domain.Plan{}, fmt.Errorf("%w: save draft: %w", domain.ErrGeneration, err)
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		if err := qt.ArchiveActivePlan(ctx, sc.HouseholdID); err != nil {
			return fmt.Errorf("archive active plan: %w", err)
		}
		n, err := qt.ActivatePlan(ctx, db.ActivatePlanParams{ID: draft.ID, HouseholdID: sc.HouseholdID})
		if err != nil {
			return fmt.Errorf("activate plan: %w", err)
		}
		if n != 1 {
			return errors.New("activate plan: draft not found")
		}
		return shopping.RebuildItems(ctx, qt, sc.HouseholdID, draft.ID)
	})
	if err != nil {
		if derr := q.DeleteDraftPlan(context.WithoutCancel(ctx), db.DeleteDraftPlanParams{ID: draft.ID, HouseholdID: sc.HouseholdID}); derr != nil {
			s.opts.Logger.Error("delete draft plan", "household_id", sc.HouseholdID, "plan_id", draft.ID, "err", derr)
		}
		return domain.Plan{}, fmt.Errorf("%w: activate plan: %w", domain.ErrGeneration, err)
	}
	s.publish(sc.HouseholdID)
	return s.loadPlan(context.WithoutCancel(ctx), q, sc.HouseholdID, draft.ID)
}

// Current returns the active plan with meals, ingredients and steps.
func (s *Service) Current(ctx context.Context, sc domain.Scope) (domain.Plan, error) {
	q := db.New(s.pool)
	p, err := q.GetActivePlan(ctx, sc.HouseholdID)
	if err != nil {
		return domain.Plan{}, notFound("active plan", err)
	}
	return s.loadPlan(ctx, q, sc.HouseholdID, p.ID)
}

// Meal returns one of the household's meals.
func (s *Service) Meal(ctx context.Context, sc domain.Scope, mealID uuid.UUID) (domain.Meal, error) {
	q := db.New(s.pool)
	m, err := q.GetMeal(ctx, db.GetMealParams{ID: mealID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Meal{}, notFound("meal", err)
	}
	return loadMeal(ctx, q, m)
}

// SwapMeal replaces (or fills) one day of the active plan. The list is
// rebuilt with upserts, so ticks on items still needed and manual items stay.
func (s *Service) SwapMeal(ctx context.Context, sc domain.Scope, planID uuid.UUID, day domain.Weekday, prompt string) (domain.Meal, error) {
	if !day.Valid() {
		return domain.Meal{}, &domain.ValidationError{Problems: []string{fmt.Sprintf("unknown day %q", day)}}
	}
	if err := checkPrompt(prompt); err != nil {
		return domain.Meal{}, err
	}
	q := db.New(s.pool)
	plan, err := q.GetPlan(ctx, db.GetPlanParams{ID: planID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Meal{}, notFound("plan", err)
	}
	if plan.Status != string(domain.PlanActive) {
		return domain.Meal{}, fmt.Errorf("swap meal: only the active plan can be changed: %w", domain.ErrConflict)
	}
	if s.client == nil {
		return domain.Meal{}, errNoClient
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()

	meals, err := q.ListMeals(ctx, db.ListMealsParams{PlanID: planID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Meal{}, fmt.Errorf("list meals: %w", err)
	}
	var old *db.Meal
	var others []string
	for i := range meals {
		if meals[i].Day == string(day) {
			old = &meals[i]
		} else {
			others = append(others, meals[i].Title)
		}
	}
	hh, err := s.loadHousehold(ctx, q, sc.HouseholdID)
	if err != nil {
		return domain.Meal{}, err
	}
	in := SwapInput{
		PromptInput: PromptInput{Profile: hh.profile, Sections: hh.sections, Feedback: hh.feedback, Request: prompt},
		Day:         day, OtherMeals: others,
	}
	if old != nil {
		in.Current = old.Title
	}
	mp, err := s.ask(ctx, sc, BuildSystem(hh.profile), BuildSwapPrompt(in), hh.rules(day), kindSwap)
	if err != nil {
		return domain.Meal{}, err
	}

	var created db.Meal
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		if old != nil {
			if err := qt.DeleteMeal(ctx, db.DeleteMealParams{ID: old.ID, HouseholdID: sc.HouseholdID}); err != nil {
				return fmt.Errorf("delete meal: %w", err)
			}
		}
		var err error
		if created, err = insertMeal(ctx, qt, sc.HouseholdID, planID, mp.Meals[0]); err != nil {
			return err
		}
		return shopping.RebuildItems(ctx, qt, sc.HouseholdID, planID)
	})
	if err != nil {
		return domain.Meal{}, fmt.Errorf("%w: save swapped meal: %w", domain.ErrGeneration, err)
	}
	s.publish(sc.HouseholdID)
	return loadMeal(context.WithoutCancel(ctx), q, created)
}

// Substitute replaces one ingredient of a meal (matched by normalized name)
// keeping its quantity, unit and section, and rebuilds the plan's list.
func (s *Service) Substitute(ctx context.Context, sc domain.Scope, mealID uuid.UUID, from, to string) (domain.Meal, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	var problems []string
	if shopping.Normalize(from) == "" {
		problems = append(problems, "from is required")
	}
	if shopping.Normalize(to) == "" {
		problems = append(problems, "to is required")
	}
	if utf8.RuneCountInString(to) > 200 {
		problems = append(problems, "to is longer than 200 characters")
	}
	if len(problems) > 0 {
		return domain.Meal{}, &domain.ValidationError{Problems: problems}
	}
	q := db.New(s.pool)
	meal, err := q.GetMeal(ctx, db.GetMealParams{ID: mealID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Meal{}, notFound("meal", err)
	}
	hh, err := s.loadHousehold(ctx, q, sc.HouseholdID)
	if err != nil {
		return domain.Meal{}, err
	}
	for _, term := range hh.profile.Avoid {
		if shopping.ContainsTerm(to, term) {
			return domain.Meal{}, &domain.ValidationError{Problems: []string{fmt.Sprintf("%q is on the avoid list (%q)", to, term)}}
		}
	}
	ings, err := q.ListIngredientsForMeal(ctx, db.ListIngredientsForMealParams{MealID: mealID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Meal{}, fmt.Errorf("list ingredients: %w", err)
	}
	key := shopping.Normalize(from)
	var target *db.Ingredient
	for i := range ings {
		if ings[i].NormalizedName == key || shopping.Normalize(ings[i].Name) == key {
			target = &ings[i]
			break
		}
	}
	if target == nil {
		return domain.Meal{}, &domain.ValidationError{Problems: []string{fmt.Sprintf("the meal has no ingredient %q", from)}}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		n, err := qt.UpdateIngredient(ctx, db.UpdateIngredientParams{
			ID: target.ID, HouseholdID: sc.HouseholdID, Name: to, NormalizedName: shopping.Normalize(to),
			Qty: target.Qty, Unit: target.Unit, Section: target.Section,
		})
		if err != nil {
			return fmt.Errorf("update ingredient: %w", err)
		}
		if n != 1 {
			return fmt.Errorf("update ingredient: %w", domain.ErrNotFound)
		}
		return shopping.RebuildItems(ctx, qt, sc.HouseholdID, meal.PlanID)
	})
	if err != nil {
		return domain.Meal{}, err
	}
	s.publish(sc.HouseholdID)
	return loadMeal(ctx, q, meal)
}

// ask calls Claude, validates, and retries once with the problems named.
func (s *Service) ask(ctx context.Context, sc domain.Scope, system, user string, rules Rules, kind string) (domain.MealPlanJSON, error) {
	req := claude.Request{
		System:    system,
		Messages:  []claude.Message{{Role: "user", Content: user}},
		Schema:    domain.MealPlanSchema(),
		MaxTokens: s.opts.MaxTokens,
	}
	log := s.opts.Logger.With("household_id", sc.HouseholdID, "kind", kind)
	for attempt := 0; ; attempt++ {
		k := kind
		if attempt > 0 {
			k = kindRetry
		}
		resp, err := s.client.Generate(ctx, req)
		s.logUsage(ctx, sc, k, resp)
		if err != nil {
			log.Warn("claude call failed", "attempt", attempt+1, "err", err)
			return domain.MealPlanJSON{}, fmt.Errorf("%w: claude: %w", domain.ErrGeneration, err)
		}
		mp, problems := Validate(resp.JSON, rules)
		if len(problems) == 0 {
			return mp, nil
		}
		log.Warn("claude answer rejected", "attempt", attempt+1, "problems", len(problems))
		log.Debug("claude answer problems", "problems", problems)
		if attempt == 1 {
			return domain.MealPlanJSON{}, fmt.Errorf("%w: answer still invalid after retry: %s",
				domain.ErrGeneration, strings.Join(problems, "; "))
		}
		req.Messages = append(req.Messages,
			claude.Message{Role: "assistant", Content: string(resp.JSON)},
			claude.Message{Role: "user", Content: BuildRetryPrompt(problems)},
		)
	}
}

func (s *Service) logUsage(ctx context.Context, sc domain.Scope, kind string, resp claude.Response) {
	if resp.Usage == (claude.Usage{}) {
		return
	}
	model := resp.Model
	if model == "" {
		model = "unknown"
	}
	err := db.New(s.pool).InsertUsage(context.WithoutCancel(ctx), db.InsertUsageParams{
		HouseholdID:  sc.HouseholdID,
		MemberID:     uuid.NullUUID{UUID: sc.MemberID, Valid: sc.MemberID != uuid.Nil},
		Kind:         kind,
		Model:        model,
		InputTokens:  clamp32(resp.Usage.InputTokens),
		OutputTokens: clamp32(resp.Usage.OutputTokens),
	})
	if err != nil {
		s.opts.Logger.Error("insert usage", "household_id", sc.HouseholdID, "err", err)
	}
}

func (s *Service) publish(householdID uuid.UUID) {
	if s.events != nil {
		s.events.Publish(householdID, events.Event{Kind: events.ListRebuilt})
	}
}

// saveDraft stores the plan as a draft with all meals in one transaction, so
// a failure leaves nothing behind.
func (s *Service) saveDraft(ctx context.Context, sc domain.Scope, prompt string, mp domain.MealPlanJSON) (db.Plan, error) {
	var draft db.Plan
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := db.New(tx)
		var err error
		draft, err = qt.CreatePlan(ctx, db.CreatePlanParams{
			HouseholdID: sc.HouseholdID,
			WeekStart:   WeekStart(s.opts.Now()),
			Prompt:      prompt,
			CreatedBy:   uuid.NullUUID{UUID: sc.MemberID, Valid: sc.MemberID != uuid.Nil},
		})
		if err != nil {
			return fmt.Errorf("create plan: %w", err)
		}
		for _, m := range mp.Meals {
			if _, err := insertMeal(ctx, qt, sc.HouseholdID, draft.ID, m); err != nil {
				return err
			}
		}
		return nil
	})
	return draft, err
}

func insertMeal(ctx context.Context, q *db.Queries, householdID, planID uuid.UUID, m domain.MealJSON) (db.Meal, error) {
	after := m.AfterBabyPortion
	if after == nil {
		after = []string{}
	}
	meal, err := q.CreateMeal(ctx, db.CreateMealParams{
		HouseholdID: householdID, PlanID: planID, Day: string(m.Day), Title: m.Title,
		Protein: string(m.Protein), Minutes: clamp32(m.Minutes), OnePot: m.OnePot,
		Servings: clamp32(m.Servings), AfterBabyPortion: after,
	})
	if err != nil {
		return db.Meal{}, fmt.Errorf("create meal: %w", err)
	}
	for i, ing := range m.Ingredients {
		norm := shopping.Normalize(ing.NormalizedName)
		if norm == "" {
			norm = shopping.Normalize(ing.Name)
		}
		if err := q.CreateIngredient(ctx, db.CreateIngredientParams{
			HouseholdID: householdID, MealID: meal.ID, Position: clamp32(i), Name: ing.Name,
			NormalizedName: norm, Qty: ing.Qty, Unit: ing.Unit, Section: ing.Section,
		}); err != nil {
			return db.Meal{}, fmt.Errorf("create ingredient: %w", err)
		}
	}
	for i, st := range m.Steps {
		var timer *int32
		if st.TimerMin != nil {
			t := clamp32(*st.TimerMin)
			timer = &t
		}
		if err := q.CreateStep(ctx, db.CreateStepParams{
			HouseholdID: householdID, MealID: meal.ID, Position: clamp32(i), Text: st.Text,
			TimerMin: timer, BabyPortion: st.BabyPortion,
		}); err != nil {
			return db.Meal{}, fmt.Errorf("create step: %w", err)
		}
	}
	return meal, nil
}

func (s *Service) loadPlan(ctx context.Context, q *db.Queries, householdID, planID uuid.UUID) (domain.Plan, error) {
	p, err := q.GetPlan(ctx, db.GetPlanParams{ID: planID, HouseholdID: householdID})
	if err != nil {
		return domain.Plan{}, notFound("plan", err)
	}
	rows, err := q.ListMeals(ctx, db.ListMealsParams{PlanID: planID, HouseholdID: householdID})
	if err != nil {
		return domain.Plan{}, fmt.Errorf("list meals: %w", err)
	}
	plan := domain.Plan{
		ID: p.ID, WeekStart: p.WeekStart, Status: domain.PlanStatus(p.Status), Prompt: p.Prompt,
		CreatedAt: p.CreatedAt, Meals: make([]domain.Meal, 0, len(rows)),
	}
	for _, r := range rows {
		m, err := loadMeal(ctx, q, r)
		if err != nil {
			return domain.Plan{}, err
		}
		plan.Meals = append(plan.Meals, m)
	}
	return plan, nil
}

func loadMeal(ctx context.Context, q *db.Queries, r db.Meal) (domain.Meal, error) {
	ings, err := q.ListIngredientsForMeal(ctx, db.ListIngredientsForMealParams{MealID: r.ID, HouseholdID: r.HouseholdID})
	if err != nil {
		return domain.Meal{}, fmt.Errorf("list ingredients: %w", err)
	}
	steps, err := q.ListStepsForMeal(ctx, db.ListStepsForMealParams{MealID: r.ID, HouseholdID: r.HouseholdID})
	if err != nil {
		return domain.Meal{}, fmt.Errorf("list steps: %w", err)
	}
	m := domain.Meal{
		ID: r.ID, Day: domain.Weekday(r.Day), Title: r.Title, Protein: domain.Protein(r.Protein),
		Minutes: int(r.Minutes), OnePot: r.OnePot, Servings: int(r.Servings),
		Ingredients: make([]domain.Ingredient, len(ings)), Steps: make([]domain.Step, len(steps)),
		AfterBabyPortion: r.AfterBabyPortion,
	}
	if m.AfterBabyPortion == nil {
		m.AfterBabyPortion = []string{}
	}
	for i, in := range ings {
		m.Ingredients[i] = shopping.IngredientFromDB(in)
	}
	for i, st := range steps {
		m.Steps[i] = domain.Step{Text: st.Text, BabyPortion: st.BabyPortion}
		if st.TimerMin != nil {
			t := int(*st.TimerMin)
			m.Steps[i].TimerMin = &t
		}
	}
	return m, nil
}

// household is what every prompt and validation needs.
type household struct {
	profile  domain.Profile
	sections []domain.Section
	feedback []FeedbackLine
}

func (h household) rules(day domain.Weekday) Rules {
	keys := make([]string, len(h.sections))
	for i, s := range h.sections {
		keys[i] = s.Key
	}
	return Rules{Profile: h.profile, SectionKeys: keys, Day: day}
}

func (s *Service) loadHousehold(ctx context.Context, q *db.Queries, householdID uuid.UUID) (household, error) {
	var h household
	pr, err := q.GetProfile(ctx, householdID)
	if err != nil {
		return h, notFound("profile", err)
	}
	h.profile = ProfileFromDB(pr)
	secs, err := q.ListSections(ctx, householdID)
	if err != nil {
		return h, fmt.Errorf("list sections: %w", err)
	}
	for _, r := range secs {
		h.sections = append(h.sections, domain.Section{Key: r.Key, Name: r.Name, SortOrder: int(r.SortOrder)})
	}
	fb, err := q.ListRecentFeedback(ctx, db.ListRecentFeedbackParams{
		HouseholdID: householdID, Since: s.opts.Now().Add(-feedbackWindow),
	})
	if err != nil {
		return h, fmt.Errorf("list feedback: %w", err)
	}
	for _, f := range fb {
		h.feedback = append(h.feedback, FeedbackLine{
			Title: f.Title, Protein: f.Protein, Rating: int(f.Rating), Note: f.Note, At: f.CreatedAt,
		})
	}
	return h, nil
}

// ProfileFromDB converts a stored profile.
func ProfileFromDB(p db.Profile) domain.Profile {
	out := domain.Profile{
		DinnersPerWeek: int(p.DinnersPerWeek), Servings: int(p.Servings),
		SpiceLevel: domain.SpiceLevel(p.SpiceLevel), OnePot: p.OnePot, Avoid: p.Avoid, Shop: p.Shop,
		ShoppingDay: domain.Weekday(p.ShoppingDay), BabyPortions: p.BabyPortions, Language: p.Language,
	}
	if len(p.ProteinMix) > 0 {
		_ = json.Unmarshal(p.ProteinMix, &out.ProteinMix) // malformed mix is ignored, not fatal
	}
	return out
}

// WeekStart is the Monday a new plan is for: today if it is Monday,
// otherwise the next Monday.
func WeekStart(now time.Time) time.Time {
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	offset := (int(time.Monday) - int(d.Weekday()) + 7) % 7
	return d.AddDate(0, 0, offset)
}

func checkPrompt(p string) error {
	if utf8.RuneCountInString(p) > MaxPromptLen {
		return &domain.ValidationError{Problems: []string{fmt.Sprintf("prompt is longer than %d characters", MaxPromptLen)}}
	}
	return nil
}

func clamp32(n int) int32 {
	switch {
	case n > math.MaxInt32:
		return math.MaxInt32
	case n < math.MinInt32:
		return math.MinInt32
	}
	return int32(n)
}

func notFound(what string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("get %s: %w", what, domain.ErrNotFound)
	}
	return fmt.Errorf("get %s: %w", what, err)
}
