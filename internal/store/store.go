package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"kkagent/internal/model"
	"os"
	"path/filepath"
	"strings"
)

func (s *Store) Client(client string) (string, error) {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(client)))
	raw, err := os.ReadFile(filepath.Join(s.Root, "clients", hash))
	return string(raw), err
}
func (s *Store) SaveClient(client, run string) error {
	root := filepath.Join(s.Root, "clients")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(client)))
	return os.WriteFile(filepath.Join(root, hash), []byte(run), 0600)
}
func (s *Store) Recover() error {
	entries, err := os.ReadDir(filepath.Join(s.Root, "runs"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		v, err := s.Load(entry.Name())
		if err != nil {
			continue
		}
		if !model.Terminal(v.Status) {
			v.Status = "failed"
			v.Error = "service restarted; automatic replay disabled"
			if err = s.Save(v); err != nil {
				return err
			}
		}
		if v.ClientID != "" {
			if err = s.SaveClient(v.ClientID, v.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

type Store struct{ Root string }

func New(root string) (*Store, error) {
	s := &Store{Root: root}
	return s, os.MkdirAll(filepath.Join(root, "runs"), 0700)
}
func valid(id string) bool               { return len(id) == 32 && !strings.ContainsAny(id, "/\\.") }
func (s *Store) RunDir(id string) string { return filepath.Join(s.Root, "runs", id) }
func (s *Store) Save(v model.Snapshot) error {
	if !valid(v.ID) {
		return errors.New("invalid run ID")
	}
	return Atomic(filepath.Join(s.RunDir(v.ID), "status.json"), v)
}
func (s *Store) Load(id string) (model.Snapshot, error) {
	var v model.Snapshot
	if !valid(id) {
		return v, errors.New("invalid run ID")
	}
	b, e := os.ReadFile(filepath.Join(s.RunDir(id), "status.json"))
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(b, &v)
	return v, e
}
func Atomic(path string, value any) error {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	dir := filepath.Dir(path)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".write-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}
func (s *Store) Append(id string, event model.Event) error {
	b, e := json.Marshal(event)
	if e != nil {
		return e
	}
	if len(b) > 1<<20 {
		return errors.New("event too large")
	}
	f, e := os.OpenFile(filepath.Join(s.RunDir(id), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = f.Write(append(b, '\n'))
	return e
}
func (s *Store) Events(id string, after int64, limit int) ([]model.Event, error) {
	if !valid(id) {
		return nil, errors.New("invalid run ID")
	}
	f, e := os.Open(filepath.Join(s.RunDir(id), "events.jsonl"))
	if os.IsNotExist(e) {
		return []model.Event{}, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	out := []model.Event{}
	for scanner.Scan() {
		var event model.Event
		if e := json.Unmarshal(scanner.Bytes(), &event); e != nil {
			return nil, fmt.Errorf("corrupt event journal")
		}
		if event.Seq > after {
			out = append(out, event)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, scanner.Err()
}
func (s *Store) All() ([]model.Snapshot, error) {
	entries, e := os.ReadDir(filepath.Join(s.Root, "runs"))
	if e != nil {
		return nil, e
	}
	out := []model.Snapshot{}
	for _, entry := range entries {
		if entry.IsDir() {
			v, e := s.Load(entry.Name())
			if e != nil {
				continue
			}
			if !model.Terminal(v.Status) {
				v.Status = "failed"
				v.Error = "service restarted; automatic replay disabled"
				_ = s.Save(v)
			}
			out = append(out, v)
		}
	}
	return out, nil
}
