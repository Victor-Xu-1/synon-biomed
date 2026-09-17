//go:build linux

package kernel

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessIdentityAliveRejectsUnreapedExit(t *testing.T) {
	command := exec.Command("sh", "-c", "read signal; exit 0")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = command.Process.Kill(); _ = command.Wait() })
	start, err := linuxProcessStartTicks(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	waitForProcessState(t, command.Process.Pid, "Z")
	alive, err := ProcessIdentityAlive(int64(command.Process.Pid), start)
	if err != nil || alive {
		t.Fatalf("unreaped exited executor still owns liveness: alive=%t error=%v", alive, err)
	}
}

func TestWorkerProcessCancelsThreadSpawnedIndependentSession(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	for _, mode := range []string{"interrupt", "kill"} {
		t.Run(mode, func(t *testing.T) {
			peerCommand := exec.Command("sleep", "60")
			peer, err := startWorkerProcess(peerCommand)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = peer.kill(); _ = peerCommand.Wait(); peer.close() })
			command := exec.Command(python, "-u", "-c", `import subprocess, threading, time
def spawn():
    child = subprocess.Popen(["sleep", "60"], start_new_session=True)
    print(child.pid, flush=True)
    child.wait()
threading.Thread(target=spawn, daemon=True).start()
while True: time.sleep(1)
`)
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			process, err := startWorkerProcess(command)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait(); process.close() })
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil || pid <= 0 {
				t.Fatalf("child PID: %q %v", line, err)
			}
			child, err := os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Kill(); _ = child.Release() })
			if mode == "interrupt" {
				err = process.signal(os.Interrupt)
			} else {
				err = process.kill()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := peerCommand.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("cancellation affected unrelated worker: %v", err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				state, err := readLinuxObservedCounter("/proc", pid)
				if os.IsNotExist(err) || err == nil && (state.process.State == "Z" || state.process.State == "X") {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("thread-spawned child in another process group survived worker cancellation")
		})
	}
}

func TestWorkerProcessIdentityRejectsStaleLaunchWithoutSignalling(t *testing.T) {
	command := exec.Command("sleep", "60")
	process, err := startWorkerProcess(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait(); process.close() })
	stale := *process
	stale.startTicks++
	worker := &Worker{process: &stale}
	if _, err := worker.ProcessIdentity(); err == nil {
		t.Fatal("worker silently rebound its process identity")
	}
	if err := stale.kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("stale launch signalled another process identity")
	}
	if err := process.signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	waitForProcessState(t, command.Process.Pid, "T")
	if alive, err := ProcessIdentityAlive(int64(command.Process.Pid), int64(process.startTicks)); err != nil || !alive {
		t.Fatalf("stopped but live process was considered dead: %t %v", alive, err)
	}
	if err := process.signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
}

func TestManagerInterruptsThreadChildAndPreservesKernelState(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	workspace := t.TempDir()
	worker, err := manager.StartSession(SessionSpec{KernelID: "thread-child-kernel", FrameID: "thread-frame", RootFrameID: "thread-root",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := manager.Submit(SubmitRequest{FrameID: "thread-frame", Language: "python", Environment: "python",
		ExecID: "thread-execution", ToolUseID: "thread-tool", Origin: "user", Code: `import subprocess, threading, time
from pathlib import Path
saved_value = 41
def background_operation():
    child = subprocess.Popen(["/bin/sleep", "60"], start_new_session=True)
    Path("thread-child-ready").write_text("ready")
    child.wait()
threading.Thread(target=background_operation, daemon=True).start()
while True: time.sleep(0.1)
`})
	if err != nil {
		t.Fatal(err)
	}
	waitForLifecycleFile(t, filepath.Join(workspace, "thread-child-ready"))
	members, _, err := readLinuxProcessTreeMembersAt("/proc", worker.process.command.Process.Pid, worker.process.startTicks, processWalkLimit)
	if err != nil {
		t.Fatal(err)
	}
	childPID := 0
	for _, member := range members {
		if member.process.Name == "sleep" {
			childPID = member.process.PID
		}
	}
	if childPID == 0 {
		t.Fatal("confined thread child was not started")
	}
	if result := manager.Interrupt("thread-frame", "thread-execution"); !result.Interrupted {
		t.Fatalf("interrupt: %#v", result)
	}
	outcome := waitForLifecycleOutcome(t, handle)
	if !outcome.Response.Interrupted || outcome.Err != nil {
		t.Fatalf("interrupted outcome: %#v", outcome)
	}
	waitForWorkerProcessGone(t, childPID, 3*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := worker.Execute(ctx, "print(saved_value + 1)", "user")
	if err != nil || result.Error != "" || strings.TrimSpace(result.Stdout) != "42" {
		t.Fatalf("kernel lost state after thread child cancellation: %#v %v", result, err)
	}
}

func waitForProcessState(t *testing.T, pid int, wanted string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		value, err := readLinuxObservedCounter("/proc", pid)
		if err == nil && value.process.State == wanted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d did not reach %s: %s", pid, wanted, fmt.Sprint(syscall.Kill(pid, 0)))
}

func TestInterruptExemptsOnlySandboxLauncherAncestry(t *testing.T) {
	for _, root := range []string{"bwrap", "python3"} {
		members := []linuxObservedCounter{
			{process: ObservedProcess{PID: 10, ParentPID: 1, Name: root}},
			{process: ObservedProcess{PID: 11, ParentPID: 10, Name: "bwrap"}},
			{process: ObservedProcess{PID: 12, ParentPID: 11, Name: "python3"}},
			{process: ObservedProcess{PID: 13, ParentPID: 12, Name: "bwrap"}},
		}
		exempt := linuxSandboxSupervisors(members)
		if exempt[13] {
			t.Fatal("an ordinary computation inherited supervisor signal protection by name")
		}
		if root == "bwrap" && (!exempt[10] || !exempt[11] || len(exempt) != 2) {
			t.Fatalf("sandbox supervisors=%v", exempt)
		}
		if root != "bwrap" && len(exempt) != 0 {
			t.Fatalf("unconfined process gained supervisor exemptions=%v", exempt)
		}
	}
}

func TestWorkerProcessCancellationConcurrentWithLaunch(t *testing.T) {
	for attempt := 0; attempt < 64; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		command := newWorkerProcessCommand(ctx, "sleep", "30")
		cancelled := make(chan struct{})
		go func() { runtime.Gosched(); cancel(); close(cancelled) }()
		process, err := startWorkerProcess(command)
		<-cancelled
		if err != nil {
			if ctx.Err() == nil {
				t.Fatal(err)
			}
			continue
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = process.kill()
			t.Fatal("startup cancellation did not settle")
		}
		process.close()
	}
}
