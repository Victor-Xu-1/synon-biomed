//go:build linux

package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestKernelEgressProxyLiveHTTPS(t *testing.T) {
	if os.Getenv("SYNON_RUN_KERNEL_EGRESS_LIVE") != "1" {
		t.Skip("set SYNON_RUN_KERNEL_EGRESS_LIVE=1 for the live public HTTPS check")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	assetRoot := filepath.Join(root, "assets", "optional")
	manager := NewManager(Config{
		Python: python, AssetRoot: assetRoot,
		ManifestPath: filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		WorkerPath:   filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
	})
	workspace := t.TempDir()
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-egress-live", OwnerID: "owner", ProjectID: "project",
		FrameID: "frame", FrameIncarnationID: "frame-incarnation",
		RootFrameID: "frame", RootFrameIncarnationID: "root-incarnation",
		KernelKind: "analysis", Language: "python", Environment: "python",
		WorkspaceDir: workspace, EgressAllowedDomains: []string{"example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = worker.Close(ctx)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, `
import socket
import urllib.request
from urllib.parse import urlparse
proxy = urlparse(__import__("os").environ["HTTPS_PROXY"])
probe = socket.create_connection((proxy.hostname, proxy.port), 2)
probe.sendall(b"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
probe.settimeout(5)
print("connect", probe.recv(128).split(b"\r\n", 1)[0].decode())
probe.close()
direct = "unexpected"
try:
    socket.create_connection(("1.1.1.1", 443), 1).close()
except OSError:
    direct = "blocked"
with urllib.request.urlopen("https://example.com", timeout=15) as response:
    print("proxy", response.status, "direct", direct)
`, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if response.Error != "" || !strings.Contains(response.Stdout, "proxy 200 direct blocked") {
		t.Fatalf("live kernel egress response = %#v", response)
	}
}
