package planner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
)

// MaxPhotoBytes caps a stored recipe photo (a resized JPEG is far smaller).
const MaxPhotoBytes = 4 << 20

// SetPhoto stores the family's photo of a recipe, replacing an older one.
// Another household's recipe is not found.
func (s *Service) SetPhoto(ctx context.Context, sc domain.Scope, recipeID uuid.UUID, jpeg []byte) error {
	if len(jpeg) == 0 || len(jpeg) > MaxPhotoBytes {
		return &domain.ValidationError{Problems: []string{fmt.Sprintf("photo: a picture up to %d MB", MaxPhotoBytes>>20)}}
	}
	q := db.New(s.pool)
	if _, _, err := s.recipe(ctx, q, sc.HouseholdID, recipeID); err != nil {
		return err
	}
	if err := q.UpsertRecipePhoto(ctx, db.UpsertRecipePhotoParams{HouseholdID: sc.HouseholdID, RecipeID: recipeID, Photo: jpeg}); err != nil {
		return fmt.Errorf("save photo: %w", err)
	}
	return nil
}

// Photo is the family's photo of a recipe; none is ErrNotFound.
func (s *Service) Photo(ctx context.Context, sc domain.Scope, recipeID uuid.UUID) (domain.RecipePhoto, error) {
	p, err := db.New(s.pool).GetRecipePhoto(ctx, db.GetRecipePhotoParams{HouseholdID: sc.HouseholdID, RecipeID: recipeID})
	if err != nil {
		return domain.RecipePhoto{}, notFound("photo", err)
	}
	return domain.RecipePhoto{JPEG: p.Photo, UpdatedAt: p.UpdatedAt}, nil
}

// PhotoAt is when the recipe's photo was added, or nil without one.
func (s *Service) PhotoAt(ctx context.Context, sc domain.Scope, recipeID uuid.UUID) (*time.Time, error) {
	return photoAt(ctx, db.New(s.pool), sc.HouseholdID, recipeID)
}

func photoAt(ctx context.Context, q *db.Queries, householdID, recipeID uuid.UUID) (*time.Time, error) {
	t, err := q.GetRecipePhotoTime(ctx, db.GetRecipePhotoTimeParams{HouseholdID: householdID, RecipeID: recipeID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("photo time: %w", err)
	}
	return &t, nil
}

// DeletePhoto removes the recipe's photo; the drawing shows again.
func (s *Service) DeletePhoto(ctx context.Context, sc domain.Scope, recipeID uuid.UUID) error {
	n, err := db.New(s.pool).DeleteRecipePhoto(ctx, db.DeleteRecipePhotoParams{HouseholdID: sc.HouseholdID, RecipeID: recipeID})
	if err != nil {
		return fmt.Errorf("delete photo: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("delete photo: %w", domain.ErrNotFound)
	}
	return nil
}
