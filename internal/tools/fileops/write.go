package fileops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Write(root string, requestedPath string, content string, requestedEncoding string, overwrite bool) (WriteResult, error) {
	fs, err := openMutationFS(root)
	if err != nil {
		return WriteResult{}, err
	}
	defer fs.Close()
	target, rel, err := fs.resolve(requestedPath)
	if err != nil {
		return WriteResult{}, err
	}
	parent := filepath.Dir(target)
	if info, err := fs.Stat(parent); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WriteResult{}, fmt.Errorf("parent directory does not exist: %s", filepath.ToSlash(filepath.Dir(rel)))
		}
		return WriteResult{}, err
	} else if !info.IsDir() {
		return WriteResult{}, fmt.Errorf("parent path is not a directory: %s", filepath.ToSlash(filepath.Dir(rel)))
	}
	encoding := normalizeContentEncoding(requestedEncoding)
	data, err := decodeContent(content, encoding)
	if err != nil {
		return WriteResult{}, err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
		if err := fs.ensureEntryAbsent(requestedPath); err != nil {
			return WriteResult{}, err
		}
	}
	overwrote := fs.exists(target) && overwrite
	file, err := fs.OpenFile(target, flags, 0o600)
	if err != nil {
		return WriteResult{}, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return WriteResult{}, err
	}
	if err := file.Close(); err != nil {
		return WriteResult{}, err
	}
	fs.notify(target, rel)
	return WriteResult{Path: filepath.ToSlash(rel), Bytes: len(data), Encoding: encoding, Overwrote: overwrote}, nil
}

func OriginalWrite(root string, requestedPath string, content string) (OriginalWriteResult, error) {
	fs, err := openMutationFS(root)
	if err != nil {
		return OriginalWriteResult{}, err
	}
	defer fs.Close()
	target, rel, err := fs.resolve(requestedPath)
	if err != nil {
		return OriginalWriteResult{}, err
	}
	parent := filepath.Dir(target)
	if info, err := fs.Stat(parent); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return OriginalWriteResult{}, fmt.Errorf("parent directory does not exist: %s", filepath.ToSlash(filepath.Dir(rel)))
		}
		return OriginalWriteResult{}, err
	} else if !info.IsDir() {
		return OriginalWriteResult{}, fmt.Errorf("parent path is not a directory: %s", filepath.ToSlash(filepath.Dir(rel)))
	}
	var originalFile string
	writeType := "create"
	if existing, err := fs.ReadFile(target); err == nil {
		originalFile = string(existing)
		writeType = "update"
	} else if !errors.Is(err, os.ErrNotExist) {
		return OriginalWriteResult{}, err
	}
	if err := fs.WriteFile(target, []byte(content), 0o600); err != nil {
		return OriginalWriteResult{}, err
	}
	fs.notify(target, rel)
	return OriginalWriteResult{
		Type:         writeType,
		FilePath:     filepath.ToSlash(rel),
		Content:      content,
		OriginalFile: originalFile,
	}, nil
}

func OriginalEdit(root string, requestedPath string, oldString string, newString string, replaceAll bool) (OriginalEditResult, error) {
	if oldString == newString {
		return OriginalEditResult{}, errors.New("No changes to make: old_string and new_string are exactly the same.")
	}
	fs, err := openMutationFS(root)
	if err != nil {
		return OriginalEditResult{}, err
	}
	defer fs.Close()
	target, rel, err := fs.resolve(requestedPath)
	if err != nil {
		return OriginalEditResult{}, err
	}
	filePath := filepath.ToSlash(rel)
	originalRaw, err := fs.ReadFile(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && oldString == "" {
			if err := fs.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return OriginalEditResult{}, err
			}
			if err := fs.createFile(target, []byte(newString)); err != nil {
				return OriginalEditResult{}, err
			}
			fs.notify(target, rel)
			return OriginalEditResult{
				FilePath:        filePath,
				OldString:       oldString,
				NewString:       newString,
				OriginalFile:    "",
				StructuredPatch: []any{},
				UserModified:    false,
				ReplaceAll:      replaceAll,
			}, nil
		}
		return OriginalEditResult{}, err
	}
	originalFile := string(originalRaw)
	if oldString == "" && originalFile != "" {
		return OriginalEditResult{}, errors.New("old_string is empty but file is not empty")
	}
	matches := strings.Count(originalFile, oldString)
	if matches == 0 && oldString != "" {
		return OriginalEditResult{}, fmt.Errorf("String to replace not found in file.\nString: %s", oldString)
	}
	if matches > 1 && !replaceAll {
		return OriginalEditResult{}, fmt.Errorf("Found %d matches of the string to replace, but replace_all is false.", matches)
	}
	updated := originalFile
	if replaceAll {
		updated = strings.ReplaceAll(originalFile, oldString, newString)
	} else if oldString == "" {
		updated = newString
	} else {
		updated = strings.Replace(originalFile, oldString, newString, 1)
	}
	if err := fs.WriteFile(target, []byte(updated), 0o600); err != nil {
		return OriginalEditResult{}, err
	}
	fs.notify(target, rel)
	return OriginalEditResult{
		FilePath:        filePath,
		OldString:       oldString,
		NewString:       newString,
		OriginalFile:    originalFile,
		StructuredPatch: []any{},
		UserModified:    false,
		ReplaceAll:      replaceAll,
	}, nil
}

func Mkdir(root string, requestedPath string, recursive bool) (MkdirResult, error) {
	fs, err := openMutationFS(root)
	if err != nil {
		return MkdirResult{}, err
	}
	defer fs.Close()
	target, rel, err := fs.resolve(requestedPath)
	if err != nil {
		return MkdirResult{}, err
	}
	_, statErr := fs.Stat(target)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return MkdirResult{}, statErr
	}
	if recursive {
		err = fs.MkdirAll(target, 0o700)
	} else {
		err = fs.Mkdir(target, 0o700)
	}
	if err != nil {
		return MkdirResult{}, err
	}
	return MkdirResult{Path: filepath.ToSlash(rel), Type: "directory", Created: created}, nil
}
