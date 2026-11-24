package household

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
)

const maxNote = 1000

// Rate records a rating of a meal of the household: the meal's outcome
// becomes "cooked" with that rating (one outcome per meal; rating again
// replaces it).
func (s *Service) Rate(ctx context.Context, sc domain.Scope, mealID uuid.UUID, rating int, note string) (domain.Feedback, error) {
	note = strings.TrimSpace(note)
	var problems []string
	if rating < 1 || rating > 5 {
		problems = append(problems, "rating: 1 to 5")
	}
	if len(note) > maxNote {
		problems = append(problems, "note: at most 1000 characters")
	}
	if len(problems) > 0 {
		return domain.Feedback{}, &domain.ValidationError{Problems: problems}
	}
	if _, err := s.q.GetMeal(ctx, db.GetMealParams{ID: mealID, HouseholdID: sc.HouseholdID}); err != nil {
		return domain.Feedback{}, notFound("rate meal", err)
	}
	r := int32(rating) //nolint:gosec // validated 1..5
	o, err := s.q.UpsertOutcome(ctx, db.UpsertOutcomeParams{
		HouseholdID: sc.HouseholdID, MealID: mealID, Status: string(domain.Cooked),
		Rating: &r, Note: note, MemberID: uuid.NullUUID{UUID: sc.MemberID, Valid: sc.MemberID != uuid.Nil},
	})
	if err != nil {
		return domain.Feedback{}, fmt.Errorf("rate meal: %w", err)
	}
	// An outcome is one per meal, so its id is the meal's.
	return domain.Feedback{ID: o.MealID, MealID: o.MealID, Rating: rating, Note: o.Note, CreatedAt: o.At}, nil
}

// SaveRecipe returns the library recipe of a meal of the household,
// saving the meal to the library first if it has none. The recipe survives
// the plan being archived or deleted.
func (s *Service) SaveRecipe(ctx context.Context, sc domain.Scope, mealID uuid.UUID) (domain.Recipe, error) {
	row, err := s.q.GetMeal(ctx, db.GetMealParams{ID: mealID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Recipe{}, notFound("save recipe", err)
	}
	if !row.RecipeID.Valid {
		m, err := loadMeal(ctx, s.q, sc, mealID)
		if err != nil {
			return domain.Recipe{}, fmt.Errorf("save recipe: %w", err)
		}
		data, err := json.Marshal(mealToJSON(m))
		if err != nil {
			return domain.Recipe{}, fmt.Errorf("save recipe: %w", err)
		}
		id, err := s.q.UpsertClaudeRecipe(ctx, db.UpsertClaudeRecipeParams{
			HouseholdID:  sc.HouseholdID,
			SourceMealID: uuid.NullUUID{UUID: m.ID, Valid: true},
			Title:        m.Title,
			Protein:      string(m.Protein),
			Minutes:      int32(m.Minutes), //nolint:gosec // from the database
			OnePot:       m.OnePot,
			Servings:     int32(m.Servings), //nolint:gosec // from the database
			Data:         data,
			CreatedBy:    uuid.NullUUID{UUID: sc.MemberID, Valid: sc.MemberID != uuid.Nil},
			MealType:     string(m.MealType),
			TitleKey:     domain.TitleKey(m.Title),
		})
		if err != nil {
			return domain.Recipe{}, fmt.Errorf("save recipe: %w", err)
		}
		if err := s.q.SetMealRecipe(ctx, db.SetMealRecipeParams{ID: mealID, HouseholdID: sc.HouseholdID, RecipeID: uuid.NullUUID{UUID: id, Valid: true}}); err != nil {
			return domain.Recipe{}, fmt.Errorf("save recipe: %w", err)
		}
		row.RecipeID = uuid.NullUUID{UUID: id, Valid: true}
	}
	r, err := s.q.GetRecipe(ctx, db.GetRecipeParams{ID: row.RecipeID.UUID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Recipe{}, notFound("save recipe", err)
	}
	return RecipeFromDB(r)
}

func (s *Service) Recipes(ctx context.Context, sc domain.Scope) ([]domain.Recipe, error) {
	rows, err := s.q.ListRecipes(ctx, sc.HouseholdID)
	if err != nil {
		return nil, fmt.Errorf("recipes: %w", err)
	}
	out := make([]domain.Recipe, 0, len(rows))
	for _, r := range rows {
		rec, err := RecipeFromDB(r)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// loadMeal reads a meal of the household with its ingredients and steps.
func loadMeal(ctx context.Context, q *db.Queries, sc domain.Scope, mealID uuid.UUID) (domain.Meal, error) {
	m, err := q.GetMeal(ctx, db.GetMealParams{ID: mealID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Meal{}, notFound("meal", err)
	}
	ings, err := q.ListIngredientsForMeal(ctx, db.ListIngredientsForMealParams{MealID: m.ID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Meal{}, err
	}
	steps, err := q.ListStepsForMeal(ctx, db.ListStepsForMealParams{MealID: m.ID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Meal{}, err
	}
	out := domain.Meal{
		ID: m.ID, MealType: domain.MealType(m.MealType), Day: domain.Weekday(m.Day.String),
		AddedFrom: domain.MealSource(m.AddedFrom), Title: m.Title, Protein: domain.Protein(m.Protein),
		Minutes: int(m.Minutes), OnePot: m.OnePot, Servings: int(m.Servings),
		Ingredients: make([]domain.Ingredient, 0, len(ings)), Steps: make([]domain.Step, 0, len(steps)),
		AfterBabyPortion: nonNil(m.AfterBabyPortion),
	}
	for _, i := range ings {
		out.Ingredients = append(out.Ingredients, domain.Ingredient{
			ID: i.ID, Name: i.Name, NormalizedName: i.NormalizedName, Qty: i.Qty, Unit: i.Unit, Section: i.Section,
		})
	}
	for _, st := range steps {
		var timer *int
		if st.TimerMin != nil {
			t := int(*st.TimerMin)
			timer = &t
		}
		out.Steps = append(out.Steps, domain.Step{Text: st.Text, TimerMin: timer, BabyPortion: st.BabyPortion})
	}
	return out, nil
}

func mealToJSON(m domain.Meal) domain.MealJSON {
	j := domain.MealJSON{
		MealType: m.MealType, Title: m.Title, Protein: m.Protein, Minutes: m.Minutes, OnePot: m.OnePot,
		Servings: m.Servings, AfterBabyPortion: nonNil(m.AfterBabyPortion),
		Ingredients: make([]domain.IngredientJSON, 0, len(m.Ingredients)),
		Steps:       make([]domain.StepJSON, 0, len(m.Steps)),
	}
	if m.Day != "" {
		d := m.Day
		j.Day = &d
	}
	for _, i := range m.Ingredients {
		j.Ingredients = append(j.Ingredients, domain.IngredientJSON{
			Name: i.Name, NormalizedName: i.NormalizedName, Qty: i.Qty, Unit: i.Unit, Section: i.Section,
		})
	}
	for _, st := range m.Steps {
		j.Steps = append(j.Steps, domain.StepJSON(st))
	}
	return j
}

// RecipeFromDB rebuilds the stored meal. Recipe.Meal.ID is the source meal
// (zero if that meal is gone); ingredient IDs are not kept.
func RecipeFromDB(r db.Recipe) (domain.Recipe, error) {
	var j domain.MealJSON
	if err := json.Unmarshal(r.Data, &j); err != nil {
		return domain.Recipe{}, fmt.Errorf("recipe %s data: %w", r.ID, err)
	}
	m := domain.Meal{
		MealType: j.MealType, Title: r.Title, Protein: domain.Protein(r.Protein), Minutes: int(r.Minutes),
		OnePot: r.OnePot, Servings: int(r.Servings), AfterBabyPortion: nonNil(j.AfterBabyPortion),
		Ingredients: make([]domain.Ingredient, 0, len(j.Ingredients)), Steps: make([]domain.Step, 0, len(j.Steps)),
	}
	m.MealType = domain.MealType(r.MealType)
	if j.Day != nil {
		m.Day = *j.Day
	}
	for _, i := range j.Ingredients {
		m.Ingredients = append(m.Ingredients, domain.Ingredient{
			Name: i.Name, NormalizedName: i.NormalizedName, Qty: i.Qty, Unit: i.Unit, Section: i.Section,
		})
	}
	for _, st := range j.Steps {
		m.Steps = append(m.Steps, domain.Step(st))
	}
	rec := domain.Recipe{ID: r.ID, Meal: m, CreatedAt: r.CreatedAt, Source: domain.RecipeSource(r.Source)}
	if r.ParentRecipeID.Valid {
		id := r.ParentRecipeID.UUID
		rec.ParentID = &id
	}
	if r.SourceMealID.Valid {
		id := r.SourceMealID.UUID
		rec.SourceMealID = &id
		rec.Meal.ID = id
	}
	return rec, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func notFound(what string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, domain.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", what, err)
}
