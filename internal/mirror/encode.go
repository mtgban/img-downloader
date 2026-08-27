package mirror

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"

	"github.com/chai2010/webp"
	"golang.org/x/image/draw"
)

// ImageExt is the extension every mirrored object carries. The corpus is one
// format so that nothing downstream has to ask what an image is: object paths,
// bundle entry names and the website's cache urls all end the same way.
const ImageExt = "webp"

// webpQuality is the lossy encode quality, and it is a number the corpus is
// stuck with: fetchOne digests what it stores, so raising or lowering it
// re-digests and re-stores every image that is not already webp.
//
// 80 rather than something higher because of what the sources are. A png card
// scan lands at roughly an eighth of its size here, while a jpeg is already
// lossy and gains almost nothing — at 90 a jpeg re-encode comes out *larger*
// than the jpeg it came from, paying generation loss for negative savings.
const webpQuality = 80

// maxWidth and maxHeight bound a stored image. This is Scryfall's grid
// geometry, which is what the Magic corpus already holds, and it clears the
// largest the site ever draws a card: the lightbox caps at 440 css px and
// every other surface is a thumbnail. Storing the publisher's full resolution
// would be paying to ship detail no one is shown.
const (
	maxWidth  = 488
	maxHeight = 680
)

// fitted returns the size img should be stored at, and whether that differs
// from what it is. Aspect ratio is preserved and an image already inside the
// box is left alone: upscaling a small source would invent detail and cost
// bytes to do it.
func fitted(b image.Rectangle) (int, int, bool) {
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || (w <= maxWidth && h <= maxHeight) {
		return w, h, false
	}
	if w*maxHeight > h*maxWidth {
		return maxWidth, max(1, h*maxWidth/w), true
	}
	return max(1, w*maxHeight/h), maxHeight, true
}

// webpHeader is the RIFF container plus the WEBP form type, at bytes 0-3 and
// 8-11 of any webp file.
func isWebP(data []byte) bool {
	return len(data) >= 12 &&
		bytes.Equal(data[0:4], []byte("RIFF")) &&
		bytes.Equal(data[8:12], []byte("WEBP"))
}

// ToWebP returns data encoded as webp.
//
// Bytes that are already webp are passed through untouched rather than decoded
// and re-encoded. That is not just an optimisation: re-encoding lossy bytes
// into the same lossy format spends quality to save nothing, and it would
// rewrite every single Scryfall already serves as webp, which is nearly the
// whole corpus. Those arrive at exactly the size this stores anyway, so the
// passthrough gives up no resizing either.
func ToWebP(data []byte) ([]byte, error) {
	if isWebP(data) {
		return data, nil
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("mirror: decoding source image: %w", err)
	}
	if w, h, resize := fitted(img.Bounds()); resize {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		// CatmullRom over the cheaper kernels because this runs once per image
		// ever, and the result is what every reader sees from then on.
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, &webp.Options{Quality: webpQuality}); err != nil {
		return nil, fmt.Errorf("mirror: encoding %s source to webp: %w", format, err)
	}
	return buf.Bytes(), nil
}
