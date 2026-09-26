package server

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"testing"
	"time"

	"synon-go/internal/processsupervisor"
)

func TestArtifactValidatorProcessActivityIdleAndCancel(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	for _, test := range []struct {
		name, code string
		cancel     bool
		want       error
	}{
		{"output", "import time\nfor i in range(15):\n print(i,flush=True); time.sleep(.04)", false, nil},
		{"cpu", "import time\nend=time.monotonic()+.6\nwhile time.monotonic()<end: pass", false, nil},
		{"idle", "import time; time.sleep(20)", false, processsupervisor.ErrInactivity},
		{"cancel", "import time; time.sleep(20)", true, context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				stop := time.AfterFunc(100*time.Millisecond, cancel)
				defer stop.Stop()
			}
			command := exec.Command(python, "-I", "-c", test.code)
			command.Stdout, command.Stderr = io.Discard, io.Discard
			started := time.Now()
			err := runArtifactValidationCommand(ctx, command, 250*time.Millisecond)
			if !errors.Is(err, test.want) {
				t.Fatalf("result=%v want=%v", err, test.want)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("process termination did not complete promptly")
			}
		})
	}
}
