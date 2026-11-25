package planner_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Zagoman/meal-planner/internal/claude"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/household"
	"github.com/Zagoman/meal-planner/internal/jobs"
	"github.com/Zagoman/meal-planner/internal/planner"
)

const cookbookJSON = `{"recipe":{"meal_type":"dinner","title":"Fiskefrikadeller","protein":"fish","minutes":40,"one_pot":false,"servings":4,
 "ingredients":[{"name":"torsk","normalized_name":"cod","qty":500,"unit":"g","section":"meat_fish"},
                {"name":"rugmel","normalized_name":"rye flour","qty":0,"unit":"g","section":"dry_goods"}],
 "steps":[{"text":"Blend fisken med løg og mel.","timer_min":null,"baby_portion":false},
          {"text":"Steg frikadellerne.","timer_min":10,"baby_portion":false}],
 "after_baby_portion":[]},"uncertain":["ingredients[1].qty"]}`

func newImportEnv(t *testing.T, fetch func(context.Context, string) (string, error), replies ...claude.FakeReply) *draftEnv {
	t.Helper()
	d := newDraftEnv(t, replies...)
	d.worker = jobs.NewWorker(d.pool, jobs.Options{})
	d.plans = planner.New(d.pool, d.fake, d.hub, planner.Options{Timeout: 10 * time.Second, Now: func() time.Time { return d.now }, Fetch: fetch})
	d.plans.RegisterJobs(d.worker)
	return d
}

// Window 5 "done when": a photo of a cookbook page becomes a saved recipe
// after checking.
func TestImportPhotoCheckSave(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newImportEnv(t, nil, claude.FakeReply{JSON: cookbookJSON})
	photo := []byte{0xff, 0xd8, 0xff, 0x00} // the web layer resizes; any bytes do here
	draft, err := d.plans.Import(ctx, d.a, domain.ImportInput{Source: domain.ImportPhoto, Photo: photo, PhotoType: "image/jpeg"})
	if err != nil || !draft.Reading() {
		t.Fatalf("import: %+v %v", draft, err)
	}
	d.run(t)
	req := d.fake.Requests[0]
	if len(req.Messages[0].Images) != 1 || req.Messages[0].Images[0].MediaType != "image/jpeg" ||
		!strings.Contains(req.Messages[0].Content, "meat_fish") {
		t.Fatalf("Claude did not get the photo and sections: %+v", req.Messages[0])
	}
	draft, err = d.plans.ImportDraft(ctx, d.a, draft.ID)
	if err != nil || draft.Recipe == nil || draft.Recipe.Title != "Fiskefrikadeller" || len(draft.Uncertain) != 1 || draft.Reading() {
		t.Fatalf("read: %+v %v", draft, err)
	}
	var photoLeft int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM recipe_drafts WHERE photo IS NOT NULL`).Scan(&photoLeft); err != nil || photoLeft != 0 {
		t.Errorf("photo still stored after reading: %d %v", photoLeft, err)
	}
	if kinds := d.usageKinds(t, d.a); len(kinds) != 1 || kinds[0] != "import" {
		t.Errorf("usage %v", kinds)
	}

	// The person fixes the unclear amount and saves.
	r := *draft.Recipe
	r.Ingredients[1].Qty = 50
	rec, err := d.plans.SaveImport(ctx, d.a, draft.ID, r)
	if err != nil {
		t.Fatal(err)
	}
	det, err := d.plans.RecipeDetail(ctx, d.a, rec.ID)
	if err != nil || det.Source != domain.SourcePhoto || det.Meal.Title != "Fiskefrikadeller" || det.Meal.Ingredients[1].Qty != 50 {
		t.Fatalf("saved: %+v %v", det, err)
	}
	if _, err := d.plans.ImportDraft(ctx, d.a, draft.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("draft not deleted: %v", err)
	}
	if own, _ := d.plans.Search(ctx, d.a, "", domain.FilterOwn); len(own) != 1 {
		t.Errorf("our own: %+v", own)
	}
}

func TestImportLinkAndFailedLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fetched := ""
	fetch := func(_ context.Context, url string) (string, error) {
		if strings.Contains(url, "broken") {
			return "", errors.New("the page answered 404")
		}
		fetched = url
		return "Title: Fiskefrikadeller\nIngredients:\n- 500 g torsk", nil
	}
	d := newImportEnv(t, fetch, claude.FakeReply{JSON: cookbookJSON})
	ok, err := d.plans.Import(ctx, d.a, domain.ImportInput{Source: domain.ImportLink, Text: "https://example.com/fisk"})
	if err != nil {
		t.Fatal(err)
	}
	bad, err := d.plans.Import(ctx, d.a, domain.ImportInput{Source: domain.ImportLink, Text: "https://example.com/broken"})
	if err != nil {
		t.Fatal(err)
	}
	d.run(t)
	if fetched != "https://example.com/fisk" || !strings.Contains(d.fake.Requests[0].Messages[0].Content, "500 g torsk") {
		t.Errorf("page text not sent: %q", fetched)
	}
	if got, _ := d.plans.ImportDraft(ctx, d.a, ok.ID); got.Recipe == nil {
		t.Error("link not read")
	}
	got, _ := d.plans.ImportDraft(ctx, d.a, bad.ID)
	if got.Recipe != nil || got.Reading() || got.Job == nil || !strings.Contains(got.Job.Error, "couldn't open that page") {
		t.Errorf("failed link: %+v", got)
	}
	for _, in := range []domain.ImportInput{
		{Source: domain.ImportLink, Text: "file:///etc/passwd"},
		{Source: domain.ImportText, Text: " "},
		{Source: domain.ImportPhoto},
		{Source: "fax", Text: "x"},
	} {
		if _, err := d.plans.Import(ctx, d.a, in); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%+v: %v", in, err)
		}
	}
}

func TestCreateRecipeByHandValidates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newImportEnv(t, nil)
	two := 2
	r := domain.MealJSON{
		MealType: domain.MealBaking, Title: "  Grandma's   apple cake ", Minutes: 60, Servings: 8,
		Ingredients: []domain.IngredientJSON{{Name: "Apples", Qty: 4, Unit: "pcs", Section: "produce"}, {Name: " "}},
		Steps:       []domain.StepJSON{{Text: "Bake.", TimerMin: &two}, {Text: ""}},
	}
	rec, err := d.plans.CreateRecipe(ctx, d.a, r)
	if err != nil {
		t.Fatal(err)
	}
	det, _ := d.plans.RecipeDetail(ctx, d.a, rec.ID)
	if det.Meal.Title != "Grandma's apple cake" || det.Source != domain.SourceManual || len(det.Meal.Ingredients) != 1 ||
		det.Meal.Ingredients[0].NormalizedName != "apple" || det.Meal.Protein != domain.ProteinOther {
		t.Errorf("saved: %+v", det)
	}
	bad := r
	bad.Ingredients = []domain.IngredientJSON{{Name: "Flour", Qty: -1, Section: "nope"}}
	bad.Steps = []domain.StepJSON{{Text: "a", BabyPortion: true}, {Text: "b", BabyPortion: true}}
	bad.Minutes = 0
	_, err = d.plans.CreateRecipe(ctx, d.a, bad)
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("invalid recipe accepted: %v", err)
	}
	for _, want := range []string{"minutes", "amount 0 or more", "shop section", "at most one baby-portion step"} {
		if !strings.Contains(strings.Join(ve.Problems, "; "), want) {
			t.Errorf("problems lack %q: %v", want, ve.Problems)
		}
	}
}

// Window 5 "done when": an ingredient avoided from a recipe never appears
// in the next draft.
func TestAvoidFromRecipeKeepsItOutOfTheNextDraft(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	withLeek := reply(t,
		meal(domain.Monday, "Leek soup", []domain.IngredientJSON{ing("leeks, sliced", 2, "pcs", "produce")}),
		meal(domain.Tuesday, "Onion rice", []domain.IngredientJSON{ing("onion", 1, "pcs", "produce")}),
	)
	d := newDraftEnv(t, withLeek, withLeek)
	hh := household.New(d.pool, "https://meals.example.test", "")
	a, err := hh.AddAvoid(ctx, d.a, "leek", domain.AvoidFromRecipe)
	if err != nil || a.Source != domain.AvoidFromRecipe {
		t.Fatalf("avoid: %+v %v", a, err)
	}
	if _, err := d.plans.RequestDraft(ctx, d.a); err != nil {
		t.Fatal(err)
	}
	d.run(t)
	if c, _ := d.plans.Card(ctx, d.a); c.State != domain.DraftFailed {
		t.Errorf("a draft with leek was saved: %+v", c)
	}
	if !strings.Contains(d.fake.Requests[0].Messages[0].Content, `"leek"`) ||
		!strings.Contains(d.fake.Requests[1].Messages[2].Content, "avoid list") {
		t.Error("avoid list not in the prompt or the retry")
	}
}

func TestImportCrossHousehold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newImportEnv(t, nil, claude.FakeReply{JSON: cookbookJSON})
	draft, err := d.plans.Import(ctx, d.a, domain.ImportInput{Source: domain.ImportText, Text: "Fiskefrikadeller ..."})
	if err != nil {
		t.Fatal(err)
	}
	d.run(t)
	nf := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: want not found, got %v", name, err)
		}
	}
	_, err = d.plans.ImportDraft(ctx, d.b, draft.ID)
	nf("ImportDraft", err)
	nf("DiscardImport", d.plans.DiscardImport(ctx, d.b, draft.ID))
	got, _ := d.plans.ImportDraft(ctx, d.a, draft.ID)
	_, err = d.plans.SaveImport(ctx, d.b, draft.ID, *got.Recipe)
	nf("SaveImport", err)
	if still, err := d.plans.ImportDraft(ctx, d.a, draft.ID); err != nil || still.Recipe == nil {
		t.Errorf("A's draft changed: %v", err)
	}
}
