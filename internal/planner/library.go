package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	recipes "github.com/Zagoman/meal-planner/internal/household"
	"github.com/Zagoman/meal-planner/internal/jobs"
	"github.com/Zagoman/meal-planner/internal/service"
	"github.com/Zagoman/meal-planner/internal/shopping"
)

var (
	_ service.Outcomes = (*Service)(nil)
	_ service.Library  = (*Service)(nil)
)

const kindTweak = "tweak"

type tweakPayload struct {
	RecipeID uuid.UUID `json:"recipe_id"`
	Prompt   string    `json:"prompt"`
}

// ---- rating ------------------------------------------------------------------

// RateList is every meal of the active plan with its outcome.
func (s *Service) RateList(ctx context.Context, sc domain.Scope) (domain.RateList, error) {
	q := db.New(s.pool)
	active, err := q.GetActivePlan(ctx, sc.HouseholdID)
	if err != nil {
		return domain.RateList{}, notFound("active plan", err)
	}
	plan, err := s.loadPlan(ctx, q, sc.HouseholdID, active.ID)
	if err != nil {
		return domain.RateList{}, err
	}
	rows, err := q.ListOutcomesForPlan(ctx, db.ListOutcomesForPlanParams{PlanID: active.ID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.RateList{}, fmt.Errorf("outcomes: %w", err)
	}
	byMeal := map[uuid.UUID]db.MealOutcome{}
	for _, o := range rows {
		byMeal[o.MealID] = o
	}
	l := domain.RateList{Plan: plan, Meals: make([]domain.RateMeal, 0, len(plan.Meals))}
	for _, m := range plan.Meals {
		rm := domain.RateMeal{Meal: m}
		if o, ok := byMeal[m.ID]; ok {
			out := outcomeFromDB(o)
			rm.Outcome = &out
		}
		l.Meals = append(l.Meals, rm)
	}
	return l, nil
}

func outcomeFromDB(o db.MealOutcome) domain.MealOutcome {
	out := domain.MealOutcome{MealID: o.MealID, Status: domain.OutcomeStatus(o.Status), Note: o.Note, At: o.At}
	if o.Rating != nil {
		out.Rating = int(*o.Rating)
	}
	if o.MemberID.Valid {
		id := o.MemberID.UUID
		out.MemberID = &id
	}
	return out
}

// unrated reports whether the active plan has meals without an outcome.
func (s *Service) unrated(ctx context.Context, q *db.Queries, householdID uuid.UUID) (bool, error) {
	active, err := q.GetActivePlan(ctx, householdID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("active plan: %w", err)
	}
	n, err := q.CountMeals(ctx, db.CountMealsParams{PlanID: active.ID, HouseholdID: householdID})
	if err != nil {
		return false, fmt.Errorf("count meals: %w", err)
	}
	rows, err := q.ListOutcomesForPlan(ctx, db.ListOutcomesForPlanParams{PlanID: active.ID, HouseholdID: householdID})
	if err != nil {
		return false, fmt.Errorf("outcomes: %w", err)
	}
	return int64(len(rows)) < n, nil
}

// SetOutcome rates a meal, marks it not cooked, clears it, or changes the
// note of its outcome.
func (s *Service) SetOutcome(ctx context.Context, sc domain.Scope, mealID uuid.UUID, in domain.OutcomeInput) (domain.RateMeal, error) {
	var problems []string
	if in.Rating != 0 && (in.Rating < 1 || in.Rating > 5) {
		problems = append(problems, "rating: 1 to 5")
	}
	if in.Note != nil {
		n := strings.TrimSpace(*in.Note)
		in.Note = &n
		if utf8.RuneCountInString(n) > 1000 {
			problems = append(problems, "note: at most 1000 characters")
		}
	}
	if len(problems) > 0 {
		return domain.RateMeal{}, &domain.ValidationError{Problems: problems}
	}
	q := db.New(s.pool)
	m, err := q.GetMeal(ctx, db.GetMealParams{ID: mealID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.RateMeal{}, notFound("meal", err)
	}
	ref := db.GetOutcomeParams{MealID: mealID, HouseholdID: sc.HouseholdID}
	old, err := q.GetOutcome(ctx, ref)
	hasOld := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.RateMeal{}, fmt.Errorf("outcome: %w", err)
	}
	note := ""
	if hasOld {
		note = old.Note
	}
	if in.Note != nil {
		note = *in.Note
	}
	params := db.UpsertOutcomeParams{
		HouseholdID: sc.HouseholdID, MealID: mealID, Note: note,
		MemberID: uuid.NullUUID{UUID: sc.MemberID, Valid: sc.MemberID != uuid.Nil},
	}
	switch {
	case in.Clear:
		if err := q.DeleteOutcome(ctx, db.DeleteOutcomeParams(ref)); err != nil {
			return domain.RateMeal{}, fmt.Errorf("clear outcome: %w", err)
		}
		return s.rateMeal(ctx, q, m)
	case in.NotCooked:
		params.Status = string(domain.NotCooked)
	case in.Rating != 0:
		r := int32(in.Rating) //nolint:gosec // validated 1..5
		params.Status, params.Rating = string(domain.Cooked), &r
	case hasOld:
		params.Status, params.Rating = old.Status, old.Rating
	default:
		return domain.RateMeal{}, &domain.ValidationError{Problems: []string{"rate the meal or mark it not cooked first"}}
	}
	if _, err := q.UpsertOutcome(ctx, params); err != nil {
		return domain.RateMeal{}, fmt.Errorf("outcome: %w", err)
	}
	return s.rateMeal(ctx, q, m)
}

func (s *Service) rateMeal(ctx context.Context, q *db.Queries, m db.Meal) (domain.RateMeal, error) {
	meal, err := loadMeal(ctx, q, m)
	if err != nil {
		return domain.RateMeal{}, err
	}
	rm := domain.RateMeal{Meal: meal}
	o, err := q.GetOutcome(ctx, db.GetOutcomeParams{MealID: m.ID, HouseholdID: m.HouseholdID})
	if err == nil {
		out := outcomeFromDB(o)
		rm.Outcome = &out
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.RateMeal{}, fmt.Errorf("outcome: %w", err)
	}
	return rm, nil
}

// ---- library -----------------------------------------------------------------

// Search lists the library.
func (s *Service) Search(ctx context.Context, sc domain.Scope, q string, filter domain.RecipeFilter) ([]domain.RecipeSummary, error) {
	params := db.SearchRecipesParams{HouseholdID: sc.HouseholdID, Q: escapeLike(strings.TrimSpace(q))}
	switch filter {
	case domain.FilterAll, "":
	case domain.FilterRated4:
		params.Rated4 = true
	case domain.FilterOwn:
		params.Own = true
	default:
		t := domain.MealType(filter)
		if !t.Valid() {
			return nil, &domain.ValidationError{Problems: []string{fmt.Sprintf("unknown filter %q", filter)}}
		}
		params.MealType = string(t)
	}
	rows, err := db.New(s.pool).SearchRecipes(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("search recipes: %w", err)
	}
	out := make([]domain.RecipeSummary, len(rows))
	for i, r := range rows {
		out[i] = domain.RecipeSummary{
			ID: r.ID, Title: r.Title, MealType: domain.MealType(r.MealType), Minutes: int(r.Minutes),
			Source: domain.RecipeSource(r.Source), Tweaked: r.ParentRecipeID.Valid,
			RecipeStats: domain.RecipeStats{Rating: r.Rating, Ratings: int(r.Ratings), TimesCooked: int(r.Cooked)},
		}
	}
	return out, nil
}

// escapeLike makes q literal inside ILIKE '%q%'.
func escapeLike(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

func (s *Service) recipe(ctx context.Context, q *db.Queries, householdID, recipeID uuid.UUID) (db.Recipe, domain.MealJSON, error) {
	r, err := q.GetRecipe(ctx, db.GetRecipeParams{ID: recipeID, HouseholdID: householdID})
	if err != nil {
		return db.Recipe{}, domain.MealJSON{}, notFound("recipe", err)
	}
	var m domain.MealJSON
	if err := json.Unmarshal(r.Data, &m); err != nil {
		return db.Recipe{}, domain.MealJSON{}, fmt.Errorf("recipe %s data: %w", r.ID, err)
	}
	m.MealType, m.Title = domain.MealType(r.MealType), r.Title
	return r, m, nil
}

// RecipeDetail is a recipe with its ratings and tweak state.
func (s *Service) RecipeDetail(ctx context.Context, sc domain.Scope, recipeID uuid.UUID) (domain.RecipeDetail, error) {
	q := db.New(s.pool)
	r, _, err := s.recipe(ctx, q, sc.HouseholdID, recipeID)
	if err != nil {
		return domain.RecipeDetail{}, err
	}
	rec, err := recipes.RecipeFromDB(r)
	if err != nil {
		return domain.RecipeDetail{}, err
	}
	st, err := q.RecipeStats(ctx, db.RecipeStatsParams{RecipeID: uuid.NullUUID{UUID: recipeID, Valid: true}, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.RecipeDetail{}, fmt.Errorf("recipe stats: %w", err)
	}
	d := domain.RecipeDetail{Recipe: rec, RecipeStats: domain.RecipeStats{
		Rating: st.Rating, Ratings: int(st.Ratings), TimesCooked: int(st.Cooked), LatestNote: st.LatestNote,
	}}
	job, err := q.LatestRecipeJob(ctx, db.LatestRecipeJobParams{HouseholdID: sc.HouseholdID, RecipeID: recipeID.String()})
	switch {
	case err == nil:
		j := jobs.FromDB(job)
		d.Tweak = &j
		var res struct {
			RecipeID uuid.UUID `json:"recipe_id"`
		}
		if len(job.Result) > 0 && json.Unmarshal(job.Result, &res) == nil && res.RecipeID != uuid.Nil {
			d.TweakedID = &res.RecipeID
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return domain.RecipeDetail{}, fmt.Errorf("tweak job: %w", err)
	}
	return d, nil
}

// RequestTweak queues a tweak of a recipe.
func (s *Service) RequestTweak(ctx context.Context, sc domain.Scope, recipeID uuid.UUID, prompt string) (domain.Job, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return domain.Job{}, &domain.ValidationError{Problems: []string{"prompt: say what to change"}}
	}
	if utf8.RuneCountInString(prompt) > 500 {
		return domain.Job{}, &domain.ValidationError{Problems: []string{"prompt: at most 500 characters"}}
	}
	q := db.New(s.pool)
	if _, _, err := s.recipe(ctx, q, sc.HouseholdID, recipeID); err != nil {
		return domain.Job{}, err
	}
	if s.client == nil {
		return domain.Job{}, errNoClient
	}
	if job, err := q.LatestRecipeJob(ctx, db.LatestRecipeJobParams{HouseholdID: sc.HouseholdID, RecipeID: recipeID.String()}); err == nil &&
		domain.JobStatus(job.Status).Pending() {
		return jobs.FromDB(job), nil
	}
	job, err := jobs.Enqueue(ctx, q, jobs.New{
		HouseholdID: sc.HouseholdID, Kind: domain.JobTweak, MemberID: sc.MemberID,
		Payload: tweakPayload{RecipeID: recipeID, Prompt: prompt},
	})
	if err != nil {
		return domain.Job{}, err
	}
	s.publishEvent(sc.HouseholdID, events.Event{Kind: events.RecipeUpdated, RecipeID: recipeID})
	s.wake()
	return jobs.FromDB(job), nil
}

// runTweak asks Claude for a changed version and saves it as a new recipe.
func (s *Service) runTweak(ctx context.Context, job db.Job) (any, error) {
	var p tweakPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, fmt.Errorf("tweak payload: %w", err)
	}
	sc := jobScope(job)
	defer s.publishEvent(sc.HouseholdID, events.Event{Kind: events.RecipeUpdated, RecipeID: p.RecipeID})
	if err := s.needClient(); err != nil {
		return nil, err
	}
	q := db.New(s.pool)
	parent, orig, err := s.recipe(ctx, q, sc.HouseholdID, p.RecipeID)
	if err != nil {
		return nil, err
	}
	hh, err := s.loadHousehold(ctx, q, sc.HouseholdID)
	if err != nil {
		return nil, err
	}
	delete(hh.banned, domain.TitleKey(orig.Title)) // a tweak of a disliked meal is fine
	in := PromptInput{Profile: hh.profile, Sections: hh.sections, Request: p.Prompt, Banned: hh.bannedLines()}
	rules := hh.rules(&SwapRule{MealType: orig.MealType})
	mp, err := s.ask(ctx, sc, BuildSystem(hh.profile), BuildTweakPrompt(in, orig), rules, kindTweak)
	if err != nil {
		return nil, err
	}
	m := mp.Meals[0]
	m.Day = nil
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("tweak data: %w", err)
	}
	r, err := q.CreateRecipe(ctx, db.CreateRecipeParams{
		HouseholdID: sc.HouseholdID, Title: m.Title, Protein: string(m.Protein), Minutes: clamp32(m.Minutes),
		OnePot: m.OnePot, Servings: clamp32(m.Servings), Data: data, CreatedBy: job.MemberID,
		Source: parent.Source, ParentRecipeID: uuid.NullUUID{UUID: parent.ID, Valid: true},
		MealType: string(m.MealType), TitleKey: domain.TitleKey(m.Title),
	})
	if err != nil {
		return nil, fmt.Errorf("save tweak: %w", err)
	}
	return map[string]any{"recipe_id": r.ID}, nil
}

// insertRecipeMeal adds a library recipe to a plan as a meal.
func insertRecipeMeal(ctx context.Context, q *db.Queries, householdID, planID uuid.UUID, position int, r db.Recipe, from domain.MealSource) (db.Meal, error) {
	var m domain.MealJSON
	if err := json.Unmarshal(r.Data, &m); err != nil {
		return db.Meal{}, fmt.Errorf("recipe %s data: %w", r.ID, err)
	}
	after := m.AfterBabyPortion
	if after == nil {
		after = []string{}
	}
	meal, err := q.CreateMeal(ctx, db.CreateMealParams{
		HouseholdID: householdID, PlanID: planID, Day: pgtype.Text{}, MealType: r.MealType,
		Position: clamp32(position), Title: r.Title, Protein: r.Protein, Minutes: r.Minutes, OnePot: r.OnePot,
		Servings: r.Servings, AfterBabyPortion: after, RecipeID: uuid.NullUUID{UUID: r.ID, Valid: true}, AddedFrom: string(from),
	})
	if err != nil {
		return db.Meal{}, fmt.Errorf("create meal: %w", err)
	}
	return meal, insertMealParts(ctx, q, householdID, meal.ID, m)
}

// keptMeal is a meal the household added to a draft by hand.
type keptMeal struct {
	recipe   db.Recipe
	from     domain.MealSource
	Title    string
	MealType string
}

// handAdded lists the meals added by hand to the draft for week, which a
// newly made draft keeps.
func (s *Service) handAdded(ctx context.Context, q *db.Queries, householdID uuid.UUID, week time.Time) ([]keptMeal, error) {
	d, err := q.GetDraftPlanForWeek(ctx, db.GetDraftPlanForWeekParams{HouseholdID: householdID, WeekStart: week})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("draft: %w", err)
	}
	meals, err := q.ListMeals(ctx, db.ListMealsParams{PlanID: d.ID, HouseholdID: householdID})
	if err != nil {
		return nil, fmt.Errorf("list meals: %w", err)
	}
	var out []keptMeal
	for _, m := range meals {
		if m.AddedFrom == string(domain.FromClaude) || !m.RecipeID.Valid {
			continue
		}
		r, err := q.GetRecipe(ctx, db.GetRecipeParams{ID: m.RecipeID.UUID, HouseholdID: householdID})
		if err != nil {
			continue // recipe deleted meanwhile
		}
		out = append(out, keptMeal{recipe: r, from: domain.MealSource(m.AddedFrom), Title: m.Title, MealType: m.MealType})
	}
	return out, nil
}

// AddToNextWeek adds a recipe to the draft; without a draft, it makes an
// empty one for the next shopping week holding just this meal (the next
// "make the plan" fills it in and keeps the meal).
func (s *Service) AddToNextWeek(ctx context.Context, sc domain.Scope, recipeID uuid.UUID, from domain.MealSource) (domain.DraftMeal, error) {
	if from != domain.FromLibrary && from != domain.FromNotCooked {
		return domain.DraftMeal{}, &domain.ValidationError{Problems: []string{"from: library or not_cooked"}}
	}
	q := db.New(s.pool)
	r, _, err := s.recipe(ctx, q, sc.HouseholdID, recipeID)
	if err != nil {
		return domain.DraftMeal{}, err
	}
	p, err := s.profile(ctx, q, sc.HouseholdID)
	if err != nil {
		return domain.DraftMeal{}, err
	}
	var meal db.Meal
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		draft, err := qt.GetDraftPlan(ctx, sc.HouseholdID)
		if errors.Is(err, pgx.ErrNoRows) {
			week := nextShoppingDate(s.opts.Now().In(location(p)), p.ShoppingDay)
			if active, err := qt.GetActivePlan(ctx, sc.HouseholdID); err == nil && covers(active, week) {
				week = week.AddDate(0, 0, 7)
			}
			draft, err = qt.CreateDraftPlan(ctx, db.CreateDraftPlanParams{HouseholdID: sc.HouseholdID, WeekStart: week, CreatedBy: memberRef(sc)})
		}
		if err != nil {
			return fmt.Errorf("draft: %w", err)
		}
		pos, err := qt.MaxMealPosition(ctx, db.MaxMealPositionParams{PlanID: draft.ID, HouseholdID: sc.HouseholdID})
		if err != nil {
			return err
		}
		meal, err = insertRecipeMeal(ctx, qt, sc.HouseholdID, draft.ID, int(pos)+1, r, from)
		return err
	})
	if err != nil {
		return domain.DraftMeal{}, err
	}
	s.publishEvent(sc.HouseholdID, events.Event{Kind: events.PlanUpdated})
	return s.DraftMeal(ctx, sc, meal.ID)
}

// AddToThisWeek puts the recipe's ingredients that are not on the active
// list yet on it as unticked items with their amounts. Water never goes on.
func (s *Service) AddToThisWeek(ctx context.Context, sc domain.Scope, recipeID uuid.UUID) (int, error) {
	q := db.New(s.pool)
	_, m, err := s.recipe(ctx, q, sc.HouseholdID, recipeID)
	if err != nil {
		return 0, err
	}
	active, err := q.GetActivePlan(ctx, sc.HouseholdID)
	if err != nil {
		return 0, notFound("active plan", err)
	}
	secRows, err := q.ListSections(ctx, sc.HouseholdID)
	if err != nil {
		return 0, fmt.Errorf("sections: %w", err)
	}
	sections := make([]domain.Section, len(secRows))
	for i, r := range secRows {
		sections[i] = domain.Section{Key: r.Key, Name: r.Name, SortOrder: int(r.SortOrder)}
	}
	ings := make([]domain.Ingredient, len(m.Ingredients))
	for i, in := range m.Ingredients {
		ings[i] = domain.Ingredient{Name: in.Name, NormalizedName: in.NormalizedName, Qty: in.Qty, Unit: in.Unit, Section: in.Section}
	}
	added := 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		items, err := qt.ListItemsForPlan(ctx, db.ListItemsForPlanParams{PlanID: active.ID, HouseholdID: sc.HouseholdID})
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, it := range items {
			have[it.NormalizedName] = true
		}
		for _, it := range shopping.Build(ings, sections, nil) {
			if have[it.NormalizedName] {
				continue
			}
			if _, err := qt.AddRecipeListItem(ctx, db.AddRecipeListItemParams{
				HouseholdID: sc.HouseholdID, PlanID: active.ID, Name: it.Name, NormalizedName: it.NormalizedName,
				QtyText: it.QtyText, Section: it.Section, AddedBy: memberRef(sc),
			}); err != nil {
				return fmt.Errorf("add list item: %w", err)
			}
			added++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if added > 0 {
		s.publishEvent(sc.HouseholdID, events.Event{Kind: events.ListRebuilt})
	}
	return added, nil
}
