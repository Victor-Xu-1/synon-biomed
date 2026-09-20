package runtimecontrol

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestScannerFailedCategoryRemainsUnknown(t *testing.T) {
	root := t.TempDir()
	writeUsageFile(t, filepath.Join(root, "not-a-directory"), 7)
	scanner := NewScanner(root, filepath.Join(root, "conda"), time.Hour)
	scanner.SetStorageRoots(StorageRoots{Logs: filepath.Join(root, "not-a-directory", "logs")})
	for _, refresh := range []bool{false, false, true} {
		usage := mustDiskUsage(t, scanner, refresh)
		encoded, err := json.Marshal(usage)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Logs struct {
				TotalBytes *int64 `json:"totalBytes"`
			} `json:"logs"`
			Temp struct {
				TotalBytes *int64 `json:"totalBytes"`
			} `json:"temp"`
		}
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if len(usage.Warnings) == 0 || wire.Logs.TotalBytes != nil {
			t.Fatalf("failed category must remain unknown: %s", encoded)
		}
		if wire.Temp.TotalBytes == nil || *wire.Temp.TotalBytes != 0 {
			t.Fatalf("absent category should still be measured as zero: %s", encoded)
		}
	}
}
