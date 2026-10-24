package conversation

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abhinavxd/libredesk/internal/conversation/models"
	"github.com/abhinavxd/libredesk/internal/countries"
	"github.com/abhinavxd/libredesk/internal/envelope"
	whatsappChannel "github.com/abhinavxd/libredesk/internal/inbox/channel/whatsapp"
	imodels "github.com/abhinavxd/libredesk/internal/inbox/models"
	"github.com/abhinavxd/libredesk/internal/stringutil"
	"github.com/jmoiron/sqlx"
)

// whatsAppMaxTextLength is WhatsApp's cap on a text message body.
const whatsAppMaxTextLength = 4096

const whatsAppMaxCaptionLength = 1024

// WhatsAppStatus values mirror the delivery lifecycle, kept in message.meta.
const (
	WhatsAppStatusSent      = "sent"
	WhatsAppStatusDelivered = "delivered"
	WhatsAppStatusRead      = "read"
	WhatsAppStatusFailed    = "failed"
)

// WhatsAppReadReceiptTarget returns the inbox ID, message ID, and recipient phone of the latest unseen inbound message.
func (m *Manager) WhatsAppReadReceiptTarget(uuid string, userID int, messageUUID string) (int, string, string, error) {
	var row struct {
		SourceID string `db:"source_id"`
		InboxID  int    `db:"inbox_id"`
		Phone    string `db:"phone"`
	}
	if err := m.q.GetWhatsAppReadReceiptTarget.Get(&row, uuid, userID, messageUUID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", "", nil
		}
		return 0, "", "", err
	}
	return row.InboxID, row.SourceID, row.Phone, nil
}

func (m *Manager) ApplyWhatsAppStatus(sourceID, metaStatus string, eventAt time.Time, errorMsg string) error {
	if sourceID == "" || metaStatus == "" {
		return nil
	}
	if eventAt.IsZero() {
		eventAt = time.Now().UTC()
	}
	ts := eventAt.Format(time.RFC3339)

	patch := map[string]any{
		"provider_status":            metaStatus,
		"provider_status_updated_at": ts,
	}
	switch metaStatus {
	case WhatsAppStatusSent:
		patch["provider_sent_at"] = ts
	case WhatsAppStatusDelivered:
		patch["provider_delivered_at"] = ts
	case WhatsAppStatusRead:
		patch["provider_read_at"] = ts
	case WhatsAppStatusFailed:
		patch["provider_failed_at"] = ts
		if errorMsg != "" {
			patch["provider_failure_reason"] = errorMsg
		}
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err
	}

	// The message_status enum collapses delivered/read into sent. The full lifecycle lives in meta.
	dbStatus := models.MessageStatusSent
	if metaStatus == WhatsAppStatusFailed {
		dbStatus = models.MessageStatusFailed
	}

	var row struct {
		UUID             string          `db:"uuid"`
		ConversationUUID string          `db:"conversation_uuid"`
		Status           string          `db:"status"`
		Meta             json.RawMessage `db:"meta"`
	}
	if err := m.q.ApplyWhatsAppMessageStatus.Get(&row, sourceID, dbStatus, patchBytes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("status update for source_id=%s status=%s: %w", sourceID, metaStatus, ErrMessageNotFound)
		}
		m.lo.Error("error applying whatsapp message status", "source_id", sourceID, "error", err)
		return err
	}
	m.BroadcastMessageUpdate(row.ConversationUUID, row.UUID, map[string]any{"status": row.Status, "meta": stripCSATUUID(row.Meta)})
	return nil
}

func (m *Manager) RecordWhatsAppSendFailure(messageUUID, errorMsg string) error {
	if messageUUID == "" || errorMsg == "" {
		return nil
	}
	patch := map[string]any{
		"provider_status":         WhatsAppStatusFailed,
		"provider_failed_at":      time.Now().UTC().Format(time.RFC3339),
		"provider_failure_reason": errorMsg,
	}
	return m.mergeWhatsAppMeta(m.q.MergeMessageMetaByUUID, messageUUID, patch)
}

// mergeWhatsAppMeta is a no-op on an unmatched key.
func (m *Manager) mergeWhatsAppMeta(stmt *sqlx.Stmt, key string, patch map[string]any) error {
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	var row struct {
		UUID             string          `db:"uuid"`
		ConversationUUID string          `db:"conversation_uuid"`
		Meta             json.RawMessage `db:"meta"`
	}
	if err := stmt.Get(&row, key, patchBytes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		m.lo.Error("error merging whatsapp meta", "key", key, "error", err)
		return err
	}
	m.BroadcastMessageUpdate(row.ConversationUUID, row.UUID, map[string]any{"meta": stripCSATUUID(row.Meta)})
	return nil
}

// sendWhatsAppCSAT sends the CSAT link as a normal text message.
func (m *Manager) sendWhatsAppCSAT(actorUserID int, conversation models.Conversation, csatUUID, csatURL string) error {
	meta := map[string]any{
		"is_csat":      true,
		"is_automated": true,
		"csat_uuid":    csatUUID,
	}

	content := m.i18n.Ts("conversation.whatsapp.csatMessage", "link", csatURL)
	inb, err := m.inboxStore.GetDBRecord(conversation.InboxID)
	if err == nil {
		var cfg whatsappChannel.Config
		if json.Unmarshal(inb.Config, &cfg) == nil && strings.TrimSpace(cfg.CSATMessage) != "" {
			content = strings.TrimSpace(cfg.CSATMessage) + "\n" + csatURL
		}
	}

	if _, err := m.QueueReply(nil, conversation.InboxID, actorUserID, conversation.ContactID, conversation.UUID, content, nil, nil, nil, "" /** sendFrom **/, meta); err != nil {
		m.lo.Error("error sending whatsapp CSAT link", "conversation_uuid", conversation.UUID, "error", err)
		return envelope.NewError(envelope.GeneralError, m.i18n.T("globals.messages.somethingWentWrong"), nil)
	}
	return nil
}

// prepareWhatsAppOutbound writes channel fields into metaMap and returns the free-form content unchanged.
func (m *Manager) prepareWhatsAppOutbound(inboxRecord imodels.Inbox, conversationUUID string, content string, hasAttachments bool, metaMap map[string]any) (string, error) {
	var conv struct {
		InboxID   int `db:"inbox_id"`
		ContactID int `db:"contact_id"`
	}
	if err := m.q.GetConversationInboxContact.Get(&conv, conversationUUID); err != nil {
		m.lo.Error("error fetching conversation inbox and contact", "conversation_uuid", conversationUUID, "error", err)
		return content, envelope.NewError(envelope.GeneralError, m.i18n.T("globals.messages.somethingWentWrong"), nil)
	}
	if conv.InboxID != inboxRecord.ID {
		return content, envelope.NewError(envelope.InputError, m.i18n.T("globals.messages.somethingWentWrong"), nil)
	}

	// The channel identity is the phone WAGO routes by. The phone columns are display data an agent may edit freely.
	toPhone, err := m.userStore.GetChannelIdentity(conv.ContactID, whatsappChannel.ChannelWhatsApp)
	if err != nil {
		return content, err
	}
	if toPhone == "" {
		contact, err := m.userStore.Get(conv.ContactID, "", nil)
		if err != nil {
			return content, err
		}
		if contact.PhoneNumber.String == "" {
			return content, envelope.NewError(envelope.InputError, m.i18n.T("conversation.whatsapp.error.contactNoPhone"), nil)
		}
		dialCode := countries.DialCodeForISO(contact.PhoneNumberCountryCode.String)
		if dialCode == "" {
			return content, envelope.NewError(envelope.InputError, m.i18n.T("conversation.whatsapp.error.contactCountryCodeInvalid"), nil)
		}
		var matchesCountry bool
		toPhone, matchesCountry = stringutil.WhatsAppPhoneForDialCode(contact.PhoneNumber.String, dialCode)
		if !matchesCountry {
			return content, envelope.NewError(envelope.InputError, m.i18n.T("conversation.whatsapp.error.contactCountryCodeInvalid"), nil)
		}
		if toPhone == "" {
			return content, envelope.NewError(envelope.InputError, m.i18n.T("conversation.whatsapp.error.contactNoPhone"), nil)
		}
		// Link the identity now so the contact's reply threads back to this contact instead of forking a duplicate.
		linkedID, err := m.userStore.LinkChannelIdentity(conv.ContactID, whatsappChannel.ChannelWhatsApp, toPhone)
		if err != nil {
			return content, err
		}
		if linkedID != conv.ContactID {
			return content, envelope.NewError(envelope.ConflictError, m.i18n.T("conversation.whatsapp.error.numberLinkedToAnotherContact"), nil)
		}
	}

	if err := m.validateWhatsAppContent(content, hasAttachments); err != nil {
		return content, err
	}

	send := whatsappChannel.SendMeta{ToPhone: toPhone}
	encoded, err := json.Marshal(send)
	if err != nil {
		return content, err
	}

	metaMap["whatsapp"] = json.RawMessage(encoded)
	return content, nil
}

func (m *Manager) validateWhatsAppContent(content string, hasAttachments bool) error {
	if strings.TrimSpace(content) == "" && !hasAttachments {
		return envelope.NewError(envelope.InputError, m.i18n.T("globals.messages.messageOrAttachmentRequired"), nil)
	}
	limit := whatsAppMaxTextLength
	if hasAttachments {
		limit = whatsAppMaxCaptionLength
	}
	if utf8.RuneCountInString(stringutil.HTML2WhatsApp(content)) > limit {
		return envelope.NewError(envelope.InputError, m.i18n.Ts("conversation.whatsapp.error.tooLong", "limit", strconv.Itoa(limit)), nil)
	}
	return nil
}

func stripCSATUUID(meta json.RawMessage) json.RawMessage {
	message := models.Message{Meta: meta}
	message.StripCSATUUID()
	return message.Meta
}
