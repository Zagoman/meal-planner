package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/fetch"
	"github.com/Zagoman/meal-planner/internal/imagefit"
)

// addRecipePage is GET /recipes/add (L2).
func (h *handlers) addRecipePage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, AddRecipePage("", ""))
}

// importRecipe is POST /recipes/import with a photo (multipart) or a text
// that is either a recipe or a link. It answers with the draft's page.
func (h *handlers) importRecipe(w http.ResponseWriter, r *http.Request) {
	in := domain.ImportInput{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, imagefit.MaxUpload+1<<20)
		file, _, err := r.FormFile("photo")
		if err != nil {
			h.render(w, r, http.StatusUnprocessableEntity, AddRecipePage("", "Choose a photo of the recipe."))
			return
		}
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(io.LimitReader(file, imagefit.MaxUpload+1))
		if err != nil {
			h.fail(w, r, err)
			return
		}
		jpg, err := imagefit.Fit(data)
		if err != nil {
			h.render(w, r, http.StatusUnprocessableEntity, AddRecipePage("", "That photo couldn't be opened. Use a JPEG or PNG under 15 MB."))
			return
		}
		in = domain.ImportInput{Source: domain.ImportPhoto, Photo: jpg, PhotoType: "image/jpeg"}
	} else {
		text := strings.TrimSpace(r.PostFormValue("text"))
		in = domain.ImportInput{Source: domain.ImportText, Text: text}
		if !strings.ContainsAny(text, " \n") {
			if _, err := fetch.CheckURL(text); err == nil {
				in.Source = domain.ImportLink
			}
		}
	}
	d, err := h.svc.Library.Import(r.Context(), scope(r), in)
	if err != nil {
		var ve *domain.ValidationError
		if errors.As(err, &ve) {
			h.render(w, r, http.StatusUnprocessableEntity, AddRecipePage(in.Text, strings.Join(ve.Problems, ". ")))
			return
		}
		h.fail(w, r, err)
		return
	}
	redirect(w, r, "/recipes/drafts/"+d.ID.String())
}

func (h *handlers) draft(r *http.Request) (domain.RecipeDraft, error) {
	id, err := recipeID(r)
	if err != nil {
		return domain.RecipeDraft{}, err
	}
	return h.svc.Library.ImportDraft(r.Context(), scope(r), id)
}

// draftPage is GET /recipes/drafts/{id}: reading, failed, or the check form.
func (h *handlers) draftPage(w http.ResponseWriter, r *http.Request) {
	d, err := h.draft(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	switch {
	case d.Recipe != nil:
		sections, err := h.svc.Profiles.Sections(r.Context(), scope(r))
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.render(w, r, http.StatusOK, RecipeFormPage(formFromDraft(d, sections)))
	case d.Reading():
		h.render(w, r, http.StatusOK, ReadingPage(d))
	default:
		linkFailed := d.Source == domain.ImportLink && d.Job != nil && strings.Contains(d.Job.Error, "couldn't open that page")
		h.render(w, r, http.StatusOK, ImportFailedPage(d, linkFailed))
	}
}

// draftStatus is the reading page's partial: unchanged while Claude reads,
// a page reload once it is done or failed.
func (h *handlers) draftStatus(w http.ResponseWriter, r *http.Request) {
	d, err := h.draft(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if d.Reading() {
		h.render(w, r, http.StatusOK, ReadingPage(d))
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// saveDraft is POST /recipes/drafts/{id}/save.
func (h *handlers) saveDraft(w http.ResponseWriter, r *http.Request) {
	id, err := recipeID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	m, f := recipeFromForm(r)
	f.Action, f.Discard, f.Heading = "/recipes/drafts/"+id.String()+"/save", "/recipes/drafts/"+id.String()+"/discard", "Check the recipe"
	rec, err := h.svc.Library.SaveImport(r.Context(), scope(r), id, m)
	h.afterRecipeSave(w, r, rec, err, f)
}

// discardDraft is POST /recipes/drafts/{id}/discard.
func (h *handlers) discardDraft(w http.ResponseWriter, r *http.Request) {
	id, err := recipeID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.svc.Library.DiscardImport(r.Context(), scope(r), id); err != nil && !errors.Is(err, domain.ErrNotFound) {
		h.fail(w, r, err)
		return
	}
	redirect(w, r, "/recipes/add")
}

// newRecipePage is GET /recipes/new: the same form, empty.
func (h *handlers) newRecipePage(w http.ResponseWriter, r *http.Request) {
	sections, err := h.svc.Profiles.Sections(r.Context(), scope(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	f := recipeForm{Action: "/recipes", Heading: "Write a recipe", MealType: domain.MealDinner, Protein: domain.ProteinOther,
		Minutes: "30", Servings: "2", Sections: sections, Uncertain: map[string]bool{}}
	f.Ingredients = padRows(nil, 4)
	f.Steps = padSteps(nil, 2)
	h.render(w, r, http.StatusOK, RecipeFormPage(f))
}

// createRecipe is POST /recipes: a recipe written by hand.
func (h *handlers) createRecipe(w http.ResponseWriter, r *http.Request) {
	m, f := recipeFromForm(r)
	f.Action, f.Heading = "/recipes", "Write a recipe"
	rec, err := h.svc.Library.CreateRecipe(r.Context(), scope(r), m)
	h.afterRecipeSave(w, r, rec, err, f)
}

func (h *handlers) afterRecipeSave(w http.ResponseWriter, r *http.Request, rec domain.Recipe, err error, f recipeForm) {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		sections, serr := h.svc.Profiles.Sections(r.Context(), scope(r))
		if serr != nil {
			h.fail(w, r, serr)
			return
		}
		f.Sections, f.Problems = sections, ve.Problems
		f.Ingredients, f.Steps = padRows(f.Ingredients, len(f.Ingredients)+1), padSteps(f.Steps, len(f.Steps)+1)
		h.render(w, r, http.StatusUnprocessableEntity, RecipeFormPage(f))
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	redirect(w, r, "/recipes/"+rec.ID.String())
}

// recipeFromForm reads the recipe form; it also returns the form as typed,
// to show again with the problems.
func recipeFromForm(r *http.Request) (domain.MealJSON, recipeForm) {
	_ = r.ParseForm()
	f := recipeForm{
		Title: r.PostFormValue("title"), MealType: domain.MealType(r.PostFormValue("meal_type")),
		Protein: domain.Protein(r.PostFormValue("protein")), Minutes: r.PostFormValue("minutes"),
		Servings: r.PostFormValue("servings"), OnePot: r.PostFormValue("one_pot") == "on", Uncertain: map[string]bool{},
	}
	m := domain.MealJSON{Title: f.Title, MealType: f.MealType, Protein: f.Protein, OnePot: f.OnePot, AfterBabyPortion: []string{}}
	m.Minutes, _ = strconv.Atoi(strings.TrimSpace(f.Minutes))
	m.Servings, _ = strconv.Atoi(strings.TrimSpace(f.Servings))
	names, qtys, units, secs := r.PostForm["ing_name"], r.PostForm["ing_qty"], r.PostForm["ing_unit"], r.PostForm["ing_section"]
	for i := range names {
		in := formIngredient{Name: names[i], Qty: at(qtys, i), Unit: at(units, i), Section: at(secs, i)}
		f.Ingredients = append(f.Ingredients, in)
		if strings.TrimSpace(in.Name) == "" {
			continue
		}
		q, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(in.Qty), ",", "."), 64)
		if strings.TrimSpace(in.Qty) == "" {
			q, err = 0, nil
		}
		if err != nil {
			q = -1 // reported as "amount 0 or more"
		}
		m.Ingredients = append(m.Ingredients, domain.IngredientJSON{Name: in.Name, Qty: q, Unit: in.Unit, Section: in.Section})
	}
	baby, _ := strconv.Atoi(r.PostFormValue("baby_step"))
	if r.PostFormValue("baby_step") == "" {
		baby = -1
	}
	texts, timers := r.PostForm["step_text"], r.PostForm["step_timer"]
	for i := range texts {
		st := formStep{Text: texts[i], Timer: at(timers, i), Baby: i == baby}
		f.Steps = append(f.Steps, st)
		js := domain.StepJSON{Text: st.Text, BabyPortion: st.Baby}
		if t, err := strconv.Atoi(strings.TrimSpace(st.Timer)); err == nil {
			js.TimerMin = &t
		}
		m.Steps = append(m.Steps, js)
	}
	return m, f
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

func padRows(rows []formIngredient, n int) []formIngredient {
	for len(rows) < n {
		rows = append(rows, formIngredient{})
	}
	return rows
}

func padSteps(rows []formStep, n int) []formStep {
	for len(rows) < n {
		rows = append(rows, formStep{})
	}
	return rows
}

// formFromDraft fills the check form from what Claude read.
func formFromDraft(d domain.RecipeDraft, sections []domain.Section) recipeForm {
	m := *d.Recipe
	f := recipeForm{
		Action: "/recipes/drafts/" + d.ID.String() + "/save", Discard: "/recipes/drafts/" + d.ID.String() + "/discard",
		Heading: "Check the recipe", Title: m.Title, MealType: m.MealType, Protein: m.Protein,
		Minutes: strconv.Itoa(m.Minutes), Servings: strconv.Itoa(m.Servings), OnePot: m.OnePot,
		Sections: sections, Uncertain: map[string]bool{},
	}
	for _, u := range d.Uncertain {
		f.Uncertain[u] = true
	}
	for _, in := range m.Ingredients {
		f.Ingredients = append(f.Ingredients, formIngredient{Name: in.Name, Qty: strconv.FormatFloat(in.Qty, 'f', -1, 64), Unit: in.Unit, Section: in.Section})
	}
	for _, st := range m.Steps {
		fs := formStep{Text: st.Text, Baby: st.BabyPortion}
		if st.TimerMin != nil {
			fs.Timer = strconv.Itoa(*st.TimerMin)
		}
		f.Steps = append(f.Steps, fs)
	}
	f.Ingredients, f.Steps = padRows(f.Ingredients, len(f.Ingredients)+1), padSteps(f.Steps, len(f.Steps)+1)
	from := map[domain.ImportSource]string{domain.ImportPhoto: "your photo", domain.ImportLink: "your link", domain.ImportText: "your text"}[d.Source]
	switch n := len(d.Uncertain); n {
	case 0:
		f.Note = "Read from " + from + ". Check it, then save."
	case 1:
		f.Note = "Read from " + from + ". 1 thing to check, marked in orange."
	default:
		f.Note = fmt.Sprintf("Read from %s. %d things to check, marked in orange.", from, n)
	}
	return f
}

// avoidConfirm is GET /avoid/confirm?ingredient=: the sheet for htmx, a page
// otherwise.
func (h *handlers) avoidConfirm(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("ingredient"))
	if name == "" {
		h.fail(w, r, &domain.ValidationError{Problems: []string{"ingredient: required"}})
		return
	}
	if isHX(r) {
		h.render(w, r, http.StatusOK, AvoidSheet(name))
		return
	}
	h.render(w, r, http.StatusOK, AvoidConfirmPage(name, safeNext(r.URL.Query().Get("back"))))
}

// avoidAdd is POST /avoid: the ingredient goes on the avoid list (source
// recipe); a toast says so, with Undo.
func (h *handlers) avoidAdd(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.Profiles.AddAvoid(r.Context(), scope(r), r.PostFormValue("name"), domain.AvoidFromRecipe)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, safeNext(r.PostFormValue("back")))
		return
	}
	w.Header().Set("HX-Retarget", "#flash")
	w.Header().Set("HX-Reswap", "innerHTML")
	h.render(w, r, http.StatusOK, AvoidToast(a))
}

// cookMeal is GET /meals/{id}/cook?step=n.
func (h *handlers) cookMeal(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	m, err := h.svc.Plans.Meal(r.Context(), scope(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderCook(w, r, m, "/meals/"+id.String())
}

// cookRecipe is GET /recipes/{id}/cook?step=n.
func (h *handlers) cookRecipe(w http.ResponseWriter, r *http.Request) {
	id, err := recipeID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	d, err := h.svc.Library.RecipeDetail(r.Context(), scope(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderCook(w, r, d.Meal, "/recipes/"+id.String())
}

func (h *handlers) renderCook(w http.ResponseWriter, r *http.Request, m domain.Meal, back string) {
	if len(m.Steps) == 0 {
		redirect(w, r, back)
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("step"))
	n = max(0, min(n, len(m.Steps)-1))
	h.render(w, r, http.StatusOK, CookPage(m.Title, back, m.Steps, m.AfterBabyPortion, n))
}
