package server

import (
	"context"
	"errors"
	"io"
	"regexp"
)

var errRunnerEnvironmentConflictFound = errors.New("artifact environment conflict found")

func runnerArtifactEnvironmentConflict(ctx context.Context, input runnerArtifactScanInput, packageName string) (found bool, resultErr error) {
	reader, err := input.openText(ctx)
	if err != nil {
		return false, err
	}
	defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	size, err := reader.Seek(0, io.SeekEnd)
	if err != nil {
		return false, err
	}
	pattern := regexp.MustCompile("(?i)" + regexp.QuoteMeta(packageName) + "(?:\\s+|[:=]\\s*)v?[0-9]+\\.[0-9]+(?:\\.[0-9]+)*\\b")
	err = scanRunnerArtifactPattern(ctx, reader, pattern, false, func(indices []int64) error {
		start, end := indices[0], indices[1]
		if start > 0 {
			previous, err := readRunnerArtifactRange(ctx, reader, start-1, start)
			if err != nil {
				return err
			}
			value := previous[0]
			if value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_' || value == '.' || value == '-' {
				return nil
			}
		}
		suppressed, err := runnerArtifactRangeContainsAny(ctx, reader, max(0, start-80), end+min(int64(80), size-end), []string{"missing", "not installed", "unavailable", "缺失", "未安装", "未找到"})
		if err != nil {
			return err
		}
		if !suppressed {
			found = true
			return errRunnerEnvironmentConflictFound
		}
		return nil
	})
	if errors.Is(err, errRunnerEnvironmentConflictFound) {
		return true, nil
	}
	return found, err
}
