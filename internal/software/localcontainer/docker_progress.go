package localcontainer

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

var dockerByteProgress = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*([kmgt]?b)\s*/\s*([0-9]+(?:\.[0-9]+)?)\s*([kmgt]?b)`)

// splitDockerProgressFrames accepts both newline-delimited diagnostics and the
// carriage-return frames used by Docker to repaint one progress row in place.
// Without the latter, a long pull can remain visually silent until the
// command exits even though Docker is continuously reporting real bytes.
func splitDockerProgressFrames(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if index := bytes.IndexAny(data, "\r\n"); index >= 0 {
		advance = index + 1
		if data[index] == '\r' && advance < len(data) && data[advance] == '\n' {
			advance++
		}
		return advance, data[:index], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func dockerProgressBytes(line string) (int64, int64) {
	match := dockerByteProgress.FindStringSubmatch(line)
	if len(match) != 5 {
		return 0, 0
	}
	return byteQuantity(match[1], match[2]), byteQuantity(match[3], match[4])
}

func byteQuantity(number, unit string) int64 {
	value, err := strconv.ParseFloat(number, 64)
	if err != nil || value < 0 {
		return 0
	}
	multiplier := float64(1)
	switch strings.ToLower(unit) {
	case "kb":
		multiplier = 1024
	case "mb":
		multiplier = 1024 * 1024
	case "gb":
		multiplier = 1024 * 1024 * 1024
	case "tb":
		multiplier = 1024 * 1024 * 1024 * 1024
	}
	return int64(value * multiplier)
}
