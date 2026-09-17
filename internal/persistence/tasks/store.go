package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Task struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Subject     string         `json:"subject,omitempty"`
	Description string         `json:"description,omitempty"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Status      string         `json:"status"`
	Owner       string         `json:"owner,omitempty"`
	Blocks      []string       `json:"blocks"`
	BlockedBy   []string       `json:"blockedBy"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

type CreateOptions struct {
	Title       string
	Subject     string
	Description string
	ActiveForm  string
	Status      string
	Owner       string
	Blocks      []string
	BlockedBy   []string
	Metadata    map[string]any
}

type UpdateOptions struct {
	Title        *string
	Subject      *string
	Description  *string
	ActiveForm   *string
	Status       *string
	Owner        *string
	Metadata     map[string]any
	MetadataSet  bool
	AddBlocks    []string
	AddBlockedBy []string
	Delete       bool
}

type Store struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

type fileData struct {
	Tasks []Task `json:"tasks"`
}

func New(path string) *Store {
	return &Store{path: path, now: time.Now}
}

func (s *Store) Create(title string) (Task, error) {
	return s.CreateWithOptions(CreateOptions{Title: title, Status: "open"})
}

func (s *Store) CreateWithOptions(options CreateOptions) (Task, error) {
	title := strings.TrimSpace(options.Title)
	subject := strings.TrimSpace(options.Subject)
	if title == "" {
		title = subject
	}
	if subject == "" {
		subject = title
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return Task{}, errors.New("task title is required")
	}
	status := strings.TrimSpace(options.Status)
	if status == "" {
		status = "open"
	}
	if !validStatus(status) {
		return Task{}, fmt.Errorf("unsupported task status: %s", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked()
	if err != nil {
		return Task{}, err
	}
	now := s.now().UTC()
	task := Task{
		ID:          fmt.Sprintf("task-%d", now.UnixNano()),
		Title:       title,
		Subject:     subject,
		Description: strings.TrimSpace(options.Description),
		ActiveForm:  strings.TrimSpace(options.ActiveForm),
		Status:      status,
		Owner:       strings.TrimSpace(options.Owner),
		Blocks:      uniqueStrings(options.Blocks),
		BlockedBy:   uniqueStrings(options.BlockedBy),
		Metadata:    cloneMetadata(options.Metadata),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	data.Tasks = append(data.Tasks, task)
	if err := s.saveLocked(data); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Store) Get(id string) (Task, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Task{}, false, errors.New("task id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked()
	if err != nil {
		return Task{}, false, err
	}
	for _, task := range data.Tasks {
		if task.ID == id {
			return task, true, nil
		}
	}
	return Task{}, false, nil
}

func (s *Store) List() ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	tasks := append([]Task(nil), data.Tasks...)
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	return tasks, nil
}

func (s *Store) Update(id string, title string, status string) (Task, error) {
	options := UpdateOptions{}
	if strings.TrimSpace(title) != "" {
		options.Title = &title
	}
	if strings.TrimSpace(status) != "" {
		options.Status = &status
	}
	task, _, _, err := s.UpdateWithOptions(id, options)
	return task, err
}

func (s *Store) UpdateWithOptions(id string, options UpdateOptions) (Task, []string, *StatusChange, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Task{}, nil, nil, errors.New("task id is required")
	}
	if options.Status != nil {
		status := strings.TrimSpace(*options.Status)
		if status != "" && !validStatus(status) {
			return Task{}, nil, nil, fmt.Errorf("unsupported task status: %s", status)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked()
	if err != nil {
		return Task{}, nil, nil, err
	}
	for index, task := range data.Tasks {
		if task.ID != id {
			continue
		}
		if options.Delete {
			statusChange := &StatusChange{From: task.Status, To: "deleted"}
			data.Tasks = append(data.Tasks[:index], data.Tasks[index+1:]...)
			removeTaskRelations(data.Tasks, id)
			if err := s.saveLocked(data); err != nil {
				return Task{}, nil, nil, err
			}
			return task, []string{"deleted"}, statusChange, nil
		}
		updatedFields := []string{}
		if options.Title != nil {
			title := strings.TrimSpace(*options.Title)
			if title != "" && title != task.Title {
				task.Title = title
				if task.Subject == "" {
					task.Subject = title
				}
				updatedFields = append(updatedFields, "title")
			}
		}
		if options.Subject != nil {
			subject := strings.TrimSpace(*options.Subject)
			if subject != "" && subject != task.Subject {
				task.Subject = subject
				task.Title = subject
				updatedFields = append(updatedFields, "subject")
			}
		}
		if options.Description != nil && *options.Description != task.Description {
			task.Description = *options.Description
			updatedFields = append(updatedFields, "description")
		}
		if options.ActiveForm != nil && *options.ActiveForm != task.ActiveForm {
			task.ActiveForm = *options.ActiveForm
			updatedFields = append(updatedFields, "activeForm")
		}
		statusChange := (*StatusChange)(nil)
		if options.Status != nil {
			status := strings.TrimSpace(*options.Status)
			if status != "" && status != task.Status {
				statusChange = &StatusChange{From: task.Status, To: status}
				task.Status = status
				updatedFields = append(updatedFields, "status")
			}
		}
		if options.Owner != nil && *options.Owner != task.Owner {
			task.Owner = *options.Owner
			updatedFields = append(updatedFields, "owner")
		}
		if options.MetadataSet {
			task.Metadata = mergeMetadata(task.Metadata, options.Metadata)
			updatedFields = append(updatedFields, "metadata")
		}
		if added := addUnique(&task.Blocks, options.AddBlocks); added {
			updatedFields = append(updatedFields, "blocks")
		}
		if added := addUnique(&task.BlockedBy, options.AddBlockedBy); added {
			updatedFields = append(updatedFields, "blockedBy")
		}
		if len(updatedFields) > 0 {
			task.UpdatedAt = s.now().UTC()
		}
		data.Tasks[index] = task
		for _, blockedID := range options.AddBlocks {
			addBlockedByRelation(data.Tasks, blockedID, task.ID)
		}
		for _, blockerID := range options.AddBlockedBy {
			addBlocksRelation(data.Tasks, blockerID, task.ID)
		}
		if err := s.saveLocked(data); err != nil {
			return Task{}, nil, nil, err
		}
		return task, updatedFields, statusChange, nil
	}
	return Task{}, nil, nil, fmt.Errorf("task not found: %s", id)
}

type StatusChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (s *Store) loadLocked() (fileData, error) {
	if s.path == "" {
		return fileData{}, errors.New("task store path is not configured")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileData{Tasks: []Task{}}, nil
		}
		return fileData{}, err
	}
	var data fileData
	if err := json.Unmarshal(raw, &data); err != nil {
		return fileData{}, err
	}
	if data.Tasks == nil {
		data.Tasks = []Task{}
	}
	for index := range data.Tasks {
		normalizeTask(&data.Tasks[index])
	}
	return data, nil
}

func (s *Store) saveLocked(data fileData) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func validStatus(status string) bool {
	switch status {
	case "open", "running", "done", "blocked", "pending", "in_progress", "completed", "stopped":
		return true
	default:
		return false
	}
}

func normalizeTask(task *Task) {
	task.Title = strings.TrimSpace(task.Title)
	task.Subject = strings.TrimSpace(task.Subject)
	if task.Subject == "" {
		task.Subject = task.Title
	}
	if task.Title == "" {
		task.Title = task.Subject
	}
	if task.Status == "" {
		task.Status = "open"
	}
	task.Blocks = uniqueStrings(task.Blocks)
	task.BlockedBy = uniqueStrings(task.BlockedBy)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func addUnique(target *[]string, values []string) bool {
	before := len(*target)
	*target = uniqueStrings(append(*target, values...))
	return len(*target) != before
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func mergeMetadata(current map[string]any, patch map[string]any) map[string]any {
	merged := cloneMetadata(current)
	if merged == nil {
		merged = map[string]any{}
	}
	for key, value := range patch {
		if value == nil {
			delete(merged, key)
			continue
		}
		merged[key] = value
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func removeTaskRelations(tasks []Task, taskID string) {
	for index := range tasks {
		removeString(&tasks[index].Blocks, taskID)
		removeString(&tasks[index].BlockedBy, taskID)
	}
}

func removeString(values *[]string, value string) {
	result := (*values)[:0]
	for _, current := range *values {
		if current != value {
			result = append(result, current)
		}
	}
	*values = result
}

func addBlockedByRelation(tasks []Task, taskID string, blockerID string) {
	for index := range tasks {
		if tasks[index].ID == taskID {
			addUnique(&tasks[index].BlockedBy, []string{blockerID})
			return
		}
	}
}

func addBlocksRelation(tasks []Task, taskID string, blockedID string) {
	for index := range tasks {
		if tasks[index].ID == taskID {
			addUnique(&tasks[index].Blocks, []string{blockedID})
			return
		}
	}
}
