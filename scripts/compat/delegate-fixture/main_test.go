package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestValidateLoopbackListen(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "localhost:39010", "[::1]:0"} {
		if err := validateLoopbackListen(address); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:39010", "192.0.2.10:39010", "missing-port"} {
		if err := validateLoopbackListen(address); err == nil {
			t.Fatalf("accepted unsafe address %q", address)
		}
	}
}

func TestRunServesFixtureAndRemovesTemporaryDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--listen", "127.0.0.1:0"}, writer)
		_ = writer.Close()
	}()
	var ready readyReport
	if err := json.NewDecoder(reader).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if ready.Status != "ready" || !ready.Temporary {
		t.Fatalf("ready = %#v", ready)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(ready.BaseURL + ready.Fixture.TracePath + "?include_messages=true")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		t.Fatalf("trace status = %d", response.StatusCode)
	}
	var parent map[string]any
	if err := json.NewDecoder(response.Body).Decode(&parent); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if parent["message_count"] != float64(9) || ready.Fixture.NotificationToolUseID == "" || ready.Fixture.NotificationToolResultMessageID == "" {
		t.Fatalf("fixture readiness or parent message count = ready %#v parent %#v", ready.Fixture, parent)
	}
	assertServedFixtureTree(t, parent, ready.Fixture.SecondChildFrameID, ready.Fixture.GrandchildFrameID)
	parentContext := parent["context_data"].(map[string]any)
	assertServedNotifications(t, parentContext["_messages"].([]any), ready.Fixture.NotificationToolUseID, ready.Fixture.ChildFrameID)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ready.Database); !os.IsNotExist(err) {
		t.Fatalf("temporary database still exists: %s err=%v", ready.Database, err)
	}
}

func TestCommandServesMultiLevelFixtureInRealProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("real helper process test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt cannot reliably stop the helper process on Windows; run this contract in WSL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	binary := t.TempDir() + "/delegate-fixture"
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real helper process: %v\n%s", err, output)
	}
	command := exec.CommandContext(ctx, binary, "--listen", "127.0.0.1:0")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited && command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	var ready readyReport
	if err := json.NewDecoder(stdout).Decode(&ready); err != nil {
		t.Fatalf("decode real process readiness: %v stderr=%s", err, stderr.String())
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(
		ready.BaseURL + ready.Fixture.TracePath + "?include_messages=true",
	)
	if err != nil {
		t.Fatalf("real process trace: %v stderr=%s", err, stderr.String())
	}
	var parent map[string]any
	if err := json.NewDecoder(response.Body).Decode(&parent); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("real process trace status=%d body=%#v stderr=%s", response.StatusCode, parent, stderr.String())
	}
	assertServedFixtureTree(t, parent, ready.Fixture.SecondChildFrameID, ready.Fixture.GrandchildFrameID)
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitResult := make(chan error, 1)
	go func() { waitResult <- command.Wait() }()
	select {
	case err := <-waitResult:
		waited = true
		if err != nil {
			t.Fatalf("real helper process exit: %v stderr=%s", err, stderr.String())
		}
	case <-time.After(8 * time.Second):
		_ = command.Process.Kill()
		<-waitResult
		waited = true
		t.Fatalf("real helper process did not stop within 8s stderr=%s", stderr.String())
	}
	if _, err := os.Stat(ready.Database); !os.IsNotExist(err) {
		t.Fatalf("real process temporary database still exists: %s err=%v", ready.Database, err)
	}
}

func assertServedFixtureTree(t *testing.T, parent map[string]any, secondChildID, grandchildID string) {
	t.Helper()
	children := parent["children"].([]any)
	if len(children) != 2 {
		t.Fatalf("children = %#v", children)
	}
	var foundSecond, foundGrandchild bool
	for _, rawChild := range children {
		child := rawChild.(map[string]any)
		if child["id"] == secondChildID && child["status"] == "awaiting_user_response" {
			foundSecond = true
		}
		for _, rawGrandchild := range child["children"].([]any) {
			grandchild := rawGrandchild.(map[string]any)
			if grandchild["id"] == grandchildID && grandchild["status"] == "failed" {
				foundGrandchild = true
			}
		}
	}
	if !foundSecond || !foundGrandchild {
		t.Fatalf("multi-level fixture second=%v grandchild=%v parent=%#v", foundSecond, foundGrandchild, parent)
	}
}

func assertServedNotifications(t *testing.T, messages []any, toolUseID, childFrameID string) {
	t.Helper()
	for _, rawMessage := range messages {
		message := rawMessage.(map[string]any)
		content, _ := message["content"].([]any)
		for _, rawBlock := range content {
			block, _ := rawBlock.(map[string]any)
			if block["type"] != "tool_result" || block["tool_use_id"] != toolUseID {
				continue
			}
			var envelope struct {
				Notifications []struct {
					NotificationType string         `json:"notification_type"`
					SenderFrameID    string         `json:"sender_frame_id"`
					Payload          map[string]any `json:"payload"`
				} `json:"notifications"`
			}
			if err := json.Unmarshal([]byte(block["content"].(string)), &envelope); err != nil {
				t.Fatal(err)
			}
			if len(envelope.Notifications) != 3 || envelope.Notifications[0].NotificationType != "completion" ||
				envelope.Notifications[0].SenderFrameID != childFrameID || envelope.Notifications[1].Payload["kind"] != "question" ||
				envelope.Notifications[2].Payload["kind"] != "info" {
				t.Fatalf("served notifications = %#v", envelope.Notifications)
			}
			return
		}
	}
	t.Fatalf("notification tool_result %q not found in %#v", toolUseID, messages)
}
