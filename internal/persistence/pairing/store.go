package pairing

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

type User struct {
	Platform    string    `json:"platform"`
	UserID      string    `json:"userId"`
	OwnerUserID string    `json:"ownerUserId,omitempty"`
	DisplayName string    `json:"displayName,omitempty"`
	PairedAt    time.Time `json:"pairedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Store struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

type fileData struct {
	Users []User `json:"users"`
}

func New(path string) *Store {
	return &Store{path: path, now: time.Now}
}

func (s *Store) Allow(platform string, userID string, displayName string) (User, error) {
	return s.AllowForOwner(platform, userID, displayName, "")
}

func (s *Store) AllowForOwner(platform string, userID string, displayName string, ownerUserID string) (User, error) {
	platform, userID, displayName, err := cleanPairingInput(platform, userID, displayName)
	if err != nil {
		return User{}, err
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	if len(ownerUserID) > 512 {
		return User{}, errors.New("pairing owner user id is too long")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.loadLocked()
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC()
	for index, user := range data.Users {
		if user.Platform != platform || user.UserID != userID {
			continue
		}
		user.DisplayName = displayName
		if ownerUserID != "" {
			if user.OwnerUserID != "" && user.OwnerUserID != ownerUserID {
				return User{}, errors.New("pairing owner user id conflict")
			}
			user.OwnerUserID = ownerUserID
		}
		user.UpdatedAt = now
		if user.PairedAt.IsZero() {
			user.PairedAt = now
		}
		data.Users[index] = user
		if err := s.saveLocked(data); err != nil {
			return User{}, err
		}
		return user, nil
	}
	user := User{
		Platform:    platform,
		UserID:      userID,
		OwnerUserID: ownerUserID,
		DisplayName: displayName,
		PairedAt:    now,
		UpdatedAt:   now,
	}
	data.Users = append(data.Users, user)
	sortUsers(data.Users)
	if err := s.saveLocked(data); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Store) Get(platform string, userID string) (User, bool, error) {
	platform, userID, _, err := cleanPairingInput(platform, userID, "")
	if err != nil {
		return User{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked()
	if err != nil {
		return User{}, false, err
	}
	for _, user := range data.Users {
		if user.Platform == platform && user.UserID == userID {
			return user, true, nil
		}
	}
	return User{}, false, nil
}

func (s *Store) Revoke(platform string, userID string) (bool, error) {
	platform, userID, _, err := cleanPairingInput(platform, userID, "")
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	kept := data.Users[:0]
	removed := false
	for _, user := range data.Users {
		if user.Platform == platform && user.UserID == userID {
			removed = true
			continue
		}
		kept = append(kept, user)
	}
	if !removed {
		return false, nil
	}
	data.Users = kept
	if err := s.saveLocked(data); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) List(platform string) ([]User, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform != "" {
		if err := validatePlatform(platform); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	users := make([]User, 0, len(data.Users))
	for _, user := range data.Users {
		if platform == "" || user.Platform == platform {
			users = append(users, user)
		}
	}
	sortUsers(users)
	return users, nil
}

func (s *Store) IsPaired(platform string, userID string) (bool, error) {
	_, found, err := s.Get(platform, userID)
	return found, err
}

func (s *Store) loadLocked() (fileData, error) {
	if s.path == "" {
		return fileData{}, errors.New("pairing store path is not configured")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileData{Users: []User{}}, nil
		}
		return fileData{}, err
	}
	var data fileData
	if err := json.Unmarshal(raw, &data); err != nil {
		return fileData{}, err
	}
	if data.Users == nil {
		data.Users = []User{}
	}
	sortUsers(data.Users)
	return data, nil
}

func (s *Store) saveLocked(data fileData) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	sortUsers(data.Users)
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func cleanPairingInput(platform string, userID string, displayName string) (string, string, string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	userID = strings.TrimSpace(userID)
	displayName = strings.TrimSpace(displayName)
	if err := validatePlatform(platform); err != nil {
		return "", "", "", err
	}
	if err := validateUserID(userID); err != nil {
		return "", "", "", err
	}
	if len(displayName) > 256 {
		return "", "", "", fmt.Errorf("pairing displayName is too long")
	}
	return platform, userID, displayName, nil
}

func validatePlatform(platform string) error {
	if platform == "" {
		return errors.New("pairing platform is required")
	}
	if len(platform) > 64 {
		return fmt.Errorf("pairing platform is too long")
	}
	for _, r := range platform {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '_' || r == '-' || r == '.' {
			continue
		}
		return fmt.Errorf("invalid pairing platform: %s", platform)
	}
	if strings.Contains(platform, "..") {
		return fmt.Errorf("invalid pairing platform: %s", platform)
	}
	return nil
}

func validateUserID(userID string) error {
	if userID == "" {
		return errors.New("pairing userId is required")
	}
	if len(userID) > 256 {
		return fmt.Errorf("pairing userId is too long")
	}
	for _, r := range userID {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid pairing userId")
		}
	}
	return nil
}

func sortUsers(users []User) {
	sort.Slice(users, func(i, j int) bool {
		if users[i].Platform == users[j].Platform {
			return users[i].UserID < users[j].UserID
		}
		return users[i].Platform < users[j].Platform
	})
}
