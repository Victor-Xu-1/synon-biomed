package feishu

import adaptercommon "synon-go/internal/adapters/common"

type PendingUploadKind = adaptercommon.PendingUploadKind

const (
	PendingUploadImage PendingUploadKind = adaptercommon.PendingUploadImage
	PendingUploadFile  PendingUploadKind = adaptercommon.PendingUploadFile
)

type UploadSourceKind = adaptercommon.UploadSourceKind

const (
	UploadSourcePath   UploadSourceKind = adaptercommon.UploadSourcePath
	UploadSourceURL    UploadSourceKind = adaptercommon.UploadSourceURL
	UploadSourceBase64 UploadSourceKind = adaptercommon.UploadSourceBase64
)

type UploadSource = adaptercommon.UploadSource
type PendingUpload = adaptercommon.PendingUpload
type ImageBlockWatcher = adaptercommon.ImageBlockWatcher
type FileBlockWatcher = adaptercommon.FileBlockWatcher

func NewImageBlockWatcher() *ImageBlockWatcher {
	return adaptercommon.NewImageBlockWatcher()
}

func NewFileBlockWatcher() *FileBlockWatcher {
	return adaptercommon.NewFileBlockWatcher()
}

func isSafeOutboundLocalPath(path string) bool {
	return adaptercommon.IsSafeOutboundLocalPath(path)
}
