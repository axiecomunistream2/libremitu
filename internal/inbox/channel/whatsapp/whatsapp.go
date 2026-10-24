// Package whatsapp implements a WhatsApp inbox backed by WAGO.
package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/abhinavxd/libredesk/internal/attachment"
	"github.com/abhinavxd/libredesk/internal/conversation/models"
	"github.com/abhinavxd/libredesk/internal/inbox"
	"github.com/abhinavxd/libredesk/internal/stringutil"
	"github.com/abhinavxd/libredesk/internal/wago"
	"github.com/zerodha/logf"
)

const ChannelWhatsApp = "whatsapp"

const WagoCallTimeout = 30 * time.Second

// Covers uploading a large attachment, then the send call.
const attachmentSendTimeout = 5*time.Minute + WagoCallTimeout

const (
	sendMaxAttempts  = 3
	sendRetryBackoff = 2 * time.Second
)

// WhatsApp's published per-media-type size caps.
const (
	maxImageBytes    = 5 * 1024 * 1024
	maxVideoBytes    = 16 * 1024 * 1024
	maxAudioBytes    = 16 * 1024 * 1024
	maxDocumentBytes = 100 * 1024 * 1024
)

// Config is the per-inbox WhatsApp configuration. WAGO connection settings are global.
type Config struct {
	CSATMessage string `json:"csat_message"`
}

// SendMeta is the per-message metadata threaded through OutboundMessage.Meta.
type SendMeta struct {
	ToPhone            string `json:"to_phone"`
	ReplyToWAMessageID string `json:"reply_to_wa_message_id,omitempty"`
}

// SourceIDUpdater persists the WAGO message ID for status correlation.
type SourceIDUpdater interface {
	UpdateMessageSourceID(messageUUID, sourceID string) error
}

type WhatsApp struct {
	id            int
	name          string
	config        Config
	client        *wago.Client
	lo            *logf.Logger
	messageStore  inbox.MessageStore
	sourceUpdater SourceIDUpdater
	retryBackoff  time.Duration
}

type Opts struct {
	ID            int
	Name          string
	Config        Config
	Client        *wago.Client
	Lo            *logf.Logger
	SourceUpdater SourceIDUpdater
}

func New(store inbox.MessageStore, opts Opts) (*WhatsApp, error) {
	if opts.Client == nil || !opts.Client.Configured() {
		return nil, fmt.Errorf("wago base_url and device_id are required")
	}
	if opts.Lo == nil {
		return nil, fmt.Errorf("logger is required")
	}
	return &WhatsApp{
		id:            opts.ID,
		name:          opts.Name,
		config:        opts.Config,
		client:        opts.Client,
		lo:            opts.Lo,
		messageStore:  store,
		sourceUpdater: opts.SourceUpdater,
		retryBackoff:  sendRetryBackoff,
	}, nil
}

func (w *WhatsApp) Identifier() int          { return w.id }
func (w *WhatsApp) Config() Config           { return w.config }
func (w *WhatsApp) Channel() string          { return ChannelWhatsApp }
func (w *WhatsApp) Name() string             { return w.name }
func (w *WhatsApp) FromAddress() string      { return "" }
func (w *WhatsApp) ReplyToAddress() string   { return "" }
func (w *WhatsApp) FromNameTemplate() string { return "" }
func (w *WhatsApp) Close() error             { return nil }

// Receive is a no-op. Inbound messages arrive via the webhook handler.
func (w *WhatsApp) Receive(ctx context.Context) error { return nil }

func (w *WhatsApp) Send(message models.OutboundMessage) error {
	var err error
	for attempt := 1; ; attempt++ {
		err = w.send(message)
		if err == nil || attempt >= sendMaxAttempts || !wagoRefusedSend(err) {
			return err
		}
		w.lo.Warn("wago refused the whatsapp send, retrying", "message_uuid", message.UUID, "attempt", attempt, "error", err)
		time.Sleep(w.retryBackoff * time.Duration(attempt))
	}
}

func wagoRefusedSend(err error) bool {
	var apiErr *wago.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= http.StatusInternalServerError
}

func (w *WhatsApp) send(message models.OutboundMessage) error {
	meta, err := parseSendMeta(message.Meta)
	if err != nil {
		return fmt.Errorf("parsing whatsapp send meta: %w", err)
	}
	if meta.ToPhone == "" {
		return fmt.Errorf("missing recipient phone number on outbound message")
	}

	timeout := WagoCallTimeout
	if len(message.Attachments) > 0 {
		timeout = attachmentSendTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var sourceID string
	switch {
	case len(message.Attachments) > 0:
		sourceID, err = w.sendAttachment(ctx, meta, message)
	case strings.TrimSpace(textBody(message)) != "":
		sourceID, err = w.client.SendText(ctx, meta.ToPhone, textBody(message), meta.ReplyToWAMessageID)
	default:
		return fmt.Errorf("outbound message has no content")
	}

	if sourceID != "" && w.sourceUpdater != nil {
		if upErr := w.sourceUpdater.UpdateMessageSourceID(message.UUID, sourceID); upErr != nil {
			w.lo.Error("failed to persist whatsapp source id", "message_uuid", message.UUID, "source_id", sourceID, "error", upErr)
		}
	}
	return err
}

// sendAttachment sends one attachment. WAGO accepts only one file per message.
func (w *WhatsApp) sendAttachment(ctx context.Context, meta SendMeta, message models.OutboundMessage) (string, error) {
	if len(message.Attachments) > 1 {
		return "", fmt.Errorf("whatsapp accepts one attachment per message, got %d", len(message.Attachments))
	}
	if bad := rejectedAttachments(message.Attachments); len(bad) > 0 {
		return "", fmt.Errorf("WhatsApp can't send these files: %s", strings.Join(bad, ", "))
	}

	att := message.Attachments[0]
	// WAGO's media endpoints have no reply field, so a reply arrives as a plain media message.
	return w.client.SendMedia(ctx, meta.ToPhone, mediaKind(att), att.Name, att.ContentType, strings.TrimSpace(textBody(message)), att.Content)
}

// RejectMediaReason returns why WhatsApp won't accept the file, or an empty string when it will.
func RejectMediaReason(name, contentType string, size int) string {
	reasons := rejectedAttachments([]attachment.Attachment{{Name: name, ContentType: contentType, Size: size}})
	if len(reasons) == 0 {
		return ""
	}
	return reasons[0]
}

func parseSendMeta(raw json.RawMessage) (SendMeta, error) {
	var meta SendMeta
	if len(raw) == 0 {
		return meta, nil
	}
	// SendMeta lives under a "whatsapp" key in message.meta to avoid colliding with email's to/cc keys.
	var envelope struct {
		WhatsApp json.RawMessage `json:"whatsapp"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.WhatsApp) > 0 {
		if err := json.Unmarshal(envelope.WhatsApp, &meta); err != nil {
			return meta, fmt.Errorf("decoding whatsapp meta envelope: %w", err)
		}
		return meta, nil
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

// textBody renders HTML replies with WhatsApp formatting markers. Raw HTML must never reach WhatsApp verbatim.
func textBody(m models.OutboundMessage) string {
	if m.ContentType == models.ContentTypeHTML && m.Content != "" {
		return stringutil.HTML2WhatsApp(m.Content)
	}
	if m.TextContent != "" {
		return m.TextContent
	}
	return m.Content
}

func rejectedAttachments(atts []attachment.Attachment) []string {
	var reasons []string
	for _, att := range atts {
		mediaType := mediaKind(att)
		if max := maxMediaBytes(mediaType); att.Size > max {
			reasons = append(reasons, fmt.Sprintf("%s (%s exceeds the %s %s limit)", att.Name, humanBytes(att.Size), humanBytes(max), mediaType))
		}
	}
	return reasons
}

func maxMediaBytes(mediaType string) int {
	switch mediaType {
	case "image":
		return effectiveLimit(maxImageBytes)
	case "video":
		return effectiveLimit(maxVideoBytes)
	case "audio":
		return effectiveLimit(maxAudioBytes)
	}
	return effectiveLimit(maxDocumentBytes)
}

func humanBytes(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%d B", n)
}

func mediaKind(att attachment.Attachment) string {
	mime := normalizeMIME(att.ContentType)
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	}
	return "file"
}

func normalizeMIME(contentType string) string {
	mime := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	return mime
}

// effectiveLimit keeps 2% headroom below WhatsApp's hard caps to absorb size-measurement skew at the boundary.
func effectiveLimit(n int) int {
	return n * 98 / 100
}
