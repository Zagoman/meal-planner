package web_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Shopping list fixes #1, #4, #6 on the List page.
func TestListFixesTickSinkAddFormClear(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna, carl := f.session(f.seed.Anna), f.session(f.seed.Carl)
	ctx := context.Background()

	// #4: the add form sits under the progress, before Check at home and the sections.
	page := f.do(t, req{path: "/list", session: anna}).Body.String()
	mustContain(t, "list page", page, `id="add-item"`, `hx-preserve="true"`, `id="add-jump"`, `aria-label="Add an item"`)
	iProg, iForm, iSec := strings.Index(page, `id="progress"`), strings.Index(page, `id="add-item"`), strings.Index(page, `id="section-`)
	if iProg >= iForm || iForm >= iSec {
		t.Errorf("order: progress %d, form %d, first section %d", iProg, iForm, iSec)
	}

	// #1: ticking the first item of a section answers with that section,
	// the item now last, and the progress out of band.
	l, err := f.store.Active(ctx, f.seed.Anna)
	if err != nil {
		t.Fatal(err)
	}
	sec := l.Sections[0]
	if len(sec.Items) < 2 {
		t.Fatalf("seed section too small: %+v", sec)
	}
	first := sec.Items[0]
	rec := f.do(t, req{method: "POST", path: "/list/items/" + first.ID.String() + "/toggle", form: url.Values{"checked": {"true"}}, session: anna, hx: true})
	body := rec.Body.String()
	mustContain(t, "tick", body, `id="section-`+sec.Key+`"`, `id="progress"`, `hx-swap-oob="true"`, `hx-target="closest section"`)
	ids := regexp.MustCompile(`id="item-([0-9a-f-]{36})"`).FindAllStringSubmatch(body, -1)
	if len(ids) != len(sec.Items) || ids[len(ids)-1][1] != first.ID.String() {
		t.Errorf("ticked item is not last in its section: %v", ids)
	}

	// Tick the rest of the section: it folds to "All in basket".
	for _, it := range sec.Items[1:] {
		rec = f.do(t, req{method: "POST", path: "/list/items/" + it.ID.String() + "/toggle", form: url.Values{"checked": {"true"}}, session: anna, hx: true})
	}
	mustContain(t, "section done", rec.Body.String(), `class="sec sec-done"`, "All in basket · ", `<details class="all-in">`, "Clear ")

	// #6: clear the ticked items: the list without them and an Undo toast.
	rec = f.do(t, req{method: "POST", path: "/list/clear", session: anna, hx: true, form: url.Values{}})
	body = rec.Body.String()
	mustContain(t, "clear", body, "items cleared", "Undo", `data-autoclose="8000"`, `id="flash"`)
	if strings.Contains(body, `id="item-`+first.ID.String()+`"`) {
		t.Error("cleared item still on the list")
	}
	batch := regexp.MustCompile(`&#34;batch&#34;:&#34;([0-9a-f-]{36})&#34;|"batch":"([0-9a-f-]{36})"`).FindStringSubmatch(body)
	if batch == nil {
		t.Fatalf("no batch in the toast: %s", body)
	}
	b := batch[1] + batch[2]
	// Another household can't undo it.
	if rec := f.do(t, req{method: "POST", path: "/list/clear/undo", session: carl, hx: true, form: url.Values{"batch": {b}}}); rec.Code != http.StatusNotFound {
		t.Errorf("other household undo: %d", rec.Code)
	}
	rec = f.do(t, req{method: "POST", path: "/list/clear/undo", session: anna, hx: true, form: url.Values{"batch": {b}}})
	mustContain(t, "undo", rec.Body.String(), `id="item-`+first.ID.String()+`"`)
	// Without JavaScript, Clear goes back to the list.
	if rec := f.do(t, req{method: "POST", path: "/list/clear", session: anna, form: url.Values{}}); rec.Code != http.StatusSeeOther {
		t.Errorf("no-JS clear: %d", rec.Code)
	}
}
