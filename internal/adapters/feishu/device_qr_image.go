package feishu

import adapterqrimage "synon-go/internal/adapters/qrimage"

// EncodeDeviceVerificationURL returns an in-memory QR image suitable for the
// authenticated settings response. The URL is never sent to a QR SaaS.
func EncodeDeviceVerificationURL(verificationURL string, size int) (string, error) {
	return adapterqrimage.EncodeURL(verificationURL, size)
}
