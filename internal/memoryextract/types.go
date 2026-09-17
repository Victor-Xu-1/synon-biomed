// Package memoryextract implements the model-independent workspace
// durable-memory extraction state machine. Runtime/provider adapters translate
// their native transcript and model response types at this boundary.
package memoryextract

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockImage      BlockType = "image"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

type Block struct {
	Type          BlockType
	Text          string
	HarnessNotice bool
	ToolName      string
	ToolUseID     string
	ToolInput     map[string]any
	ToolError     bool
	DiskReference bool
}

type Message struct {
	Role    string
	Content []Block
	Ignore  bool
}

type AppendOperation struct {
	Text     string
	Evidence string
	Entity   string
	Category string
}

type ReplaceOperation struct {
	ID       string
	Text     string
	Evidence string
}

type Operations struct {
	Append  []AppendOperation
	Replace []ReplaceOperation
	Remove  []string
}
