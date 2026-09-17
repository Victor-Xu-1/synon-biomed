package kernel

import (
	"context"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"synon-go/internal/toolprogress"
)

const managedEnvironmentProgressLineLimit = 8 << 10

var (
	managedEnvironmentANSISequencePattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	managedEnvironmentPercentPattern      = regexp.MustCompile(`(?i)(?:^|[^0-9])([0-9]{1,3}(?:\.[0-9]+)?)\s*%`)
	managedEnvironmentByteFractionPattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(b|kb|mb|gb|kib|mib|gib)\s*/\s*([0-9]+(?:\.[0-9]+)?)\s*(b|kb|mb|gb|kib|mib|gib)`)
	managedEnvironmentRatePattern         = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(b|kb|mb|gb|kib|mib|gib)\s*(?:/s|/sec|ps)\b`)
)

// managedEnvironmentProgressObserver derives only bounded, public-safe facts
// from installer output. Raw lines remain in the diagnostic tail and are
// never copied into the user-visible progress event.
type managedEnvironmentProgressObserver struct {
	ctx         context.Context
	mu          sync.Mutex
	pending     []byte
	lastPhase   string
	lastPercent *float64
}

func newManagedEnvironmentProgressObserver(
	ctx context.Context,
	executable string,
	arguments []string,
) *managedEnvironmentProgressObserver {
	observer := &managedEnvironmentProgressObserver{ctx: ctx}
	phase := managedEnvironmentInitialProcessPhase(executable, arguments)
	if phase != "" {
		observer.publish(phase, nil, nil, nil, nil)
	}
	return observer
}

func (o *managedEnvironmentProgressObserver) Write(value []byte) (int, error) {
	if o == nil {
		return len(value), nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, character := range value {
		if character == '\n' || character == '\r' {
			o.flushLocked()
			continue
		}
		if len(o.pending) < managedEnvironmentProgressLineLimit {
			o.pending = append(o.pending, character)
		}
	}
	return len(value), nil
}

func (o *managedEnvironmentProgressObserver) Flush() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.flushLocked()
}

func (o *managedEnvironmentProgressObserver) flushLocked() {
	line := strings.TrimSpace(managedEnvironmentANSISequencePattern.ReplaceAllString(string(o.pending), ""))
	o.pending = o.pending[:0]
	if line == "" {
		return
	}
	phase := managedEnvironmentInstallerPhase(line)
	percent := managedEnvironmentObservedPercent(line)
	bytesCompleted, bytesTotal := managedEnvironmentObservedByteCounts(line)
	bytesPerSecond := managedEnvironmentObservedBytesPerSecond(line)
	if phase == "" {
		phase = o.lastPhase
	}
	if phase == "" && percent == nil && bytesPerSecond == nil && bytesCompleted == nil && bytesTotal == nil {
		return
	}
	o.publish(phase, percent, bytesPerSecond, bytesCompleted, bytesTotal)
}

func (o *managedEnvironmentProgressObserver) publish(
	phase string,
	percent, bytesPerSecond *float64,
	bytesCompleted, bytesTotal *int64,
) {
	if o == nil {
		return
	}
	phase = strings.TrimSpace(phase)
	changed := phase != "" && phase != o.lastPhase
	if percent != nil {
		if o.lastPercent == nil || math.Abs(*percent-*o.lastPercent) >= 1 || *percent == 0 || *percent == 100 {
			changed = true
		}
	}
	if bytesPerSecond != nil {
		changed = true
	}
	if bytesCompleted != nil || bytesTotal != nil {
		changed = true
	}
	if !changed {
		return
	}
	if phase != "" {
		if phase != o.lastPhase {
			o.lastPercent = nil
		}
		o.lastPhase = phase
	}
	if percent != nil {
		value := *percent
		o.lastPercent = &value
	}
	toolprogress.Report(o.ctx, toolprogress.Update{
		Phase: phase, PhasePercent: percent, BytesPerSecond: bytesPerSecond,
		BytesCompleted: bytesCompleted, BytesTotal: bytesTotal, Indeterminate: percent == nil,
	})
}

func (o *managedEnvironmentProgressObserver) Complete() {
	if o == nil {
		return
	}
	o.Flush()
	percent := float64(100)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.publish("installer_process_completed", &percent, nil, nil, nil)
}

func reportManagedEnvironmentMilestone(ctx context.Context, phase string, completed, total int64) {
	completedValue, totalValue := completed, total
	toolprogress.Report(ctx, toolprogress.Update{
		Phase: phase, CompletedItems: &completedValue, TotalItems: &totalValue, Indeterminate: true,
	})
}

func managedEnvironmentInitialProcessPhase(executable string, arguments []string) string {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(executable)))
	joined := strings.ToLower(strings.Join(arguments, " "))
	switch {
	case strings.Contains(base, "micromamba"):
		return "resolving_dependencies"
	case strings.Contains(joined, " -m pip install ") || strings.HasPrefix(joined, "-m pip install "):
		return "resolving_python_packages"
	case strings.Contains(joined, " -m venv ") || strings.HasPrefix(joined, "-m venv "):
		return "creating_environment"
	default:
		return "configuring_environment"
	}
}

func managedEnvironmentInstallerPhase(line string) string {
	normalized := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.Contains(normalized, "successfully installed") ||
		strings.Contains(normalized, "transaction finished") || strings.Contains(normalized, "transaction complete"):
		return "verifying_environment"
	case strings.Contains(normalized, "installing collected packages") || strings.Contains(normalized, "linking ") ||
		strings.Contains(normalized, "transaction starting") || strings.Contains(normalized, "executing transaction"):
		return "installing_packages"
	case strings.Contains(normalized, "building wheel") || strings.Contains(normalized, "building wheels") ||
		strings.Contains(normalized, "preparing metadata"):
		return "building_packages"
	case strings.Contains(normalized, "downloading and extracting"):
		// Micromamba announces the combined transfer phase before individual
		// extraction messages. Treat its first determinate counters as download
		// progress; a later extraction line advances the phase explicitly.
		return "downloading_packages"
	case strings.Contains(normalized, "extracting") || strings.Contains(normalized, "extract package"):
		return "extracting_packages"
	case strings.Contains(normalized, "downloading") || strings.Contains(normalized, "download "):
		return "downloading_packages"
	case strings.Contains(normalized, "package plan") || strings.Contains(normalized, "transaction summary"):
		return "preparing_transaction"
	case strings.Contains(normalized, "collecting ") || strings.Contains(normalized, "looking for:") ||
		strings.Contains(normalized, "repodata") || strings.Contains(normalized, "solving environment"):
		return "resolving_dependencies"
	default:
		return ""
	}
}

func managedEnvironmentObservedPercent(line string) *float64 {
	if match := managedEnvironmentPercentPattern.FindStringSubmatch(line); len(match) == 2 {
		if value, err := strconv.ParseFloat(match[1], 64); err == nil && value >= 0 && value <= 100 {
			return &value
		}
	}
	completed, total := managedEnvironmentObservedByteCounts(line)
	if completed == nil || total == nil || *total <= 0 {
		return nil
	}
	value := math.Min(100, float64(*completed)/float64(*total)*100)
	return &value
}

func managedEnvironmentObservedByteCounts(line string) (*int64, *int64) {
	match := managedEnvironmentByteFractionPattern.FindStringSubmatch(line)
	if len(match) != 5 {
		return nil, nil
	}
	completed, completedOK := managedEnvironmentByteValue(match[1], match[2])
	total, totalOK := managedEnvironmentByteValue(match[3], match[4])
	if !completedOK || !totalOK || total <= 0 || completed < 0 || completed > total ||
		completed > math.MaxInt64 || total > math.MaxInt64 {
		return nil, nil
	}
	completedBytes, totalBytes := int64(math.Round(completed)), int64(math.Round(total))
	return &completedBytes, &totalBytes
}

func managedEnvironmentObservedBytesPerSecond(line string) *float64 {
	match := managedEnvironmentRatePattern.FindStringSubmatch(line)
	if len(match) != 3 {
		return nil
	}
	value, ok := managedEnvironmentByteValue(match[1], match[2])
	if !ok {
		return nil
	}
	return &value
}

func managedEnvironmentByteValue(number, unit string) (float64, bool) {
	value, err := strconv.ParseFloat(number, 64)
	if err != nil || value < 0 {
		return 0, false
	}
	switch strings.ToLower(unit) {
	case "b":
		return value, true
	case "kb":
		return value * 1e3, true
	case "mb":
		return value * 1e6, true
	case "gb":
		return value * 1e9, true
	case "kib":
		return value * 1024, true
	case "mib":
		return value * 1024 * 1024, true
	case "gib":
		return value * 1024 * 1024 * 1024, true
	default:
		return 0, false
	}
}
