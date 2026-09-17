//go:build linux

package processsupervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryPressureReaderIsBoundedAndRejectsUnknownTelemetry(t *testing.T) {
	root := t.TempDir()
	group := filepath.Join(root, "owned")
	if err := os.Mkdir(group, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"memory.current": "95", "memory.high": "80", "memory.max": "100", "memory.swap.current": "5", "cpu.stat": "user_usec 42\nsystem_usec 3\n", "memory.pressure": "some avg10=95.00 avg60=0.00 total=123\nfull avg10=85.00 avg60=0.00 total=456\n"}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(group, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := readMemoryPressureAt(root, "/owned", time.Now())
	if err != nil || s.Status != "pressured" || s.FullStallUsec != 456 || s.UserCPUUsec != 42 || s.LimitBytes != 100 {
		t.Fatalf("sample=%+v err=%v", s, err)
	}
	for _, input := range []string{"/../owned", "/owned/..", "relative", "/owned\n"} {
		if _, err := readMemoryPressureAt(root, input, time.Now()); err == nil {
			t.Fatalf("unsafe identity accepted: %q", input)
		}
	}
	for _, value := range []string{"full avg10=NaN total=1", "full avg10=101 total=1", "some avg10=90 total=1", strings.Repeat("x", 17000)} {
		if err := os.WriteFile(filepath.Join(group, "memory.pressure"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readMemoryPressureAt(root, "/owned", time.Now()); err == nil {
			t.Fatal("invalid telemetry accepted")
		}
	}
	if err := os.Remove(filepath.Join(group, "memory.pressure")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(group, "memory.pressure")); err != nil {
		t.Fatal(err)
	}
	if _, err := readMemoryPressureAt(root, "/owned", time.Now()); err == nil {
		t.Fatal("external telemetry link accepted")
	}
}

func TestMemoryPressureReaderSamplesRealOwnCgroup(t *testing.T) {
	s, err := SampleMemoryPressure(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if s.At.IsZero() || s.Cgroup == "" || s.Status == "unavailable" {
		t.Fatalf("real observation missing: %+v", s)
	}
}
