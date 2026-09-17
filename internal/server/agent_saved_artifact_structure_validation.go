package server

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var errAgentSavedArtifactStructureEmpty = errors.New("saved PDB/PDBQT artifact contains no atom records")

// validateAgentSavedArtifactStructure prevents a header-only fragment from
// being published as a molecular structure. This is intentionally a narrow
// exchange-format boundary check: it confirms at least one finite fixed-column
// atom coordinate while leaving chemistry-specific interpretation to the
// governed scientific workflow that produced the file.
func validateAgentSavedArtifactStructure(relativePath string, snapshot *os.File) error {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(relativePath)))
	if extension != ".pdb" && extension != ".pdbqt" {
		return nil
	}
	if snapshot == nil {
		return errAgentSavedArtifactStructureEmpty
	}
	if _, err := snapshot.Seek(0, 0); err != nil {
		return err
	}
	defer func() { _, _ = snapshot.Seek(0, 0) }()

	atomCount := 0
	scanner := bufio.NewScanner(snapshot)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 6 {
			continue
		}
		record := strings.TrimSpace(line[:6])
		if record != "ATOM" && record != "HETATM" {
			continue
		}
		if len(line) < 54 {
			return fmt.Errorf("saved %s artifact contains a truncated atom record", strings.TrimPrefix(extension, "."))
		}
		for _, field := range []string{line[30:38], line[38:46], line[46:54]} {
			coordinate, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
			if err != nil || math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
				return fmt.Errorf("saved %s artifact contains an invalid atom coordinate", strings.TrimPrefix(extension, "."))
			}
		}
		atomCount++
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if atomCount == 0 {
		return errAgentSavedArtifactStructureEmpty
	}
	return nil
}
