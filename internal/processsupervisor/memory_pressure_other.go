//go:build !linux

package processsupervisor

import "errors"

func SampleMemoryPressure(int) (MemoryPressureSample, error) {
	return MemoryPressureSample{}, errors.New("memory pressure observation is unavailable on this platform")
}

func SampleCgroupMemoryPressure(string) (MemoryPressureSample, error) {
	return MemoryPressureSample{}, errors.New("memory pressure observation is unavailable on this platform")
}
