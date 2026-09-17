package server

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalSpeechTarPathRejectsTraversal(t *testing.T) {
	tests := []string{"../escape", "/absolute", "a/../../escape", `a\..\escape`, ""}
	for _, name := range tests {
		if _, err := localSpeechTarPath(name); err == nil {
			t.Errorf("localSpeechTarPath(%q) accepted an unsafe path", name)
		}
	}

	got, err := localSpeechTarPath("model/./tokens.txt")
	if err != nil {
		t.Fatalf("localSpeechTarPath accepted path returned error: %v", err)
	}
	if got != filepath.Join("model", "tokens.txt") {
		t.Fatalf("localSpeechTarPath returned %q", got)
	}
}

func TestLocalSpeechPathWithin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "staging")
	if !localSpeechPathWithin(root, filepath.Join(root, "model", "tokens.txt")) {
		t.Fatal("expected a descendant path to remain within staging")
	}
	if localSpeechPathWithin(root, filepath.Join(root, "..", "escape")) {
		t.Fatal("accepted a path outside staging")
	}
}

func TestDecodeWebSpeechConfigSettingLocal(t *testing.T) {
	_, config, err := decodeWebSpeechConfigSetting(map[string]any{
		"enabled":  true,
		"provider": localSpeechProvider,
		"local": map[string]any{
			"language": "zh-CN",
			"model":    localSpeechModelName,
		},
	})
	if err != nil {
		t.Fatalf("local speech config was rejected: %v", err)
	}
	if !config.Enabled || config.Provider != localSpeechProvider || config.Local == nil {
		t.Fatalf("decoded local speech config is incomplete: %#v", config)
	}

	if _, _, err := decodeWebSpeechConfigSetting(map[string]any{
		"enabled":  true,
		"provider": localSpeechProvider,
		"local":    map[string]any{"model": "unsupported-model"},
	}); err == nil {
		t.Fatal("unsupported local speech model was accepted")
	}
}

func TestResolveWebSpeechRuntimeConfigDefaultsToLocalWithoutSavedSettings(t *testing.T) {
	app := New(Options{FileRoot: t.TempDir()})
	config, speechErr := app.resolveWebSpeechRuntimeConfig("new-user")
	if speechErr != nil {
		t.Fatalf("resolve default local speech config: %v", speechErr)
	}
	if !config.Enabled || config.Provider != localSpeechProvider || config.Local == nil {
		t.Fatalf("default local speech config is incomplete: %#v", config)
	}
	if config.Local.Language != "zh-CN" || config.Local.Model != localSpeechModelName {
		t.Fatalf("default local speech settings = %#v", config.Local)
	}
}

func TestResolveWebSpeechRuntimeConfigPreservesExplicitDisabledSetting(t *testing.T) {
	app := New(Options{FileRoot: t.TempDir()})
	if err := app.writeWebSpeechConfig("local", map[string]any{
		"enabled":  false,
		"provider": localSpeechProvider,
		"local": map[string]any{
			"language": "zh-CN",
			"model":    localSpeechModelName,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, speechErr := app.resolveWebSpeechRuntimeConfig("local"); speechErr == nil || speechErr.Code != "STT_DISABLED" {
		t.Fatalf("explicit disabled setting error = %#v", speechErr)
	}
}

func TestLocalSpeechTranscriptReadsRuntimeJSON(t *testing.T) {
	text, err := localSpeechTranscript("noise\n{\"text\":\"你好，世界\"}\n")
	if err != nil {
		t.Fatalf("local speech transcript parse failed: %v", err)
	}
	if text != "你好，世界" {
		t.Fatalf("local speech transcript = %q", text)
	}
}

func TestLocalSpeechTranscribeRemovesTemporaryAudioOnSuccessAndConversionFailure(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the local speech runtime is currently linux/amd64 only")
	}

	tests := []struct {
		name       string
		ffmpegBody string
		wantText   string
		wantErr    string
	}{
		{
			name:       "success",
			ffmpegBody: "for arg do output=\"$arg\"; done\ndd if=/dev/zero of=\"$output\" bs=1 count=128 2>/dev/null\n",
			wantText:   "临时录音不会留存",
		},
		{
			name:       "conversion failure",
			ffmpegBody: "exit 7\n",
			wantErr:    "audio conversion failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			if err := os.MkdirAll(binDir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeLocalSpeechExecutable(t, filepath.Join(binDir, "ffmpeg"), "#!/bin/sh\n"+test.ffmpegBody)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			active := filepath.Join(root, "active")
			for _, relative := range []string{
				filepath.Join("runtime", "bin", "sherpa-onnx"),
				filepath.Join("runtime", "lib", "libonnxruntime.so"),
				filepath.Join("runtime", "lib", "libsherpa-onnx-c-api.so"),
				filepath.Join("runtime", "lib", "libsherpa-onnx-cxx-api.so"),
				filepath.Join("model", localSpeechModelName, "model.int8.onnx"),
				filepath.Join("model", localSpeechModelName, "bbpe.model"),
				filepath.Join("model", localSpeechModelName, "tokens.txt"),
			} {
				path := filepath.Join(active, relative)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				contents := []byte("fixture")
				if strings.HasSuffix(path, "sherpa-onnx") {
					contents = []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"临时录音不会留存\"}' >&2\n")
				}
				if err := os.WriteFile(path, contents, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			audioPath := filepath.Join(root, "recorded.webm")
			if err := os.WriteFile(audioPath, []byte("temporary recording"), 0o600); err != nil {
				t.Fatal(err)
			}
			orphanedDir := filepath.Join(root, localSpeechTempDirName, localSpeechRequestPrefix+"orphaned")
			if err := os.MkdirAll(orphanedDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(orphanedDir, "input.audio"), []byte("orphaned recording"), 0o600); err != nil {
				t.Fatal(err)
			}
			audio, err := os.Open(audioPath)
			if err != nil {
				t.Fatal(err)
			}
			input := &webSpeechAudioInput{File: audio}

			runtimeState := &localSpeechRuntime{
				root:           root,
				ready:          true,
				phase:          "ready",
				activeTempDirs: make(map[string]struct{}),
			}
			text, transcribeErr := runtimeState.transcribe(context.Background(), input)
			if closeErr := input.File.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if test.wantErr == "" {
				if transcribeErr != nil || text != test.wantText {
					t.Fatalf("transcribe text=%q err=%v", text, transcribeErr)
				}
			} else if transcribeErr == nil || !strings.Contains(transcribeErr.Error(), test.wantErr) {
				t.Fatalf("transcribe err=%v want substring %q", transcribeErr, test.wantErr)
			}

			tmpRoot := filepath.Join(root, "tmp")
			entries, err := os.ReadDir(tmpRoot)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("temporary speech files remained: %v", entries)
			}
		})
	}
}

func writeLocalSpeechExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("executable %s is not executable: %v", path, err)
	}
}
