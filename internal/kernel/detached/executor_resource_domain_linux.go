//go:build linux

package detached

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"synon-go/internal/processsupervisor"
)

const executorSupervisorReserveBytes = uint64(128 << 20)

type executorMemoryDomain struct {
	root      *os.Root
	group     string
	directory string
}

// The launcher creates the control subgroup before ExecStart, leaving the
// delegated parent free of processes. The existing worker launcher atomically
// places the confined process in the workload subgroup (CLONE_INTO_CGROUP).
func prepareExecutorMemoryDomain(backendID string, generation int64) (*executorMemoryDomain, error) {
	group, err := currentUnifiedCgroupPath()
	if err != nil {
		return nil, err
	}
	unit := executorUnitName(backendID, generation)
	if !strings.HasSuffix(group, "/"+unit+"/control") {
		if strings.Contains(group, "/synon-kernel-executor-") {
			return nil, errors.New("executor resource delegation is unavailable")
		}
		// Embedded/test launchers have no owned resource domain. Do not acquire
		// control of the caller's cgroup or infer a per-job budget from it.
		return nil, nil
	}
	parent := path.Dir(group)
	budget, err := processsupervisor.SampleCgroupMemoryPressure(parent)
	if err != nil || budget.LimitBytes <= 2*executorSupervisorReserveBytes {
		return nil, errors.New("executor admitted memory budget is unavailable")
	}
	root, err := os.OpenRoot(filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(parent, "/")))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = writeResourceControl(root, "cgroup.subtree_control", "+memory"); err != nil {
		return nil, err
	}
	if err = root.Mkdir("workload", 0700); err != nil {
		return nil, err
	}
	workerRoot, err := root.OpenRoot("workload")
	if err != nil {
		return nil, err
	}
	limit := budget.LimitBytes - executorSupervisorReserveBytes
	for file, value := range map[string]uint64{"memory.max": limit, "memory.high": limit * uint64(executorMemoryHighFraction) / 100, "memory.swap.max": uint64(executorMemorySwapMaxBytes)} {
		if err = writeResourceControl(workerRoot, file, strconv.FormatUint(value, 10)); err != nil {
			workerRoot.Close()
			return nil, err
		}
	}
	workerGroup := path.Join(parent, "workload")
	return &executorMemoryDomain{root: workerRoot, group: workerGroup, directory: filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(workerGroup, "/"))}, nil
}

func writeResourceControl(root *os.Root, name, value string) error {
	file, err := root.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(value)
	return errors.Join(writeErr, file.Close())
}

func (d *executorMemoryDomain) sample() (processsupervisor.MemoryPressureSample, error) {
	return processsupervisor.SampleCgroupMemoryPressure(d.group)
}

func (d *executorMemoryDomain) relieve(observed processsupervisor.MemoryPressureSample) error {
	current, err := d.sample()
	if err != nil || current.Cgroup != observed.Cgroup || current.LimitBytes == 0 || observed.LimitBytes == 0 {
		return errors.New("resource authority changed before relief")
	}
	target := min(current.LimitBytes, observed.LimitBytes)
	if current.HighBytes >= target {
		return nil
	}
	if err := writeResourceControl(d.root, "memory.high", strconv.FormatUint(target, 10)); err != nil {
		return err
	}
	after, err := d.sample()
	if err != nil || after.HighBytes != target || after.LimitBytes > observed.LimitBytes {
		return errors.New("resource relief could not be verified")
	}
	return nil
}
