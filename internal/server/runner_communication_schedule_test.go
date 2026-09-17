package server

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCommunicationScheduleSurvivesRestoreWithoutRepeatingIntroduction(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	schedule := &sessionRunnerCommunicationSchedule{now: func() time.Time { return now }}
	if !schedule.due() || !schedule.allowed() {
		t.Fatal("initial explanation not due")
	}
	if err := schedule.published(); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if err := schedule.settled(); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(schedule)
	if err != nil {
		t.Fatal(err)
	}
	restored := &sessionRunnerCommunicationSchedule{now: func() time.Time { return now }}
	if err := json.Unmarshal(encoded, restored); err != nil {
		t.Fatal(err)
	}
	if restored.due() || restored.allowed() {
		t.Fatal("restore restarted cadence")
	}
	now = now.Add(30 * time.Second)
	if restored.due() || !restored.allowed() {
		t.Fatal("optional transition cadence mismatch")
	}
	now = now.Add(60 * time.Second)
	if !restored.due() {
		t.Fatal("progress update not due after evidence and time")
	}
	if err := restored.published(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if restored.due() {
		t.Fatal("idle time without completed actions created progress")
	}
}
