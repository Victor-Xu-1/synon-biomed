package server

import (
	"mime"
	"strings"

	"synon-go/internal/tools/securefetch"
)

func agentPublicScientificReportedContentType(response *securefetch.Response) string {
	if response.ReportedContentType != "" {
		return response.ReportedContentType
	}
	return response.ContentType
}

func agentPublicScientificResumeContentTypeMatches(previous, current string) bool {
	previousType, previousParameters, previousErr := mime.ParseMediaType(previous)
	currentType, currentParameters, currentErr := mime.ParseMediaType(current)
	if previousErr != nil || currentErr != nil || previousType != currentType {
		return false
	}
	// Older checkpoints retained only the base media type. Existing byte/ETag
	// resume validation remains authoritative; adopt parameters once observed.
	if len(previousParameters) == 0 {
		return true
	}
	previousParameters["charset"] = strings.ToLower(previousParameters["charset"])
	currentParameters["charset"] = strings.ToLower(currentParameters["charset"])
	return mime.FormatMediaType(previousType, previousParameters) == mime.FormatMediaType(currentType, currentParameters)
}
