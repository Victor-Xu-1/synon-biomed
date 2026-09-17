package workspace

import "encoding/json"

// Leave room for metadata and the realtime event envelope below its 256 KiB
// boundary. Full results remain in durable execution storage. Account for JSON
// escaping as well as UTF-8 bytes so quotes, controls and HTML cannot overflow.
const kernelNotificationPreviewBytes = 192 << 10

func kernelNotificationTransportPreview(output string) string {
	output = truncateKernelSettlementBytes(output, kernelNotificationPreviewBytes)
	encoded, _ := json.Marshal(output)
	if len(encoded) <= kernelNotificationPreviewBytes {
		return output
	}
	low, high := 0, len(output)
	for low < high {
		mid := low + (high-low+1)/2
		candidate := truncateKernelSettlementBytes(output, int64(mid))
		encoded, _ := json.Marshal(candidate)
		if len(encoded) <= kernelNotificationPreviewBytes {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return truncateKernelSettlementBytes(output, int64(low))
}
