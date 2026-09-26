package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"synon-go/internal/httpreliability"
	"synon-go/internal/tools/securefetch"
)

// The logical transfer outlives individual connections. Only connections that
// fail to retain new validator-bound bytes consume the no-progress budget.
func (s *Server) continueAgentPublicScientificDownload(ctx context.Context, workspaceDir string, request agentPublicScientificFileRequest, stage *agentPublicScientificDownloadStage, offset *int64) error {
	highWater, stalled, attempts := *offset, 0, 0
	validator := stage.state.Validator
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if stage.state.ExpectedTotal > 0 && *offset == stage.state.ExpectedTotal {
			return nil
		}
		attempts++
		complete, err := s.transferAgentPublicScientificAttempt(ctx, workspaceDir, request, stage, offset)
		if err == nil && complete {
			return nil
		}
		sameRepresentation := validator == stage.state.Validator || (validator == "" && attempts == 1)
		progressed := sameRepresentation && stage.state.Validator != "" && *offset > highWater
		if validator != stage.state.Validator {
			// If-Range can return a new full representation. Its partial
			// prefix replaces, rather than extends, the previous version.
			// Only subsequent advancement of this same version is progress.
			validator, highWater = stage.state.Validator, *offset
		}
		if progressed {
			highWater, stalled = *offset, 0
		}
		if err == nil && progressed {
			// A valid server-selected range may end before the full representation.
			// Its EOF is a segment boundary, not a failed attempt.
			continue
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !isRetryableAgentPublicScientificDownloadError(err) {
			return err
		}
		if !progressed {
			stalled++
		}
		delay := max(httpreliability.Backoff(max(0, stalled-1)), securefetch.HTTPRetryAfter(err))
		stage.state.RetryNotBefore = time.Now().UTC().Add(delay)
		if persistErr := stage.persist(); persistErr != nil {
			return errAgentPublicScientificFileAuthority
		}
		if stalled >= agentPublicScientificDownloadMaxAttempts || delay > s.agentPublicScientificTransferPolicy(request).Timeout {
			return &agentPublicScientificTransferInterrupted{Cause: err, BytesRetained: *offset,
				Resumable: *offset > 0 && stage.state.Validator != "", Attempts: attempts, StalledAttempts: stalled,
				RetryNotBefore: stage.state.RetryNotBefore}
		}
		log.Printf("download_public_scientific_file reconnecting filename=%q host=%q connection=%d retained_bytes=%d stalled_attempts=%d reason=%s",
			request.Filename, request.SourceHost, attempts, *offset, stalled, agentPublicScientificTransferErrorCode(err))
		reportAgentPublicScientificDownloadProgress(ctx, "downloading_file", *offset, stage.state.ExpectedTotal, stage.transferRate())
		if err := httpreliability.Wait(ctx, delay); err != nil {
			return err
		}
	}
}

func (s *Server) transferAgentPublicScientificAttempt(ctx context.Context, workspaceDir string, request agentPublicScientificFileRequest, stage *agentPublicScientificDownloadStage, offset *int64) (bool, error) {
	maximumBytes := request.maximumBytes()
	stage.state.RetryNotBefore = time.Time{}
	if *offset > 0 && stage.state.Validator == "" {
		if err := stage.reset(request); err != nil {
			return false, errAgentPublicScientificFileAuthority
		}
		*offset = 0
	}
	if _, err := stage.file.Seek(*offset, io.SeekStart); err != nil {
		return false, errors.New("public scientific file download staging failed")
	}
	policy := s.agentPublicScientificTransferPolicy(request)
	if *offset > 0 {
		policy.RangeStart, policy.IfRange = *offset, stage.state.Validator
	}
	response, err := s.publicScientificFiles.Fetch(ctx, request.DownloadURL, policy)
	if err != nil {
		return false, err
	}
	if response == nil || response.Body == nil || response.FinalURL == nil || !agentPublicScientificResponseHostAllowed(request, response.FinalURL.Hostname()) {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return false, errAgentPublicScientificFileAuthority
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusPartialContent:
		if response.ContentRangeStart != *offset || response.ContentRangeEnd < *offset ||
			response.ContentRangeTotal <= response.ContentRangeEnd || response.ContentRangeTotal > maximumBytes ||
			stage.state.Validator == "" || securefetch.ResumeValidator(response) != stage.state.Validator ||
			(stage.state.ExpectedTotal > 0 && stage.state.ExpectedTotal != response.ContentRangeTotal) {
			return false, errAgentPublicScientificFileResume
		}
		stage.state.ExpectedTotal = response.ContentRangeTotal
	case http.StatusOK:
		if *offset > 0 {
			if err := stage.file.Truncate(0); err != nil {
				return false, errors.New("public scientific file download staging failed")
			}
			*offset = 0
			if _, err := stage.file.Seek(0, io.SeekStart); err != nil {
				return false, errors.New("public scientific file download staging failed")
			}
		}
		stage.state.ExpectedTotal = response.ContentLength
		stage.state.Validator = securefetch.ResumeValidator(response)
		// A complete 200 response replaces the old representation instead of
		// extending its bytes. Its validated MIME parameters replace them too.
		stage.state.ContentType = ""
	default:
		return false, errAgentPublicScientificFileResume
	}
	contentType := agentPublicScientificReportedContentType(response)
	if stage.state.ContentType == "" {
		stage.state.ContentType = contentType
	} else if contentType != "" && !agentPublicScientificResumeContentTypeMatches(stage.state.ContentType, contentType) {
		return false, errAgentPublicScientificFileResume
	} else if contentType != "" {
		stage.state.ContentType = contentType
	}
	if err := stage.persist(); err != nil {
		return false, errAgentPublicScientificFileAuthority
	}
	guard := newAgentDownloadDiskGuard(workspaceDir, s.fileRoot)
	if err := guard.check(*offset, max(1, stage.state.ExpectedTotal-*offset)); err != nil {
		return false, err
	}
	startingOffset := *offset
	remaining := maximumBytes - startingOffset
	if response.StatusCode == http.StatusPartialContent {
		remaining = response.ContentRangeEnd - startingOffset + 1
	}
	bodyClosed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = response.Body.Close()
		case <-bodyClosed:
		}
	}()
	progress := newAgentPublicScientificProgressReader(ctx, response.Body, startingOffset, stage.state.ExpectedTotal)
	started := time.Now()
	written, copyErr := io.Copy(&agentDownloadDiskWriter{ctx: ctx, writer: stage.file, guard: guard, written: startingOffset},
		io.LimitReader(&contextReader{ctx: ctx, reader: progress}, remaining+1))
	stage.recordTransfer(written, time.Since(started))
	progress.Complete()
	close(bodyClosed)
	closeErr := response.Body.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	*offset += written
	if *offset > maximumBytes || written > remaining || (stage.state.ExpectedTotal > 0 && *offset > stage.state.ExpectedTotal) {
		// A malformed extent must not poison the previously retained prefix.
		truncateErr := stage.file.Truncate(startingOffset)
		*offset = startingOffset
		return false, errors.Join(errAgentPublicScientificFileResume, truncateErr, stage.file.Sync())
	}
	if syncErr := stage.file.Sync(); syncErr != nil {
		return false, errors.New("public scientific file download could not preserve its checkpoint")
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if copyErr != nil {
		return false, copyErr
	}
	if *offset <= 0 {
		return false, errors.New("public scientific file download staging failed")
	}
	if stage.state.ExpectedTotal <= 0 || *offset == stage.state.ExpectedTotal {
		return true, nil
	}
	if response.StatusCode == http.StatusPartialContent && written == remaining {
		return false, nil
	}
	return false, io.ErrUnexpectedEOF
}

func (s *Server) agentPublicScientificTransferPolicy(request agentPublicScientificFileRequest) securefetch.Policy {
	headerTimeout, idleTimeout := s.publicScientificResponseHeaderTimeout, s.publicScientificTransferIdleTimeout
	if headerTimeout <= 0 {
		headerTimeout = httpreliability.DefaultHeaderTimeout
	}
	if idleTimeout <= 0 {
		idleTimeout = httpreliability.DefaultTransferIdleTimeout
	}
	return securefetch.Policy{
		AllowedHosts: agentPublicScientificAllowedHosts(request), AllowedPorts: []string{"443"},
		AllowPublicRedirects: request.AllowPublicRedirects,
		AcceptedMediaTypes:   request.AcceptedTypes, MaxRedirects: 3, AllowMissingContentType: true, IdentityEncoding: true,
		MaxBytes: request.maximumBytes(), Timeout: headerTimeout, LongLivedTransfer: true, TransferIdleTimeout: idleTimeout,
		UserAgent: "Synon-Biomed-scientific-data/1.0",
	}
}
