package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

const visualReviewTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"

func writeVisualReviewTestPNG(t *testing.T, path string) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(visualReviewTestPNG)
	if err != nil {
		t.Fatalf("decode visual review PNG: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write visual review PNG: %v", err)
	}
}

func TestServerAgentRuntimeRejectsRetiredVisualReviewDispatch(t *testing.T) {
	fileRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	writeVisualReviewTestPNG(t, filepath.Join(fileRoot, "result.png"))
	writeVisualReviewTestPNG(t, filepath.Join(workspaceRoot, "result.png"))

	srv := New(Options{FileRoot: fileRoot})
	srv.visualReviewChallengeToken = func() (string, error) { return "2345ABCD", nil }
	gateway := serverAgentRuntimeToolGateway{
		server: srv, allowedTools: []string{"VisualReview"}, suppressHooks: true,
		kernel: &agentKernelContext{workspaceDir: workspaceRoot},
	}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "visual-workspace", Name: "VisualReview",
		Arguments: json.RawMessage(`{"action":"collect","objective":"inspect generated result","image_path":"result.png"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := mapValue(result.Value)
	if payload["code"] != "retired_tool" || payload["ok"] != false || len(result.Parts) != 0 {
		t.Fatalf("retired VisualReview dispatch result=%#v", result)
	}
}

func TestVisualReviewValidateLayoutRecomputesHashCanvasClippingAndOverlaps(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "chart.png")
	rawImage := writeVisualReviewSizedPNG(t, imagePath, 200, 120)
	digest := sha256.Sum256(rawImage)
	manifestPath := filepath.Join(root, "chart.layout.json")
	writeManifest := func(boxes []any, imageDigest string) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"schema": visualReviewLayoutSchema, "image_path": "chart.png", "image_sha256": imageDigest,
			"canvas": map[string]any{"width": 200, "height": 120}, "text_boxes": boxes,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	goodBoxes := []any{
		map[string]any{"id": "label-a", "text": "A", "x0": 10, "y0": 10, "x1": 45, "y1": 28},
		map[string]any{"id": "label-b", "text": "B", "x0": 70, "y0": 10, "x1": 105, "y1": 28},
	}
	writeManifest(goodBoxes, hex.EncodeToString(digest[:]))
	srv := New(Options{FileRoot: root})
	ctx := withVisualReviewWorkspaceRoot(context.Background(), root)
	result, err := srv.executeVisualReviewTool(ctx, map[string]any{
		"action": "validate_layout", "objective": "verify generated chart labels",
		"image_path": "chart.png", "render_manifest_path": "chart.layout.json",
	})
	passed := mapValue(result)
	if err != nil || passed["status"] != "passed" || passed["layout_verified"] != true ||
		mapValue(passed["layout_validation"])["overlap_count"] != float64(0) {
		t.Fatalf("valid layout result=%#v err=%v", passed, err)
	}

	badBoxes := append([]any{}, goodBoxes...)
	badBoxes[1] = map[string]any{"id": "label-b", "text": "B", "x0": 30, "y0": 18, "x1": 80, "y1": 38}
	badBoxes = append(badBoxes, map[string]any{"id": "label-c", "text": "C", "x0": 180, "y0": 100, "x1": 220, "y1": 125})
	writeManifest(badBoxes, strings.Repeat("0", sha256.Size*2))
	result, err = srv.executeVisualReviewTool(ctx, map[string]any{
		"action": "validate_layout", "objective": "verify generated chart labels after rerender",
		"image_path": "chart.png", "render_manifest_path": "chart.layout.json",
	})
	failed := mapValue(result)
	validation := mapValue(failed["layout_validation"])
	if err != nil || failed["status"] != "needs_fix" || failed["layout_verified"] != false ||
		numberValue(validation["overlap_count"]) < 1 || numberValue(validation["clipped_count"]) != 1 ||
		!visualReviewHasBlocker(failed, "layout_image_hash_mismatch") ||
		!visualReviewHasBlocker(failed, "layout_text_overlap") || !visualReviewHasBlocker(failed, "layout_text_clipped") {
		t.Fatalf("invalid layout result=%#v err=%v", failed, err)
	}
}

func writeVisualReviewSizedPNG(t *testing.T, path string, width int, height int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.SetRGBA(x, y, color.RGBA{R: 245, G: 245, B: 245, A: 255})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestVisualReviewImagePathRejectsForeignAbsoluteTraversalAndSymlinkEscape(t *testing.T) {
	fileRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	foreignRoot := t.TempDir()
	foreignImage := filepath.Join(foreignRoot, "foreign.png")
	writeVisualReviewTestPNG(t, foreignImage)

	srv := New(Options{FileRoot: fileRoot})
	ctx := withVisualReviewWorkspaceRoot(context.Background(), workspaceRoot)
	for _, path := range []string{foreignImage, filepath.Join("..", filepath.Base(foreignRoot), "foreign.png")} {
		if _, err := srv.visualReviewImageEvidence(ctx, path, "primary"); err == nil ||
			(!strings.Contains(err.Error(), "authorized") && !strings.Contains(err.Error(), "escapes root")) {
			t.Fatalf("VisualReview path %q was not rejected at the authorized-root boundary: %v", path, err)
		}
	}

	symlinkPath := filepath.Join(workspaceRoot, "foreign-link.png")
	if err := os.Symlink(foreignImage, symlinkPath); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if _, err := srv.visualReviewImageEvidence(ctx, "foreign-link.png", "primary"); err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("VisualReview symlink escape was not rejected: %v", err)
	}
}
