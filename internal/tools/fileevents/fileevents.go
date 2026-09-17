package fileevents

import (
	"path/filepath"
	"strings"
	"sync"
)

type ChangeHandler func(path string)

var handlers = struct {
	sync.RWMutex
	items []ChangeHandler
}{}

func RegisterChangeHandler(handler ChangeHandler) {
	if handler == nil {
		return
	}
	handlers.Lock()
	defer handlers.Unlock()
	handlers.items = append(handlers.items, handler)
}

func NotifyChanged(paths ...string) {
	cleaned := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			absolute = filepath.Clean(path)
		}
		if _, ok := seen[absolute]; ok {
			continue
		}
		seen[absolute] = struct{}{}
		cleaned = append(cleaned, absolute)
	}
	if len(cleaned) == 0 {
		return
	}
	handlers.RLock()
	copied := append([]ChangeHandler(nil), handlers.items...)
	handlers.RUnlock()
	for _, handler := range copied {
		for _, path := range cleaned {
			handler(path)
		}
	}
}
