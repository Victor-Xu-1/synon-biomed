package server

import (
	"mime"
	"path/filepath"
	"sort"
	"strings"
)

// agentWorkspaceFileFormatContract is the single runtime registry used to
// classify an attachment and choose its reader. Extensions and browser MIME
// values are hints; container markers and sniffed media types are stronger
// evidence. The model receives the same contract when a specialist reader is
// required, so uncommon formats can be routed without task-specific prompts.
type agentWorkspaceFileFormatContract struct {
	ID                 string
	Family             string
	Reader             string
	Extensions         []string
	ActiveExtensions   []string
	MediaTypes         []string
	ArchivePart        string
	ArchivePrefix      string
	ArchiveSuffix      string
	PreferredTools     []string
	UpgradeTools       []string
	EquivalentPackages []string
	SecurityMode       string
	Representation     string
	Preserves          []string
	Omits              []string
}

var agentWorkspaceFileFormats = []agentWorkspaceFileFormatContract{
	{
		ID: "word_ooxml", Family: "word_processing", Reader: "builtin_docx",
		Extensions:       []string{".docx", ".dotx", ".docm", ".dotm"},
		ActiveExtensions: []string{".docm", ".dotm"},
		MediaTypes: []string{
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			"application/vnd.ms-word.document.macroenabled.12",
		},
		ArchivePart: "word/document.xml", SecurityMode: "passive_extract_no_macros",
		Representation: "semantic_preview", Preserves: []string{"paragraph_text", "heading_levels"},
		Omits: []string{"page_layout", "embedded_media", "tracked_changes", "macros"}, UpgradeTools: []string{"search_skills", "repl"},
		EquivalentPackages: []string{"python-docx"},
	},
	{
		ID: "excel_ooxml", Family: "spreadsheet", Reader: "builtin_xlsx",
		Extensions:       []string{".xlsx", ".xltx", ".xlsm", ".xltm"},
		ActiveExtensions: []string{".xlsm", ".xltm"},
		MediaTypes: []string{
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"application/vnd.ms-excel.sheet.macroenabled.12",
		},
		ArchivePart: "xl/workbook.xml", SecurityMode: "passive_extract_no_macros",
		Representation: "structured_preview", Preserves: []string{"sheet_order", "cell_values", "merged_ranges"},
		Omits: []string{"formulas", "styles", "charts", "embedded_media", "macros"}, UpgradeTools: []string{"search_skills", "repl"},
		EquivalentPackages: []string{"openpyxl"},
	},
	{
		ID: "powerpoint_ooxml", Family: "presentation", Reader: "builtin_pptx",
		Extensions:       []string{".pptx", ".potx", ".ppsx", ".pptm", ".potm", ".ppsm"},
		ActiveExtensions: []string{".pptm", ".potm", ".ppsm"},
		MediaTypes: []string{
			"application/vnd.openxmlformats-officedocument.presentationml.presentation",
			"application/vnd.ms-powerpoint.presentation.macroenabled.12",
		},
		ArchivePrefix: "ppt/slides/slide", ArchiveSuffix: ".xml", SecurityMode: "passive_extract_no_macros",
		Representation: "semantic_preview", Preserves: []string{"slide_order", "paragraph_text"},
		Omits: []string{"visual_layout", "embedded_media", "speaker_notes", "animations", "macros"}, UpgradeTools: []string{"search_skills", "repl"},
		EquivalentPackages: []string{"python-pptx"},
	},
	{
		ID: "html_document", Family: "web_document", Reader: "builtin_html",
		Extensions: []string{".html", ".htm", ".xhtml"},
		MediaTypes: []string{"text/html", "application/xhtml+xml"}, SecurityMode: "passive_extract_no_remote_resources",
		Representation: "semantic_preview", Preserves: []string{"title", "readable_text"},
		Omits: []string{"scripts", "styles", "forms", "remote_media", "visual_layout"}, UpgradeTools: []string{"search_skills", "repl"},
		EquivalentPackages: []string{"beautifulsoup4"},
	},
	{
		ID: "delimited_table", Family: "spreadsheet", Reader: "builtin_delimited",
		Extensions: []string{".csv", ".tsv", ".tab"},
		MediaTypes: []string{"text/csv", "text/tab-separated-values"}, SecurityMode: "passive_extract",
		Representation: "structured_content", Preserves: []string{"rows", "columns", "cell_text"},
	},
	{
		ID: "pdf_document", Family: "document", Reader: "builtin_pdf",
		Extensions: []string{".pdf"}, MediaTypes: []string{"application/pdf"}, SecurityMode: "passive_multimodal",
		Representation: "multimodal_original", Preserves: []string{"extractable_text", "visual_pages", "source_bytes"},
		EquivalentPackages: []string{"pypdf", "pypdf2", "pdfplumber"},
	},
	{
		ID: "raster_image", Family: "image", Reader: "builtin_image",
		Extensions: []string{".png", ".jpg", ".jpeg", ".gif", ".webp"},
		MediaTypes: []string{"image/png", "image/jpeg", "image/gif", "image/webp"}, SecurityMode: "passive_multimodal",
		Representation: "multimodal_original", Preserves: []string{"pixels", "source_bytes"},
	},
	{
		ID: "structured_text", Family: "text", Reader: "builtin_text",
		Extensions: []string{".txt", ".md", ".markdown", ".rst", ".json", ".jsonl", ".ndjson", ".xml", ".yaml", ".yml", ".toml", ".ini", ".log", ".tex"},
		MediaTypes: []string{"text/plain", "text/markdown", "application/json", "application/xml", "text/xml", "application/yaml"}, SecurityMode: "passive_extract",
		Representation: "exact_text", Preserves: []string{"utf8_text"},
	},
	{
		ID: "legacy_office", Family: "office_binary", Reader: "specialist_reader",
		Extensions:     []string{".doc", ".xls", ".ppt"},
		MediaTypes:     []string{"application/msword", "application/vnd.ms-excel", "application/vnd.ms-powerpoint"},
		PreferredTools: []string{"search_skills", "repl"}, SecurityMode: "passive_conversion_no_macros",
		Representation: "original_only",
	},
	{
		ID: "open_document", Family: "office_container", Reader: "specialist_reader",
		Extensions:     []string{".odt", ".ods", ".odp"},
		MediaTypes:     []string{"application/vnd.oasis.opendocument.text", "application/vnd.oasis.opendocument.spreadsheet", "application/vnd.oasis.opendocument.presentation"},
		PreferredTools: []string{"search_skills", "repl"}, SecurityMode: "passive_conversion_no_macros",
		Representation: "original_only",
	},
	{
		ID: "archive", Family: "archive", Reader: "specialist_reader",
		Extensions:     []string{".zip", ".tar", ".tgz", ".gz", ".bz2", ".xz", ".7z", ".rar"},
		MediaTypes:     []string{"application/zip", "application/x-tar", "application/gzip", "application/x-gzip", "application/x-7z-compressed", "application/vnd.rar"},
		PreferredTools: []string{"repl", "search_skills"}, SecurityMode: "inspect_members_no_execution",
		Representation: "original_only",
	},
	{
		ID: "columnar_table", Family: "structured_data", Reader: "specialist_reader",
		Extensions:     []string{".parquet", ".feather", ".arrow", ".orc", ".avro"},
		PreferredTools: []string{"repl", "search_skills"}, SecurityMode: "passive_extract",
		Representation: "original_only",
	},
	{
		ID: "scientific_binary", Family: "scientific_data", Reader: "specialist_reader",
		Extensions:     []string{".h5", ".hdf5", ".nc", ".cdf", ".npy", ".npz", ".mat", ".mrc", ".map", ".dcd", ".xtc", ".trr", ".mae", ".maegz", ".cdx"},
		MediaTypes:     []string{"chemical/x-cdx", "chemical/x-cdxml", "application/x-hdf5", "application/x-netcdf"},
		PreferredTools: []string{"search_skills", "repl"}, SecurityMode: "passive_extract",
		Representation: "original_only",
	},
	{
		ID: "database", Family: "database", Reader: "specialist_reader",
		Extensions:     []string{".sqlite", ".sqlite3", ".db"},
		MediaTypes:     []string{"application/vnd.sqlite3", "application/x-sqlite3"},
		PreferredTools: []string{"repl", "search_skills"}, SecurityMode: "read_only_queries",
		Representation: "original_only",
	},
	{
		ID: "audio_video", Family: "media", Reader: "specialist_reader",
		Extensions:     []string{".wav", ".mp3", ".m4a", ".flac", ".ogg", ".mp4", ".mov", ".mkv", ".webm"},
		PreferredTools: []string{"search_skills", "repl"}, SecurityMode: "passive_multimodal",
		Representation: "original_only",
	},
}

func agentWorkspaceFormatFromArchive(archive *webOOXMLArchive) (agentWorkspaceFileFormatContract, bool) {
	if archive == nil {
		return agentWorkspaceFileFormatContract{}, false
	}
	for _, format := range agentWorkspaceFileFormats {
		if format.ArchivePart != "" {
			if _, found := archive.parts[format.ArchivePart]; found {
				return format, true
			}
			continue
		}
		if format.ArchivePrefix != "" && len(archive.Names(format.ArchivePrefix, format.ArchiveSuffix)) > 0 {
			return format, true
		}
	}
	return agentWorkspaceFileFormatContract{}, false
}

func agentWorkspaceFormatHint(filename, contentType string) (agentWorkspaceFileFormatContract, string, bool) {
	mediaType, _, _ := mime.ParseMediaType(strings.TrimSpace(contentType))
	mediaType = strings.ToLower(mediaType)
	if mediaType != "" && mediaType != "application/octet-stream" && mediaType != "application/zip" {
		for _, format := range agentWorkspaceFileFormats {
			if containsAgentWorkspaceFormatValue(format.MediaTypes, mediaType) {
				return format, "detected_media_type", true
			}
		}
	}
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	if extension != "" {
		for _, format := range agentWorkspaceFileFormats {
			if containsAgentWorkspaceFormatValue(format.Extensions, extension) {
				return format, "filename_hint", true
			}
		}
	}
	return agentWorkspaceFileFormatContract{
		ID: "unknown_binary", Family: "unknown", Reader: "specialist_reader",
		PreferredTools: []string{"search_skills", "repl"}, SecurityMode: "inspect_magic_before_parser_selection", Representation: "original_only",
	}, "unknown", false
}

func containsAgentWorkspaceFormatValue(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}

func agentWorkspaceFormatMetadata(format agentWorkspaceFileFormatContract, confidence, status, filename string) map[string]any {
	tools := append([]string(nil), format.PreferredTools...)
	if len(tools) == 0 && (status == "reader_required" || status == "parse_failed" || status == "unavailable") {
		tools = []string{"search_skills", "repl"}
	}
	metadata := map[string]any{
		"format_id": format.ID, "family": format.Family, "reader": format.Reader,
		"recognition": confidence, "status": status, "security_mode": format.SecurityMode,
	}
	if format.Representation != "" {
		metadata["representation"] = format.Representation
	}
	if len(format.Preserves) > 0 {
		metadata["preserves"] = append([]string(nil), format.Preserves...)
	}
	if len(format.Omits) > 0 {
		metadata["omits"] = append([]string(nil), format.Omits...)
	}
	if len(format.UpgradeTools) > 0 {
		metadata["upgrade_tools"] = append([]string(nil), format.UpgradeTools...)
	}
	if len(tools) > 0 {
		metadata["preferred_tools"] = tools
	}
	if containsAgentWorkspaceFormatValue(format.ActiveExtensions, strings.ToLower(filepath.Ext(filename))) {
		metadata["active_content_ignored"] = true
	}
	return metadata
}

func validateAgentWorkspaceFileFormatRegistry() []string {
	problems := make([]string, 0)
	ids := map[string]bool{}
	extensions := map[string]string{}
	for _, format := range agentWorkspaceFileFormats {
		if strings.TrimSpace(format.ID) == "" || strings.TrimSpace(format.Family) == "" || strings.TrimSpace(format.Reader) == "" {
			problems = append(problems, "format registry entry has an incomplete identity")
			continue
		}
		if ids[format.ID] {
			problems = append(problems, "duplicate format id: "+format.ID)
		}
		ids[format.ID] = true
		for _, extension := range format.Extensions {
			extension = strings.ToLower(strings.TrimSpace(extension))
			if extension == "" || !strings.HasPrefix(extension, ".") {
				problems = append(problems, "invalid extension in "+format.ID)
				continue
			}
			if owner := extensions[extension]; owner != "" && owner != format.ID {
				problems = append(problems, "duplicate extension "+extension+" in "+owner+" and "+format.ID)
			}
			extensions[extension] = format.ID
		}
	}
	sort.Strings(problems)
	return problems
}
