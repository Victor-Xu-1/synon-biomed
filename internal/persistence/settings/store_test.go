package settings

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestUpdateIsAtomicAcrossConcurrentWriters(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "settings.json"))
	if _, err := store.Set("counter", 0); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.Update("counter", func(current any, found bool) (any, error) {
				value := 0
				switch typed := current.(type) {
				case float64:
					value = int(typed)
				case int:
					value = typed
				}
				return value + 1, nil
			})
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	setting, found, err := store.Get("counter")
	if err != nil || !found {
		t.Fatalf("counter found=%v err=%v", found, err)
	}
	value, ok := setting.Value.(float64)
	if !ok || int(value) != 20 {
		t.Fatalf("counter = %#v", setting.Value)
	}
}
