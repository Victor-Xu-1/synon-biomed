package fileops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"synon-go/internal/tools/fileevents"
)

func Write(root string, requestedPath string, content string, requestedEncoding string, overwrite bool) (WriteResult, error) {
	target, rel, err := resolveForWrite(root, requestedPath)
	if err != nil {
		return WriteResult{}, err
	}
	parent := filepath.Dir(target)
	if info, err := os.Stat(parent); err != nil {
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
	}
	overwrote := pathExists(target) && overwrite
	file, err := os.OpenFile(target, flags, 0o600)
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
	fileevents.NotifyChanged(target)
	return WriteResult{Path: filepath.ToSlash(rel), Bytes: len(data), Encoding: encoding, Overwrote: overwrote}, nil
}

func OriginalWrite(root string, requestedPath string, content string) (OriginalWriteResult, error) {
	target, rel, err := resolveForWrite(root, requestedPath)
	if err != nil {
		return OriginalWriteResult{}, err
	}
	parent := filepath.Dir(target)
	if info, err := os.Stat(parent); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return OriginalWriteResult{}, fmt.Errorf("parent directory does not exist: %s", filepath.ToSlash(filepath.Dir(rel)))
		}
		return OriginalWriteResult{}, err
	} else if !info.IsDir() {
		return OriginalWriteResult{}, fmt.Errorf("parent path is not a directory: %s", filepath.ToSlash(filepath.Dir(rel)))
	}
	var originalFile string
	writeType := "create"
	if existing, err := os.ReadFile(target); err == nil {
		originalFile = string(existing)
		writeType = "update"
	} else if !errors.Is(err, os.ErrNotExist) {
		return OriginalWriteResult{}, err
	}
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		return OriginalWriteResult{}, err
	}
	fileevents.NotifyChanged(target)
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
	target, rel, err := resolveForWrite(root, requestedPath)
	if err != nil {
		return OriginalEditResult{}, err
	}
	filePath := filepath.ToSlash(rel)
	originalRaw, err := os.ReadFile(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && oldString == "" {
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return OriginalEditResult{}, err
			}
			if err := os.WriteFile(target, []byte(newString), 0o600); err != nil {
				return OriginalEditResult{}, err
			}
			fileevents.NotifyChanged(target)
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
	if err := os.WriteFile(target, []byte(updated), 0o600); err != nil {
		return OriginalEditResult{}, err
	}
	fileevents.NotifyChanged(target)
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
	target, rel, err := resolveForWrite(root, requestedPath)
	if err != nil {
		return MkdirResult{}, err
	}
	_, statErr := os.Stat(target)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return MkdirResult{}, statErr
	}
	if recursive {
		err = os.MkdirAll(target, 0o700)
	} else {
		err = os.Mkdir(target, 0o700)
	}
	if err != nil {
		return MkdirResult{}, err
	}
	return MkdirResult{Path: filepath.ToSlash(rel), Type: "directory", Created: created}, nil
}

func Copy(root string, sourcePath string, targetPath string, overwrite bool, recursive bool) (TransferResult, error) {
	source, sourceRel, err := resolveExisting(root, sourcePath)
	if err != nil {
		return TransferResult{}, err
	}
	target, targetRel, err := resolveForWrite(root, targetPath)
	if err != nil {
		return TransferResult{}, err
	}
	if err := ensureNotSamePath(source, target); err != nil {
		return TransferResult{}, err
	}
	if err := ensureTargetNotRoot(targetRel); err != nil {
		return TransferResult{}, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return TransferResult{}, err
	}
	overwrote := pathExists(target)
	if info.IsDir() {
		if !recursive {
			return TransferResult{}, errors.New("file_copy directory requires recursive=true")
		}
		if err := ensureTargetNotInsideSource(source, target); err != nil {
			return TransferResult{}, err
		}
		if err := ensureTargetCanBeWritten(target, overwrite); err != nil {
			return TransferResult{}, err
		}
		if err := copyDirectory(source, target, overwrite); err != nil {
			return TransferResult{}, err
		}
	} else {
		if err := ensureTargetCanBeWritten(target, overwrite); err != nil {
			return TransferResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return TransferResult{}, err
		}
		if err := copyFile(source, target); err != nil {
			return TransferResult{}, err
		}
	}
	fileevents.NotifyChanged(target)
	return TransferResult{
		Path:       filepath.ToSlash(sourceRel),
		TargetPath: filepath.ToSlash(targetRel),
		Type:       fileType(info),
		Overwrote:  overwrote && overwrite,
	}, nil
}

func Move(root string, sourcePath string, targetPath string, overwrite bool) (TransferResult, error) {
	source, sourceRel, err := resolveExisting(root, sourcePath)
	if err != nil {
		return TransferResult{}, err
	}
	target, targetRel, err := resolveForWrite(root, targetPath)
	if err != nil {
		return TransferResult{}, err
	}
	if err := ensureNotSamePath(source, target); err != nil {
		return TransferResult{}, err
	}
	if err := ensureTargetNotRoot(targetRel); err != nil {
		return TransferResult{}, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return TransferResult{}, err
	}
	if info.IsDir() {
		if err := ensureTargetNotInsideSource(source, target); err != nil {
			return TransferResult{}, err
		}
	}
	overwrote := pathExists(target)
	if err := ensureTargetCanBeWritten(target, overwrite); err != nil {
		return TransferResult{}, err
	}
	if overwrite {
		if err := os.RemoveAll(target); err != nil {
			return TransferResult{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return TransferResult{}, err
	}
	if err := os.Rename(source, target); err != nil {
		return TransferResult{}, err
	}
	fileevents.NotifyChanged(source, target)
	return TransferResult{
		Path:       filepath.ToSlash(sourceRel),
		TargetPath: filepath.ToSlash(targetRel),
		Type:       fileType(info),
		Overwrote:  overwrote && overwrite,
	}, nil
}

func Delete(root string, requestedPath string, recursive bool) (DeleteResult, error) {
	target, rel, err := resolveExisting(root, requestedPath)
	if err != nil {
		return DeleteResult{}, err
	}
	if rel == "." {
		return DeleteResult{}, errors.New("file_delete refuses to delete file root")
	}
	info, err := os.Stat(target)
	if err != nil {
		return DeleteResult{}, err
	}
	if info.IsDir() {
		if !recursive {
			return DeleteResult{}, errors.New("file_delete directory requires recursive=true")
		}
		if err := os.RemoveAll(target); err != nil {
			return DeleteResult{}, err
		}
	} else if err := os.Remove(target); err != nil {
		return DeleteResult{}, err
	}
	fileevents.NotifyChanged(target)
	return DeleteResult{Path: filepath.ToSlash(rel), Type: fileType(info), Deleted: true}, nil
}
