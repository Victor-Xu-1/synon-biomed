package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Setting struct {
	Key       string    `json:"key"`
	Value     any       `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Store struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

type fileData struct {
	Settings map[string]Setting `json:"settings"`
}

func New(path string) *Store {
	return &Store{path: path, now: time.Now}
}

func (s *Store) Set(key string, value any) (Setting, error) {
	if err := validateKey(key); err != nil {
		return Setting{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked()
	if err != nil {
		return Setting{}, err
	}
	setting := Setting{Key: key, Value: value, UpdatedAt: s.now().UTC()}
	data.Settings[key] = setting
	if err := s.saveLocked(data); err != nil {
		return Setting{}, err
	}
	return setting, nil
}

func (s *Store) Update(key string, update func(current any, found bool) (any, error)) (Setting, error) {
	if err := validateKey(key); err != nil {
		return Setting{}, err
	}
	if update == nil {
		return Setting{}, errors.New("settings update function is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked()
	if err != nil {
		return Setting{}, err
	}
	current, found := data.Settings[key]
	value, err := update(current.Value, found)
	if err != nil {
		return Setting{}, err
	}
	setting := Setting{Key: key, Value: value, UpdatedAt: s.now().UTC()}
	data.Settings[key] = setting
	if err := s.saveLocked(data); err != nil {
		return Setting{}, err
	}
	return setting, nil
}
func (s *Store) Get(key string) (Setting, bool, error) {
	if err := validateKey(key); err != nil {
		return Setting{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked()
	if err != nil {
		return Setting{}, false, err
	}
	setting, ok := data.Settings[key]
	return setting, ok, nil
}

func (s *Store) Delete(key string) (bool, error) {
	if err := validateKey(key); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	_, existed := data.Settings[key]
	if existed {
		delete(data.Settings, key)
		if err := s.saveLocked(data); err != nil {
			return false, err
		}
	}
	return existed, nil
}

func (s *Store) List() (map[string]Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	result := make(map[string]Setting, len(data.Settings))
	keys := make([]string, 0, len(data.Settings))
	for key := range data.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result[key] = data.Settings[key]
	}
	return result, nil
}

func (s *Store) loadLocked() (fileData, error) {
	if s.path == "" {
		return fileData{}, errors.New("settings store path is not configured")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileData{Settings: map[string]Setting{}}, nil
		}
		return fileData{}, err
	}
	var data fileData
	if err := json.Unmarshal(raw, &data); err != nil {
		return fileData{}, err
	}
	if data.Settings == nil {
		data.Settings = map[string]Setting{}
	}
	return data, nil
}

func (s *Store) saveLocked(data fileData) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func validateKey(key string) error {
	if key == "" {
		return errors.New("settings key is required")
	}
	for _, r := range key {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '_' || r == '-' || r == '.' {
			continue
		}
		return fmt.Errorf("invalid settings key: %s", key)
	}
	if strings.Contains(key, "..") {
		return fmt.Errorf("invalid settings key: %s", key)
	}
	return nil
}
