package kernel

import (
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"synon-go/internal/processsupervisor"
	"time"
)

type KernelProcessResources struct {
	PIDVisible  bool                  `json:"pid_visible"`
	RSSBytes    *uint64               `json:"rss_bytes"`
	CPUPct      *float64              `json:"cpu_pct"`
	Observation *ExecutionObservation `json:"execution_observation,omitempty"`
}

type MachineResourceSnapshot struct {
	SampledAt            time.Time `json:"sampled_at"`
	TotalMemoryBytes     *uint64   `json:"total_mem_bytes"`
	AvailableMemoryBytes *uint64   `json:"avail_mem_bytes"`
	Cores                int       `json:"cores"`
	HostCores            int       `json:"host_cores"`
	TotalCPUPct          *float64  `json:"total_cpu_pct"`
	CgroupCPUPct         *float64  `json:"cgroup_cpu_pct"`
	KernelRSSBytes       uint64    `json:"kernel_rss_bytes"`
	KernelCPUPct         *float64  `json:"kernel_cpu_pct"`
	KernelCount          int       `json:"kernel_count"`
	BusyCount            int       `json:"busy_count"`
	DiskTotalBytes       *uint64   `json:"disk_total_bytes"`
	DiskAvailableBytes   *uint64   `json:"disk_avail_bytes"`
}

type KernelInventorySnapshot struct {
	Kernels []SessionKernel
	Machine MachineResourceSnapshot
}

type resourceTarget struct {
	kernelID      string
	pid           int
	pidStartTicks uint64
}

// ExternalSessionKernel describes a kernel process owned by another service
// process, such as the durable detached executor. PIDStartTicks fences PID
// reuse before resources are attributed to the session.
type ExternalSessionKernel struct {
	Kernel        SessionKernel
	PID           int
	PIDStartTicks uint64
	DiskPath      string
}

type processResourceCounter struct {
	pid                int
	visible            bool
	rssBytes           uint64
	cpuCounter         uint64
	startIdentity      uint64
	observedProcesses  []ObservedProcess
	observationPartial bool
	memoryPressure     *processsupervisor.MemoryPressure
}

type platformResourceSnapshot struct {
	sampledAt            time.Time
	totalMemoryBytes     *uint64
	availableMemoryBytes *uint64
	hostCores            int
	hostTotalCPU         uint64
	hostBusyCPU          uint64
	cgroupCPUUsec        *uint64
	diskTotalBytes       *uint64
	diskAvailableBytes   *uint64
	processes            map[string]processResourceCounter
}

type previousProcessCounter struct {
	pid           int
	cpuCounter    uint64
	startIdentity uint64
}

type resourceSampler struct {
	mu              sync.Mutex
	hasHostSample   bool
	hostTotalCPU    uint64
	hostBusyCPU     uint64
	hasCgroupSample bool
	cgroupCPUUsec   uint64
	lastSampledAt   time.Time
	processes       map[string]previousProcessCounter
}

func newResourceSampler() *resourceSampler {
	return &resourceSampler{processes: map[string]previousProcessCounter{}}
}

func (s *resourceSampler) sample(targets []resourceTarget, diskPath string) (MachineResourceSnapshot, map[string]KernelProcessResources) {
	// Serialize acquisition as well as delta publication. Concurrent inventory
	// requests must not overwrite a newer CPU baseline with an older sample.
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := readPlatformResourceSnapshot(targets, diskPath)
	if raw.sampledAt.IsZero() {
		raw.sampledAt = time.Now().UTC()
	}
	if raw.hostCores < 1 {
		raw.hostCores = runtime.NumCPU()
	}
	if raw.hostCores < 1 {
		raw.hostCores = 1
	}
	machine := MachineResourceSnapshot{
		SampledAt:            raw.sampledAt,
		TotalMemoryBytes:     raw.totalMemoryBytes,
		AvailableMemoryBytes: raw.availableMemoryBytes,
		Cores:                raw.hostCores,
		HostCores:            raw.hostCores,
		DiskTotalBytes:       raw.diskTotalBytes,
		DiskAvailableBytes:   raw.diskAvailableBytes,
	}
	resources := make(map[string]KernelProcessResources, len(targets))

	deltaTotal := uint64(0)
	if s.hasHostSample && raw.hostTotalCPU >= s.hostTotalCPU {
		deltaTotal = raw.hostTotalCPU - s.hostTotalCPU
		if raw.hostBusyCPU >= s.hostBusyCPU && deltaTotal > 0 {
			value := float64(raw.hostBusyCPU-s.hostBusyCPU) / float64(deltaTotal) * float64(raw.hostCores) * 100
			machine.TotalCPUPct = float64Pointer(clampCPU(value, raw.hostCores))
		}
	}
	if raw.cgroupCPUUsec != nil && s.hasCgroupSample && *raw.cgroupCPUUsec >= s.cgroupCPUUsec &&
		!s.lastSampledAt.IsZero() && raw.sampledAt.After(s.lastSampledAt) {
		elapsedUsec := raw.sampledAt.Sub(s.lastSampledAt).Microseconds()
		if elapsedUsec > 0 {
			value := float64(*raw.cgroupCPUUsec-s.cgroupCPUUsec) / float64(elapsedUsec) * 100
			machine.CgroupCPUPct = float64Pointer(clampCPU(value, raw.hostCores))
		}
	}
	nextProcesses := make(map[string]previousProcessCounter, len(targets))
	var kernelCPUTotal float64
	hasKernelCPU := false
	for _, target := range targets {
		counter, ok := raw.processes[target.kernelID]
		if !ok {
			resources[target.kernelID] = KernelProcessResources{}
			continue
		}
		resource := KernelProcessResources{PIDVisible: counter.visible, Observation: executionObservation(counter, raw.sampledAt)}
		if counter.visible {
			resource.RSSBytes = uint64Pointer(counter.rssBytes)
		}
		if previous, found := s.processes[target.kernelID]; found && previous.pid == counter.pid && previous.startIdentity == counter.startIdentity &&
			counter.cpuCounter >= previous.cpuCounter && deltaTotal > 0 {
			value := float64(counter.cpuCounter-previous.cpuCounter) / float64(deltaTotal) * float64(raw.hostCores) * 100
			value = clampCPU(value, raw.hostCores)
			resource.CPUPct = float64Pointer(value)
			kernelCPUTotal += value
			hasKernelCPU = true
		}
		nextProcesses[target.kernelID] = previousProcessCounter{pid: counter.pid, cpuCounter: counter.cpuCounter, startIdentity: counter.startIdentity}
		resources[target.kernelID] = resource
	}
	if len(targets) == 0 {
		zero := float64(0)
		machine.KernelCPUPct = &zero
	} else if hasKernelCPU {
		machine.KernelCPUPct = float64Pointer(kernelCPUTotal)
	}
	s.hasHostSample = raw.hostTotalCPU > 0
	s.hostTotalCPU = raw.hostTotalCPU
	s.hostBusyCPU = raw.hostBusyCPU
	s.hasCgroupSample = raw.cgroupCPUUsec != nil
	if raw.cgroupCPUUsec != nil {
		s.cgroupCPUUsec = *raw.cgroupCPUUsec
	}
	s.lastSampledAt = raw.sampledAt
	s.processes = nextProcesses
	return machine, resources
}

func clampCPU(value float64, cores int) float64 {
	maximum := float64(max(1, cores)) * 100
	if value < 0 {
		return 0
	}
	if value > maximum {
		return maximum
	}
	return value
}

func uint64Pointer(value uint64) *uint64 {
	return &value
}

func float64Pointer(value float64) *float64 {
	return &value
}

func (m *Manager) ListAllSessionKernelsWithResources(diskPath string) KernelInventorySnapshot {
	return m.ListAllSessionKernelsWithExternalResources(diskPath, nil)
}

func (m *Manager) ListAllSessionKernelsWithExternalResources(
	diskPath string,
	external []ExternalSessionKernel,
) KernelInventorySnapshot {
	if m == nil {
		return KernelInventorySnapshot{Kernels: []SessionKernel{}, Machine: MachineResourceSnapshot{
			SampledAt: time.Now().UTC(), Cores: max(1, runtime.NumCPU()), HostCores: max(1, runtime.NumCPU()),
		}}
	}
	m.mu.Lock()
	workers := make([]*Worker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	sampler := m.resourceSampler
	if sampler == nil {
		sampler = newResourceSampler()
		m.resourceSampler = sampler
	}
	m.mu.Unlock()

	kernels := make([]SessionKernel, 0, len(workers)+len(external))
	fences := make(map[string]workerResourceFence, len(workers))
	targets := make([]resourceTarget, 0, len(workers)+len(external))
	seen := make(map[string]struct{}, len(workers)+len(external))
	for _, worker := range workers {
		state := lifecycleWorkerState(worker)
		if state == nil {
			continue
		}
		state.mu.Lock()
		projection := snapshotSessionKernel(state)
		fences[projection.KernelID] = workerResourceFence{worker: worker, state: state, current: state.current, generation: state.generation}
		if strings.TrimSpace(diskPath) == "" && strings.TrimSpace(state.spec.WorkspaceDir) != "" {
			diskPath = state.spec.WorkspaceDir
		}
		state.mu.Unlock()
		kernels = append(kernels, projection)
		pid := workerPID(worker)
		var startTicks uint64
		if runtime.GOOS == "linux" && pid > 0 {
			identity, err := worker.ProcessIdentity()
			if err != nil {
				pid = 0
			} else {
				startTicks = uint64(identity.StartTicks)
			}
		}
		targets = append(targets, resourceTarget{kernelID: projection.KernelID, pid: pid, pidStartTicks: startTicks})
		seen[projection.KernelID] = struct{}{}
	}
	for _, item := range external {
		kernelID := strings.TrimSpace(item.Kernel.KernelID)
		if kernelID == "" {
			continue
		}
		if _, duplicate := seen[kernelID]; duplicate {
			continue
		}
		if strings.TrimSpace(diskPath) == "" && strings.TrimSpace(item.DiskPath) != "" {
			diskPath = item.DiskPath
		}
		item.Kernel.KernelID = kernelID
		kernels = append(kernels, item.Kernel)
		targets = append(targets, resourceTarget{
			kernelID: kernelID, pid: item.PID, pidStartTicks: item.PIDStartTicks,
		})
		seen[kernelID] = struct{}{}
	}
	if strings.TrimSpace(diskPath) == "" {
		diskPath = os.TempDir()
	}
	machine, resources := sampler.sample(targets, diskPath)
	for index := range kernels {
		resource := resources[kernels[index].KernelID]
		if fence, exists := fences[kernels[index].KernelID]; exists && !fence.unchanged() {
			resource = KernelProcessResources{Observation: executionObservation(processResourceCounter{}, machine.SampledAt)}
		}
		kernels[index].PIDVisible = resource.PIDVisible
		kernels[index].RSSBytes = resource.RSSBytes
		kernels[index].CPUPct = resource.CPUPct
		kernels[index].ExecutionObservation = resource.Observation
		if resource.Observation != nil && kernels[index].CurrentCellTag != nil {
			resource.Observation.ExecutionID = *kernels[index].CurrentCellTag
		}
		if resource.RSSBytes != nil {
			machine.KernelRSSBytes += *resource.RSSBytes
		}
		if kernels[index].Busy || kernels[index].Starting {
			machine.BusyCount++
		}
	}
	machine.KernelCount = len(kernels)
	sort.Slice(kernels, func(i, j int) bool {
		if kernels[i].RootFrameID != kernels[j].RootFrameID {
			return kernels[i].RootFrameID < kernels[j].RootFrameID
		}
		if kernels[i].FrameID != kernels[j].FrameID {
			return kernels[i].FrameID < kernels[j].FrameID
		}
		return kernels[i].KernelID < kernels[j].KernelID
	})
	return KernelInventorySnapshot{Kernels: kernels, Machine: machine}
}

func workerPID(worker *Worker) int {
	if worker == nil || worker.process == nil || worker.process.command == nil || worker.process.command.Process == nil {
		return 0
	}
	select {
	case <-worker.done:
		return 0
	default:
	}
	return worker.process.command.Process.Pid
}

type workerResourceFence struct {
	worker     *Worker
	state      *workerLifecycle
	current    *currentExecution
	generation uint64
}

func (f workerResourceFence) unchanged() bool {
	f.state.mu.Lock()
	defer f.state.mu.Unlock()
	return workerPID(f.worker) > 0 && f.state.current == f.current && f.state.generation == f.generation && !f.state.closing
}
