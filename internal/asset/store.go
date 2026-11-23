package asset

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"kkagent/internal/model"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Assets are content-addressed on disk; sessions keep references, never repeated base64.
type Store struct {
	root string
	max  int64
	mu   sync.Mutex
}

func New(root string, max int64) (*Store, error) {
	e := os.MkdirAll(root, 0700)
	return &Store{root: root, max: max}, e
}
func (s *Store) put(raw []byte) (string, error) {
	if int64(len(raw)) > s.max {
		return "", fmt.Errorf("asset exceeds size limit")
	}
	hash := sha256.Sum256(raw)
	id := hex.EncodeToString(hash[:])
	path := filepath.Join(s.root, id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, e := os.Stat(path); e == nil {
		return id, nil
	}
	f, e := os.CreateTemp(s.root, ".asset-")
	if e != nil {
		return "", e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(raw); e != nil {
		f.Close()
		return "", e
	}
	if e = f.Close(); e != nil {
		return "", e
	}
	if e = os.Rename(name, path); e != nil {
		return "", e
	}
	return id, nil
}
func (s *Store) Import(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return "", e
	}
	if st.Size() > s.max {
		return "", fmt.Errorf("asset too large")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	return s.put(b)
}
func valid(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, e := hex.DecodeString(id)
	return e == nil
}
func (s *Store) Read(id string) ([]byte, error) {
	if !valid(id) {
		return nil, fmt.Errorf("invalid asset reference")
	}
	return os.ReadFile(filepath.Join(s.root, id))
}
func (s *Store) Exists(id string) bool {
	if !valid(id) {
		return false
	}
	_, e := os.Stat(filepath.Join(s.root, id))
	return e == nil
}
func (s *Store) Spool(blocks []model.Block) ([]model.Block, error) {
	out := make([]model.Block, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if len(b.Text) > 8<<20 {
				return nil, fmt.Errorf("text block too large")
			}
		case "image":
			if b.MIME == "" {
				b.MIME = "image/png"
			}
			if !strings.HasPrefix(b.MIME, "image/") {
				return nil, fmt.Errorf("invalid image MIME")
			}
			if b.Data != "" {
				raw, e := base64.StdEncoding.DecodeString(b.Data)
				if e != nil {
					return nil, fmt.Errorf("invalid image base64")
				}
				id, e := s.put(raw)
				if e != nil {
					return nil, e
				}
				b.Asset = id
				b.Data = ""
			}
			if !s.Exists(b.Asset) {
				return nil, fmt.Errorf("image asset not found")
			}
		default:
			return nil, fmt.Errorf("unsupported content block %q", b.Type)
		}
		out = append(out, b)
	}
	return out, nil
}
func (s *Store) Encode(b model.Block) (string, error) {
	if b.Data != "" {
		return b.Data, nil
	}
	raw, e := s.Read(b.Asset)
	if e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
