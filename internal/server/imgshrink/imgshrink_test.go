package imgshrink

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand"
	"testing"
)

func noisePNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rand.New(rand.NewSource(1)).Read(img.Pix)
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func TestShrink(t *testing.T) {
	small := []byte("not even an image")
	if out, name, err := Shrink(small, "a.png", 100); err != nil || !bytes.Equal(out, small) || name != "a.png" {
		t.Fatalf("under-limit input must pass through untouched: %v", err)
	}
	big := noisePNG(600, 600) // ~1.4 MB of incompressible noise
	out, name, err := Shrink(big, "pic.png", 400_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 400_000 || name != "pic.jpg" {
		t.Errorf("len=%d name=%s", len(out), name)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != 600 || cfg.Height != 600 {
		t.Errorf("resolution changed or not a JPEG: %+v %v", cfg, err)
	}
	if _, _, err := Shrink(big, "pic.png", 1000); err == nil {
		t.Error("impossible limit should error, not shrink resolution")
	}
}
