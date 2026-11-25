package imagefit_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/Zagoman/meal-planner/internal/imagefit"
)

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestFit(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ w, h, ww, wh int }{
		{4000, 3000, 1600, 1200},
		{1200, 2400, 800, 1600},
		{800, 600, 800, 600},
	} {
		out, err := imagefit.Fit(encodePNG(t, c.w, c.h))
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("not a jpeg: %v", err)
		}
		if cfg.Width != c.ww || cfg.Height != c.wh {
			t.Errorf("%dx%d -> %dx%d, want %dx%d", c.w, c.h, cfg.Width, cfg.Height, c.ww, c.wh)
		}
	}
	if _, err := imagefit.Fit([]byte("%PDF-1.4")); !errors.Is(err, imagefit.ErrNotImage) {
		t.Errorf("pdf: %v", err)
	}
}
