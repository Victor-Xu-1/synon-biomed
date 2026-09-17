package journal

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxJournalEntryBytes = 1 << 20
	defaultMaxJournalFileBytes  = 64 << 20
	defaultMaxJournalEntries    = 100000
	journalTimestampLayout      = "2006-01-02T15:04:05.000000000Z"
)

type Limits struct {
	MaxEntryBytes int
	MaxFileBytes  int64
	MaxEntries    int
}

type Message map[string]any

type Metadata struct {
	RunID           string
	ClientMessageID string
}

type Entry struct {
	SessionID       string  `json:"sessionId"`
	EventID         int64   `json:"eventId"`
	CreatedAt       string  `json:"createdAt"`
	RunID           string  `json:"runId,omitempty"`
	ClientMessageID string  `json:"clientMessageId,omitempty"`
	Message         Message `json:"message"`
	// SourceEventType is trusted in-memory projection provenance. It is never
	// accepted from or serialized into the legacy journal JSON surface.
	SourceEventType string `json:"-"`
}

// ReadCursor is an append-only journal position. It lets live projections
// consume only newly appended bytes while detecting replacement or truncation.
// It is an in-process optimization cursor, not a durable/public API cursor.
type ReadCursor struct {
	ByteOffset  int64
	EntriesSeen int
	LastEventID int64
	HeadSHA256  [sha256.Size]byte
}

type EventJournal struct {
	root         string
	mu           sync.Mutex
	sessionLocks map[string]*sync.Mutex
	lastEventIDs map[string]int64
	entryCounts  map[string]int
	now          func() time.Time
	limits       Limits
}

var (
	ErrIdempotencyConflict = errors.New("journal idempotency conflict")
	ErrEntrySizeLimit      = errors.New("durable journal entry exceeds size limit")
	ErrFileSizeLimit       = errors.New("durable journal file exceeds size limit")
	ErrEntryCountLimit     = errors.New("durable journal entry count exceeds limit")
	ErrReadCursorStale     = errors.New("durable journal read cursor is stale")
)

func NewEventJournal(root string) *EventJournal {
	return NewEventJournalWithLimits(root, Limits{})
}

func NewEventJournalWithLimits(root string, limits Limits) *EventJournal {
	if limits.MaxEntryBytes <= 0 {
		limits.MaxEntryBytes = defaultMaxJournalEntryBytes
	}
	if limits.MaxFileBytes <= 0 {
		limits.MaxFileBytes = defaultMaxJournalFileBytes
	}
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = defaultMaxJournalEntries
	}
	return &EventJournal{
		root:         root,
		sessionLocks: make(map[string]*sync.Mutex),
		lastEventIDs: make(map[string]int64),
		entryCounts:  make(map[string]int),
		now:          time.Now,
		limits:       limits,
	}
}

func (j *EventJournal) Root() string {
	return j.root
}

func (j *EventJournal) Append(sessionID string, message Message, metadata Metadata) (*Entry, error) {
	if !shouldPersist(message) {
		return nil, nil
	}

	lock := j.lockForSession(sessionID)
	lock.Lock()
	defer lock.Unlock()
	return j.appendLocked(sessionID, message, metadata)
}

// PreflightAppend validates the exact next encoded journal entry and cumulative
// limits without changing the compatibility projection.
func (j *EventJournal) PreflightAppend(sessionID string, message Message, metadata Metadata) error {
	if !shouldPersist(message) {
		return nil
	}
	lock := j.lockForSession(sessionID)
	lock.Lock()
	defer lock.Unlock()
	_, _, _, err := j.prepareAppendLocked(sessionID, message, metadata)
	return err
}

// PreflightAppendIdempotent validates either the matching existing projection
// or the exact next encoded entry without mutating the journal.
func (j *EventJournal) PreflightAppendIdempotent(sessionID string, message Message, metadata Metadata) error {
	if !shouldPersist(message) {
		return nil
	}
	metadata.RunID = cleanMetadata(metadata.RunID)
	metadata.ClientMessageID = cleanMetadata(metadata.ClientMessageID)
	if metadata.ClientMessageID == "" {
		return j.PreflightAppend(sessionID, message, metadata)
	}
	if err := validateJournalMetadata(message, metadata); err != nil {
		return err
	}
	lock := j.lockForSession(sessionID)
	lock.Lock()
	defer lock.Unlock()
	entries, err := j.readAllStrict(sessionID)
	if err != nil {
		return err
	}
	j.setJournalPosition(sessionID, maxEntryID(entries), len(entries))
	var matched *Entry
	for index := range entries {
		entry := &entries[index]
		messageClientID := cleanMetadata(stringValueForJournal(entry.Message["clientMessageId"]))
		if cleanMetadata(entry.ClientMessageID) != metadata.ClientMessageID && messageClientID != metadata.ClientMessageID {
			continue
		}
		if matched != nil {
			return fmt.Errorf("%w: duplicate durable entries", ErrIdempotencyConflict)
		}
		matched = entry
	}
	if matched != nil {
		equal, err := equivalentJournalEntry(*matched, message, metadata)
		if err != nil {
			return err
		}
		if !equal {
			return fmt.Errorf("%w: durable payload mismatch", ErrIdempotencyConflict)
		}
		return nil
	}
	_, _, _, err = j.prepareAppendLocked(sessionID, message, metadata)
	return err
}

// AppendIdempotent atomically resolves or creates one client-scoped event.
func (j *EventJournal) AppendIdempotent(sessionID string, message Message, metadata Metadata) (*Entry, bool, error) {
	entry, created, _, err := j.AppendIdempotentSnapshot(sessionID, message, metadata)
	return entry, created, err
}

// AppendIdempotentSnapshot resolves one client-scoped event and returns the
// same strict snapshot used for resolution. Repair callers avoid a second
// full-file scan.
func (j *EventJournal) AppendIdempotentSnapshot(sessionID string, message Message, metadata Metadata) (*Entry, bool, []Entry, error) {
	if !shouldPersist(message) {
		return nil, false, nil, nil
	}
	metadata.RunID = cleanMetadata(metadata.RunID)
	metadata.ClientMessageID = cleanMetadata(metadata.ClientMessageID)
	if metadata.ClientMessageID == "" {
		entry, err := j.Append(sessionID, message, metadata)
		return entry, entry != nil, nil, err
	}
	if err := validateJournalMetadata(message, metadata); err != nil {
		return nil, false, nil, err
	}

	lock := j.lockForSession(sessionID)
	lock.Lock()
	defer lock.Unlock()
	entries, err := j.readAllStrict(sessionID)
	if err != nil {
		return nil, false, nil, err
	}
	j.setJournalPosition(sessionID, maxEntryID(entries), len(entries))
	var matched *Entry
	for index := range entries {
		entry := &entries[index]
		messageClientID := cleanMetadata(stringValueForJournal(entry.Message["clientMessageId"]))
		if cleanMetadata(entry.ClientMessageID) != metadata.ClientMessageID && messageClientID != metadata.ClientMessageID {
			continue
		}
		if matched != nil {
			return nil, false, nil, fmt.Errorf("%w: duplicate durable entries", ErrIdempotencyConflict)
		}
		matched = entry
	}
	if matched != nil {
		equal, err := equivalentJournalEntry(*matched, message, metadata)
		if err != nil {
			return nil, false, nil, err
		}
		if !equal {
			return nil, false, nil, fmt.Errorf("%w: durable payload mismatch", ErrIdempotencyConflict)
		}
		return matched, false, entries, nil
	}
	entry, err := j.appendLocked(sessionID, message, metadata)
	if err != nil {
		return nil, false, nil, err
	}
	if entry != nil {
		entries = append(entries, *entry)
	}
	return entry, entry != nil, entries, nil
}

func (j *EventJournal) readAllStrict(sessionID string) ([]Entry, error) {
	file, err := os.Open(j.filePath(sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Entry{}, nil
		}
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > j.limits.MaxFileBytes {
		return nil, ErrFileSizeLimit
	}

	entries := make([]Entry, 0)
	reader := bufio.NewReaderSize(file, j.limits.MaxEntryBytes+1)
	lineNumber := 0
	var previousEventID int64
	for {
		lineBytes, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) || len(lineBytes) > j.limits.MaxEntryBytes {
			return nil, ErrEntrySizeLimit
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, errors.New("durable journal read failed")
		}
		if len(lineBytes) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		lineNumber++
		line := strings.TrimSpace(string(lineBytes))
		if line == "" {
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		if len(entries) >= j.limits.MaxEntries {
			return nil, ErrEntryCountLimit
		}
		entry, err := decodeStrictJournalEntry([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("durable journal entry %d is invalid", lineNumber)
		}
		if _, err := normalizeJournalMessageType(entry.Message); err != nil {
			return nil, fmt.Errorf("durable journal entry %d has invalid metadata", lineNumber)
		}
		if entry.SessionID != sessionID || entry.EventID <= 0 || entry.EventID <= previousEventID || entry.CreatedAt == "" || entry.Message == nil {
			return nil, fmt.Errorf("durable journal entry %d is incomplete", lineNumber)
		}
		if _, err := time.Parse(time.RFC3339Nano, entry.CreatedAt); err != nil {
			return nil, fmt.Errorf("durable journal entry %d has invalid timestamp", lineNumber)
		}
		messageType, ok := entry.Message["type"].(string)
		if !ok || strings.TrimSpace(messageType) == "" || !journalMetadataMatchesEntry(entry) {
			return nil, fmt.Errorf("durable journal entry %d has invalid metadata", lineNumber)
		}
		if messageType == "im_message" && (strings.TrimSpace(stringValueForJournal(entry.Message["role"])) != "user" || cleanMetadata(entry.ClientMessageID) == "") {
			return nil, fmt.Errorf("durable journal entry %d has invalid IM metadata", lineNumber)
		}
		entries = append(entries, entry)
		previousEventID = entry.EventID
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return entries, nil
}

func journalMetadataMatchesEntry(entry Entry) bool {
	messageEventID, ok := int64ValueForJournal(entry.Message["eventId"])
	if !ok || messageEventID != entry.EventID || stringValueForJournal(entry.Message["createdAt"]) != entry.CreatedAt {
		return false
	}
	return cleanMetadata(stringValueForJournal(entry.Message["runId"])) == cleanMetadata(entry.RunID) &&
		cleanMetadata(stringValueForJournal(entry.Message["clientMessageId"])) == cleanMetadata(entry.ClientMessageID)
}

func decodeStrictJournalEntry(data []byte) (Entry, error) {
	var entry Entry
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entry); err != nil {
		return Entry{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Entry{}, errors.New("journal entry has trailing content")
	}
	return entry, nil
}

func int64ValueForJournal(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		converted := int64(typed)
		return converted, float64(converted) == typed
	case int64:
		return typed, true
	case json.Number:
		converted, err := typed.Int64()
		return converted, err == nil
	default:
		return 0, false
	}
}

func (j *EventJournal) appendLocked(sessionID string, message Message, metadata Metadata) (*Entry, error) {
	entry, data, entryCount, err := j.prepareAppendLocked(sessionID, message, metadata)
	if err != nil {
		return nil, err
	}
	filePath := j.filePath(sessionID)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	j.setJournalPosition(sessionID, entry.EventID, entryCount+1)
	return entry, nil
}

func (j *EventJournal) prepareAppendLocked(sessionID string, message Message, metadata Metadata) (*Entry, []byte, int, error) {
	if sessionID = strings.TrimSpace(sessionID); sessionID == "" {
		return nil, nil, 0, errors.New("session id is required")
	}
	journaledMessage := cloneMessage(message)
	messageType, err := normalizeJournalMessageType(journaledMessage)
	if err != nil {
		return nil, nil, 0, err
	}
	if messageType == "im_message" && (strings.TrimSpace(stringValueForJournal(message["role"])) != "user" || cleanMetadata(metadata.ClientMessageID) == "") {
		return nil, nil, 0, errors.New("IM journal messages require user role and client message id")
	}

	filePath := j.filePath(sessionID)
	lastID, entryCount, err := j.journalPosition(sessionID)
	if err != nil {
		return nil, nil, 0, err
	}
	if entryCount >= j.limits.MaxEntries {
		return nil, nil, 0, ErrEntryCountLimit
	}
	eventID := lastID + 1
	createdAt := j.now().UTC().Format(journalTimestampLayout)
	runID := cleanMetadata(metadata.RunID)
	clientMessageID := cleanMetadata(metadata.ClientMessageID)
	journaledMessage["eventId"] = eventID
	journaledMessage["createdAt"] = createdAt
	if runID != "" {
		journaledMessage["runId"] = runID
	}
	if clientMessageID != "" {
		journaledMessage["clientMessageId"] = clientMessageID
	}

	entry := Entry{
		SessionID:       sessionID,
		EventID:         eventID,
		CreatedAt:       createdAt,
		RunID:           runID,
		ClientMessageID: clientMessageID,
		Message:         journaledMessage,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, nil, 0, err
	}
	if len(data)+1 > j.limits.MaxEntryBytes {
		return nil, nil, 0, ErrEntrySizeLimit
	}
	if info, err := os.Stat(filePath); err == nil {
		if info.Size()+int64(len(data)+1) > j.limits.MaxFileBytes {
			return nil, nil, 0, ErrFileSizeLimit
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, 0, err
	}
	return &entry, data, entryCount, nil
}

func validateJournalMetadata(message Message, metadata Metadata) error {
	for key, expected := range map[string]string{
		"runId": metadata.RunID, "clientMessageId": metadata.ClientMessageID,
	} {
		actual := cleanMetadata(stringValueForJournal(message[key]))
		if actual != "" && expected != "" && actual != expected {
			return fmt.Errorf("%w: message metadata mismatch", ErrIdempotencyConflict)
		}
	}
	return nil
}

func equivalentJournalEntry(entry Entry, message Message, metadata Metadata) (bool, error) {
	if cleanMetadata(entry.RunID) != metadata.RunID || cleanMetadata(entry.ClientMessageID) != metadata.ClientMessageID {
		return false, nil
	}
	existing := cloneMessage(entry.Message)
	delete(existing, "eventId")
	delete(existing, "createdAt")
	candidate := cloneMessage(message)
	delete(candidate, "eventId")
	delete(candidate, "createdAt")
	if metadata.RunID != "" {
		candidate["runId"] = metadata.RunID
	}
	if metadata.ClientMessageID != "" {
		candidate["clientMessageId"] = metadata.ClientMessageID
	}
	existingJSON, err := json.Marshal(existing)
	if err != nil {
		return false, err
	}
	candidateJSON, err := json.Marshal(candidate)
	if err != nil {
		return false, err
	}
	return bytes.Equal(existingJSON, candidateJSON), nil
}

func (j *EventJournal) ReadAfter(sessionID string, afterEventID int64, limit int) ([]Entry, error) {
	if afterEventID < 0 {
		afterEventID = 0
	}
	if limit < 0 {
		limit = 0
	}
	if limit == 0 {
		return []Entry{}, nil
	}
	entries, err := j.readAll(sessionID)
	if err != nil {
		return nil, err
	}
	result := make([]Entry, 0)
	for _, entry := range entries {
		if entry.EventID > afterEventID {
			result = append(result, entry)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

// ReadFromCursor reads at most limit valid entries beginning at an exact byte
// boundary. Unlike ReadAfter it does not rescan the prefix on every call.
// Callers retain the returned cursor and retry from the zero cursor when
// ErrReadCursorStale reports a replaced or truncated journal.
func (j *EventJournal) ReadFromCursor(
	sessionID string,
	cursor ReadCursor,
	limit int,
) ([]Entry, ReadCursor, bool, error) {
	if cursor.ByteOffset < 0 || cursor.EntriesSeen < 0 || cursor.LastEventID < 0 || limit <= 0 {
		return nil, ReadCursor{}, false, ErrReadCursorStale
	}
	lock := j.lockForSession(sessionID)
	lock.Lock()
	defer lock.Unlock()

	file, err := os.Open(j.filePath(sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && cursor.ByteOffset == 0 {
			return []Entry{}, cursor, true, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, ReadCursor{}, false, ErrReadCursorStale
		}
		return nil, ReadCursor{}, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, ReadCursor{}, false, err
	}
	if info.Size() > j.limits.MaxFileBytes {
		return nil, ReadCursor{}, false, ErrFileSizeLimit
	}
	if cursor.ByteOffset > info.Size() || cursor.EntriesSeen > j.limits.MaxEntries {
		return nil, ReadCursor{}, false, ErrReadCursorStale
	}

	next := cursor
	if cursor.ByteOffset > 0 {
		head, err := journalHeadSHA256(file, j.limits.MaxEntryBytes)
		if err != nil {
			return nil, ReadCursor{}, false, err
		}
		if head != cursor.HeadSHA256 {
			return nil, ReadCursor{}, false, ErrReadCursorStale
		}
	}
	if _, err := file.Seek(cursor.ByteOffset, io.SeekStart); err != nil {
		return nil, ReadCursor{}, false, err
	}
	reader := bufio.NewReaderSize(file, j.limits.MaxEntryBytes+1)
	entries := make([]Entry, 0, min(limit, 128))
	for len(entries) < limit {
		lineBytes, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) || len(lineBytes) > j.limits.MaxEntryBytes {
			return nil, ReadCursor{}, false, ErrEntrySizeLimit
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, ReadCursor{}, false, errors.New("durable journal read failed")
		}
		if len(lineBytes) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		next.ByteOffset += int64(len(lineBytes))
		line := bytes.TrimSpace(lineBytes)
		if len(line) > 0 {
			if next.HeadSHA256 == ([sha256.Size]byte{}) {
				next.HeadSHA256 = sha256.Sum256(line)
			}
			var entry Entry
			if json.Unmarshal(line, &entry) == nil && entry.SessionID != "" && entry.EventID > 0 &&
				entry.CreatedAt != "" && entry.Message != nil {
				if entry.SessionID != sessionID || entry.EventID <= next.LastEventID {
					return nil, ReadCursor{}, false, ErrReadCursorStale
				}
				next.LastEventID = entry.EventID
				next.EntriesSeen++
				if next.EntriesSeen > j.limits.MaxEntries {
					return nil, ReadCursor{}, false, ErrEntryCountLimit
				}
				entries = append(entries, entry)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return entries, next, next.ByteOffset >= info.Size(), nil
}

func journalHeadSHA256(file *os.File, maxEntryBytes int) ([sha256.Size]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return [sha256.Size]byte{}, err
	}
	reader := bufio.NewReaderSize(file, maxEntryBytes+1)
	for {
		lineBytes, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) || len(lineBytes) > maxEntryBytes {
			return [sha256.Size]byte{}, ErrEntrySizeLimit
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return [sha256.Size]byte{}, errors.New("durable journal read failed")
		}
		line := bytes.TrimSpace(lineBytes)
		if len(line) > 0 {
			return sha256.Sum256(line), nil
		}
		if errors.Is(readErr, io.EOF) {
			return [sha256.Size]byte{}, nil
		}
	}
}

func (j *EventJournal) ReadAll(sessionID string) ([]Entry, error) {
	return j.readAll(sessionID)
}

// ReadAllStrict returns a complete durable snapshot or fails on any malformed
// entry. Callers rebuilding a read model must not silently skip corruption.
func (j *EventJournal) ReadAllStrict(sessionID string) ([]Entry, error) {
	lock := j.lockForSession(sessionID)
	lock.Lock()
	defer lock.Unlock()
	return j.readAllStrict(sessionID)
}

func (j *EventJournal) CloneSession(sourceSessionID string, targetSessionID string) ([]Entry, error) {
	sourceSessionID = strings.TrimSpace(sourceSessionID)
	targetSessionID = strings.TrimSpace(targetSessionID)
	if sourceSessionID == "" {
		return nil, errors.New("source session id is required")
	}
	if targetSessionID == "" {
		return nil, errors.New("target session id is required")
	}
	if sourceSessionID == targetSessionID {
		return nil, errors.New("source and target session ids must differ")
	}

	sourceLock := j.lockForSession(sourceSessionID)
	sourceLock.Lock()
	entries, err := j.readAllStrict(sourceSessionID)
	sourceLock.Unlock()
	if err != nil {
		return nil, err
	}

	targetLock := j.lockForSession(targetSessionID)
	targetLock.Lock()
	defer targetLock.Unlock()

	filePath := j.filePath(targetSessionID)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return nil, err
	}
	cloned := make([]Entry, 0, len(entries))
	var buf bytes.Buffer
	for _, entry := range entries {
		copyEntry := Entry{
			SessionID:       targetSessionID,
			EventID:         entry.EventID,
			CreatedAt:       entry.CreatedAt,
			RunID:           entry.RunID,
			ClientMessageID: entry.ClientMessageID,
			Message:         cloneMessage(entry.Message),
		}
		if copyEntry.Message != nil {
			copyEntry.Message["eventId"] = copyEntry.EventID
			copyEntry.Message["createdAt"] = copyEntry.CreatedAt
		}
		data, err := json.Marshal(copyEntry)
		if err != nil {
			return nil, err
		}
		if err := j.appendBoundedJournalLine(&buf, data, len(cloned)); err != nil {
			return nil, err
		}
		cloned = append(cloned, copyEntry)
	}
	if err := os.WriteFile(filePath, buf.Bytes(), 0o600); err != nil {
		return nil, err
	}
	j.setJournalPosition(targetSessionID, maxEntryID(cloned), len(cloned))
	return cloned, nil
}

// ReplaceSession atomically replaces one session's active journal projection.
// Branch history remains durable in the workspace branch archive; the journal
// contains only the branch that the Runner may execute.
func (j *EventJournal) ReplaceSession(sessionID string, messages []Message) ([]Entry, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	lock := j.lockForSession(sessionID)
	lock.Lock()
	defer lock.Unlock()

	filePath := j.filePath(sessionID)
	directory := filepath.Dir(filePath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(messages))
	var buffer bytes.Buffer
	for _, message := range messages {
		if !shouldPersist(message) {
			continue
		}
		eventID := int64(len(entries) + 1)
		createdAt := j.now().UTC().Format(journalTimestampLayout)
		journaled := cloneMessage(message)
		if _, err := normalizeJournalMessageType(journaled); err != nil {
			return nil, err
		}
		journaled["eventId"] = eventID
		journaled["createdAt"] = createdAt
		clientMessageID := cleanMetadata(stringValueForJournal(journaled["clientMessageId"]))
		entry := Entry{
			SessionID: sessionID, EventID: eventID, CreatedAt: createdAt,
			ClientMessageID: clientMessageID, Message: journaled,
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		if err := j.appendBoundedJournalLine(&buffer, encoded, len(entries)); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	temporary, err := os.CreateTemp(directory, ".journal-replace-*")
	if err != nil {
		return nil, err
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := temporary.Write(buffer.Bytes()); err != nil {
		cleanup()
		return nil, err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, err
	}
	if err := os.Rename(temporaryPath, filePath); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, err
	}
	j.setJournalPosition(sessionID, maxEntryID(entries), len(entries))
	return entries, nil
}

func (j *EventJournal) TruncateAfter(sessionID string, afterEventID int64) ([]Entry, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if afterEventID < 0 {
		return nil, errors.New("afterEventId must be non-negative")
	}
	lock := j.lockForSession(sessionID)
	lock.Lock()
	defer lock.Unlock()

	entries, err := j.readAllStrict(sessionID)
	if err != nil {
		return nil, err
	}
	kept := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.EventID <= afterEventID {
			kept = append(kept, entry)
		}
	}
	filePath := j.filePath(sessionID)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for index, entry := range kept {
		data, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		if err := j.appendBoundedJournalLine(&buf, data, index); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filePath, buf.Bytes(), 0o600); err != nil {
		return nil, err
	}
	j.setJournalPosition(sessionID, maxEntryID(kept), len(kept))
	return kept, nil
}

func (j *EventJournal) appendBoundedJournalLine(buffer *bytes.Buffer, encoded []byte, currentEntries int) error {
	if currentEntries >= j.limits.MaxEntries {
		return ErrEntryCountLimit
	}
	if len(encoded)+1 > j.limits.MaxEntryBytes {
		return ErrEntrySizeLimit
	}
	if int64(buffer.Len()+len(encoded)+1) > j.limits.MaxFileBytes {
		return ErrFileSizeLimit
	}
	buffer.Write(encoded)
	buffer.WriteByte('\n')
	return nil
}

func (j *EventJournal) ReadByClientMessage(sessionID string, clientMessageID string) ([]Entry, error) {
	normalized := cleanMetadata(clientMessageID)
	if normalized == "" {
		return []Entry{}, nil
	}
	entries, err := j.readAll(sessionID)
	if err != nil {
		return nil, err
	}
	result := make([]Entry, 0)
	for _, entry := range entries {
		messageClientID, _ := entry.Message["clientMessageId"].(string)
		if entry.ClientMessageID == normalized || messageClientID == normalized {
			result = append(result, entry)
		}
	}
	return result, nil
}

func (j *EventJournal) HasClientMessage(sessionID string, clientMessageID string) (bool, error) {
	entries, err := j.ReadByClientMessage(sessionID, clientMessageID)
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

func (j *EventJournal) Remove(sessionID string) error {
	lock := j.lockForSession(sessionID)
	lock.Lock()
	defer lock.Unlock()

	j.mu.Lock()
	delete(j.lastEventIDs, sessionID)
	delete(j.entryCounts, sessionID)
	j.mu.Unlock()

	err := os.Remove(j.filePath(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func maxEntryID(entries []Entry) int64 {
	var max int64
	for _, entry := range entries {
		if entry.EventID > max {
			max = entry.EventID
		}
	}
	return max
}

func (j *EventJournal) lockForSession(sessionID string) *sync.Mutex {
	j.mu.Lock()
	defer j.mu.Unlock()
	lock := j.sessionLocks[sessionID]
	if lock == nil {
		lock = &sync.Mutex{}
		j.sessionLocks[sessionID] = lock
	}
	return lock
}

func (j *EventJournal) journalPosition(sessionID string) (int64, int, error) {
	j.mu.Lock()
	if cached, ok := j.lastEventIDs[sessionID]; ok {
		count := j.entryCounts[sessionID]
		j.mu.Unlock()
		return cached, count, nil
	}
	j.mu.Unlock()

	entries, err := j.readAllStrict(sessionID)
	if err != nil {
		return 0, 0, err
	}
	var last int64
	for _, entry := range entries {
		if entry.EventID > last {
			last = entry.EventID
		}
	}
	j.setJournalPosition(sessionID, last, len(entries))
	return last, len(entries), nil
}

func (j *EventJournal) setJournalPosition(sessionID string, eventID int64, entryCount int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lastEventIDs[sessionID] = eventID
	j.entryCounts[sessionID] = entryCount
}

func (j *EventJournal) readAll(sessionID string) ([]Entry, error) {
	file, err := os.Open(j.filePath(sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Entry{}, nil
		}
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil {
		return nil, err
	} else if info.Size() > j.limits.MaxFileBytes {
		return nil, ErrFileSizeLimit
	}

	entries := make([]Entry, 0)
	reader := bufio.NewReaderSize(file, j.limits.MaxEntryBytes+1)
	for {
		lineBytes, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) || len(lineBytes) > j.limits.MaxEntryBytes {
			return nil, ErrEntrySizeLimit
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, errors.New("durable journal read failed")
		}
		if len(lineBytes) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		line := strings.TrimSpace(string(lineBytes))
		if line == "" {
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.SessionID == "" || entry.EventID == 0 || entry.CreatedAt == "" || entry.Message == nil {
			continue
		}
		entries = append(entries, entry)
		if len(entries) > j.limits.MaxEntries {
			return nil, ErrEntryCountLimit
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return entries, nil
}

func (j *EventJournal) filePath(sessionID string) string {
	return filepath.Join(j.root, "session-events", url.QueryEscape(sessionID)+".jsonl")
}

func shouldPersist(message Message) bool {
	messageType, _ := message["type"].(string)
	return messageType != "connected" && messageType != "pong"
}

func cleanMetadata(value string) string {
	return strings.TrimSpace(value)
}

func cloneMessage(message Message) Message {
	copied := make(Message, len(message)+4)
	for key, value := range message {
		copied[key] = value
	}
	return copied
}

func stringValueForJournal(value any) string {
	text, _ := value.(string)
	return text
}

func normalizeJournalMessageType(message Message) (string, error) {
	messageType := strings.TrimSpace(stringValueForJournal(message["type"]))
	if messageType != "" {
		return messageType, nil
	}
	switch strings.TrimSpace(stringValueForJournal(message["role"])) {
	case "user", "assistant", "system", "tool":
		message["type"] = "message"
		return "message", nil
	default:
		return "", errors.New("message type is required")
	}
}
