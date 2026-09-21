package server

import (
	"errors"
	"strings"
)

var errKernelHostAccessFenced = errors.New("kernel host access is fenced until isolation can be re-established")

// kernelHostGrantFenceActive is a short-lived read of the revocation fence.
// Session startup and backend handoff happen outside the global mutation lock;
// callers revalidate this fence before any operation can be admitted.
func (s *Server) kernelHostGrantFenceActive(userID string) bool {
	if s == nil {
		return true
	}
	userID = strings.TrimSpace(userID)
	s.hostGrantKernelMu.Lock()
	fenced := s.hostGrantKernelFences[userID]
	s.hostGrantKernelMu.Unlock()
	return fenced
}

// withKernelHostGrantAdmission serializes the short durable-admission window
// with grant revocation. The revocation path holds the same lock only while it
// publishes the fence, then releases it before terminating physical workers;
// this keeps the lock bounded while ensuring no execution is accepted after a
// fence becomes visible.
func (s *Server) withKernelHostGrantAdmission(userID string, admit func() error) error {
	if s == nil || admit == nil {
		return errKernelHostAccessFenced
	}
	userID = strings.TrimSpace(userID)
	s.hostGrantKernelMu.Lock()
	defer s.hostGrantKernelMu.Unlock()
	if s.hostGrantKernelFences[userID] {
		return errKernelHostAccessFenced
	}
	return admit()
}
