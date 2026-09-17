package notebookedit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"synon-go/internal/tools/fileevents"
)

type Input struct {
	NotebookPath string
	CellID       string
	NewSource    string
	CellType     string
	EditMode     string
}

type Output struct {
	NewSource    string `json:"new_source"`
	CellID       string `json:"cell_id,omitempty"`
	CellType     string `json:"cell_type"`
	Language     string `json:"language"`
	EditMode     string `json:"edit_mode"`
	Error        string `json:"error,omitempty"`
	NotebookPath string `json:"notebook_path"`
	OriginalFile string `json:"original_file"`
	UpdatedFile  string `json:"updated_file"`
}

type notebookFile struct {
	Raw           map[string]any
	Cells         []map[string]any
	Metadata      map[string]any
	NBFormat      int
	NBFormatMinor int
}

var cellIndexPattern = regexp.MustCompile(`^cell-(\d+)$`)

func Run(root string, input Input) (Output, error) {
	mode := strings.TrimSpace(input.EditMode)
	if mode == "" {
		mode = "replace"
	}
	output := Output{
		NewSource:    input.NewSource,
		CellID:       input.CellID,
		CellType:     firstNonEmpty(input.CellType, "code"),
		Language:     "python",
		EditMode:     mode,
		NotebookPath: filepath.ToSlash(strings.TrimSpace(input.NotebookPath)),
	}
	if err := validateStaticInput(input, mode); err != nil {
		return output, err
	}

	fullPath, relPath, err := resolveNotebookPath(root, input.NotebookPath)
	if err != nil {
		return output, err
	}
	output.NotebookPath = relPath

	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return output, err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return output, err
	}
	output.OriginalFile = string(raw)

	nb, err := parseNotebook(raw)
	if err != nil {
		output.Error = "Notebook is not valid JSON."
		return output, nil
	}
	if nb.Cells == nil {
		return output, errors.New("Notebook cells must be an array")
	}
	output.Language = notebookLanguage(nb)

	index, err := resolveCellIndex(nb.Cells, input.CellID, mode)
	if err != nil {
		return output, err
	}
	if mode == "replace" && index == len(nb.Cells) {
		mode = "insert"
		output.EditMode = mode
		if input.CellType == "" {
			input.CellType = "code"
			output.CellType = "code"
		}
	}

	switch mode {
	case "delete":
		if index < 0 || index >= len(nb.Cells) {
			return output, fmt.Errorf("Cell index %d does not exist in notebook", index)
		}
		output.CellType = cellType(nb.Cells[index])
		output.CellID = cellID(nb.Cells[index])
		nb.Cells = append(nb.Cells[:index], nb.Cells[index+1:]...)
	case "insert":
		if index < 0 || index > len(nb.Cells) {
			return output, fmt.Errorf("Insert index %d is outside notebook cells", index)
		}
		cell, generatedID, err := newCell(input.NewSource, input.CellType, nb)
		if err != nil {
			return output, err
		}
		output.CellType = cellType(cell)
		output.CellID = generatedID
		nb.Cells = append(nb.Cells, nil)
		copy(nb.Cells[index+1:], nb.Cells[index:])
		nb.Cells[index] = cell
	case "replace":
		if index < 0 || index >= len(nb.Cells) {
			return output, fmt.Errorf("Cell index %d does not exist in notebook", index)
		}
		target := nb.Cells[index]
		if input.CellType != "" {
			if input.CellType != "code" && input.CellType != "markdown" {
				return output, errors.New("cell_type must be code or markdown")
			}
			target["cell_type"] = input.CellType
		}
		target["source"] = input.NewSource
		if cellType(target) == "code" {
			target["execution_count"] = nil
			target["outputs"] = []any{}
		}
		output.CellType = cellType(target)
		output.CellID = cellID(target)
	default:
		return output, errors.New("edit_mode must be replace, insert, or delete")
	}

	nb.Raw["cells"] = nb.Cells
	updated, err := json.MarshalIndent(nb.Raw, "", " ")
	if err != nil {
		return output, err
	}
	if err := os.WriteFile(fullPath, updated, info.Mode().Perm()); err != nil {
		return output, err
	}
	fileevents.NotifyChanged(fullPath)
	output.UpdatedFile = string(updated)
	return output, nil
}

func parseNotebook(raw []byte) (notebookFile, error) {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return notebookFile{}, err
	}
	rawCells, ok := data["cells"].([]any)
	if !ok {
		return notebookFile{Raw: data}, nil
	}
	cells := make([]map[string]any, 0, len(rawCells))
	for index, rawCell := range rawCells {
		cell, ok := rawCell.(map[string]any)
		if !ok {
			return notebookFile{}, fmt.Errorf("cell %d must be an object", index)
		}
		cells = append(cells, cell)
	}
	metadata, _ := data["metadata"].(map[string]any)
	return notebookFile{
		Raw:           data,
		Cells:         cells,
		Metadata:      metadata,
		NBFormat:      intNumber(data["nbformat"]),
		NBFormatMinor: intNumber(data["nbformat_minor"]),
	}, nil
}

func validateStaticInput(input Input, mode string) error {
	if strings.TrimSpace(input.NotebookPath) == "" {
		return errors.New("notebook_path is required")
	}
	if filepath.Ext(input.NotebookPath) != ".ipynb" {
		return errors.New("File must be a Jupyter notebook (.ipynb file)")
	}
	switch mode {
	case "replace", "insert", "delete":
	default:
		return errors.New("edit_mode must be replace, insert, or delete")
	}
	if mode == "insert" && input.CellType == "" {
		return errors.New("cell_type is required when using edit_mode=insert")
	}
	if mode != "insert" && strings.TrimSpace(input.CellID) == "" {
		return errors.New("cell_id must be specified when not inserting a new cell")
	}
	if input.CellType != "" && input.CellType != "code" && input.CellType != "markdown" {
		return errors.New("cell_type must be code or markdown")
	}
	return nil
}

func resolveNotebookPath(root string, requested string) (string, string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", "", errors.New("file root is not configured")
	}
	if strings.HasPrefix(requested, `\\`) || strings.HasPrefix(requested, "//") {
		return "", "", errors.New("UNC notebook paths are not allowed")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	if evaluatedRoot, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = evaluatedRoot
	}
	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("notebook path escapes file root: %s", requested)
	}
	evaluatedTarget, err := filepath.EvalSymlinks(targetAbs)
	if err != nil {
		return "", "", err
	}
	evaluatedRel, err := filepath.Rel(rootAbs, evaluatedTarget)
	if err != nil {
		return "", "", err
	}
	if evaluatedRel == ".." || strings.HasPrefix(evaluatedRel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("notebook path escapes file root: %s", requested)
	}
	return evaluatedTarget, filepath.ToSlash(rel), nil
}

func resolveCellIndex(cells []map[string]any, cellID string, mode string) (int, error) {
	if strings.TrimSpace(cellID) == "" {
		if mode == "insert" {
			return 0, nil
		}
		return -1, errors.New("cell_id must be specified when not inserting a new cell")
	}
	for i, cell := range cells {
		if cellID == cellIDValue(cell["id"]) {
			if mode == "insert" {
				return i + 1, nil
			}
			return i, nil
		}
	}
	if parsed, ok := parseCellIndex(cellID); ok {
		if parsed < 0 || parsed >= len(cells) {
			return -1, fmt.Errorf("Cell with index %d does not exist in notebook", parsed)
		}
		if mode == "insert" {
			return parsed + 1, nil
		}
		return parsed, nil
	}
	return -1, fmt.Errorf("Cell with ID %q not found in notebook", cellID)
}

func parseCellIndex(value string) (int, bool) {
	matches := cellIndexPattern.FindStringSubmatch(value)
	if len(matches) != 2 {
		return 0, false
	}
	var parsed int
	for _, r := range matches[1] {
		parsed = parsed*10 + int(r-'0')
	}
	return parsed, true
}

func newCell(source string, requestedType string, nb notebookFile) (map[string]any, string, error) {
	cellType := requestedType
	if cellType == "" {
		cellType = "code"
	}
	if cellType != "code" && cellType != "markdown" {
		return nil, "", errors.New("cell_type must be code or markdown")
	}
	cell := map[string]any{
		"cell_type": cellType,
		"metadata":  map[string]any{},
		"source":    source,
	}
	generatedID := ""
	if nb.NBFormat > 4 || (nb.NBFormat == 4 && nb.NBFormatMinor >= 5) {
		generatedID = randomCellID()
		cell["id"] = generatedID
	}
	if cellType == "code" {
		cell["execution_count"] = nil
		cell["outputs"] = []any{}
	}
	return cell, generatedID, nil
}

func randomCellID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return ""
	}
	return hex.EncodeToString(buffer)
}

func notebookLanguage(nb notebookFile) string {
	if nb.Metadata == nil {
		return "python"
	}
	metadata, ok := nb.Metadata["language_info"].(map[string]any)
	if !ok {
		return "python"
	}
	if name, ok := metadata["name"].(string); ok && strings.TrimSpace(name) != "" {
		return name
	}
	return "python"
}

func intNumber(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}

func cellType(cell map[string]any) string {
	if value, ok := cell["cell_type"].(string); ok && value != "" {
		return value
	}
	return "code"
}

func cellID(cell map[string]any) string {
	return cellIDValue(cell["id"])
}

func cellIDValue(value any) string {
	if raw, ok := value.(string); ok {
		return raw
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
