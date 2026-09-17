package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type Notification struct {
	ID               string         `json:"id"`
	Sequence         int64          `json:"sequence"`
	SenderFrameID    string         `json:"sender_frame_id"`
	RecipientFrameID string         `json:"recipient_frame_id"`
	RootFrameID      string         `json:"root_frame_id"`
	NotificationType string         `json:"notification_type"`
	Payload          map[string]any `json:"payload"`
	ReadAt           *time.Time     `json:"read_at,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	ClaimToken       string         `json:"-"`
	ClaimExpiresAt   *time.Time     `json:"-"`
}

type CreateNotificationInput struct {
	ID               string
	SenderFrameID    string
	RecipientFrameID string
	RootFrameID      string
	OwnerUserID      string
	NotificationType string
	Payload          map[string]any
	allowCrossRoot   bool
}

type BackgroundKernelExecution struct {
	ExecID                 string    `json:"exec_id"`
	ToolID                 string    `json:"tool_id"`
	ToolName               string    `json:"tool_name"`
	FrameID                string    `json:"frame_id"`
	RootFrameID            string    `json:"root_frame_id"`
	FrameIncarnationID     string    `json:"frame_incarnation_id"`
	RootFrameIncarnationID string    `json:"root_frame_incarnation_id"`
	StartedAt              time.Time `json:"started_at"`
}

func (s *Store) CreateNotification(ctx context.Context, input CreateNotificationInput) (Notification, FrameEvent, error) {
	if s == nil || s.db == nil {
		return Notification{}, FrameEvent{}, errors.New("workspace store is closed")
	}
	var notification Notification
	var event FrameEvent
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		var err error
		notification, event, err = createNotificationTx(ctx, tx, input, s.now().UTC())
		return err
	})
	return notification, event, err
}

func createNotificationTx(
	ctx context.Context,
	tx workspaceTransaction,
	input CreateNotificationInput,
	now time.Time,
) (Notification, FrameEvent, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.SenderFrameID = strings.TrimSpace(input.SenderFrameID)
	input.RecipientFrameID = strings.TrimSpace(input.RecipientFrameID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.NotificationType = strings.TrimSpace(input.NotificationType)
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	if input.SenderFrameID == "" || input.RecipientFrameID == "" || input.RootFrameID == "" ||
		input.OwnerUserID == "" || input.NotificationType == "" {
		return Notification{}, FrameEvent{}, errors.New("notification sender, recipient, root, owner, and type are required")
	}
	payload := input.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return Notification{}, FrameEvent{}, errors.New("notification payload is invalid")
	}
	if err := validateNotificationAuthorityTx(ctx, tx, input.SenderFrameID, input.RecipientFrameID, input.RootFrameID, input.OwnerUserID, input.allowCrossRoot); err != nil {
		return Notification{}, FrameEvent{}, err
	}
	existing, found, err := notificationByIDTx(ctx, tx, input.ID)
	if err != nil {
		return Notification{}, FrameEvent{}, err
	}
	eventPayload := map[string]any{
		"notification_type": input.NotificationType,
		"sender_frame_id":   input.SenderFrameID,
		"payload":           payload,
	}
	eventID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("workspace-notification:"+input.ID)).String()
	if found {
		existingPayload, _ := json.Marshal(existing.Payload)
		if existing.SenderFrameID != input.SenderFrameID || existing.RecipientFrameID != input.RecipientFrameID ||
			existing.RootFrameID != input.RootFrameID || existing.NotificationType != input.NotificationType ||
			string(existingPayload) != string(rawPayload) {
			return Notification{}, FrameEvent{}, errors.New("notification id already identifies another notification")
		}
		event, err := appendKernelSupervisionFrameEvent(ctx, tx, FrameEventInput{
			ID: eventID, FrameID: input.RecipientFrameID, Type: "notification", Payload: eventPayload,
		}, existing.CreatedAt)
		return existing, event, err
	}
	notification := Notification{
		ID: input.ID, SenderFrameID: input.SenderFrameID, RecipientFrameID: input.RecipientFrameID,
		RootFrameID: input.RootFrameID, NotificationType: input.NotificationType,
		Payload: payload, CreatedAt: now.UTC(),
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM notifications`).Scan(&notification.Sequence); err != nil {
		return Notification{}, FrameEvent{}, errors.New("allocate notification sequence")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notifications(
		id,sequence,sender_frame_id,recipient_frame_id,root_frame_id,notification_type,payload_json,
		claim_token,claim_expires_at,read_at,created_at
	) VALUES(?,?,?,?,?,?,?,'',NULL,NULL,?)`, notification.ID, notification.Sequence, notification.SenderFrameID, notification.RecipientFrameID,
		notification.RootFrameID, notification.NotificationType, string(rawPayload), notification.CreatedAt); err != nil {
		return Notification{}, FrameEvent{}, fmt.Errorf("insert notification: %w", err)
	}
	event, err := appendKernelSupervisionFrameEvent(ctx, tx, FrameEventInput{
		ID: eventID, FrameID: input.RecipientFrameID, Type: "notification", Payload: eventPayload,
	}, notification.CreatedAt)
	if err != nil {
		return Notification{}, FrameEvent{}, err
	}
	return notification, event, nil
}

func validateNotificationAuthorityTx(ctx context.Context, tx workspaceTransaction, senderFrameID, recipientFrameID, rootFrameID, ownerUserID string, allowCrossRoot bool) error {
	for _, frameID := range []string{senderFrameID, recipientFrameID, rootFrameID} {
		var ownerID, actualRoot string
		if err := tx.QueryRowContext(ctx, `SELECT project.user_id,frame.root_frame_id
			FROM frames frame JOIN projects project ON project.id=frame.project_id WHERE frame.id=?`, frameID).Scan(&ownerID, &actualRoot); err != nil {
			return errors.New("notification frame authority is unavailable")
		}
		if ownerID != ownerUserID || actualRoot != rootFrameID && !(allowCrossRoot && frameID == senderFrameID) {
			return errors.New("notification frame authority is unavailable")
		}
	}
	if allowCrossRoot {
		mainFrameID, mainRootID, err := compatibilityAsideMainTargetTx(ctx, tx, senderFrameID, ownerUserID)
		if err != nil || mainFrameID != recipientFrameID || mainRootID != rootFrameID {
			return errors.New("notification frame authority is unavailable")
		}
	}
	return nil
}

func (s *Store) CreateCompatibilityAsideMainNotification(ctx context.Context, input CreateNotificationInput) (Notification, FrameEvent, error) {
	if s == nil || s.db == nil {
		return Notification{}, FrameEvent{}, errors.New("workspace store is closed")
	}
	var notification Notification
	var event FrameEvent
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		mainFrameID, rootFrameID, err := compatibilityAsideMainTargetTx(ctx, tx, strings.TrimSpace(input.SenderFrameID), strings.TrimSpace(input.OwnerUserID))
		if err != nil {
			return err
		}
		input.RecipientFrameID = mainFrameID
		input.RootFrameID = rootFrameID
		input.allowCrossRoot = true
		notification, event, err = createNotificationTx(ctx, tx, input, s.now().UTC())
		return err
	})
	return notification, event, err
}

func compatibilityAsideMainTargetTx(ctx context.Context, tx workspaceTransaction, senderFrameID, ownerUserID string) (string, string, error) {
	current := strings.TrimSpace(senderFrameID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if current == "" || ownerUserID == "" {
		return "", "", errors.New("compatibility aside authority is unavailable")
	}
	seen := map[string]bool{}
	projectID := ""
	for depth := 0; depth < 8; depth++ {
		if seen[current] {
			return "", "", errors.New("compatibility aside authority is unavailable")
		}
		seen[current] = true
		var frameProjectID, frameOwnerID, rootFrameID, rawInput string
		if err := tx.QueryRowContext(ctx, `SELECT frame.project_id,project.user_id,frame.root_frame_id,
			COALESCE(metadata.input_data,'{}') FROM frames frame
			JOIN projects project ON project.id=frame.project_id
			LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id=frame.id
			WHERE frame.id=?`, current).Scan(&frameProjectID, &frameOwnerID, &rootFrameID, &rawInput); err != nil {
			return "", "", errors.New("compatibility aside authority is unavailable")
		}
		if frameOwnerID != ownerUserID || projectID != "" && frameProjectID != projectID {
			return "", "", errors.New("compatibility aside authority is unavailable")
		}
		if projectID == "" {
			projectID = frameProjectID
		}
		inputData := map[string]any{}
		if err := json.Unmarshal([]byte(rawInput), &inputData); err != nil {
			return "", "", errors.New("compatibility aside authority is unavailable")
		}
		asideParent, _ := inputData["_aside_parent"].(map[string]any)
		next, _ := asideParent["root_frame_id"].(string)
		next = strings.TrimSpace(next)
		if next == "" {
			if current == senderFrameID {
				return "", "", errors.New("compatibility aside authority is unavailable")
			}
			return current, rootFrameID, nil
		}
		current = next
	}
	return "", "", errors.New("compatibility aside authority is unavailable")
}

func notificationByIDTx(ctx context.Context, tx workspaceTransaction, id string) (Notification, bool, error) {
	var notification Notification
	var rawPayload string
	var readAt sql.NullTime
	var claimExpiresAt sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT id,sequence,sender_frame_id,recipient_frame_id,root_frame_id,
		notification_type,payload_json,claim_token,claim_expires_at,read_at,created_at FROM notifications WHERE id=?`, id).Scan(
		&notification.ID, &notification.Sequence, &notification.SenderFrameID, &notification.RecipientFrameID, &notification.RootFrameID,
		&notification.NotificationType, &rawPayload, &notification.ClaimToken, &claimExpiresAt, &readAt, &notification.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Notification{}, false, nil
	}
	if err != nil {
		return Notification{}, false, err
	}
	if err := json.Unmarshal([]byte(rawPayload), &notification.Payload); err != nil {
		return Notification{}, false, errors.New("stored notification payload is invalid")
	}
	if readAt.Valid {
		value := readAt.Time.UTC()
		notification.ReadAt = &value
	}
	if claimExpiresAt.Valid {
		value := claimExpiresAt.Time.UTC()
		notification.ClaimExpiresAt = &value
	}
	return notification, true, nil
}

func (s *Store) ConsumeUnreadNotifications(ctx context.Context, recipientFrameID, rootFrameID, ownerUserID string, limit int) ([]Notification, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	recipientFrameID = strings.TrimSpace(recipientFrameID)
	rootFrameID = strings.TrimSpace(rootFrameID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if recipientFrameID == "" || rootFrameID == "" || ownerUserID == "" {
		return nil, errors.New("notification recipient, root, and owner are required")
	}
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("notification consume limit must be between 1 and 1000")
	}
	result := []Notification{}
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := validateNotificationAuthorityTx(ctx, tx, recipientFrameID, recipientFrameID, rootFrameID, ownerUserID, false); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,sequence,sender_frame_id,recipient_frame_id,root_frame_id,
			notification_type,payload_json,created_at FROM notifications
			WHERE recipient_frame_id=? AND root_frame_id=? AND read_at IS NULL
				AND notification_type!='child_landed'
			ORDER BY sequence LIMIT ?`, recipientFrameID, rootFrameID, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var notification Notification
			var rawPayload string
			if err := rows.Scan(&notification.ID, &notification.Sequence, &notification.SenderFrameID, &notification.RecipientFrameID,
				&notification.RootFrameID, &notification.NotificationType, &rawPayload, &notification.CreatedAt); err != nil {
				_ = rows.Close()
				return err
			}
			if err := json.Unmarshal([]byte(rawPayload), &notification.Payload); err != nil {
				_ = rows.Close()
				return errors.New("stored notification payload is invalid")
			}
			result = append(result, notification)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		now := s.now().UTC()
		for index := range result {
			updated, err := tx.ExecContext(ctx, `UPDATE notifications SET read_at=? WHERE id=? AND read_at IS NULL`, now, result[index].ID)
			if err != nil {
				return err
			}
			if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
				return errors.New("notification consumption lost its durable claim")
			}
			readAt := now
			result[index].ReadAt = &readAt
		}
		return nil
	})
	return result, err
}

const notificationClaimLease = 5 * time.Minute

func (s *Store) ClaimUnreadNotifications(ctx context.Context, recipientFrameID, rootFrameID, ownerUserID, claimToken string, limit int) ([]Notification, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	recipientFrameID = strings.TrimSpace(recipientFrameID)
	rootFrameID = strings.TrimSpace(rootFrameID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	claimToken = strings.TrimSpace(claimToken)
	if recipientFrameID == "" || rootFrameID == "" || ownerUserID == "" || claimToken == "" {
		return nil, errors.New("notification recipient, root, owner, and claim token are required")
	}
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("notification claim limit must be between 1 and 1000")
	}
	result := []Notification{}
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := validateNotificationAuthorityTx(ctx, tx, recipientFrameID, recipientFrameID, rootFrameID, ownerUserID, false); err != nil {
			return err
		}
		now := s.now().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE notifications SET claim_token='',claim_expires_at=NULL
			WHERE recipient_frame_id=? AND root_frame_id=? AND read_at IS NULL
				AND notification_type!='child_landed'
				AND claim_token!='' AND (claim_expires_at IS NULL OR claim_expires_at<=?)`,
			recipientFrameID, rootFrameID, now); err != nil {
			return err
		}
		var activeToken string
		err := tx.QueryRowContext(ctx, `SELECT claim_token FROM notifications
			WHERE recipient_frame_id=? AND root_frame_id=? AND read_at IS NULL
				AND notification_type!='child_landed'
				AND claim_token!='' AND claim_expires_at>?
			ORDER BY sequence LIMIT 1`, recipientFrameID, rootFrameID, now).Scan(&activeToken)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if activeToken != "" && activeToken != claimToken {
			return nil
		}
		query := `SELECT id,sequence,sender_frame_id,recipient_frame_id,root_frame_id,
			notification_type,payload_json,created_at FROM notifications
			WHERE recipient_frame_id=? AND root_frame_id=? AND read_at IS NULL
				AND notification_type!='child_landed'`
		args := []any{recipientFrameID, rootFrameID}
		if activeToken == claimToken {
			query += ` AND claim_token=? AND claim_expires_at>? ORDER BY sequence`
			args = append(args, claimToken, now)
		} else {
			query += ` AND claim_token='' ORDER BY sequence LIMIT ?`
			args = append(args, limit)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var notification Notification
			var rawPayload string
			if err := rows.Scan(&notification.ID, &notification.Sequence, &notification.SenderFrameID,
				&notification.RecipientFrameID, &notification.RootFrameID, &notification.NotificationType,
				&rawPayload, &notification.CreatedAt); err != nil {
				_ = rows.Close()
				return err
			}
			if err := json.Unmarshal([]byte(rawPayload), &notification.Payload); err != nil {
				_ = rows.Close()
				return errors.New("stored notification payload is invalid")
			}
			result = append(result, notification)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		expiresAt := now.Add(notificationClaimLease)
		for index := range result {
			updated, err := tx.ExecContext(ctx, `UPDATE notifications
				SET claim_token=?,claim_expires_at=?
				WHERE id=? AND read_at IS NULL
					AND (claim_token='' OR claim_token=? OR claim_expires_at IS NULL OR claim_expires_at<=?)`,
				claimToken, expiresAt, result[index].ID, claimToken, now)
			if err != nil {
				return err
			}
			if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
				return errors.New("notification claim lost its durable authority")
			}
			result[index].ClaimToken = claimToken
			value := expiresAt
			result[index].ClaimExpiresAt = &value
		}
		return nil
	})
	return result, err
}

func (s *Store) AckClaimedNotifications(ctx context.Context, recipientFrameID, rootFrameID, ownerUserID, claimToken string, expected int) (int64, error) {
	acknowledged, _, err := s.AckClaimedNotificationsDetailed(ctx, recipientFrameID, rootFrameID, ownerUserID, claimToken, expected)
	return acknowledged, err
}

func (s *Store) AckClaimedNotificationsDetailed(ctx context.Context, recipientFrameID, rootFrameID, ownerUserID, claimToken string, expected int) (int64, []string, error) {
	if s == nil || s.db == nil {
		return 0, nil, errors.New("workspace store is closed")
	}
	claimToken = strings.TrimSpace(claimToken)
	if claimToken == "" || expected < 0 || expected > 1000 {
		return 0, nil, errors.New("notification claim token and expected count are required")
	}
	var acknowledged int64
	peerNotificationIDs := []string{}
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := validateNotificationAuthorityTx(ctx, tx, recipientFrameID, recipientFrameID, rootFrameID, ownerUserID, false); err != nil {
			return err
		}
		var claimed int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
			WHERE recipient_frame_id=? AND root_frame_id=? AND read_at IS NULL AND claim_token=?`,
			strings.TrimSpace(recipientFrameID), strings.TrimSpace(rootFrameID), claimToken).Scan(&claimed); err != nil {
			return err
		}
		if claimed != expected {
			return errors.New("notification claim manifest changed before acknowledgement")
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM notifications
			WHERE recipient_frame_id=? AND root_frame_id=? AND read_at IS NULL AND claim_token=?
				AND notification_type='child_message' ORDER BY sequence`, strings.TrimSpace(recipientFrameID), strings.TrimSpace(rootFrameID), claimToken)
		if err != nil {
			return err
		}
		for rows.Next() {
			var notificationID string
			if err := rows.Scan(&notificationID); err != nil {
				_ = rows.Close()
				return err
			}
			peerNotificationIDs = append(peerNotificationIDs, notificationID)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if expected == 0 {
			return nil
		}
		updated, err := tx.ExecContext(ctx, `UPDATE notifications
			SET read_at=?,claim_token='',claim_expires_at=NULL
			WHERE recipient_frame_id=? AND root_frame_id=? AND read_at IS NULL AND claim_token=?`,
			s.now().UTC(), strings.TrimSpace(recipientFrameID), strings.TrimSpace(rootFrameID), claimToken)
		if err != nil {
			return err
		}
		acknowledged, err = updated.RowsAffected()
		if err != nil {
			return err
		}
		if acknowledged != int64(expected) {
			return errors.New("notification acknowledgement lost its durable claim")
		}
		return nil
	})
	return acknowledged, peerNotificationIDs, err
}

func (s *Store) CountUnreadNotifications(ctx context.Context, recipientFrameID, rootFrameID, ownerUserID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications notification
		JOIN frames frame ON frame.id=notification.recipient_frame_id
		JOIN projects project ON project.id=frame.project_id
		WHERE notification.recipient_frame_id=? AND notification.root_frame_id=? AND project.user_id=?
			AND frame.root_frame_id=? AND notification.read_at IS NULL
			AND notification.notification_type!='child_landed'`, strings.TrimSpace(recipientFrameID),
		strings.TrimSpace(rootFrameID), strings.TrimSpace(ownerUserID), strings.TrimSpace(rootFrameID)).Scan(&count)
	return count, err
}

func (s *Store) ListKernelChildLandings(ctx context.Context, recipientFrameID, rootFrameID, ownerUserID string, limit int) ([]Notification, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if limit <= 0 || limit > 100 {
		return nil, errors.New("kernel child landing limit must be between 1 and 100")
	}
	if _, found, err := s.GetKernelFrameAccessContext(ctx, strings.TrimSpace(recipientFrameID)); err != nil || !found {
		return nil, errors.New("kernel child landing authority is unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT notification.id,notification.sequence,notification.sender_frame_id,
		notification.recipient_frame_id,notification.root_frame_id,notification.notification_type,
		notification.payload_json,notification.created_at
		FROM notifications notification
		JOIN frames frame ON frame.id=notification.recipient_frame_id
		JOIN projects project ON project.id=frame.project_id
		WHERE notification.recipient_frame_id=? AND notification.root_frame_id=?
			AND notification.notification_type='child_landed' AND project.user_id=?
			AND frame.root_frame_id=?
		ORDER BY notification.created_at,notification.sequence LIMIT ?`,
		strings.TrimSpace(recipientFrameID), strings.TrimSpace(rootFrameID), strings.TrimSpace(ownerUserID),
		strings.TrimSpace(rootFrameID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Notification{}
	for rows.Next() {
		var notification Notification
		var rawPayload string
		if err := rows.Scan(&notification.ID, &notification.Sequence, &notification.SenderFrameID,
			&notification.RecipientFrameID, &notification.RootFrameID, &notification.NotificationType,
			&rawPayload, &notification.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rawPayload), &notification.Payload); err != nil {
			return nil, errors.New("stored kernel child landing payload is invalid")
		}
		result = append(result, notification)
	}
	return result, rows.Err()
}

func (s *Store) CountUndeliveredKernelChildLandings(ctx context.Context, recipientFrameID, rootFrameID, ownerUserID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return 0, err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_child_supervision child
		LEFT JOIN kernel_child_message_clock clock ON clock.frame_id=child.frame_id
		WHERE child.parent_frame_id=? AND child.root_frame_id=? AND child.owner_user_id=?
			AND child.status IN ('completed','failed','cancelled')
			AND NOT EXISTS (SELECT 1 FROM notifications notification
				WHERE notification.sender_frame_id=child.frame_id
					AND notification.recipient_frame_id=child.parent_frame_id
					AND notification.notification_type='child_landed')
			AND NOT EXISTS (SELECT 1 FROM frame_events collected
				WHERE collected.frame_id=child.parent_frame_id
					AND collected.event_type='kernel_child_landing_collected'
					AND json_extract(collected.payload,'$.child_frame_id')=child.frame_id
					AND json_extract(collected.payload,'$.generation')=COALESCE(clock.consumed_generation,0))`,
		strings.TrimSpace(recipientFrameID), strings.TrimSpace(rootFrameID), strings.TrimSpace(ownerUserID)).Scan(&count)
	return count, err
}

func (s *Store) CollectKernelChildLanding(ctx context.Context, recipientFrameID, rootFrameID, ownerUserID, childFrameID string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	return transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := validateNotificationAuthorityTx(ctx, tx, recipientFrameID, recipientFrameID, rootFrameID, ownerUserID, false); err != nil {
			return err
		}
		var status string
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT child.status,COALESCE(clock.consumed_generation,0)
			FROM kernel_child_supervision child
			LEFT JOIN kernel_child_message_clock clock ON clock.frame_id=child.frame_id
			WHERE child.frame_id=? AND child.parent_frame_id=? AND child.root_frame_id=? AND child.owner_user_id=?`,
			strings.TrimSpace(childFrameID), strings.TrimSpace(recipientFrameID), strings.TrimSpace(rootFrameID),
			strings.TrimSpace(ownerUserID)).Scan(&status, &generation); err != nil {
			return errors.New("kernel child landing is unavailable")
		}
		if !isKernelChildTerminalStatus(status) {
			return errors.New("kernel child landing is not terminal")
		}
		eventID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("kernel-child-landing-collected:%s:%s:%d", recipientFrameID, childFrameID, generation))).String()
		if _, err := appendKernelSupervisionFrameEvent(ctx, tx, FrameEventInput{
			ID: eventID, FrameID: recipientFrameID, Type: "kernel_child_landing_collected",
			Payload: map[string]any{"child_frame_id": strings.TrimSpace(childFrameID), "generation": generation},
		}, s.now().UTC()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM notifications
			WHERE sender_frame_id=? AND recipient_frame_id=? AND root_frame_id=? AND notification_type='child_landed'
				AND COALESCE(json_extract(payload_json,'$.generation'),0)=?`,
			strings.TrimSpace(childFrameID), strings.TrimSpace(recipientFrameID), strings.TrimSpace(rootFrameID), generation)
		return err
	})
}

func (s *Store) RecordBackgroundKernelExecutionStarted(ctx context.Context, access KernelFrameAccess, execution BackgroundKernelExecution) (FrameEvent, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, errors.New("workspace store is closed")
	}
	var event FrameEvent
	if strings.TrimSpace(execution.ExecID) == "" || strings.TrimSpace(execution.ToolID) == "" ||
		execution.FrameID != access.Frame.ID || execution.RootFrameID != access.Frame.RootFrameID || execution.StartedAt.IsZero() {
		return FrameEvent{}, errors.New("background kernel execution identity is invalid")
	}
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := validateNotificationAuthorityTx(ctx, tx, access.Frame.ID, access.Frame.ID, access.Frame.RootFrameID, access.UserID, false); err != nil {
			return err
		}
		if access.Frame.IncarnationID != execution.FrameIncarnationID || access.RootFrameIncarnationID != execution.RootFrameIncarnationID {
			return errors.New("background kernel execution authority changed")
		}
		payload := map[string]any{
			"exec_id": execution.ExecID, "tool_id": execution.ToolID, "tool_name": execution.ToolName,
			"frame_incarnation_id":      execution.FrameIncarnationID,
			"root_frame_incarnation_id": execution.RootFrameIncarnationID,
			"started_at":                execution.StartedAt.UTC().Format(time.RFC3339Nano),
		}
		eventID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-background-start:"+execution.FrameID+":"+execution.ExecID)).String()
		if existing, _, found, err := frameEventByID(ctx, tx, eventID); err != nil {
			return err
		} else if found {
			// Recovery reconstructs StartedAt from the executor receipt, whose
			// timestamp can differ by a few milliseconds from the original
			// controller observation. The execution identity is the idempotency
			// authority; accept that harmless clock-source difference while still
			// rejecting any conflicting frame, tool, or incarnation identity.
			if existing.FrameID != execution.FrameID || existing.Type != "kernel_execution_background_started" ||
				stringValue(existing.Payload["exec_id"]) != execution.ExecID ||
				stringValue(existing.Payload["tool_id"]) != execution.ToolID ||
				stringValue(existing.Payload["tool_name"]) != execution.ToolName ||
				stringValue(existing.Payload["frame_incarnation_id"]) != execution.FrameIncarnationID ||
				stringValue(existing.Payload["root_frame_incarnation_id"]) != execution.RootFrameIncarnationID {
				return fmt.Errorf("background kernel execution %q already identifies a different start event", execution.ExecID)
			}
			event = existing
			return nil
		}
		var err error
		event, err = appendKernelSupervisionFrameEvent(ctx, tx, FrameEventInput{
			ID:      eventID,
			FrameID: execution.FrameID, Type: "kernel_execution_background_started", Payload: payload,
		}, execution.StartedAt.UTC())
		return err
	})
	return event, err
}

func (s *Store) ListPendingBackgroundKernelExecutions(ctx context.Context, access KernelFrameAccess) ([]BackgroundKernelExecution, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		json_extract(start.payload,'$.exec_id'),json_extract(start.payload,'$.tool_id'),
		json_extract(start.payload,'$.tool_name'),start.frame_id,frame.root_frame_id,
		json_extract(start.payload,'$.frame_incarnation_id'),
		json_extract(start.payload,'$.root_frame_incarnation_id'),start.created_at
		FROM frame_events start
		JOIN frames frame ON frame.id=start.frame_id
		JOIN projects project ON project.id=frame.project_id
		WHERE start.frame_id=? AND start.event_type='kernel_execution_background_started'
			AND project.user_id=? AND frame.root_frame_id=?
			AND json_extract(start.payload,'$.frame_incarnation_id')=?
			AND json_extract(start.payload,'$.root_frame_incarnation_id')=?
			AND NOT EXISTS(
				SELECT 1 FROM notifications notification
				WHERE notification.recipient_frame_id=start.frame_id
					AND notification.notification_type='cell_result'
					AND json_extract(notification.payload_json,'$.exec_id')=json_extract(start.payload,'$.exec_id')
			)
			AND NOT EXISTS(
				SELECT 1 FROM frame_events terminal
				WHERE terminal.frame_id=start.frame_id AND terminal.event_type='kernel_execution_background_lost'
					AND json_extract(terminal.payload,'$.exec_id')=json_extract(start.payload,'$.exec_id')
			)
			AND NOT EXISTS(
				SELECT 1 FROM workspace_outbox settlement
				WHERE settlement.topic=? AND settlement.aggregate_type='kernel_execution'
					AND settlement.aggregate_id=json_extract(start.payload,'$.exec_id')
					AND settlement.status IN ('pending','inflight','dead_letter')
			)
		ORDER BY start.sequence`, access.Frame.ID, access.UserID, access.Frame.RootFrameID,
		access.Frame.IncarnationID, access.RootFrameIncarnationID, KernelResultSettlementOutboxTopic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []BackgroundKernelExecution{}
	for rows.Next() {
		var execution BackgroundKernelExecution
		if err := rows.Scan(&execution.ExecID, &execution.ToolID, &execution.ToolName, &execution.FrameID,
			&execution.RootFrameID, &execution.FrameIncarnationID, &execution.RootFrameIncarnationID, &execution.StartedAt); err != nil {
			return nil, err
		}
		result = append(result, execution)
	}
	return result, rows.Err()
}

func (s *Store) CountPendingBackgroundKernelSettlements(ctx context.Context, access KernelFrameAccess) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_outbox settlement
		JOIN frame_events start ON start.frame_id=? AND start.event_type='kernel_execution_background_started'
			AND json_extract(start.payload,'$.exec_id')=settlement.aggregate_id
		JOIN frames frame ON frame.id=start.frame_id
		JOIN projects project ON project.id=frame.project_id
		WHERE settlement.topic=? AND settlement.aggregate_type='kernel_execution'
			AND settlement.status IN ('pending','inflight','dead_letter')
			AND project.user_id=? AND frame.root_frame_id=?
			AND json_extract(start.payload,'$.frame_incarnation_id')=?
			AND json_extract(start.payload,'$.root_frame_incarnation_id')=?`,
		access.Frame.ID, KernelResultSettlementOutboxTopic, access.UserID, access.Frame.RootFrameID,
		access.Frame.IncarnationID, access.RootFrameIncarnationID).Scan(&count)
	return count, err
}

func (s *Store) MarkBackgroundKernelExecutionLostIfPending(ctx context.Context, access KernelFrameAccess, execution BackgroundKernelExecution) (FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, false, errors.New("workspace store is closed")
	}
	var event FrameEvent
	if execution.FrameID != access.Frame.ID || execution.RootFrameID != access.Frame.RootFrameID ||
		execution.FrameIncarnationID != access.Frame.IncarnationID || execution.RootFrameIncarnationID != access.RootFrameIncarnationID {
		return FrameEvent{}, false, errors.New("background kernel execution authority changed")
	}
	marked := false
	now := s.now().UTC()
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := validateNotificationAuthorityTx(ctx, tx, access.Frame.ID, access.Frame.ID, access.Frame.RootFrameID, access.UserID, false); err != nil {
			return err
		}
		var operationCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_local_operations
			WHERE frame_id=? AND execution_id=?`, execution.FrameID, execution.ExecID).Scan(&operationCount); err != nil {
			return err
		}
		if operationCount > 0 {
			return nil
		}
		var settlementCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_outbox
			WHERE topic=? AND aggregate_type='kernel_execution' AND aggregate_id=?
				AND status IN ('pending','inflight','dead_letter')`,
			KernelResultSettlementOutboxTopic, execution.ExecID).Scan(&settlementCount); err != nil {
			return err
		}
		if settlementCount > 0 {
			return nil
		}
		startID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-background-start:"+execution.FrameID+":"+execution.ExecID)).String()
		var startCount, resultCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM frame_events WHERE id=? AND frame_id=? AND event_type='kernel_execution_background_started'`, startID, execution.FrameID).Scan(&startCount); err != nil || startCount != 1 {
			return errors.New("background kernel execution start is unavailable")
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
			WHERE recipient_frame_id=? AND notification_type='cell_result'
				AND json_extract(payload_json,'$.exec_id')=?`, execution.FrameID, execution.ExecID).Scan(&resultCount); err != nil {
			return err
		}
		if resultCount > 0 {
			return nil
		}
		lostID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-background-lost:"+execution.FrameID+":"+execution.ExecID)).String()
		if _, _, _, err := frameEventByID(ctx, tx, lostID); err != nil {
			return err
		}
		if _, err := appendKernelSupervisionFrameEvent(ctx, tx, FrameEventInput{
			ID:      lostID,
			FrameID: execution.FrameID, Type: "kernel_execution_background_lost",
			Payload: map[string]any{
				"exec_id": execution.ExecID, "tool_id": execution.ToolID, "status": "interrupted",
				"frame_incarnation_id":      execution.FrameIncarnationID,
				"root_frame_incarnation_id": execution.RootFrameIncarnationID,
				"reason":                    "runtime_restart",
			},
		}, now); err != nil {
			return err
		}
		toolName := strings.TrimSpace(execution.ToolName)
		if toolName == "" {
			toolName = "kernel"
		}
		_, notificationEvent, err := createNotificationTx(ctx, tx, CreateNotificationInput{
			ID:            uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-background-lost-notification:"+execution.FrameID+":"+execution.ExecID)).String(),
			SenderFrameID: execution.FrameID, RecipientFrameID: execution.FrameID,
			RootFrameID: execution.RootFrameID, OwnerUserID: access.UserID, NotificationType: "cell_result",
			Payload: map[string]any{
				"exec_id": execution.ExecID, "tool_id": execution.ToolID, "status": "interrupted",
				"output": fmt.Sprintf("[CANCELLED] This %s cell was running when the session restarted; kernel state was lost.", toolName),
			},
		}, now)
		if err != nil {
			return err
		}
		event, marked = notificationEvent, true
		return nil
	})
	return event, marked, err
}

func (s *Store) CompleteBackgroundKernelExecution(
	ctx context.Context,
	logInput SaveExecutionLogInput,
	notificationInput CreateNotificationInput,
) (ExecutionLogRecord, Notification, FrameEvent, error) {
	if s == nil || s.db == nil {
		return ExecutionLogRecord{}, Notification{}, FrameEvent{}, errors.New("workspace store is closed")
	}
	execID := strings.TrimSpace(stringValue(notificationInput.Payload["exec_id"]))
	if logInput.Record.FrameID != strings.TrimSpace(notificationInput.SenderFrameID) ||
		logInput.Record.FrameID != strings.TrimSpace(notificationInput.RecipientFrameID) ||
		execID == "" || execID != strings.TrimSpace(logInput.Record.ID) || notificationInput.NotificationType != "cell_result" {
		return ExecutionLogRecord{}, Notification{}, FrameEvent{}, errors.New("background completion authority is invalid")
	}
	prepared, err := s.prepareExecutionLog(logInput)
	if err != nil {
		return ExecutionLogRecord{}, Notification{}, FrameEvent{}, err
	}
	var record ExecutionLogRecord
	var notification Notification
	var event FrameEvent
	err = transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		startID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-background-start:"+logInput.Record.FrameID+":"+execID)).String()
		var startCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM frame_events
			WHERE id=? AND frame_id=? AND event_type='kernel_execution_background_started'`,
			startID, logInput.Record.FrameID).Scan(&startCount); err != nil {
			return err
		}
		if startCount != 1 {
			return errors.New("background kernel execution start is unavailable")
		}
		lostID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-background-lost:"+logInput.Record.FrameID+":"+execID)).String()
		var lostCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM frame_events
			WHERE id=? AND frame_id=? AND event_type='kernel_execution_background_lost'`,
			lostID, logInput.Record.FrameID).Scan(&lostCount); err != nil {
			return err
		}
		if lostCount != 0 {
			return errors.New("background kernel execution is already terminal")
		}
		var txErr error
		record, txErr = executionLogByIDTx(ctx, tx, prepared.record.ID)
		if errors.Is(txErr, sql.ErrNoRows) {
			record, txErr = saveExecutionLogTx(ctx, tx, prepared)
		} else if txErr == nil && !executionLogEquivalent(prepared.record, record) {
			return errors.New("execution log id already identifies another execution")
		}
		if txErr != nil {
			return txErr
		}
		notification, event, txErr = createNotificationTx(ctx, tx, notificationInput, s.now().UTC())
		return txErr
	})
	if err != nil {
		return ExecutionLogRecord{}, Notification{}, FrameEvent{}, err
	}
	return record, notification, event, nil
}

func executionLogByIDTx(ctx context.Context, tx workspaceTransaction, id string) (ExecutionLogRecord, error) {
	return scanExecutionLog(tx.QueryRowContext(ctx, executionLogSelect+` WHERE log.id=?`, strings.TrimSpace(id)))
}

func executionLogEquivalent(expected, existing ExecutionLogRecord) bool {
	if expected.ID != existing.ID || expected.FrameID != existing.FrameID || expected.CellIndex != existing.CellIndex ||
		expected.KernelID != existing.KernelID || expected.KernelKind != existing.KernelKind || expected.CondaEnv != existing.CondaEnv ||
		expected.Language != existing.Language || expected.Source != existing.Source || expected.Stdout != existing.Stdout ||
		expected.Stderr != existing.Stderr || expected.ExitStatus != existing.ExitStatus || expected.Origin != existing.Origin ||
		!expected.ExecutedAt.Equal(existing.ExecutedAt) || !equalOptionalInt(expected.ErrorLine, existing.ErrorLine) {
		return false
	}
	return canonicalJSONEqual(expected.FilesWritten, existing.FilesWritten) &&
		canonicalJSONEqual(expected.FilesRead, existing.FilesRead) && canonicalJSONEqual(expected.Detection, existing.Detection)
}

func equalOptionalInt(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func canonicalJSONEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
