package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"github.com/Zagoman/meal-planner/internal/domain"
)

func (h *handlers) profilePage(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Profiles.Get(r.Context(), scope(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderProfile(w, r, http.StatusOK, p, nil)
}

// renderProfile is the Profile page with p in Preferences (the stored
// profile, or what was typed when a no-JavaScript save failed).
func (h *handlers) renderProfile(w http.ResponseWriter, r *http.Request, status int, p domain.Profile, errs prefErrors) {
	s := scope(r)
	sections, err := h.svc.Profiles.Sections(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	pantry, err := h.svc.Profiles.Pantry(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	avoid, err := h.svc.Profiles.AvoidItems(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	hv, err := h.householdView(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, status, ProfilePage(p, errs, avoid, sections, pantry, hv))
}

func (h *handlers) addAvoid(w http.ResponseWriter, r *http.Request) {
	if _, err := h.svc.Profiles.AddAvoid(r.Context(), scope(r), r.PostFormValue("name"), domain.AvoidFromProfile); err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderAvoid(w, r)
}

func (h *handlers) deleteAvoid(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.svc.Profiles.DeleteAvoid(r.Context(), scope(r), id); err != nil {
		h.fail(w, r, err)
		return
	}
	if r.FormValue("toast") == "1" { // Undo in the "added to avoid list" toast
		h.render(w, r, http.StatusOK, Notice("Removed from the avoid list."))
		return
	}
	h.renderAvoid(w, r)
}

func (h *handlers) renderAvoid(w http.ResponseWriter, r *http.Request) {
	if !isHX(r) {
		redirect(w, r, "/profile")
		return
	}
	items, err := h.svc.Profiles.AvoidItems(r.Context(), scope(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, AvoidList(items))
}

// lines splits a textarea on newlines and commas, dropping blanks.
func lines(s string) []string {
	out := []string{}
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' }) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func pantryText(p []domain.PantryItem) string {
	names := make([]string, len(p))
	for i, it := range p {
		names[i] = it.Name
	}
	return strings.Join(names, "\n")
}

// prefFields are the Preferences fields that save on their own. A
// per-field save (htmx, field=<name>) posts the whole form but only that
// field is read and merged into the stored profile, so a stale or
// half-typed value elsewhere on the page is never written.
var prefFields = func() []string {
	out := []string{}
	for _, t := range domain.PlannedMealTypes {
		out = append(out, "meals_"+string(t))
	}
	return append(out, "people", "protein", "spice_level", "one_pot", "baby_portions", "cuisine",
		"shop", "shopping_day", "auto_confirm_time", "language")
}()

// languages are the recipe languages offered in Profile.
var languages = []struct{ Code, Name string }{{"en", "English"}, {"da", "Danish"}}

// applyField reads one Preferences field from the form into p and returns
// its problems (which leave p's value for that field as typed, or zero).
func applyField(p *domain.Profile, field string, r *http.Request) []string {
	var problems []string
	num := func(name string, lo, hi int) int {
		v := strings.TrimSpace(r.PostFormValue(name))
		if v == "" {
			return 0
		}
		n, err := strconv.Atoi(v)
		switch {
		case err != nil:
			problems = append(problems, name+": must be a whole number")
		case n < lo || n > hi:
			problems = append(problems, fmt.Sprintf("%s: must be %d to %d", name, lo, hi))
		}
		return n
	}
	switch {
	case strings.HasPrefix(field, "meals_"):
		t := domain.MealType(strings.TrimPrefix(field, "meals_"))
		if p.MealTargets == nil {
			p.MealTargets = map[domain.MealType]int{}
		}
		p.MealTargets[t] = num(field, 0, domain.MaxMealsPerType)
	case field == "people":
		p.People = num("people", 1, 12)
	case field == "protein":
		p.ProteinMix = map[domain.Protein]int{}
		for _, pr := range proteins {
			if n := num("protein_"+string(pr), 0, 7); n > 0 {
				p.ProteinMix[pr] = n
			}
		}
	case field == "spice_level":
		p.SpiceLevel = domain.SpiceLevel(r.PostFormValue("spice_level"))
		if !slices.Contains(spiceLevels, p.SpiceLevel) {
			problems = append(problems, "spice_level: pick one of the options")
		}
	case field == "one_pot":
		p.OnePot = r.PostFormValue("one_pot") == "on"
	case field == "baby_portions":
		p.BabyPortions = r.PostFormValue("baby_portions") == "on"
	case field == "cuisine":
		p.Cuisines = r.PostForm["cuisine"]
		if p.Cuisines == nil {
			p.Cuisines = []string{}
		}
	case field == "shop":
		p.Shop = strings.TrimSpace(r.PostFormValue("shop"))
	case field == "shopping_day":
		p.ShoppingDay = domain.Weekday(r.PostFormValue("shopping_day"))
		if !p.ShoppingDay.Valid() {
			problems = append(problems, "shopping_day: pick a weekday")
		}
	case field == "auto_confirm_time":
		p.AutoConfirmTime = strings.TrimSpace(r.PostFormValue("auto_confirm_time"))
	case field == "language":
		p.Language = strings.TrimSpace(r.PostFormValue("language"))
	default:
		problems = append(problems, "field: unknown preference")
	}
	return problems
}

// parseProfile reads the whole Preferences form (the no-JavaScript save).
func parseProfile(r *http.Request) (domain.Profile, error) {
	_ = r.ParseForm()
	// Avoid stays nil: the avoid list has its own form.
	p := domain.Profile{MealTargets: map[domain.MealType]int{}, ProteinMix: map[domain.Protein]int{}}
	var problems []string
	for _, f := range prefFields {
		problems = append(problems, applyField(&p, f, r)...)
	}
	if len(problems) > 0 {
		return p, &domain.ValidationError{Problems: problems}
	}
	return p, nil
}

// prefErrors are validation problems by Preferences field, shown under it.
type prefErrors map[string][]string

// problemField is the Preferences field a problem belongs to ("" for the
// form as a whole). Problems are "<field>: <message>".
func problemField(problem string) (field, msg string) {
	key, msg, ok := strings.Cut(problem, ": ")
	if !ok {
		return "", problem
	}
	switch {
	case strings.HasPrefix(key, "meal_targets."):
		key = "meals_" + strings.TrimPrefix(key, "meal_targets.")
	case key == "meal_targets":
		key = "meals_" + string(domain.MealDinner)
	case strings.HasPrefix(key, "protein"):
		key = "protein"
	case key == "cuisines":
		key = "cuisine"
	}
	if !slices.Contains(prefFields, key) {
		return "", problem
	}
	return key, msg
}

// sentence upper-cases the first letter and ends with a full stop.
func sentence(s string) string {
	if s == "" {
		return s
	}
	s = capitalize(s)
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// errorsFor groups problems by field; with only set, every problem goes
// under that field (a per-field save changed nothing else).
func errorsFor(problems []string, only string) prefErrors {
	out := prefErrors{}
	for _, pr := range problems {
		f, msg := problemField(pr)
		if only != "" {
			f = only
		}
		out[f] = append(out[f], sentence(msg))
	}
	return out
}

func (h *handlers) saveProfile(w http.ResponseWriter, r *http.Request) {
	field := r.PostFormValue("field")
	if field != "" && isHX(r) {
		h.saveProfileField(w, r, field)
		return
	}
	p, err := parseProfile(r)
	if err == nil {
		var saved domain.Profile
		if saved, err = h.svc.Profiles.Put(r.Context(), scope(r), p); err == nil {
			if !isHX(r) {
				redirect(w, r, "/profile")
				return
			}
			h.render(w, r, http.StatusOK, PrefsForm(saved, true, nil))
			return
		}
	}
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || isHX(r) {
		h.fail(w, r, err)
		return
	}
	// No JavaScript: the page again, with what was typed and the problems
	// under their fields.
	h.renderProfile(w, r, http.StatusUnprocessableEntity, p, errorsFor(ve.Problems, ""))
}

// saveProfileField merges one field into the stored profile. The answer
// swaps nothing in place: "Saved" goes to #flash and the field's error
// line (and the protein helper) are updated out of band.
func (h *handlers) saveProfileField(w http.ResponseWriter, r *http.Request, field string) {
	if !slices.Contains(prefFields, field) {
		h.fail(w, r, &domain.ValidationError{Problems: []string{"field: unknown preference"}})
		return
	}
	s := scope(r)
	p, err := h.svc.Profiles.Get(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p.Avoid = nil // the avoid list has its own form; nil leaves it alone
	if problems := applyField(&p, field, r); len(problems) > 0 {
		h.render(w, r, http.StatusUnprocessableEntity, PrefFieldResult(field, p, errorsFor(problems, field)))
		return
	}
	saved, err := h.svc.Profiles.Put(r.Context(), s, p)
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		h.render(w, r, http.StatusUnprocessableEntity, PrefFieldResult(field, p, errorsFor(ve.Problems, field)))
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, PrefFieldResult(field, saved, nil))
}

// saveName sets the signed-in member's own name (the members list's "Add
// your name"). Only the scope's member is ever changed.
func (h *handlers) saveName(w http.ResponseWriter, r *http.Request) {
	if _, err := h.svc.Households.SetMyName(r.Context(), scope(r), r.PostFormValue("name")); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/profile")
		return
	}
	hv, err := h.householdView(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, HouseholdMembers(hv))
}

// proteinHelp is the line under the protein split: "2 of 4 dinners set ·
// the rest is up to us". app.js keeps it current while typing.
func proteinHelp(p domain.Profile) string {
	set, dinners := 0, p.MealTargets[domain.MealDinner]
	for _, n := range p.ProteinMix {
		set += n
	}
	switch {
	case set > dinners:
		return fmt.Sprintf("%d of %d dinners set · that's more than there are dinners", set, dinners)
	case set == dinners:
		return fmt.Sprintf("%d of %d dinners set", set, dinners)
	}
	return fmt.Sprintf("%d of %d dinners set · the rest is up to us", set, dinners)
}

func knownLanguage(code string) bool {
	for _, l := range languages {
		if l.Code == code {
			return true
		}
	}
	return false
}

// moveSection swaps a section with its neighbour and renumbers sort orders
// in steps of 10. The pantry-check section, if a service lists it, stays last.
func (h *handlers) moveSection(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	sections, err := h.svc.Profiles.Sections(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	key, dir := r.PostFormValue("key"), r.PostFormValue("dir")
	i := slices.IndexFunc(sections, func(x domain.Section) bool { return x.Key == key })
	if i < 0 {
		h.fail(w, r, domain.ErrNotFound)
		return
	}
	j := i - 1
	if dir == "down" {
		j = i + 1
	} else if dir != "up" {
		h.fail(w, r, &domain.ValidationError{Problems: []string{"dir: up or down"}})
		return
	}
	if j >= 0 && j < len(sections) &&
		sections[i].Key != domain.PantryCheckSection && sections[j].Key != domain.PantryCheckSection {
		sections[i], sections[j] = sections[j], sections[i]
		for k := range sections {
			sections[k].SortOrder = (k + 1) * 10
		}
		if sections, err = h.svc.Profiles.PutSections(r.Context(), s, sections); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	if !isHX(r) {
		redirect(w, r, "/profile")
		return
	}
	h.render(w, r, http.StatusOK, SectionsList(sections))
}

func (h *handlers) savePantry(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.Profiles.PutPantry(r.Context(), scope(r), lines(r.PostFormValue("pantry")))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/profile")
		return
	}
	h.render(w, r, http.StatusOK, PantryForm(items, true))
}

// autosave are the attributes that save one Preferences field on change
// (number inputs: while typing, after 400 ms). The whole form is posted,
// the server reads only field.
func autosave(field string, number bool, errs prefErrors) templ.Attributes {
	trigger := "change"
	if number {
		trigger = "input changed delay:400ms"
	}
	a := templ.Attributes{
		"hx-post":          "/profile",
		"hx-trigger":       trigger,
		"hx-swap":          "none",
		"hx-vals":          `{"field":"` + field + `"}`,
		"data-autosave":    field,
		"aria-describedby": "err-" + field,
	}
	if len(errs[field]) > 0 {
		a["aria-invalid"] = "true"
	}
	return a
}

func oobAttr(oob bool) templ.Attributes {
	if oob {
		return templ.Attributes{"hx-swap-oob": "true"}
	}
	return nil
}

// fieldOrForm names the error line of a field; "" is the form's own.
func fieldOrForm(field string) string {
	if field == "" {
		return "prefs"
	}
	return field
}

func blankZero(n int) string {
	if n == 0 {
		return ""
	}
	return itoa(n)
}

// memberName is a member's name, or the start of their email while they
// have none, so every row shows a name.
func memberName(m domain.Member) string {
	if m.Name != "" {
		return m.Name
	}
	local, _, _ := strings.Cut(m.Email, "@")
	return local
}
