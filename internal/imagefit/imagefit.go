// Package imagefit shrinks an uploaded photo before it is sent to Claude:
// at most 1600 px on the long side, re-encoded as JPEG. The photo is never
// stored once it has been read.
package imagefit

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoders for uploads
	"image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// MaxSide is the longest side sent to Claude, in pixels.
const MaxSide = 1600

// MaxUpload is the largest photo accepted, in bytes.
const MaxUpload = 15 << 20

// ErrNotImage is returned for data that is not a JPEG, PNG, GIF or WebP.
var ErrNotImage = errors.New("imagefit: not a JPEG, PNG, GIF or WebP image")

// Fit decodes a photo and returns it as a JPEG whose longest side is at
// most MaxSide.
func Fit(data []byte) ([]byte, error) {
	if len(data) > MaxUpload {
		return nil, fmt.Errorf("imagefit: the photo is larger than %d MB", MaxUpload>>20)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotImage
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil, ErrNotImage
	}
	dst := src
	if long := max(w, h); long > MaxSide {
		nw, nh := w*MaxSide/long, h*MaxSide/long
		rgba := image.NewRGBA(image.Rect(0, 0, max(nw, 1), max(nh, 1)))
		draw.CatmullRom.Scale(rgba, rgba.Bounds(), src, b, draw.Over, nil)
		dst = rgba
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("imagefit: %w", err)
	}
	return out.Bytes(), nil
}
