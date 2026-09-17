package common

import (
	"strings"
	"testing"
	"time"
)

func TestMessageBufferAccumulatesAndFlushesOnComplete(t *testing.T) {
	var flushed []struct {
		text       string
		isComplete bool
	}
	buffer := NewMessageBuffer(func(text string, isComplete bool) error {
		flushed = append(flushed, struct {
			text       string
			isComplete bool
		}{text: text, isComplete: isComplete})
		return nil
	}, 500*time.Millisecond, 1000)

	buffer.Append("Hello ")
	buffer.Append("World")
	buffer.Complete()

	if len(flushed) == 0 {
		t.Fatal("expected at least one flush")
	}
	var all strings.Builder
	for _, item := range flushed {
		all.WriteString(item.text)
	}
	if all.String() != "Hello World" {
		t.Fatalf("flushed text = %q", all.String())
	}
	if !flushed[len(flushed)-1].isComplete {
		t.Fatal("last flush should be complete")
	}
}

func TestMessageBufferFlushesWhenThresholdReached(t *testing.T) {
	flushed := make(chan string, 1)
	buffer := NewMessageBuffer(func(text string, _ bool) error {
		flushed <- text
		return nil
	}, 10*time.Second, 10)

	buffer.Append("12345678901")
	select {
	case text := <-flushed:
		if text != "12345678901" {
			t.Fatalf("flushed = %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("threshold flush timed out")
	}
	buffer.Reset()
}

func TestMessageBufferFlushesOnInterval(t *testing.T) {
	flushed := make(chan string, 1)
	buffer := NewMessageBuffer(func(text string, _ bool) error {
		flushed <- text
		return nil
	}, 50*time.Millisecond, 1000)

	buffer.Append("hi")
	select {
	case text := <-flushed:
		if text != "hi" {
			t.Fatalf("flushed = %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("interval flush timed out")
	}
}

func TestMessageBufferDoesNotFlushEmptyComplete(t *testing.T) {
	var flushed []string
	buffer := NewMessageBuffer(func(text string, _ bool) error {
		flushed = append(flushed, text)
		return nil
	}, 500*time.Millisecond, 1000)

	buffer.Complete()
	if len(flushed) != 0 {
		t.Fatalf("flushed = %#v", flushed)
	}
}

func TestMessageBufferResetClearsPendingText(t *testing.T) {
	var flushed []string
	buffer := NewMessageBuffer(func(text string, _ bool) error {
		flushed = append(flushed, text)
		return nil
	}, 500*time.Millisecond, 1000)

	buffer.Append("first")
	buffer.Reset()
	buffer.Append("second")
	buffer.Complete()

	if strings.Join(flushed, "") != "second" {
		t.Fatalf("flushed = %#v", flushed)
	}
}
