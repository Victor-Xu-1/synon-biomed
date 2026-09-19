package kernel

import (
	"context"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"synon-go/internal/toolprogress"
)

const managedEnvironmentProgressLineLimit = 8 << 10

var (
	managedEnvironmentANSISequencePattern        = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	managedEnvironmentPercentPattern             = regexp.MustCompile(`(?i)(?:^|[^0-9])([0-9]{1,3}(?:\.[0-9]+)?)\s*%`)
	managedEnvironmentByteFractionPattern        = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(bytes?|b|kb|mb|gb|kib|mib|gib)\s*/\s*([0-9]+(?:\.[0-9]+)?)\s*(bytes?|b|kb|mb|gb|kib|mib|gib)`)
	managedEnvironmentCompactByteFractionPattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*/\s*([0-9]+(?:\.[0-9]+)?)\s*(bytes?|b|kb|mb|gb|kib|mib|gib)\b`)
	managedEnvironmentRatePattern                = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(bytes?|b|kb|mb|gb|kib|mib|gib)\s*(?:/s|/sec|ps)\b`)
)

// managedEnvironmentProgressObserver derives only bounded, public-safe facts
// from installer output. Raw lines remain in the diagnostic tail and are
// never copied into the user-visible progress event.
type managedEnvironmentProgressObserver struct {
	ctx             context.Context
	mu              sync.Mutex
	pending         []byte
	process         string
	lastPhase       string
	lastPercent     *float64
	packageCount    *int64
	packageCountTag string
	plannedBytes    *int64
	clock           func() time.Time
	lastBytesDone   int64
	lastBytesTotal  int64
	lastBytesAt     time.Time
	hasByteSnapshot bool
}

func newManagedEnvironmentProgressObserver(
	ctx context.Context,
	executable string,
	arguments []string,
) *managedEnvironmentProgressObserver {
	observer := &managedEnvironmentProgressObserver{
		ctx: ctx, process: managedEnvironmentProcessName(executable, arguments), clock: time.Now,
	}
	phase := managedEnvironmentInitialProcessPhase(executable, arguments)
	if phase != "" {
		observer.publish(phase, nil, nil, nil, nil, nil, false)
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
	packageCount := managedEnvironmentObservedPackageCount(line)
	packageCountFresh := false
	if packageCount != nil {
		packageCountFresh = o.packageCount == nil || *packageCount != *o.packageCount || line != o.packageCountTag
		o.packageCount, o.packageCountTag = packageCount, line
	}
	o.observePlannedTotal(line)
	if phase == "" {
		phase = o.lastPhase
	}
	if phase == "" && percent == nil && bytesPerSecond == nil && bytesCompleted == nil && bytesTotal == nil && packageCount == nil {
		return
	}
	var completedItems *int64
	if packageCount != nil && packageCountFresh {
		zero := int64(0)
		completedItems = &zero
	}
	transferDone := managedEnvironmentTransferDonePattern.MatchString(line)
	if bytesTotal == nil && bytesCompleted == nil && o.plannedTotal() != nil &&
		(managedEnvironmentInstallerPhase(line) != "" || transferDone) {
		bytesTotal = o.plannedTotal()
		zero := int64(0)
		bytesCompleted = &zero
		if transferDone {
			done := *o.plannedTotal()
			bytesCompleted = &done
		}
	}
	if derivedRate := o.observeByteRate(bytesCompleted, bytesTotal); bytesPerSecond == nil {
		bytesPerSecond = derivedRate
	}
	o.publish(phase, percent, bytesPerSecond, bytesCompleted, bytesTotal, completedItems, packageCountFresh)
}

func (o *managedEnvironmentProgressObserver) publish(
	phase string,
	percent, bytesPerSecond *float64,
	bytesCompleted, bytesTotal *int64,
	completedItems *int64,
	packageCountFresh bool,
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
	if completedItems != nil && packageCountFresh {
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
	update := toolprogress.Update{
		Phase: phase, PhasePercent: percent, BytesPerSecond: bytesPerSecond,
		BytesCompleted: bytesCompleted, BytesTotal: bytesTotal, Indeterminate: percent == nil,
		Process: o.process,
	}
	if completedItems != nil && packageCountFresh {
		completed, total := *completedItems, *o.packageCount
		update.CompletedItems, update.TotalItems = &completed, &total
	}
	toolprogress.Report(o.ctx, update)
}

func (o *managedEnvironmentProgressObserver) Complete() {
	if o == nil {
		return
	}
	o.Flush()
	percent := float64(100)
	o.mu.Lock()
	defer o.mu.Unlock()
	var bytesCompleted, bytesTotal *int64
	if o.plannedBytes != nil {
		total := *o.plannedBytes
		completed := total
		bytesCompleted, bytesTotal = &completed, &total
	} else if o.hasByteSnapshot {
		completed, total := o.lastBytesTotal, o.lastBytesTotal
		if total > 0 {
			bytesCompleted, bytesTotal = &completed, &total
		}
	}
	o.publish("installer_process_completed", &percent, nil, bytesCompleted, bytesTotal, nil, false)
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
	if completed, total := managedEnvironmentObservedByteCounts(normalized); completed != nil && total != nil {
		return "downloading_packages"
	}
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
	case strings.HasPrefix(normalized, "progress "):
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
	if match := managedEnvironmentRawByteProgressPattern.FindStringSubmatch(line); len(match) == 3 {
		completed, completedErr := strconv.ParseInt(match[1], 10, 64)
		total, totalErr := strconv.ParseInt(match[2], 10, 64)
		if completedErr == nil && totalErr == nil && completed >= 0 && total > 0 && completed <= total {
			return &completed, &total
		}
		return nil, nil
	}
	match := managedEnvironmentByteFractionPattern.FindStringSubmatch(line)
	if len(match) == 5 {
		return managedEnvironmentNormalizeByteCounts(match[1], match[2], match[3], match[4])
	}
	if match := managedEnvironmentCompactByteFractionPattern.FindStringSubmatch(line); len(match) == 4 {
		return managedEnvironmentNormalizeByteCounts(match[1], match[3], match[2], match[3])
	}
	return nil, nil
}

func managedEnvironmentNormalizeByteCounts(completedNumber, completedUnit, totalNumber, totalUnit string) (*int64, *int64) {
	completed, completedOK := managedEnvironmentByteValue(completedNumber, completedUnit)
	total, totalOK := managedEnvironmentByteValue(totalNumber, totalUnit)
	if !completedOK || !totalOK || total <= 0 || completed < 0 || completed > total ||
		completed > math.MaxInt64 || total > math.MaxInt64 {
		return nil, nil
	}
	completedBytes, totalBytes := int64(math.Round(completed)), int64(math.Round(total))
	return &completedBytes, &totalBytes
}

func (o *managedEnvironmentProgressObserver) observeByteRate(completed, total *int64) *float64 {
	if completed == nil || total == nil || *total <= 0 || *completed < 0 || *completed > *total {
		return nil
	}
	now := time.Now()
	if o.clock != nil {
		now = o.clock()
	}
	var rate *float64
	if o.hasByteSnapshot && o.lastBytesTotal == *total && *completed >= o.lastBytesDone {
		elapsed := now.Sub(o.lastBytesAt).Seconds()
		delta := *completed - o.lastBytesDone
		if delta > 0 && elapsed > 0 {
			value := float64(delta) / elapsed
			if !math.IsNaN(value) && !math.IsInf(value, 0) && value <= 1e15 {
				rate = &value
			}
		}
	}
	o.lastBytesDone, o.lastBytesTotal, o.lastBytesAt, o.hasByteSnapshot = *completed, *total, now, true
	return rate
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
	case "byte", "bytes", "b":
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

var (
	managedEnvironmentTotalDownloadPattern   = regexp.MustCompile(`(?i)^total download:\s*([0-9]+(?:\.[0-9]+)?)\s*(bytes?|b|kb|mb|gb|kib|mib|gib)\s*$`)
	managedEnvironmentTransferDonePattern    = regexp.MustCompile("(?i)(transaction finished|transaction complete|successfully installed)")
	managedEnvironmentRawByteProgressPattern = regexp.MustCompile(`(?i)^\s*progress\s+([0-9]+)\s+of\s+([0-9]+)\s*$`)
)

// observePlannedTotal records the installer's own planned download size from
// its transaction summary.
func (o *managedEnvironmentProgressObserver) observePlannedTotal(line string) {
	if match := managedEnvironmentTotalDownloadPattern.FindStringSubmatch(line); len(match) == 3 {
		if value, ok := managedEnvironmentByteValue(match[1], match[2]); ok && value > 0 && value <= math.MaxInt64 {
			total := int64(math.Round(value))
			o.plannedBytes = &total
		}
	}
}

// managedEnvironmentPlannedTotal exposes the stored planned size for publish.
func (o *managedEnvironmentProgressObserver) plannedTotal() *int64 {
	return o.plannedBytes
}

// managedEnvironmentProcessName derives the bounded public-safe installer
// identity that produced the output. It is the generic process boundary every
// caller already passes; no task, topic, or example name is encoded here.
func managedEnvironmentProcessName(executable string, arguments []string) string {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(executable)))
	base = strings.TrimSuffix(base, ".exe")
	joined := strings.ToLower(strings.Join(arguments, " "))
	switch {
	case strings.Contains(base, "micromamba"):
		return "micromamba"
	case strings.Contains(joined, " -m pip ") || strings.HasPrefix(joined, "-m pip "):
		return "pip"
	case strings.Contains(base, "python"):
		return "python"
	case base == "r", strings.HasPrefix(base, "rscript"):
		return "r"
	default:
		if base != "" {
			return managedEnvironmentSafeProcessToken(base)
		}
		return ""
	}
}

func managedEnvironmentSafeProcessToken(value string) string {
	var kept []rune
	for _, character := range value {
		allowed := (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '_' || character == '-' || character == '.'
		if !allowed {
			break
		}
		kept = append(kept, character)
		if len(kept) >= 40 {
			break
		}
	}
	return string(kept)
}

var managedEnvironmentPackageListPattern = regexp.MustCompile(
	`(?i)^installing collected packages:\s*(.+)$`)

// managedEnvironmentObservedPackageCount captures the pip installing batch
// size as a workflow milestone; the count stays in the observer only, and the
// line itself never becomes public.
func managedEnvironmentObservedPackageCount(line string) *int64 {
	match := managedEnvironmentPackageListPattern.FindStringSubmatch(line)
	if len(match) != 2 {
		return nil
	}
	count := int64(0)
	for _, name := range strings.FieldsFunc(strings.TrimSpace(match[1]), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if strings.TrimSpace(name) != "" {
			count++
		}
	}
	if count <= 0 {
		return nil
	}
	return &count
}
