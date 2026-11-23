package asset

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"kkagent/internal/model"
	"os"
	"path/filepath"
)

func (s *Store) Size(b model.Block) (int64, error) {
	if b.Data != "" {
		return int64(base64.StdEncoding.DecodedLen(len(b.Data))), nil
	}
	if !valid(b.Asset) {
		return 0, fmt.Errorf("invalid asset reference")
	}
	info, err := os.Stat(filepath.Join(s.root, b.Asset))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// JPEGTransmission creates a lossy upload derivative. Originals, image count,
// resolution, timestamps and frame coverage remain unchanged.
func (s *Store) JPEGTransmission(b model.Block, quality int) (model.Block, error) {
	if b.Type != "image" {
		return b, nil
	}
	raw, err := s.Read(b.Asset)
	if err != nil {
		return b, err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return b, err
	}
	if format != "png" && format != "jpeg" {
		return b, nil
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 40_000_000 {
		return b, fmt.Errorf("image dimensions exceed transmission limit")
	}
	cacheKey := fmt.Sprintf("wire-jpeg-v1-q%d-%s", quality, b.Asset)
	marker := filepath.Join(s.root, cacheKey)
	if saved, e := os.ReadFile(marker); e == nil {
		candidate := string(saved)
		if s.Exists(candidate) {
			b.Asset = candidate
			b.Data = ""
			b.MIME = "image/jpeg"
			return b, nil
		}
	}
	source, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return b, err
	}
	bounds := source.Bounds()
	rgb := image.NewRGBA(bounds)
	draw.Draw(rgb, bounds, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(rgb, bounds, source, bounds.Min, draw.Over)
	var output bytes.Buffer
	if err = jpeg.Encode(&output, rgb, &jpeg.Options{Quality: quality}); err != nil {
		return b, err
	}
	if output.Len() >= len(raw) {
		return b, nil
	}
	derived, err := s.put(output.Bytes())
	if err != nil {
		return b, err
	}
	s.mu.Lock()
	temp, e := os.CreateTemp(s.root, ".wire-")
	if e == nil {
		name := temp.Name()
		_, e = temp.WriteString(derived)
		closeErr := temp.Close()
		if e == nil {
			e = closeErr
		}
		if e == nil {
			e = os.Rename(name, marker)
		}
		_ = os.Remove(name)
	}
	s.mu.Unlock()
	if e != nil {
		return b, e
	}
	b.Asset = derived
	b.Data = ""
	b.MIME = "image/jpeg"
	return b, nil
}
