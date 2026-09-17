package server

import (
	"encoding/json"
	"synon-go/internal/processsupervisor"
)

func kernelResourcePressureReceipt(trace map[string]any) *processsupervisor.MemoryPressure {
	value, exists := trace["resource_pressure"]
	if !exists {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 2048 {
		return nil
	}
	var observation processsupervisor.MemoryPressure
	if json.Unmarshal(raw, &observation) != nil || observation.Status != "pressured" || observation.LimitBytes == 0 || observation.FullStallPercent < 0 || observation.FullStallPercent > 100 {
		return nil
	}
	return &observation
}
