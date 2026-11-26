package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/imagefit"
)

// recipePhoto is GET /recipes/{id}/photo: the family's photo, only for its
// own household. The URL carries ?v=<time>, so it can be cached for good.
func (h *handlers) recipePhoto(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p, err := h.svc.Library.Photo(r.Context(), scope(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(p.JPEG)))
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	_, _ = w.Write(p.JPEG)
}

// setRecipePhoto is POST /recipes/{id}/photo (multipart "photo"): resized
// to a JPEG (imagefit) and stored; back to the page it came from.
func (h *handlers) setRecipePhoto(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, imagefit.MaxUpload+1<<20)
	file, _, err := r.FormFile("photo")
	if err != nil {
		h.fail(w, r, &domain.ValidationError{Problems: []string{"Choose a photo."}})
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
		h.fail(w, r, &domain.ValidationError{Problems: []string{"That photo couldn't be opened. Use a JPEG or PNG under 15 MB."}})
		return
	}
	if err := h.svc.Library.SetPhoto(r.Context(), scope(r), id, jpg); err != nil {
		h.fail(w, r, err)
		return
	}
	redirect(w, r, photoBack(r, id.String()))
}

// deleteRecipePhoto is POST /recipes/{id}/photo/delete.
func (h *handlers) deleteRecipePhoto(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	err = h.svc.Library.DeletePhoto(r.Context(), scope(r), id)
	if errors.Is(err, domain.ErrNotFound) {
		// No photo is fine (two phones removed it); another household's
		// recipe is still not found.
		_, err = h.svc.Library.RecipeDetail(r.Context(), scope(r), id)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	redirect(w, r, photoBack(r, id.String()))
}

// photoBack is the page to return to: the form's back, if it is ours.
func photoBack(r *http.Request, recipeID string) string {
	if b := r.FormValue("back"); b != "" && safeNext(b) == b {
		return b
	}
	return fmt.Sprintf("/recipes/%s", recipeID)
}
