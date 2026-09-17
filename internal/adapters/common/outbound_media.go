package common

import (
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type PendingUploadKind string

const (
	PendingUploadImage PendingUploadKind = "image"
	PendingUploadFile  PendingUploadKind = "file"
)

type UploadSourceKind string

const (
	UploadSourcePath   UploadSourceKind = "path"
	UploadSourceURL    UploadSourceKind = "url"
	UploadSourceBase64 UploadSourceKind = "base64"
)

type UploadSource struct {
	Kind UploadSourceKind
	Path string
	URL  string
	MIME string
	Data string
}

type PendingUpload struct {
	ID     string
	Kind   PendingUploadKind
	Source UploadSource
	Alt    string
}

type ImageBlockWatcher struct {
	buffer      string
	seen        map[string]struct{}
	accumulated []PendingUpload
}

type FileBlockWatcher struct {
	buffer      string
	seen        map[string]struct{}
	accumulated []PendingUpload
}

var (
	imageMarkdownPattern  = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	linkMarkdownPattern   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	dataURIPattern        = regexp.MustCompile(`^data:([^;,]+);base64,(.+)$`)
	imageExtensionPattern = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp|bmp|svg|ico|heic)$`)
	fileExtensionPattern  = regexp.MustCompile(`(?i)\.(pdf|zip|docx?|xlsx?|pptx?|csv|tsv|json|txt|md|html?|xml|ya?ml|mp4|mp3|wav|ogg|m4a)$`)
)

func NewImageBlockWatcher() *ImageBlockWatcher {
	return &ImageBlockWatcher{seen: map[string]struct{}{}}
}

func (w *ImageBlockWatcher) Feed(chunk string) []PendingUpload {
	if w == nil {
		return nil
	}
	w.buffer += chunk
	out := make([]PendingUpload, 0)
	lastConsumedEnd := 0
	matches := imageMarkdownPattern.FindAllStringSubmatchIndex(w.buffer, -1)
	for _, match := range matches {
		alt := w.buffer[match[2]:match[3]]
		target := w.buffer[match[4]:match[5]]
		if source, ok := classifyImageUploadSource(target); ok {
			if pending, emitted := w.pendingUpload(PendingUploadImage, source, target, alt); emitted {
				out = append(out, pending)
			}
		}
		lastConsumedEnd = match[1]
	}
	w.compactBuffer(lastConsumedEnd)
	return out
}

func (w *ImageBlockWatcher) Drain() []PendingUpload {
	if w == nil {
		return nil
	}
	return append([]PendingUpload(nil), w.accumulated...)
}

func (w *ImageBlockWatcher) Reset() {
	if w == nil {
		return
	}
	w.buffer = ""
	w.seen = map[string]struct{}{}
	w.accumulated = nil
}

func (w *ImageBlockWatcher) pendingUpload(kind PendingUploadKind, source UploadSource, target string, alt string) (PendingUpload, bool) {
	if w.seen == nil {
		w.seen = map[string]struct{}{}
	}
	id := uploadFingerprint(string(source.Kind) + ":" + target)
	if _, ok := w.seen[id]; ok {
		return PendingUpload{}, false
	}
	w.seen[id] = struct{}{}
	pending := PendingUpload{ID: id, Kind: kind, Source: source, Alt: emptyToZero(alt)}
	w.accumulated = append(w.accumulated, pending)
	return pending, true
}

func (w *ImageBlockWatcher) compactBuffer(lastConsumedEnd int) {
	if lastConsumedEnd > 0 {
		w.buffer = w.buffer[lastConsumedEnd:]
	}
	if len(w.buffer) > 4096 {
		w.buffer = w.buffer[len(w.buffer)-2048:]
	}
}

func NewFileBlockWatcher() *FileBlockWatcher {
	return &FileBlockWatcher{seen: map[string]struct{}{}}
}

func (w *FileBlockWatcher) Feed(chunk string) []PendingUpload {
	if w == nil {
		return nil
	}
	w.buffer += chunk
	out := make([]PendingUpload, 0)
	lastConsumedEnd := 0
	matches := linkMarkdownPattern.FindAllStringSubmatchIndex(w.buffer, -1)
	for _, match := range matches {
		if match[0] > 0 && w.buffer[match[0]-1] == '!' {
			lastConsumedEnd = match[1]
			continue
		}
		label := w.buffer[match[2]:match[3]]
		target := w.buffer[match[4]:match[5]]
		if !looksLikeFileTarget(target) {
			lastConsumedEnd = match[1]
			continue
		}
		if source, ok := classifyFileUploadSource(target); ok {
			if pending, emitted := w.pendingUpload(PendingUploadFile, source, target, label); emitted {
				out = append(out, pending)
			}
		}
		lastConsumedEnd = match[1]
	}
	w.compactBuffer(lastConsumedEnd)
	return out
}

func (w *FileBlockWatcher) Drain() []PendingUpload {
	if w == nil {
		return nil
	}
	return append([]PendingUpload(nil), w.accumulated...)
}

func (w *FileBlockWatcher) Reset() {
	if w == nil {
		return
	}
	w.buffer = ""
	w.seen = map[string]struct{}{}
	w.accumulated = nil
}

func (w *FileBlockWatcher) pendingUpload(kind PendingUploadKind, source UploadSource, target string, alt string) (PendingUpload, bool) {
	if w.seen == nil {
		w.seen = map[string]struct{}{}
	}
	id := uploadFingerprint(string(source.Kind) + ":" + target)
	if _, ok := w.seen[id]; ok {
		return PendingUpload{}, false
	}
	w.seen[id] = struct{}{}
	pending := PendingUpload{ID: id, Kind: kind, Source: source, Alt: emptyToZero(alt)}
	w.accumulated = append(w.accumulated, pending)
	return pending, true
}

func (w *FileBlockWatcher) compactBuffer(lastConsumedEnd int) {
	if lastConsumedEnd > 0 {
		w.buffer = w.buffer[lastConsumedEnd:]
	}
	if len(w.buffer) > 4096 {
		w.buffer = w.buffer[len(w.buffer)-2048:]
	}
}

func classifyImageUploadSource(target string) (UploadSource, bool) {
	if match := dataURIPattern.FindStringSubmatch(target); len(match) == 3 {
		if !strings.HasPrefix(strings.ToLower(match[1]), "image/") {
			return UploadSource{}, false
		}
		return UploadSource{Kind: UploadSourceBase64, MIME: match[1], Data: match[2]}, true
	}
	if strings.HasPrefix(target, "file://") {
		return classifyPathUploadSource(strings.TrimPrefix(target, "file://"))
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return UploadSource{Kind: UploadSourceURL, URL: target}, true
	}
	if filepath.IsAbs(target) {
		return classifyPathUploadSource(target)
	}
	return UploadSource{}, false
}

func classifyFileUploadSource(target string) (UploadSource, bool) {
	if match := dataURIPattern.FindStringSubmatch(target); len(match) == 3 {
		if strings.HasPrefix(strings.ToLower(match[1]), "image/") {
			return UploadSource{}, false
		}
		return UploadSource{Kind: UploadSourceBase64, MIME: match[1], Data: match[2]}, true
	}
	if strings.HasPrefix(target, "file://") {
		return classifyPathUploadSource(strings.TrimPrefix(target, "file://"))
	}
	if filepath.IsAbs(target) {
		return classifyPathUploadSource(target)
	}
	return UploadSource{}, false
}

func classifyPathUploadSource(path string) (UploadSource, bool) {
	if !filepath.IsAbs(path) || !IsSafeOutboundLocalPath(path) {
		return UploadSource{}, false
	}
	return UploadSource{Kind: UploadSourcePath, Path: path}, true
}

func looksLikeFileTarget(target string) bool {
	if match := dataURIPattern.FindStringSubmatch(target); len(match) == 3 {
		return !strings.HasPrefix(strings.ToLower(match[1]), "image/")
	}
	if imageExtensionPattern.MatchString(target) {
		return false
	}
	return fileExtensionPattern.MatchString(target)
}

func IsSafeOutboundLocalPath(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	cleanPath := filepath.Clean(path)
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(cleanPath))
	if err != nil {
		return false
	}
	sourcePath := filepath.Join(resolvedParent, filepath.Base(cleanPath))
	realPath, err := filepath.EvalSymlinks(cleanPath)
	if err != nil {
		return false
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if outboundMediaPathSensitive(realPath) {
		return false
	}
	root := mostSpecificOutboundRoot(outboundMediaRoots(), sourcePath)
	if root == "" {
		return false
	}
	return pathWithinOutboundRoot(root, realPath)
}

func mostSpecificOutboundRoot(roots []string, path string) string {
	selected := ""
	for _, root := range roots {
		if pathWithinOutboundRoot(root, path) && len(filepath.Clean(root)) > len(filepath.Clean(selected)) {
			selected = root
		}
	}
	return selected
}

func outboundMediaRoots() []string {
	values := []string{os.TempDir()}
	values = append(values, filepath.SplitList(os.Getenv("SYNON_OUTBOUND_MEDIA_ROOTS"))...)
	roots := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || !filepath.IsAbs(value) {
			continue
		}
		root, err := filepath.EvalSymlinks(filepath.Clean(value))
		if err != nil {
			continue
		}
		volumeRoot := filepath.Clean(filepath.VolumeName(root) + string(filepath.Separator))
		if filepath.Clean(root) == volumeRoot {
			continue
		}
		key := strings.ToLower(filepath.Clean(root))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		roots = append(roots, root)
	}
	return roots
}

func pathWithinOutboundRoot(root string, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func outboundMediaPathSensitive(path string) bool {
	normalized := strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
	blockedDirectories := []string{"/.aws/", "/.config/", "/.git/", "/.gnupg/", "/.ssh/", "/.synon/", "/boot/", "/dev/", "/etc/", "/proc/", "/root/", "/sys/", "/var/"}
	for _, token := range blockedDirectories {
		if strings.Contains(normalized+"/", token) {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, ".env") || base == "credentials" || strings.HasPrefix(base, "credentials.") || base == "id_rsa" || base == "id_ed25519" {
		return true
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".key", ".pem", ".p12", ".pfx":
		return true
	default:
		return false
	}
}

func uploadFingerprint(raw string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(raw))
	return strings.ToLower(strconv.FormatUint(uint64(hash.Sum32()), 16))
}

func emptyToZero(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return value
}
