package fileops

import "time"

const defaultReadLimit int64 = 64 * 1024
const defaultSearchLimit = 100
const defaultReadBatchMaxFiles int64 = 10
const maxReadBatchFiles int64 = 50
const defaultReadBatchMaxBytes int64 = 128 * 1024
const maxReadBatchBytes int64 = 1024 * 1024

type Entry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Type       string    `json:"type"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type ListResult struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}

type InfoResult struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Type       string    `json:"type"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
	Mode       string    `json:"mode"`
	SHA256     string    `json:"sha256,omitempty"`
	MIME       string    `json:"mime,omitempty"`
	Encoding   string    `json:"encoding,omitempty"`
	Binary     bool      `json:"binary"`
}

type ReadResult struct {
	Path            string    `json:"path"`
	Content         string    `json:"content"`
	ContentEncoding string    `json:"contentEncoding"`
	Bytes           int       `json:"bytes"`
	Truncated       bool      `json:"truncated"`
	Size            int64     `json:"size"`
	ModifiedAt      time.Time `json:"modifiedAt"`
	SHA256          string    `json:"sha256,omitempty"`
	MIME            string    `json:"mime,omitempty"`
	Encoding        string    `json:"encoding,omitempty"`
	Binary          bool      `json:"binary"`
}

type OriginalReadResult struct {
	Type string           `json:"type"`
	File OriginalReadFile `json:"file"`
}

type OriginalReadFile struct {
	FilePath     string `json:"filePath"`
	Content      string `json:"content,omitempty"`
	NumLines     int    `json:"numLines,omitempty"`
	StartLine    int    `json:"startLine,omitempty"`
	TotalLines   int    `json:"totalLines,omitempty"`
	Base64       string `json:"base64,omitempty"`
	Type         string `json:"type,omitempty"`
	OriginalSize int64  `json:"originalSize,omitempty"`
}

type ReadBatchFile struct {
	FilePath      string `json:"filePath"`
	Content       string `json:"content"`
	SizeBytes     int64  `json:"sizeBytes"`
	TotalLines    int    `json:"totalLines"`
	ReturnedLines int    `json:"returnedLines"`
	Encoding      string `json:"encoding"`
	Truncated     bool   `json:"truncated"`
}

type ReadBatchError struct {
	FilePath string `json:"filePath"`
	Reason   string `json:"reason"`
	Message  string `json:"message"`
}

type ReadBatchResult struct {
	Files  []ReadBatchFile  `json:"files"`
	Errors []ReadBatchError `json:"errors"`
}

type WriteResult struct {
	Path      string `json:"path"`
	Bytes     int    `json:"bytes"`
	Encoding  string `json:"encoding"`
	Overwrote bool   `json:"overwrote"`
}

type OriginalWriteResult struct {
	Type         string `json:"type"`
	FilePath     string `json:"filePath"`
	Content      string `json:"content"`
	OriginalFile string `json:"originalFile,omitempty"`
}

type OriginalEditResult struct {
	FilePath        string `json:"filePath"`
	OldString       string `json:"oldString"`
	NewString       string `json:"newString"`
	OriginalFile    string `json:"originalFile"`
	StructuredPatch []any  `json:"structuredPatch"`
	UserModified    bool   `json:"userModified"`
	ReplaceAll      bool   `json:"replaceAll"`
}

type MkdirResult struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Created bool   `json:"created"`
}

type TransferResult struct {
	Path       string `json:"path"`
	TargetPath string `json:"targetPath"`
	Type       string `json:"type"`
	Overwrote  bool   `json:"overwrote"`
}

type DeleteResult struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Deleted bool   `json:"deleted"`
}

type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type SearchResult struct {
	Query        string        `json:"query"`
	Matches      []SearchMatch `json:"matches"`
	AppliedLimit int64         `json:"appliedLimit"`
	Truncated    bool          `json:"truncated"`
}

type GlobResult struct {
	DurationMs int64    `json:"durationMs"`
	NumFiles   int      `json:"numFiles"`
	Filenames  []string `json:"filenames"`
	Truncated  bool     `json:"truncated"`
}

type GrepOptions struct {
	Glob            string
	OutputMode      string
	Before          int64
	After           int64
	Context         int64
	ShowLineNumbers bool
	CaseInsensitive bool
	Type            string
	HeadLimit       int64
	Offset          int64
	Multiline       bool
}

type GrepResult struct {
	Mode          string   `json:"mode,omitempty"`
	NumFiles      int      `json:"numFiles"`
	Filenames     []string `json:"filenames"`
	Content       string   `json:"content,omitempty"`
	NumLines      int      `json:"numLines,omitempty"`
	NumMatches    int      `json:"numMatches,omitempty"`
	AppliedLimit  int      `json:"appliedLimit,omitempty"`
	AppliedOffset int      `json:"appliedOffset,omitempty"`
}

type ReplaceResult struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	Bytes        int    `json:"bytes"`
}

type PatchOperation struct {
	Type      string
	StartLine int
	EndLine   int
	Line      int
	Content   string
}

type PatchResult struct {
	Path       string `json:"path"`
	Operations int    `json:"operations"`
	Bytes      int    `json:"bytes"`
}

type OriginalPatchResult struct {
	Applied      bool                `json:"applied"`
	DryRun       bool                `json:"dryRun"`
	Files        []OriginalPatchFile `json:"files"`
	DeletedFiles []string            `json:"deletedFiles,omitempty"`
	FinalDiff    string              `json:"finalDiff"`
}

type OriginalPatchFile struct {
	FilePath   string `json:"filePath"`
	ChangeType string `json:"changeType,omitempty"`
	Additions  int    `json:"additions"`
	Deletions  int    `json:"deletions"`
}

type JSONPatchOperation struct {
	Op    string
	Path  string
	Value any
}

type JSONPatchResult struct {
	Path       string `json:"path"`
	Operations int    `json:"operations"`
	Bytes      int    `json:"bytes"`
}

type CodeSymbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Line      int    `json:"line"`
	Signature string `json:"signature,omitempty"`
}

type CodeFile struct {
	Path    string       `json:"path"`
	Symbols []CodeSymbol `json:"symbols"`
}

type CodeIndexResult struct {
	Path  string     `json:"path"`
	Files []CodeFile `json:"files"`
}

type CodeIndexOptions struct {
	Limit       int64
	SymbolLimit int64
	Extensions  []string
	SymbolKinds []string
	Query       string
}

type CodeReference struct {
	Path   string                     `json:"path"`
	Line   int                        `json:"line"`
	Text   string                     `json:"text"`
	Before []CodeReferenceContextLine `json:"before,omitempty"`
	After  []CodeReferenceContextLine `json:"after,omitempty"`
}

type CodeReferenceContextLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

type CodeReferencesResult struct {
	Path       string          `json:"path"`
	Symbol     string          `json:"symbol"`
	References []CodeReference `json:"references"`
	Truncated  bool            `json:"truncated"`
}

type CodeReferencesOptions struct {
	Limit        int64
	Extensions   []string
	ContextLines int64
}
