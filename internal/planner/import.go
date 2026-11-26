package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Zagoman/meal-planner/internal/claude"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/fetch"
	"github.com/Zagoman/meal-planner/internal/jobs"
	"github.com/Zagoman/meal-planner/internal/shopping"
)

const kindImport = "import"

// MaxImportText is the longest pasted recipe accepted, in characters.
const MaxImportText = 20000

type importPayload struct {
	DraftID uuid.UUID `json:"draft_id"`
}

// ErrFetch wraps a failed link fetch: the form then asks for the text.
var ErrFetch = errors.New("couldn't open that page")

// Import stores the input as a draft and queues an import job.
func (s *Service) Import(ctx context.Context, sc domain.Scope, in domain.ImportInput) (domain.RecipeDraft, error) {
	in.Text = strings.TrimSpace(in.Text)
	var problems []string
	switch in.Source {
	case domain.ImportPhoto:
		if len(in.Photo) == 0 {
			problems = append(problems, "photo: choose a photo")
		}
		in.Text = ""
	case domain.ImportLink:
		if _, err := fetch.CheckURL(in.Text); err != nil {
			problems = append(problems, "link: an http or https address")
		}
	case domain.ImportText:
		if in.Text == "" {
			problems = append(problems, "text: paste the recipe")
		}
		if utf8.RuneCountInString(in.Text) > MaxImportText {
			problems = append(problems, fmt.Sprintf("text: at most %d characters", MaxImportText))
		}
	default:
		problems = append(problems, "source: photo, text or link")
	}
	if len(problems) > 0 {
		return domain.RecipeDraft{}, &domain.ValidationError{Problems: problems}
	}
	if s.client == nil {
		return domain.RecipeDraft{}, errNoClient
	}
	q := db.New(s.pool)
	var d db.RecipeDraft
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		var err error
		d, err = qt.CreateRecipeDraft(ctx, db.CreateRecipeDraftParams{
			HouseholdID: sc.HouseholdID, Source: string(in.Source), RawInput: in.Text,
			Photo: in.Photo, PhotoType: in.PhotoType, CreatedBy: memberRef(sc),
		})
		if err != nil {
			return fmt.Errorf("draft: %w", err)
		}
		_, err = jobs.Enqueue(ctx, qt, jobs.New{
			HouseholdID: sc.HouseholdID, Kind: domain.JobImport, MemberID: sc.MemberID,
			Payload: importPayload{DraftID: d.ID},
		})
		return err
	})
	if err != nil {
		return domain.RecipeDraft{}, err
	}
	s.wake()
	return s.ImportDraft(ctx, sc, d.ID)
}

// ImportDraft returns a draft with the state of its import job.
func (s *Service) ImportDraft(ctx context.Context, sc domain.Scope, id uuid.UUID) (domain.RecipeDraft, error) {
	q := db.New(s.pool)
	d, err := q.GetRecipeDraft(ctx, db.GetRecipeDraftParams{ID: id, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.RecipeDraft{}, notFound("recipe draft", err)
	}
	out := domain.RecipeDraft{ID: d.ID, Source: domain.ImportSource(d.Source), RawInput: d.RawInput, Uncertain: d.Uncertain}
	if out.Uncertain == nil {
		out.Uncertain = []string{}
	}
	if len(d.Parsed) > 0 {
		var m domain.MealJSON
		if err := json.Unmarshal(d.Parsed, &m); err != nil {
			return domain.RecipeDraft{}, fmt.Errorf("draft %s: %w", d.ID, err)
		}
		out.Recipe = &m
	}
	job, err := q.LatestDraftJob(ctx, db.LatestDraftJobParams{HouseholdID: sc.HouseholdID, DraftID: id.String()})
	switch {
	case err == nil:
		j := jobs.FromDB(job)
		out.Job = &j
	case !errors.Is(err, pgx.ErrNoRows):
		return domain.RecipeDraft{}, fmt.Errorf("import job: %w", err)
	}
	return out, nil
}

// DiscardImport deletes a draft.
func (s *Service) DiscardImport(ctx context.Context, sc domain.Scope, id uuid.UUID) error {
	n, err := db.New(s.pool).DeleteRecipeDraft(ctx, db.DeleteRecipeDraftParams{ID: id, HouseholdID: sc.HouseholdID})
	if err != nil {
		return fmt.Errorf("discard draft: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("discard draft: %w", domain.ErrNotFound)
	}
	return nil
}

// SaveImport saves a checked draft to the library (source photo, text or
// link) and deletes the draft.
func (s *Service) SaveImport(ctx context.Context, sc domain.Scope, draftID uuid.UUID, r domain.MealJSON) (domain.Recipe, error) {
	q := db.New(s.pool)
	d, err := q.GetRecipeDraft(ctx, db.GetRecipeDraftParams{ID: draftID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Recipe{}, notFound("recipe draft", err)
	}
	return s.saveOwnRecipe(ctx, sc, r, domain.RecipeSource(d.Source), &draftID)
}

// CreateRecipe saves a recipe written by hand (source manual).
func (s *Service) CreateRecipe(ctx context.Context, sc domain.Scope, r domain.MealJSON) (domain.Recipe, error) {
	return s.saveOwnRecipe(ctx, sc, r, domain.SourceManual, nil)
}

func (s *Service) saveOwnRecipe(ctx context.Context, sc domain.Scope, r domain.MealJSON, source domain.RecipeSource, draftID *uuid.UUID) (domain.Recipe, error) {
	q := db.New(s.pool)
	secs, err := q.ListSections(ctx, sc.HouseholdID)
	if err != nil {
		return domain.Recipe{}, fmt.Errorf("sections: %w", err)
	}
	keys := make([]string, len(secs))
	for i, sec := range secs {
		keys[i] = sec.Key
	}
	r, problems := CleanRecipe(r, keys)
	if len(problems) > 0 {
		return domain.Recipe{}, &domain.ValidationError{Problems: problems}
	}
	data, err := json.Marshal(r)
	if err != nil {
		return domain.Recipe{}, fmt.Errorf("recipe data: %w", err)
	}
	var row db.Recipe
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		var err error
		row, err = qt.CreateRecipe(ctx, db.CreateRecipeParams{
			HouseholdID: sc.HouseholdID, Title: r.Title, Protein: string(r.Protein), Minutes: clamp32(r.Minutes),
			OnePot: r.OnePot, Servings: clamp32(r.Servings), Data: data, CreatedBy: memberRef(sc),
			Source: string(source), MealType: string(r.MealType), TitleKey: domain.TitleKey(r.Title),
		})
		if err != nil {
			return fmt.Errorf("save recipe: %w", err)
		}
		if draftID != nil {
			if _, err := qt.DeleteRecipeDraft(ctx, db.DeleteRecipeDraftParams{ID: *draftID, HouseholdID: sc.HouseholdID}); err != nil {
				return fmt.Errorf("delete draft: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return domain.Recipe{}, err
	}
	return domain.Recipe{ID: row.ID, Source: source, CreatedAt: row.CreatedAt, InLibrary: row.InLibrary}, nil
}

// CleanRecipe validates a recipe written or checked by a person and fills
// in what can be derived (normalized names, defaults). Empty ingredient and
// step rows are dropped.
func CleanRecipe(r domain.MealJSON, sectionKeys []string) (domain.MealJSON, []string) {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	r.Title = strings.Join(strings.Fields(r.Title), " ")
	if r.Title == "" || utf8.RuneCountInString(r.Title) > 200 {
		add("title: 1 to 200 characters")
	}
	if !r.MealType.Valid() {
		add("meal type: breakfast, lunch, dinner, baking or other")
	}
	switch r.Protein {
	case domain.ProteinChicken, domain.ProteinFish, domain.ProteinVegetarian, domain.ProteinOther:
	case "":
		r.Protein = domain.ProteinOther
	default:
		add("protein: chicken, fish, vegetarian or other")
	}
	if r.Minutes < 1 || r.Minutes > 24*60 {
		add("minutes: 1 to 1440")
	}
	if r.Servings < 1 || r.Servings > 50 {
		add("serves: 1 to 50")
	}
	sections := map[string]bool{}
	for _, k := range sectionKeys {
		sections[k] = true
	}
	ings := make([]domain.IngredientJSON, 0, len(r.Ingredients))
	for i, in := range r.Ingredients {
		in.Name = strings.Join(strings.Fields(in.Name), " ")
		if in.Name == "" {
			continue
		}
		in.NormalizedName = shopping.Normalize(in.Name)
		in.Unit = strings.TrimSpace(in.Unit)
		if in.NormalizedName == "" {
			add("ingredient %d: a name with letters", i+1)
		}
		if in.Qty < 0 {
			add("ingredient %d (%s): amount 0 or more", i+1, in.Name)
		}
		if !sections[in.Section] {
			add("ingredient %d (%s): pick a shop section", i+1, in.Name)
		}
		ings = append(ings, in)
	}
	if len(ings) == 0 {
		add("ingredients: at least one")
	}
	r.Ingredients = ings
	steps := make([]domain.StepJSON, 0, len(r.Steps))
	baby := 0
	for i, st := range r.Steps {
		st.Text = strings.TrimSpace(st.Text)
		if st.Text == "" {
			continue
		}
		if st.TimerMin != nil && (*st.TimerMin < 1 || *st.TimerMin > 24*60) {
			add("step %d: timer 1 to 1440 minutes", i+1)
		}
		if st.BabyPortion {
			baby++
		}
		steps = append(steps, st)
	}
	if len(steps) == 0 {
		add("steps: at least one")
	}
	if baby > 1 {
		add("steps: at most one baby-portion step")
	}
	r.Steps = steps
	r.Day = nil
	if r.AfterBabyPortion == nil {
		r.AfterBabyPortion = []string{}
	}
	return r, problems
}

const importSystem = `You read one recipe from a photo, a pasted text or a web page and return it as JSON matching the provided schema: {"recipe": {...}, "uncertain": [...]}.
Rules:
- Copy the recipe faithfully; do not invent ingredients or steps. Keep the recipe's language for title, ingredient names and steps.
- Ingredients: "name" as written; "normalized_name" singular, lowercase, without preparation, in English if you can; "qty" a number (0 when there is none or it can't be read); "unit" one of g, kg, ml, dl, l, tsp, tbsp, pcs, or a count noun such as clove or can; "section" one of the shop section keys listed below.
- "meal_type": breakfast, lunch, dinner, baking or other. "protein": chicken, fish, vegetarian or other. "minutes": total time (estimate if missing). "servings": as stated (estimate if missing). "one_pot": true only if it is cooked in one pot or pan.
- Steps short and in order; "timer_min" when a step has a wait or cook time; "baby_portion" false unless the recipe says to take out a baby's portion; "after_baby_portion" [].
- "uncertain" lists the path of every field you could not read clearly or had to guess, e.g. "title", "minutes", "servings", "ingredients[2].qty", "steps[0].text". Empty if everything was clear.`

var (
	importSchemaOnce sync.Once
	importSchema     *jsonschema.Schema
	importSchemaErr  error
)

func compiledImportSchema() (*jsonschema.Schema, error) {
	importSchemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(domain.RecipeImportSchema()))
		if err != nil {
			importSchemaErr = err
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource("recipe_import.schema.json", doc); err != nil {
			importSchemaErr = err
			return
		}
		importSchema, importSchemaErr = c.Compile("recipe_import.schema.json")
	})
	return importSchema, importSchemaErr
}

// ValidateImport parses an import answer and returns its problems.
func ValidateImport(raw []byte, sectionKeys []string) (domain.RecipeImportJSON, []string) {
	var out domain.RecipeImportJSON
	sch, err := compiledImportSchema()
	if err != nil {
		return out, []string{"internal: import schema does not compile: " + err.Error()}
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return out, []string{"the answer is not valid JSON: " + err.Error()}
	}
	if err := sch.Validate(inst); err != nil {
		return out, []string{"the answer does not match the schema: " + truncate(err.Error(), 1500)}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, []string{"the answer does not match the schema: " + err.Error()}
	}
	sections := map[string]bool{}
	for _, k := range sectionKeys {
		sections[k] = true
	}
	var problems []string
	for i, in := range out.Recipe.Ingredients {
		if !sections[in.Section] {
			problems = append(problems, fmt.Sprintf("ingredients[%d] (%q) uses unknown section %q; use one of the listed keys", i, in.Name, in.Section))
		}
	}
	return out, problems
}

// runImport reads the draft's photo, text or link with Claude.
func (s *Service) runImport(ctx context.Context, job db.Job) (any, error) {
	var p importPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, fmt.Errorf("import payload: %w", err)
	}
	sc := jobScope(job)
	defer s.publishEvent(sc.HouseholdID, events.Event{Kind: events.RecipeUpdated, RecipeID: p.DraftID})
	if err := s.needClient(); err != nil {
		return nil, err
	}
	q := db.New(s.pool)
	d, err := q.GetRecipeDraft(ctx, db.GetRecipeDraftParams{ID: p.DraftID, HouseholdID: sc.HouseholdID})
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{"skipped": true}, nil // discarded meanwhile
	}
	if err != nil {
		return nil, fmt.Errorf("draft: %w", err)
	}
	if len(d.Parsed) > 0 {
		return map[string]any{"skipped": true}, nil // read already (a retried job)
	}
	hh, err := s.loadHousehold(ctx, q, sc.HouseholdID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(hh.sections))
	var b strings.Builder
	b.WriteString("## Shop section keys (use exactly these for \"section\")\n")
	for i, sec := range hh.sections {
		keys[i] = sec.Key
		fmt.Fprintf(&b, "- %s (%s)\n", sec.Key, sec.Name)
	}
	msg := claude.Message{Role: "user"}
	switch domain.ImportSource(d.Source) {
	case domain.ImportPhoto:
		if len(d.Photo) == 0 {
			return nil, errors.New("the photo is gone; add it again")
		}
		msg.Images = []claude.Image{{MediaType: d.PhotoType, Data: d.Photo}}
		b.WriteString("\n## Task\nRead the recipe in this photo.\n")
	case domain.ImportLink:
		text, err := s.opts.Fetch(ctx, d.RawInput)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrFetch, err)
		}
		fmt.Fprintf(&b, "\n## Task\nRead the recipe from this web page (%s):\n<page>\n%s\n</page>\n", d.RawInput, text)
	default:
		fmt.Fprintf(&b, "\n## Task\nRead the recipe in this text:\n<text>\n%s\n</text>\n", d.RawInput)
	}
	msg.Content = b.String()
	req := claude.Request{System: importSystem, Messages: []claude.Message{msg}, Schema: domain.RecipeImportSchema(), MaxTokens: 8000}
	var parsed domain.RecipeImportJSON
	for attempt := 0; ; attempt++ {
		k := kindImport
		if attempt > 0 {
			k = kindRetry
		}
		resp, err := s.client.Generate(ctx, req)
		s.logUsage(ctx, sc, k, resp)
		if err != nil {
			return nil, fmt.Errorf("%w: claude: %w", domain.ErrGeneration, err)
		}
		var problems []string
		parsed, problems = ValidateImport(resp.JSON, keys)
		if len(problems) == 0 {
			break
		}
		if attempt == 1 {
			return nil, fmt.Errorf("%w: answer still invalid after retry: %s", domain.ErrGeneration, strings.Join(problems, "; "))
		}
		req.Messages = append(req.Messages,
			claude.Message{Role: "assistant", Content: string(resp.JSON)},
			claude.Message{Role: "user", Content: BuildRetryPrompt(problems)})
	}
	parsed.Recipe.Day = nil
	data, err := json.Marshal(parsed.Recipe)
	if err != nil {
		return nil, fmt.Errorf("parsed: %w", err)
	}
	uncertain := parsed.Uncertain
	if uncertain == nil {
		uncertain = []string{}
	}
	if err := q.SetRecipeDraftParsed(ctx, db.SetRecipeDraftParsedParams{ID: d.ID, HouseholdID: sc.HouseholdID, Parsed: data, Uncertain: uncertain}); err != nil {
		return nil, fmt.Errorf("save parsed: %w", err)
	}
	return map[string]any{"uncertain": len(uncertain)}, nil
}
