// Package imgshrink brings an oversized image file under a byte limit by
// re-encoding it as JPEG at falling quality. Pixel dimensions are never
// changed — only the encoded size. Images already within the limit are
// returned untouched.
package imgshrink

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	_ "image/png"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"
)

// MaxPixels bounds the decoded size (decode memory is ~4 bytes/pixel).
const MaxPixels = 100_000_000

// qualities are tried in order; below the last the image is rejected rather
// than degraded further (shortcut: no resolution fallback by request).
var qualities = []int{90, 80, 70, 60, 50, 40, 30}

// Shrink returns data unchanged when len(data) <= limit. Otherwise it decodes
// the image and re-encodes it as JPEG (transparency flattened onto white; an
// animated GIF keeps only its first frame) at the highest quality that fits,
// returning the new bytes and name (extension changed to .jpg).
func Shrink(data []byte, name string, limit int) ([]byte, string, error) {
	if len(data) <= limit {
		return data, name, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("unreadable image: %w", err)
	}
	if cfg.Width*cfg.Height > MaxPixels {
		return nil, "", fmt.Errorf("image is too large to process (%dx%d pixels)", cfg.Width, cfg.Height)
	}
	var img image.Image
	if strings.EqualFold(filepath.Ext(name), ".gif") {
		g, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil || len(g.Image) == 0 {
			return nil, "", fmt.Errorf("unreadable image")
		}
		img = g.Image[0]
	} else if img, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return nil, "", fmt.Errorf("unreadable image: %w", err)
	}
	flat := image.NewRGBA(img.Bounds())
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), img, img.Bounds().Min, draw.Over)

	var buf bytes.Buffer
	for _, q := range qualities {
		buf.Reset()
		if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: q}); err != nil {
			return nil, "", err
		}
		if buf.Len() <= limit {
			return buf.Bytes(), strings.TrimSuffix(name, filepath.Ext(name)) + ".jpg", nil
		}
	}
	return nil, "", fmt.Errorf("image cannot be compressed below %d MiB without reducing its resolution", limit>>20)
}
