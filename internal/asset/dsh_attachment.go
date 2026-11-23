package asset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"kkagent/internal/model"
	"os"
	"path/filepath"
)

// DSHAttachment describes an actual persisted attachment, matching the SDK's
// text representation. It does not turn a local path into model-visible pixels.
func (s *Store) DSHAttachment(b model.Block) (string, error) {
	raw, err := s.Read(b.Asset)
	if err != nil {
		return "", err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("invalid tool attachment: %w", err)
	}
	mime, extension := "image/"+format, "."+format
	if format == "jpeg" {
		extension = ".jpg"
	}
	path := filepath.Join(filepath.Dir(s.root), "attachments", "v1", "objects", b.Asset[:2], b.Asset)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if _, err = os.Stat(path); os.IsNotExist(err) {
		// Assets are immutable. A hard link avoids another in-memory image cache.
		if err = os.Link(filepath.Join(s.root, b.Asset), path); err != nil {
			err = os.WriteFile(path, raw, 0600)
		}
		if err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	quoted, _ := json.Marshal(path)
	return fmt.Sprintf("Image sha256:%s; request preview %dx%dpx. Normalized copy (read-only; may be resized or re-encoded): %s (%dx%dpx, %s). Source dimensions, format, and byte size may differ. Copy to a writable path ending in %s before editing.", b.Asset, config.Width, config.Height, quoted, config.Width, config.Height, mime, extension), nil
}
