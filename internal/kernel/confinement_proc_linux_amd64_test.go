//go:build linux && amd64

package kernel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestKernelConfinementPreservesProcAliasesAndParentPrivacy(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "proc-boundary", FrameID: "proc-frame", RootFrameID: "proc-frame",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, `
import json
paths = ['/proc/1/environ', '/proc/1/cmdline', '/proc/1/maps']
result = {}
for path in paths:
    try:
        result[path] = len(open(path, 'rb').read())
    except PermissionError:
        result[path] = -1
result['mount_alias_equal'] = int(open('/proc/self/mounts', 'rb').read() == open('/proc/mounts', 'rb').read())
print(json.dumps(result))
`, "user")
	if err != nil || response.Error != "" {
		t.Fatalf("confined proc reader: response=%+v error=%v", response, err)
	}
	var observed map[string]int
	if err := json.Unmarshal([]byte(strings.TrimSpace(response.Stdout)), &observed); err != nil {
		t.Fatal(err)
	}
	// The worker keeps its own namespaced proc view for runtime inspection.
	// The setup parent's environment and command remain masked.
	for _, path := range []string{"/proc/1/environ", "/proc/1/cmdline", "/proc/1/maps"} {
		if size, found := observed[path]; !found || (size != 0 && size != -1) {
			t.Fatalf("parent metadata not masked at %s: bytes=%d found=%t", path, size, found)
		}
	}
	if observed["mount_alias_equal"] != 1 {
		t.Fatal("namespaced mount aliases disagree")
	}
}
