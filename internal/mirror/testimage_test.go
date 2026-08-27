package mirror

import (
	"bytes"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// testImage returns a small decodable png whose pixels are derived from seed.
//
// Fixtures cannot serve arbitrary bytes any more: the mirror decodes what it
// fetches so it can store webp, and refuses what it cannot read. Seeding the
// pixels keeps the property the old string fixtures had, that two fetches
// meant to differ end up with different digests.
func testImage(seed string) []byte {
	h := fnv.New32a()
	h.Write([]byte(seed))
	sum := h.Sum32()

	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(sum >> 16),
				G: uint8(sum >> 8),
				B: uint8(sum),
				A: 255,
			})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic("encoding test image: " + err.Error())
	}
	return buf.Bytes()
}

// TestTestImageVariesWithSeed guards the helper's whole reason for existing:
// fixtures that differ have to keep producing different stored bytes.
func TestTestImageVariesWithSeed(t *testing.T) {
	if bytes.Equal(testImage("a"), testImage("b")) {
		t.Error("two seeds produced identical images, so digests would collide")
	}
	if !bytes.Equal(testImage("a"), testImage("a")) {
		t.Error("one seed produced two different images")
	}
}
