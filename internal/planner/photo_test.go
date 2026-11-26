package planner_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// Design step C: a recipe's photo is stored once, replaced, shown on the
// recipe and the meals made from it, removed; another household can't
// read, set or remove it.
func TestRecipePhoto(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newImportEnv(t, nil)
	rec, err := d.plans.CreateRecipe(ctx, d.a, domain.MealJSON{
		MealType: domain.MealDinner, Title: "Leek soup", Minutes: 30, Servings: 4,
		Ingredients: []domain.IngredientJSON{{Name: "Leeks", Qty: 2, Unit: "pcs", Section: "produce"}},
		Steps:       []domain.StepJSON{{Text: "Cook."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if at, err := d.plans.PhotoAt(ctx, d.a, rec.ID); err != nil || at != nil {
		t.Fatalf("no photo yet: %v %v", at, err)
	}
	if _, err := d.plans.Photo(ctx, d.a, rec.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("photo before upload: %v", err)
	}
	var ve *domain.ValidationError
	if err := d.plans.SetPhoto(ctx, d.a, rec.ID, nil); !errors.As(err, &ve) {
		t.Errorf("empty photo: %v", err)
	}
	if err := d.plans.SetPhoto(ctx, d.a, rec.ID, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := d.plans.SetPhoto(ctx, d.a, rec.ID, []byte("second")); err != nil {
		t.Fatal(err)
	}
	p, err := d.plans.Photo(ctx, d.a, rec.ID)
	if err != nil || !bytes.Equal(p.JPEG, []byte("second")) {
		t.Errorf("photo: %q %v", p.JPEG, err)
	}
	if det, err := d.plans.RecipeDetail(ctx, d.a, rec.ID); err != nil || det.PhotoAt == nil {
		t.Errorf("detail has no photo time: %+v %v", det.PhotoAt, err)
	}

	// Another household: not found for every call, and A's photo stays.
	nf := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: want not found, got %v", what, err)
		}
	}
	nf("SetPhoto", d.plans.SetPhoto(ctx, d.b, rec.ID, []byte("theirs")))
	_, err = d.plans.Photo(ctx, d.b, rec.ID)
	nf("Photo", err)
	nf("DeletePhoto", d.plans.DeletePhoto(ctx, d.b, rec.ID))
	if at, _ := d.plans.PhotoAt(ctx, d.b, rec.ID); at != nil {
		t.Error("B sees A's photo time")
	}
	if p, _ := d.plans.Photo(ctx, d.a, rec.ID); !bytes.Equal(p.JPEG, []byte("second")) {
		t.Errorf("A's photo changed: %q", p.JPEG)
	}

	if err := d.plans.DeletePhoto(ctx, d.a, rec.ID); err != nil {
		t.Fatal(err)
	}
	nf("DeletePhoto again", d.plans.DeletePhoto(ctx, d.a, rec.ID))
	if at, _ := d.plans.PhotoAt(ctx, d.a, rec.ID); at != nil {
		t.Error("photo time after delete")
	}
}
