//go:build linux

package processsupervisor

import (
	"errors"
	"io"
	"math"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

func SampleMemoryPressure(pid int) (MemoryPressureSample, error) {
	if pid <= 0 {
		return MemoryPressureSample{}, errors.New("invalid resource process identity")
	}
	membership, err := readPressureFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return MemoryPressureSample{}, err
	}
	group := ""
	for _, line := range strings.Split(membership, "\n") {
		if strings.HasPrefix(line, "0::/") {
			group = strings.TrimPrefix(line, "0::")
			break
		}
	}
	return readMemoryPressureAt("/sys/fs/cgroup", group, time.Now())
}

func SampleCgroupMemoryPressure(group string) (MemoryPressureSample, error) {
	return readMemoryPressureAt("/sys/fs/cgroup", group, time.Now())
}

func readMemoryPressureAt(root, group string, now time.Time) (MemoryPressureSample, error) {
	s := MemoryPressureSample{At: now, Cgroup: group, MemoryPressure: MemoryPressure{Status: "unavailable"}}
	if group == "" || !strings.HasPrefix(group, "/") || path.Clean(group) != group || strings.ContainsAny(group, "\x00\r\n") {
		return s, errors.New("invalid resource group identity")
	}
	directory, err := os.OpenRoot(root)
	if err != nil {
		return s, err
	}
	defer directory.Close()
	read := func(name string) (string, error) {
		file, err := directory.Open(path.Join(strings.TrimPrefix(group, "/"), name))
		if err != nil {
			return "", err
		}
		defer file.Close()
		return boundedPressureText(file)
	}
	for name, target := range map[string]*uint64{
		"memory.current": &s.CurrentBytes, "memory.high": &s.HighBytes, "memory.max": &s.LimitBytes, "memory.swap.current": &s.SwapBytes,
	} {
		text, err := read(name)
		if err != nil {
			return s, err
		}
		if text == "max" && (name == "memory.high" || name == "memory.max") {
			continue
		}
		value, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return s, errors.New("invalid memory resource counter")
		}
		*target = value
	}
	pressure, err := read("memory.pressure")
	if err != nil {
		return s, err
	}
	full := pressureFields(pressure, "full")
	s.FullStallUsec, err = strconv.ParseUint(full["total"], 10, 64)
	if err != nil {
		return s, errors.New("missing memory stall counter")
	}
	s.FullStallPercent, err = strconv.ParseFloat(full["avg10"], 64)
	if err != nil || math.IsNaN(s.FullStallPercent) || math.IsInf(s.FullStallPercent, 0) || s.FullStallPercent < 0 || s.FullStallPercent > 100 {
		return s, errors.New("invalid memory stall ratio")
	}
	cpu, err := read("cpu.stat")
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(cpu, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "user_usec" {
			s.UserCPUUsec, err = strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return s, err
			}
			s.Status = "normal"
			if s.FullStallPercent >= 50 {
				s.Status = "pressured"
			}
			return s, nil
		}
	}
	return s, errors.New("missing user CPU counter")
}

func pressureFields(text, kind string) map[string]string {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != kind {
			continue
		}
		values := map[string]string{}
		for _, field := range fields[1:] {
			parts := strings.SplitN(field, "=", 2)
			if len(parts) == 2 {
				values[parts[0]] = parts[1]
			}
		}
		return values
	}
	return nil
}

func readPressureFile(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return boundedPressureText(file)
}

func boundedPressureText(reader io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, 16385))
	if err != nil {
		return "", err
	}
	if len(raw) > 16384 {
		return "", errors.New("resource counters exceed size bound")
	}
	return strings.TrimSpace(string(raw)), nil
}
