package feishu

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestImageBlockWatcherExtractsRealLocalHTTPAndDataImages(t *testing.T) {
	tmp := t.TempDir()
	imagePath := filepath.Join(tmp, "cat.png")
	if err := os.WriteFile(imagePath, []byte{0x89, 'P', 'N', 'G'}, 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	watcher := NewImageBlockWatcher()
	data := base64.StdEncoding.EncodeToString([]byte("inline image"))
	out := watcher.Feed("![cat](" + imagePath + ") ![remote](https://example.com/dog.png) ![inline](data:image/png;base64," + data + ")")
	if len(out) != 3 {
		t.Fatalf("uploads = %+v", out)
	}
	if out[0].Kind != PendingUploadImage || out[0].Alt != "cat" || out[0].Source.Kind != UploadSourcePath || out[0].Source.Path != imagePath {
		t.Fatalf("local image upload = %+v", out[0])
	}
	if out[1].Source.Kind != UploadSourceURL || out[1].Source.URL != "https://example.com/dog.png" {
		t.Fatalf("remote image upload = %+v", out[1])
	}
	if out[2].Source.Kind != UploadSourceBase64 || out[2].Source.MIME != "image/png" || out[2].Source.Data != data {
		t.Fatalf("data image upload = %+v", out[2])
	}
}

func TestImageBlockWatcherHandlesSplitChunksDedupResetAndSkipsUnsafePaths(t *testing.T) {
	tmp := t.TempDir()
	imagePath := filepath.Join(tmp, "split.jpg")
	if err := os.WriteFile(imagePath, []byte("jpg"), 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	watcher := NewImageBlockWatcher()
	if out := watcher.Feed("start ![spl"); len(out) != 0 {
		t.Fatalf("partial feed emitted uploads = %+v", out)
	}
	if out := watcher.Feed("it](" + imagePath + ") end"); len(out) != 1 || out[0].Source.Path != imagePath {
		t.Fatalf("split feed uploads = %+v", out)
	}
	if out := watcher.Feed(" repeated ![split](" + imagePath + ")"); len(out) != 0 {
		t.Fatalf("duplicate upload emitted = %+v", out)
	}
	if all := watcher.Drain(); len(all) != 1 {
		t.Fatalf("drain = %+v", all)
	}

	if out := watcher.Feed("![rel](relative/path.png) ![etc](/etc/passwd)"); len(out) != 0 {
		t.Fatalf("unsafe paths emitted = %+v", out)
	}
	watcher.Reset()
	if len(watcher.Drain()) != 0 {
		t.Fatal("reset did not clear accumulated uploads")
	}
	if out := watcher.Feed("![split](" + imagePath + ")"); len(out) != 1 {
		t.Fatalf("reset did not clear dedup state: %+v", out)
	}
}

func TestFileBlockWatcherExtractsRealLocalFileAndDataURI(t *testing.T) {
	tmp := t.TempDir()
	reportPath := filepath.Join(tmp, "report.pdf")
	if err := os.WriteFile(reportPath, []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatalf("write report fixture: %v", err)
	}
	data := base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`))

	watcher := NewFileBlockWatcher()
	out := watcher.Feed("下载 [report.pdf](" + reportPath + ") and [json](data:application/json;base64," + data + ")")
	if len(out) != 2 {
		t.Fatalf("uploads = %+v", out)
	}
	if out[0].Kind != PendingUploadFile || out[0].Alt != "report.pdf" || out[0].Source.Kind != UploadSourcePath || out[0].Source.Path != reportPath {
		t.Fatalf("file upload = %+v", out[0])
	}
	if out[1].Source.Kind != UploadSourceBase64 || out[1].Source.MIME != "application/json" || out[1].Source.Data != data {
		t.Fatalf("data file upload = %+v", out[1])
	}
}

func TestFileBlockWatcherSkipsImagesRemoteLinksRelativePathsAndDeduplicates(t *testing.T) {
	tmp := t.TempDir()
	reportPath := filepath.Join(tmp, "report.pdf")
	if err := os.WriteFile(reportPath, []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatalf("write report fixture: %v", err)
	}

	watcher := NewFileBlockWatcher()
	if out := watcher.Feed("![img](" + filepath.Join(tmp, "pic.png") + ") [docs](https://example.com) [rel](relative.pdf)"); len(out) != 0 {
		t.Fatalf("unexpected upload = %+v", out)
	}
	if out := watcher.Feed("文件 [rep"); len(out) != 0 {
		t.Fatalf("partial feed emitted uploads = %+v", out)
	}
	if out := watcher.Feed("ort](" + reportPath + ")"); len(out) != 1 || out[0].Source.Path != reportPath {
		t.Fatalf("split file upload = %+v", out)
	}
	if out := watcher.Feed(" repeated [report](" + reportPath + ")"); len(out) != 0 {
		t.Fatalf("duplicate upload emitted = %+v", out)
	}
	if out := watcher.Feed(" [bad](/etc/passwd)"); len(out) != 0 {
		t.Fatalf("unsafe file path emitted = %+v", out)
	}
}
