package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Zagoman/meal-planner/internal/domain"
)

func (h *handlers) profilePage(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	p, err := h.svc.Profiles.Get(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
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
	h.render(w, r, http.StatusOK, ProfilePage(p, sections, pantry))
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

func parseProfile(r *http.Request) (domain.Profile, error) {
	var problems []string
	num := func(field string) int {
		v := strings.TrimSpace(r.PostFormValue(field))
		if v == "" {
			return 0
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			problems = append(problems, field+": must be a whole number")
		}
		return n
	}
	p := domain.Profile{
		DinnersPerWeek: num("dinners_per_week"),
		Servings:       num("servings"),
		SpiceLevel:     domain.SpiceLevel(r.PostFormValue("spice_level")),
		OnePot:         r.PostFormValue("one_pot") == "on",
		BabyPortions:   r.PostFormValue("baby_portions") == "on",
		Avoid:          lines(r.PostFormValue("avoid")),
		Shop:           strings.TrimSpace(r.PostFormValue("shop")),
		ShoppingDay:    domain.Weekday(r.PostFormValue("shopping_day")),
		Language:       strings.TrimSpace(r.PostFormValue("language")),
		ProteinMix:     map[domain.Protein]int{},
	}
	for _, pr := range proteins {
		if n := num("protein_" + string(pr)); n > 0 {
			p.ProteinMix[pr] = n
		}
	}
	if !slices.Contains(spiceLevels, p.SpiceLevel) {
		problems = append(problems, "spice_level: pick one of the options")
	}
	if !p.ShoppingDay.Valid() {
		problems = append(problems, "shopping_day: pick a weekday")
	}
	if len(problems) > 0 {
		return p, &domain.ValidationError{Problems: problems}
	}
	return p, nil
}

func (h *handlers) saveProfile(w http.ResponseWriter, r *http.Request) {
	p, err := parseProfile(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if p, err = h.svc.Profiles.Put(r.Context(), scope(r), p); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/profile")
		return
	}
	h.render(w, r, http.StatusOK, PrefsForm(p, true))
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
