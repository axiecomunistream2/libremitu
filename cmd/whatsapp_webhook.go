package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abhinavxd/libredesk/internal/attachment"
	"github.com/abhinavxd/libredesk/internal/conversation"
	cmodels "github.com/abhinavxd/libredesk/internal/conversation/models"
	"github.com/abhinavxd/libredesk/internal/envelope"
	whatsappChannel "github.com/abhinavxd/libredesk/internal/inbox/channel/whatsapp"
	imodels "github.com/abhinavxd/libredesk/internal/inbox/models"
	umodels "github.com/abhinavxd/libredesk/internal/user/models"
	"github.com/abhinavxd/libredesk/internal/wago"
	"github.com/valyala/fasthttp"
	"github.com/volatiletech/null/v9"
	"github.com/zerodha/fastglue"
)

const (
	whatsAppDefaultContactName = "Contact"
	// Retry window for a status whose message row is missing. Older events reference an ID that will never exist locally.
	whatsAppStatusNotFoundGrace = 30 * time.Minute

	whatsAppMediaAttemptTimeout = 3 * time.Minute
	whatsAppMediaMaxAttempts    = 3
	whatsAppMediaRetryDelay     = 2 * time.Second
)

var (
	errNoEnabledWhatsAppInbox = errors.New("no enabled whatsapp inbox for event")

	// whatsAppConversationLocks serializes the open-conversation lookup + create per contact and inbox, across webhook ingest and agent-initiated creates.
	whatsAppConversationLocks = &keyedLock{entries: make(map[string]*keyedLockEntry)}
)

func lockWhatsAppConversation(contactID, inboxID int) func() {
	return whatsAppConversationLocks.lock(strconv.Itoa(contactID) + ":" + strconv.Itoa(inboxID))
}

func handleWhatsAppWebhookEvent(r *fastglue.Request) error {
	app := r.Context.(*App)

	inboxID, err := inboxIDFromPath(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "invalid inbox id", nil, envelope.InputError)
	}

	body := append([]byte(nil), r.RequestCtx.PostBody()...)

	if app.wagoClient == nil || !app.wagoClient.Configured() {
		app.lo.Error("whatsapp webhook rejected: wago is not configured")
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "wago is not configured", nil, envelope.GeneralError)
	}
	secret := strings.TrimSpace(ko.String("wago.webhook_secret"))
	if secret == "" {
		app.lo.Error("whatsapp webhook rejected: wago webhook secret not configured")
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "wago webhook secret not configured", nil, envelope.GeneralError)
	}
	signature := string(r.RequestCtx.Request.Header.Peek("X-Hub-Signature-256"))
	if !wago.VerifySignature(body, signature, secret) {
		app.lo.Warn("whatsapp webhook signature verification failed", "inbox_id", inboxID)
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "invalid signature", nil, envelope.PermissionError)
	}

	ingester := app.ingester()
	if ingester == nil {
		// A webhook can arrive before the first inbox save has started the pipeline.
		if err := ensureWhatsAppIngester(app); err != nil {
			app.lo.Error("error starting whatsapp ingester on demand", "inbox_id", inboxID, "error", err)
		}
		ingester = app.ingester()
	}
	if ingester == nil {
		app.lo.Error("whatsapp ingester not initialized", "inbox_id", inboxID)
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "whatsapp ingester unavailable", nil, envelope.GeneralError)
	}
	if err := ingester.Enqueue(inboxID, body); err != nil {
		app.lo.Error("error enqueuing whatsapp webhook to durable stream, asking wago to retry", "inbox_id", inboxID, "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "busy, retry shortly", nil, envelope.GeneralError)
	}

	return r.SendEnvelope(map[string]string{"status": "ok"})
}

func processWhatsAppPayload(ctx context.Context, app *App, inboxID int, payload *wago.WebhookPayload) error {
	var errs []error

	for _, msg := range payload.ExtractMessages() {
		if err := ingestWhatsAppMessage(ctx, app, inboxID, msg); err != nil {
			app.lo.Error("error ingesting whatsapp message", "inbox_id", inboxID, "wa_message_id", msg.ID, "error", err)
			errs = append(errs, err)
		}
	}

	for _, ack := range payload.ExtractAcks() {
		for _, id := range ack.MessageIDs {
			if err := app.conversation.ApplyWhatsAppStatus(id, ack.Status, ack.Timestamp, ack.Error); err != nil {
				if errors.Is(err, conversation.ErrMessageNotFound) && time.Since(ack.Timestamp) > whatsAppStatusNotFoundGrace {
					app.lo.Warn("dropping whatsapp status for unknown message", "wa_message_id", id, "status", ack.Status, "event_at", ack.Timestamp)
				} else {
					app.lo.Error("error applying whatsapp status update", "wa_message_id", id, "status", ack.Status, "error", err)
					errs = append(errs, err)
				}
			}
		}
	}

	return errors.Join(errs...)
}

func ingestWhatsAppMessage(ctx context.Context, app *App, inboxID int, m wago.ParsedMessage) error {
	if m.ID == "" || m.From == "" {
		return fmt.Errorf("missing message id or sender")
	}

	if ing := app.ingester(); ing != nil {
		unlock := ing.lockSender(m.From)
		defer unlock()
	}

	inbRec, err := resolveWhatsAppInbox(app, inboxID)
	if errors.Is(err, errNoEnabledWhatsAppInbox) {
		app.lo.Warn("dropping whatsapp message: no enabled inbox for event", "url_inbox_id", inboxID, "wa_message_id", m.ID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolving inbox: %w", err)
	}
	inboxID = inbRec.ID

	app.lo.Debug("ingesting whatsapp message", "wa_message_id", m.ID, "type", m.Type, "media_path", m.MediaPath, "mime", m.MediaMIMEType, "context_id", m.ReplyToID)

	contactID, err := upsertWhatsAppContact(app, m)
	if err != nil {
		return fmt.Errorf("resolving contact: %w", err)
	}
	contact, err := app.user.GetContactOrVisitor(contactID, "" /** email **/)
	if err != nil {
		return fmt.Errorf("checking contact: %w", err)
	}
	if !contact.Enabled {
		return nil
	}

	// Skip the media download up front when the message is already ingested (retries, WAGO redeliveries).
	if exists, err := app.conversation.AdvanceWhatsAppWindowForMessage(m.ID, m.Timestamp); err != nil {
		return fmt.Errorf("repairing duplicate: %w", err)
	} else if exists {
		return nil
	}

	attachments, err := fetchWhatsAppAttachments(ctx, app, m)
	if err != nil {
		return fmt.Errorf("downloading whatsapp media: %w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	defer lockWhatsAppConversation(contactID, inboxID)()

	isNewConversation := false
	conversationID, conversationUUID, err := app.conversation.GetLatestOpenConversationForContact(contactID, inboxID)
	if errors.Is(err, sql.ErrNoRows) && inbRec.ReopenWindowHours > 0 {
		// Reuse a recently-resolved conversation. The message insert hook reopens it.
		conversationID, conversationUUID, err = app.conversation.GetReopenableConversationForContact(contactID, inboxID, inbRec.ReopenWindowHours)
	}
	if errors.Is(err, sql.ErrNoRows) {
		conversationID, conversationUUID, err = app.conversation.CreateConversation(
			contactID,
			inboxID,
			textPreview(m), /** last_message **/
			time.Now(),     /** last_message_at **/
			"",             /** subject **/
			false,          /** append_ref_num_to_subject **/
			nil,            /** meta **/
			nil,            /** custom_attributes **/
			0,              /** max_conversations **/
			0,              /** rate_limit_window **/
		)
		if err != nil {
			return fmt.Errorf("creating conversation: %w", err)
		}
		isNewConversation = true
	} else if err != nil {
		return fmt.Errorf("looking up conversation: %w", err)
	}

	content, contentType := textPreview(m), cmodels.ContentTypeText

	// The "[image]"-style placeholder only stays when the media download failed.
	if m.Text == "" && len(attachments) > 0 {
		content = ""
	}

	msg := cmodels.Message{
		Channel:          whatsappChannel.ChannelWhatsApp,
		ConversationID:   conversationID,
		ConversationUUID: conversationUUID,
		SenderID:         contactID,
		SenderType:       cmodels.SenderTypeContact,
		Type:             cmodels.MessageIncoming,
		Status:           cmodels.MessageStatusReceived,
		InboxID:          inboxID,
		Content:          content,
		ContentType:      contentType,
		SourceID:         null.StringFrom(m.ID),
		Attachments:      attachments,
		Meta:             buildInboundMeta(app, m),
	}

	if _, err := app.conversation.ProcessIncomingWhatsAppMessage(msg, isNewConversation, m.Timestamp); err != nil {
		return fmt.Errorf("processing whatsapp message: %w", err)
	}
	return nil
}

// buildInboundMeta returns nil when there's nothing to record.
func buildInboundMeta(app *App, m wago.ParsedMessage) json.RawMessage {
	patch := map[string]any{}
	if m.Type == "unsupported" {
		patch["wa_unsupported"] = true
	}
	if len(patch) == 0 {
		return nil
	}
	raw, err := json.Marshal(patch)
	if err != nil {
		app.lo.Error("error marshalling whatsapp inbound meta", "wa_message_id", m.ID, "error", err)
		return nil
	}
	return raw
}

// fetchWhatsAppAttachments returns (nil, nil) on a permanent (4xx) failure so a placeholder is stored. Any other error propagates for a queue retry.
func fetchWhatsAppAttachments(ctx context.Context, app *App, m wago.ParsedMessage) (attachment.Attachments, error) {
	if m.MediaPath == "" || app.wagoClient == nil {
		return nil, nil
	}
	maxBytes := int64(app.consts.Load().(*constants).MaxFileUploadSizeMB) * 1024 * 1024
	var (
		body []byte
		err  error
	)
	for attempt := 1; ; attempt++ {
		dlCtx, cancel := context.WithTimeout(ctx, whatsAppMediaAttemptTimeout)
		body, err = app.wagoClient.DownloadMedia(dlCtx, m.MediaPath, maxBytes)
		cancel()
		if err == nil {
			break
		}
		if errors.Is(err, wago.ErrMediaTooLarge) {
			app.lo.Warn("whatsapp media exceeds the configured upload limit, inserting placeholder", "media_path", m.MediaPath, "max_bytes", maxBytes)
			return nil, nil
		}
		if ctx.Err() != nil {
			return nil, nil
		}
		if attempt >= whatsAppMediaMaxAttempts {
			if isPermanentMediaError(err) {
				app.lo.Warn("whatsapp media permanently unavailable, inserting placeholder", "media_path", m.MediaPath, "attempts", attempt, "error", err)
				return nil, nil
			}
			app.lo.Warn("error downloading whatsapp media, will retry job", "media_path", m.MediaPath, "attempts", attempt, "error", err)
			return nil, err
		}
		app.lo.Warn("error downloading whatsapp media, retrying", "media_path", m.MediaPath, "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(whatsAppMediaRetryDelay):
		}
	}

	if len(body) == 0 {
		app.lo.Warn("whatsapp media downloaded empty, inserting placeholder", "media_path", m.MediaPath, "type", m.Type)
		return nil, nil
	}

	contentType := m.MediaMIMEType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	filename := m.MediaFilename
	if filename == "" {
		filename = defaultMediaFilename(m.Type, contentType)
	}

	return attachment.Attachments{
		attachment.Attachment{
			Name:        filename,
			ContentType: contentType,
			Content:     body,
			Size:        len(body),
			Disposition: attachment.DispositionAttachment,
		},
	}, nil
}

func isPermanentMediaError(err error) bool {
	var apiErr *wago.APIError
	if errors.As(err, &apiErr) {
		// 408 and 429 are 4xx but retryable. 401/403 recover once the operator fixes WAGO credentials. 5xx are transient.
		if apiErr.StatusCode == http.StatusRequestTimeout || apiErr.StatusCode == http.StatusTooManyRequests ||
			apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden {
			return false
		}
		return apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	return true
}

func defaultMediaFilename(messageType, mime string) string {
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	ext := "bin"
	if i := strings.LastIndex(mime, "/"); i >= 0 && i+1 < len(mime) {
		ext = mime[i+1:]
	}
	switch messageType {
	case "image":
		return "image." + ext
	case "video":
		return "video." + ext
	case "audio", "voice":
		return "audio." + ext
	case "document":
		return "document." + ext
	case "sticker":
		return "sticker." + ext
	}
	return "attachment." + ext
}

func upsertWhatsAppContact(app *App, m wago.ParsedMessage) (int, error) {
	first, last := splitName(m.ContactName)
	contact := umodels.User{
		Type:      umodels.UserTypeContact,
		FirstName: first,
		LastName:  last,
	}
	id, err := app.user.UpsertContactByChannelIdentity(whatsappChannel.ChannelWhatsApp, m.From, &contact)
	if err != nil {
		return 0, err
	}
	if err := app.user.SetContactPhoneIfMissing(id, m.From, ""); err != nil {
		app.lo.Error("error setting whatsapp contact phone", "user_id", id, "error", err)
	}
	// A contact created from a message without a profile name picks the real name up later.
	if m.ContactName != "" {
		if err := app.user.UpdateContactNameIfDefault(id, first, last, whatsAppDefaultContactName); err != nil {
			app.lo.Error("error updating whatsapp contact name", "user_id", id, "error", err)
		}
	}
	return id, nil
}

func splitName(name string) (string, string) {
	if name == "" {
		return whatsAppDefaultContactName, ""
	}
	first, last, _ := strings.Cut(name, " ")
	return first, last
}

func textPreview(m wago.ParsedMessage) string {
	if m.Text != "" {
		return m.Text
	}
	switch m.Type {
	case "image":
		return "[image]"
	case "video":
		return "[video]"
	case "audio", "voice":
		return "[audio]"
	case "document":
		return "[document]"
	case "sticker":
		return "[sticker]"
	case "unsupported":
		return "[unsupported message: not delivered by WhatsApp]"
	}
	return "[whatsapp message]"
}

func inboxIDFromPath(r *fastglue.Request) (int, error) {
	raw, ok := r.RequestCtx.UserValue("inbox_id").(string)
	if !ok || raw == "" {
		return 0, fmt.Errorf("missing inbox_id")
	}
	return strconv.Atoi(raw)
}

func resolveWhatsAppInbox(app *App, urlInboxID int) (imodels.Inbox, error) {
	if rec, err := app.inbox.GetDBRecord(urlInboxID); err == nil && rec.Enabled && rec.Channel == whatsappChannel.ChannelWhatsApp {
		return rec, nil
	}

	var enabled []imodels.Inbox
	if err := forEachEnabledWhatsAppInbox(app, func(rec imodels.Inbox) bool {
		enabled = append(enabled, rec)
		return true
	}); err != nil {
		return imodels.Inbox{}, err
	}
	switch len(enabled) {
	case 0:
		return imodels.Inbox{}, errNoEnabledWhatsAppInbox
	case 1:
		if enabled[0].ID != urlInboxID {
			app.lo.Info("routing whatsapp message to the only enabled inbox", "url_inbox_id", urlInboxID, "resolved_inbox_id", enabled[0].ID)
		}
		return enabled[0], nil
	default:
		return imodels.Inbox{}, fmt.Errorf("multiple enabled whatsapp inboxes found; exactly one is supported")
	}
}

// forEachEnabledWhatsAppInbox invokes fn with each enabled WhatsApp inbox record. Returning false stops iteration.
func forEachEnabledWhatsAppInbox(app *App, fn func(rec imodels.Inbox) bool) error {
	inboxes, err := app.inbox.GetAll()
	if err != nil {
		return fmt.Errorf("listing inboxes: %w", err)
	}
	for _, rec := range inboxes {
		if rec.Channel != whatsappChannel.ChannelWhatsApp || !rec.Enabled {
			continue
		}
		if !fn(rec) {
			return nil
		}
	}
	return nil
}

// markWhatsAppMessageRead sends a read receipt to WAGO for an inbound message. Best-effort, logs and swallows failures.
func markWhatsAppMessageRead(app *App, inboxID int, sourceID, phone string) {
	if app.wagoClient == nil || sourceID == "" || phone == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), whatsappChannel.WagoCallTimeout)
	defer cancel()
	if err := app.wagoClient.MarkRead(ctx, sourceID, phone); err != nil {
		app.lo.Warn("error marking whatsapp message read", "inbox_id", inboxID, "source_id", sourceID, "error", err)
	}
}
