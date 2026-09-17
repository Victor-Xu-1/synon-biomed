package server

import (
	"errors"
	"fmt"
	"strings"
)

const memoryAutoExtractionSettingKey = "memory.autoExtractionEnabled"

func (s *Server) memoryAutoExtractionEnabled(userID string) (bool, error) {
	if s == nil || s.settingsStore == nil {
		return false, errors.New("automatic memory settings are unavailable")
	}
	setting, found, err := s.settingsStore.Get(userPreferenceSettingKey(memoryAutoExtractionSettingKey, strings.TrimSpace(userID)))
	if err != nil {
		return false, fmt.Errorf("read automatic memory setting: %w", err)
	}
	if !found {
		return s.memoryConfig.ExtractEnabled, nil
	}
	enabled, ok := setting.Value.(bool)
	if !ok {
		return false, errors.New("automatic memory setting is invalid")
	}
	return enabled, nil
}

func (s *Server) setMemoryAutoExtractionEnabled(userID string, enabled bool) error {
	if s == nil || s.settingsStore == nil {
		return errors.New("automatic memory settings are unavailable")
	}
	_, err := s.settingsStore.Set(
		userPreferenceSettingKey(memoryAutoExtractionSettingKey, strings.TrimSpace(userID)),
		enabled,
	)
	if err != nil {
		return fmt.Errorf("persist automatic memory setting: %w", err)
	}
	return nil
}
