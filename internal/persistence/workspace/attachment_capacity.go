package workspace

import (
	"context"
	"fmt"
	"time"

	"synon-go/internal/runtimecontrol"
)

const attachmentUploadReservationTTL = 24 * time.Hour

// InsufficientAttachmentSpaceError is a recoverable resource condition, not a
// file-format or fixed file-size rejection. Existing uploads remain resumable.
type InsufficientAttachmentSpaceError struct{ RequiredBytes, AvailableBytes uint64 }

func (e *InsufficientAttachmentSpaceError) Error() string {
	return fmt.Sprintf("attachment upload needs %d additional writable bytes; %d available after pending uploads and storage reserve", e.RequiredBytes, e.AvailableBytes)
}

// Account for remaining chunks plus the final assembled copy, since both
// coexist until the durable blob commit finishes. Caller holds attachmentMu.
func (s *Store) checkAttachmentUploadSpace(ctx context.Context, newSize int64) error {
	if newSize < 0 {
		return fmt.Errorf("attachment upload reservation must not be negative")
	}
	available := runtimecontrol.AvailableBytes(s.blobRoot)
	if available == nil {
		return nil
	} // Platforms without measurements still enforce OS write failures.
	remaining := *available
	reserve := func(bytes uint64) error {
		if bytes > remaining {
			return &InsufficientAttachmentSpaceError{RequiredBytes: bytes, AvailableBytes: remaining}
		}
		remaining -= bytes
		return nil
	}
	// Leave room for the database transaction/receipt and cancellation metadata.
	if err := reserve(64 << 20); err != nil {
		return err
	}
	reserved, err := s.pendingAttachmentUploadReservedBytes(ctx, s.now().UTC().Add(-attachmentUploadReservationTTL))
	if err != nil {
		return err
	}
	if err := reserve(reserved); err != nil {
		return err
	}
	if err = reserve(uint64(newSize)); err != nil {
		return err
	}
	return reserve(uint64(newSize))
}

func (s *Store) pendingAttachmentUploadReservedBytes(ctx context.Context, cutoff time.Time) (uint64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.total_size, COALESCE(SUM(c.size_bytes),0)
 FROM attachment_uploads u LEFT JOIN attachment_upload_chunks c ON c.upload_id=u.id
 WHERE u.updated_at >= ? GROUP BY u.id`, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("inspect pending attachment storage: %w", err)
	}
	defer rows.Close()
	var reserved uint64
	for rows.Next() {
		var total, received int64
		if err = rows.Scan(&total, &received); err != nil {
			return 0, err
		}
		if total < 0 || received < 0 || received > total {
			return 0, fmt.Errorf("pending attachment storage metadata is inconsistent")
		}
		remaining := uint64(total - received)
		assembled := uint64(total)
		if ^uint64(0)-reserved < remaining || ^uint64(0)-(reserved+remaining) < assembled {
			return 0, fmt.Errorf("pending attachment storage reservation overflow")
		}
		reserved += remaining + assembled
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	return reserved, nil
}
