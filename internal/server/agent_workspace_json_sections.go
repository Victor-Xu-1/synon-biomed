package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type agentWorkspaceJSONSection struct {
	path  string
	start int
	end   int
}

// Index JSON structure rather than guessing content meaning. A large result
// may put hundreds of discovery records before the retrieved documents; the
// directory makes every top-level field directly addressable with the existing
// read_file line cursor, without another tool or model-written extraction.
func agentWorkspaceJSONSectionDirectory(raw []byte, lineStarts []int) string {
	sections := agentWorkspaceJSONSections(raw)
	if len(sections) == 0 {
		return ""
	}
	var directory strings.Builder
	directory.WriteString("JSON field directory (json_pointer or display line ranges)\n")
	shift := len(sections) + 2
	for _, section := range sections {
		first := sort.Search(len(lineStarts), func(i int) bool { return lineStarts[i] > section.start })
		last := sort.Search(len(lineStarts), func(i int) bool { return lineStarts[i] >= section.end })
		encodedPath, _ := json.Marshal(section.path)
		fmt.Fprintf(&directory, "%s\t%d-%d\n", encodedPath, first+shift, last+shift)
	}
	directory.WriteString("--- JSON content ---\n")
	return directory.String()
}

func agentWorkspaceJSONSections(raw []byte) []agentWorkspaceJSONSection {
	sections := []agentWorkspaceJSONSection{}
	var walk func([]byte, int, string, int)
	walk = func(data []byte, base int, path string, depth int) {
		decoder := json.NewDecoder(bytes.NewReader(data))
		token, err := decoder.Token()
		if err != nil || token != json.Delim('{') {
			return
		}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return
			}
			name, ok := key.(string)
			if !ok {
				return
			}
			var value json.RawMessage
			if decoder.Decode(&value) != nil {
				return
			}
			end := base + int(decoder.InputOffset())
			start := end - len(value)
			pointer := path + "/" + strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
			sections = append(sections, agentWorkspaceJSONSection{pointer, start, end})
			if depth < 2 {
				walk(value, start, pointer, depth+1)
			}
		}
	}
	walk(raw, 0, "", 1)
	return sections
}
