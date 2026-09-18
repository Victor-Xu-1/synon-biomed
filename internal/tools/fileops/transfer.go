package fileops

import (
	"errors"
	"path/filepath"
)

func Copy(root string, sourcePath string, targetPath string, overwrite bool, recursive bool) (TransferResult, error) {
	fs, err := openMutationFS(root)
	if err != nil {
		return TransferResult{}, err
	}
	defer fs.Close()
	source, sourceRel, err := fs.resolve(sourcePath)
	if err != nil {
		return TransferResult{}, err
	}
	target, targetRel, err := fs.resolve(targetPath)
	if err != nil {
		return TransferResult{}, err
	}
	if err := ensureNotSamePath(source, target); err != nil {
		return TransferResult{}, err
	}
	if err := ensureTargetNotRoot(target); err != nil {
		return TransferResult{}, err
	}
	info, err := fs.Stat(source)
	if err != nil {
		return TransferResult{}, err
	}
	overwrote := fs.exists(target)
	if info.IsDir() {
		if !recursive {
			return TransferResult{}, errors.New("file_copy directory requires recursive=true")
		}
		if err := ensureTargetNotInsideSource(source, target); err != nil {
			return TransferResult{}, err
		}
		if err := ensureTargetNotInsideSource(target, source); err != nil {
			return TransferResult{}, err
		}
		if err := fs.ensureWritable(target, overwrite); err != nil {
			return TransferResult{}, err
		}
		if err := fs.copyDirectory(source, target, overwrite); err != nil {
			return TransferResult{}, err
		}
	} else {
		if err := fs.ensureWritable(target, overwrite); err != nil {
			return TransferResult{}, err
		}
		if err := fs.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return TransferResult{}, err
		}
		if err := fs.copyFile(source, target, overwrite); err != nil {
			return TransferResult{}, err
		}
	}
	fs.notify(target, targetRel)
	return TransferResult{
		Path:       filepath.ToSlash(sourceRel),
		TargetPath: filepath.ToSlash(targetRel),
		Type:       fileType(info),
		Overwrote:  overwrote && overwrite,
	}, nil
}

func Move(root string, sourcePath string, targetPath string, overwrite bool) (TransferResult, error) {
	fs, err := openMutationFS(root)
	if err != nil {
		return TransferResult{}, err
	}
	defer fs.Close()
	source, sourceRel, err := fs.resolve(sourcePath)
	if err != nil {
		return TransferResult{}, err
	}
	if source == "." {
		return TransferResult{}, errors.New("file_move refuses to move file root")
	}
	target, targetRel, err := fs.resolve(targetPath)
	if err != nil {
		return TransferResult{}, err
	}
	if err := ensureNotSamePath(source, target); err != nil {
		return TransferResult{}, err
	}
	if err := ensureTargetNotRoot(target); err != nil {
		return TransferResult{}, err
	}
	info, err := fs.Stat(source)
	if err != nil {
		return TransferResult{}, err
	}
	if info.IsDir() {
		if err := ensureTargetNotInsideSource(source, target); err != nil {
			return TransferResult{}, err
		}
		if err := ensureTargetNotInsideSource(target, source); err != nil {
			return TransferResult{}, err
		}
	}
	overwrote := fs.exists(target)
	if err := fs.ensureWritable(target, overwrite); err != nil {
		return TransferResult{}, err
	}
	if overwrite {
		if err := fs.RemoveAll(target); err != nil {
			return TransferResult{}, err
		}
	}
	if err := fs.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return TransferResult{}, err
	}
	if err := fs.Rename(source, target); err != nil {
		return TransferResult{}, err
	}
	fs.notify(source, sourceRel, target, targetRel)
	return TransferResult{
		Path:       filepath.ToSlash(sourceRel),
		TargetPath: filepath.ToSlash(targetRel),
		Type:       fileType(info),
		Overwrote:  overwrote && overwrite,
	}, nil
}

func Delete(root string, requestedPath string, recursive bool) (DeleteResult, error) {
	fs, err := openMutationFS(root)
	if err != nil {
		return DeleteResult{}, err
	}
	defer fs.Close()
	target, rel, err := fs.resolve(requestedPath)
	if err != nil {
		return DeleteResult{}, err
	}
	if target == "." {
		return DeleteResult{}, errors.New("file_delete refuses to delete file root")
	}
	info, err := fs.Stat(target)
	if err != nil {
		return DeleteResult{}, err
	}
	if info.IsDir() {
		if !recursive {
			return DeleteResult{}, errors.New("file_delete directory requires recursive=true")
		}
		if err := fs.RemoveAll(target); err != nil {
			return DeleteResult{}, err
		}
	} else if err := fs.Remove(target); err != nil {
		return DeleteResult{}, err
	}
	fs.notify(target, rel)
	return DeleteResult{Path: filepath.ToSlash(rel), Type: fileType(info), Deleted: true}, nil
}
