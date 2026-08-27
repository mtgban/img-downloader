package mirror

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/chai2010/webp"
)

// sourceImage builds a png of the given size, with varying pixels so an
// encoder has something real to chew on rather than one flat colour.
func sourceImage(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), uint8((x + y) % 256), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodedSize(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, err := webp.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("stored bytes are not decodable webp: %v", err)
	}
	return cfg.Width, cfg.Height
}

func TestToWebPConvertsAndBounds(t *testing.T) {
	out, err := ToWebP(sourceImage(t, maxWidth*2, maxHeight*2))
	if err != nil {
		t.Fatal(err)
	}
	if !isWebP(out) {
		t.Fatal("output is not webp")
	}
	if w, h := decodedSize(t, out); w != maxWidth || h != maxHeight {
		t.Errorf("stored at %dx%d, want %dx%d", w, h, maxWidth, maxHeight)
	}
}

// A source smaller than the box is left at its own size: scaling it up would
// invent detail and pay bytes for the privilege.
func TestToWebPDoesNotUpscale(t *testing.T) {
	out, err := ToWebP(sourceImage(t, 100, 140))
	if err != nil {
		t.Fatal(err)
	}
	if w, h := decodedSize(t, out); w != 100 || h != 140 {
		t.Errorf("stored at %dx%d, want the source's own 100x140", w, h)
	}
}

// Fitting is by aspect ratio, not by stretching to the box: a card that is not
// exactly the reference shape still comes out the shape it went in.
func TestToWebPPreservesAspectRatio(t *testing.T) {
	out, err := ToWebP(sourceImage(t, 2000, 1000))
	if err != nil {
		t.Fatal(err)
	}
	w, h := decodedSize(t, out)
	if w != maxWidth {
		t.Errorf("width = %d, want the bound %d for a wide source", w, maxWidth)
	}
	if h != maxWidth/2 {
		t.Errorf("height = %d, want %d to hold the 2:1 source ratio", h, maxWidth/2)
	}
}

// Scryfall serves webp already, and that is nearly the whole corpus: decoding
// and re-encoding it would spend quality on every card to save nothing, and
// would rewrite every object in the bucket to do it.
func TestToWebPPassesWebpThrough(t *testing.T) {
	var src bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 20, 28))
	if err := webp.Encode(&src, img, &webp.Options{Quality: 50}); err != nil {
		t.Fatal(err)
	}
	out, err := ToWebP(src.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, src.Bytes()) {
		t.Error("webp input was re-encoded rather than passed through")
	}
}

func TestToWebPAcceptsJpeg(t *testing.T) {
	var src bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 40, 56))
	if err := jpeg.Encode(&src, img, nil); err != nil {
		t.Fatal(err)
	}
	out, err := ToWebP(src.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !isWebP(out) {
		t.Error("jpeg source did not come back as webp")
	}
}

// Storing bytes the mirror could not read would put something in the bucket
// that no reader can display, and record a digest for it as though it were a
// real image.
func TestToWebPRejectsUndecodableBytes(t *testing.T) {
	if _, err := ToWebP([]byte("this is not an image")); err == nil {
		t.Error("undecodable bytes were accepted")
	}
}
