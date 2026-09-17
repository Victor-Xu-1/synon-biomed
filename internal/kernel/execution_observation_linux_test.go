//go:build linux

package kernel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestExecutionObservationReportsRealDescendantsNotEnvironment(t *testing.T) {
	command := exec.Command("sh", "-c", "sleep 30 & printf 'ready\\n'; wait")
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		members, _, _ := readLinuxProcessTreeMembersAt("/proc", command.Process.Pid, 0, processWalkLimit)
		for index := 1; index < len(members); index++ {
			_ = signalLinuxProcessMember(members[index], syscall.SIGKILL)
		}
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child readiness = %q, %v", line, err)
	}
	start, err := linuxProcessStartTicks(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{}
	tag := "current-execution"
	snapshot := manager.ListAllSessionKernelsWithExternalResources(t.TempDir(), []ExternalSessionKernel{{
		Kernel: SessionKernel{KernelID: "owned-worker", Environment: "named-for-an-unrelated-program", Busy: true, CurrentCellTag: &tag},
		PID:    command.Process.Pid, PIDStartTicks: uint64(start),
	}})
	raw, err := json.Marshal(snapshot.Kernels[0])
	if err != nil {
		t.Fatal(err)
	}
	var kernel struct {
		Observation *struct {
			ExecutionID string `json:"execution_id"`
			Status      string `json:"status"`
			SampledAt   string `json:"sampled_at"`
			Processes   []struct {
				Name     string `json:"name"`
				PID      int    `json:"pid"`
				Identity string `json:"start_identity"`
			} `json:"processes"`
		} `json:"execution_observation"`
	}
	if err := json.Unmarshal(raw, &kernel); err != nil {
		t.Fatal(err)
	}
	observation := kernel.Observation
	if observation == nil || observation.ExecutionID != tag || observation.SampledAt == "" || observation.Status != "observed" {
		t.Fatalf("missing current process observation: %s", raw)
	}
	found := false
	for _, process := range observation.Processes {
		if process.Name == "sleep" && process.PID > 0 && process.Identity != "" {
			found = true
		}
		if process.Name == "named-for-an-unrelated-program" {
			t.Fatal("environment was projected as an executable")
		}
	}
	if !found {
		t.Fatalf("live child absent from observation: %s", raw)
	}
}

func TestExecutionObservationLocalFenceRejectsCellSwitchAndWorkerExit(t *testing.T) {
	command := exec.Command("sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	worker := &Worker{process: &workerProcess{command: command}, done: make(chan struct{})}
	current := &currentExecution{toolUseID: "same-tool-tag", startedAt: time.Now()}
	state := &workerLifecycle{current: current, generation: 1}
	fence := workerResourceFence{worker: worker, state: state, current: current, generation: 1}
	if !fence.unchanged() {
		t.Fatal("live unchanged cell rejected")
	}
	state.current = &currentExecution{toolUseID: "same-tool-tag", startedAt: current.startedAt}
	if fence.unchanged() {
		t.Fatal("new execution with reused display tag accepted")
	}
	state.current = current
	state.generation++
	if fence.unchanged() {
		t.Fatal("new worker generation accepted")
	}
	state.generation--
	state.closing = true
	if fence.unchanged() {
		t.Fatal("closing worker accepted")
	}
	state.closing = false
	close(worker.done)
	if fence.unchanged() || workerPID(worker) != 0 {
		t.Fatal("reaped worker PID remains observable")
	}
}

func TestExecutionObservationConcurrentInventoryKeepsLatestBaseline(t *testing.T) {
	sampler := newResourceSampler()
	directory := t.TempDir()
	var group sync.WaitGroup
	samples := make(chan time.Time, 12)
	for i := 0; i < cap(samples); i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			machine, _ := sampler.sample([]resourceTarget{{kernelID: "self", pid: os.Getpid()}}, directory)
			samples <- machine.SampledAt
		}()
	}
	group.Wait()
	close(samples)
	for sampledAt := range samples {
		if sampledAt.After(sampler.lastSampledAt) {
			t.Fatal("concurrent sample rolled the CPU baseline backwards")
		}
	}
}

func writeObservedProcessFixture(t *testing.T, root string, pid, parent int, start uint64, name, state string, children ...int) {
	t.Helper()
	directory := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(directory, "task", strconv.Itoa(pid)), 0o700); err != nil {
		t.Fatal(err)
	}
	fields := make([]string, 22)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0], fields[1] = state, strconv.Itoa(parent)
	fields[11], fields[12], fields[19], fields[21] = "2", "3", strconv.FormatUint(start, 10), "1"
	if err := os.WriteFile(filepath.Join(directory, "stat"), []byte(fmt.Sprintf("%d (%s) %s", pid, name, strings.Join(fields, " "))), 0o600); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(children))
	for i, child := range children {
		ids[i] = strconv.Itoa(child)
	}
	if err := os.WriteFile(filepath.Join(directory, "task", strconv.Itoa(pid), "children"), []byte(strings.Join(ids, " ")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionObservationChecksRootAndChildIdentity(t *testing.T) {
	root := t.TempDir()
	writeObservedProcessFixture(t, root, 10, 1, 100, "runner", "S", 11, 12)
	writeObservedProcessFixture(t, root, 11, 10, 101, "owned", "R")
	writeObservedProcessFixture(t, root, 12, 99, 102, "foreign", "R")
	snapshot := readLinuxProcessTreeAt(root, 10, 100)
	if !snapshot.visible || !snapshot.observationPartial || len(snapshot.observedProcesses) != 2 {
		t.Fatalf("unexpected tree: %#v", snapshot)
	}
	for _, process := range snapshot.observedProcesses {
		if process.PID == 12 {
			t.Fatal("foreign child attributed to execution")
		}
	}
	for _, expected := range []uint64{99, 101} {
		if value := readLinuxProcessTreeAt(root, 10, expected); value.visible || len(value.observedProcesses) != 0 {
			t.Fatalf("reused PID accepted: %#v", value)
		}
	}
	if err := os.Remove(filepath.Join(root, "10", "stat")); err != nil {
		t.Fatal(err)
	}
	if value := readLinuxProcessTreeAt(root, 10, 100); value.visible || len(value.observedProcesses) != 0 {
		t.Fatalf("missing root accepted: %#v", value)
	}
}

func TestExecutionObservationIncludesThreadChildrenAndLabelsNameFallback(t *testing.T) {
	root := t.TempDir()
	writeObservedProcessFixture(t, root, 10, 1, 100, "run) ner", "S")
	writeObservedProcessFixture(t, root, 12, 10, 102, "child\u202e\n", "R")
	directory := filepath.Join(root, "10", "task", "11")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "children"), []byte("12"), 0o600); err != nil {
		t.Fatal(err)
	}
	value := readLinuxProcessTreeAt(root, 10, 100)
	if !value.visible || value.observationPartial || len(value.observedProcesses) != 2 {
		t.Fatalf("thread children: %#v", value)
	}
	child := value.observedProcesses[1]
	if child.Name != "child" || child.NameSource != "process_name" {
		t.Fatalf("unsafe or unlabelled fallback: %#v", child)
	}
}

func TestExecutionObservationBoundsNamesWithoutUnderCountingKnownResources(t *testing.T) {
	root := t.TempDir()
	children := make([]int, processObservationLimit+2)
	for i := range children {
		children[i] = 20 + i
		writeObservedProcessFixture(t, root, children[i], 10, uint64(101+i), "parallel-worker", "S")
	}
	writeObservedProcessFixture(t, root, 10, 1, 100, "root", "S", children...)
	value := readLinuxProcessTreeAt(root, 10, 100)
	if !value.observationPartial || len(value.observedProcesses) != processObservationLimit || value.cpuCounter != uint64(5*(len(children)+1)) {
		t.Fatalf("bounded observation lost its partial marker or metrics: %#v", value)
	}
	writeObservedProcessFixture(t, root, 10, 1, 100, "zombie", "Z", children...)
	if value := readLinuxProcessTreeAt(root, 10, 100); value.visible {
		t.Fatal("zombie root treated as executing")
	}
}

func TestExecutionObservationBoundsThreadAndChildEnumeration(t *testing.T) {
	root := t.TempDir()
	children := make([]int, processWalkLimit+10)
	for i := range children {
		children[i] = 100 + i
	}
	writeObservedProcessFixture(t, root, 10, 1, 100, "root", "S", children...)
	budget := processWalkLimit
	ids, complete := linuxObservedChildren(root, 10, &budget)
	if complete || len(ids) != processWalkLimit {
		t.Fatalf("enumeration bound: complete=%t count=%d", complete, len(ids))
	}
	budget = 0
	if ids, complete := linuxObservedChildren(root, 10, &budget); complete || len(ids) != 0 {
		t.Fatal("exhausted thread budget was ignored")
	}
	path := filepath.Join(root, "10", "task", "10", "children")
	if err := os.WriteFile(path, []byte("11 "+strings.Repeat("9", 10000)), 0o600); err != nil {
		t.Fatal(err)
	}
	budget = 1
	ids, complete = linuxObservedChildren(root, 10, &budget)
	if complete || len(ids) != 1 || ids[0] != 11 {
		t.Fatal("oversized malformed child field was not bounded or labelled partial")
	}
}
