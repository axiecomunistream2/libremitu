package wago

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"path"
	"strconv"
	"strings"
	"time"
)

type WebhookPayload struct {
	Event     string          `json:"event"`
	DeviceID  string          `json:"device_id"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type messagePayload struct {
	ID          string          `json:"id"`
	ChatID      string          `json:"chat_id"`
	From        string          `json:"from"`
	FromLID     string          `json:"from_lid"`
	FromName    string          `json:"from_name"`
	Timestamp   string          `json:"timestamp"`
	IsFromMe    bool            `json:"is_from_me"`
	Body        string          `json:"body"`
	RepliedToID string          `json:"replied_to_id"`
	Image       json.RawMessage `json:"image"`
	Video       json.RawMessage `json:"video"`
	Audio       json.RawMessage `json:"audio"`
	Document    json.RawMessage `json:"document"`
	Sticker     json.RawMessage `json:"sticker"`
	VideoNote   json.RawMessage `json:"video_note"`
	Location    json.RawMessage `json:"location"`
	Contact     json.RawMessage `json:"contact"`
	Contacts    json.RawMessage `json:"contacts_array"`
}

type ackPayload struct {
	IDs         []string `json:"ids"`
	ReceiptType string   `json:"receipt_type"`
}

type mediaPayload struct {
	Path     string
	URL      string
	Caption  string
	Filename string
	MIMEType string
}

func ParsePayload(body []byte) (*WebhookPayload, error) {
	var p WebhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("decoding wago webhook payload: %w", err)
	}
	return &p, nil
}

func (p *WebhookPayload) ExtractMessages() []ParsedMessage {
	if p == nil || p.Event != "message" {
		return nil
	}
	var m messagePayload
	if err := json.Unmarshal(p.Payload, &m); err != nil || m.ID == "" || m.IsFromMe {
		return nil
	}
	if !isDirectChat(m.ChatID) {
		return nil
	}

	from := directIdentity(m.ChatID, m.From)
	if from == "" {
		return nil
	}

	ts := firstTime(m.Timestamp, p.Timestamp)
	pm := ParsedMessage{
		ID:          m.ID,
		From:        from,
		ChatID:      m.ChatID,
		ContactName: strings.TrimSpace(m.FromName),
		Timestamp:   ts,
		Text:        m.Body,
		ReplyToID:   m.RepliedToID,
	}

	media, mediaType := pickMedia(m)
	if mediaType != "" {
		pm.Type = mediaType
		pm.MediaPath = media.Path
		pm.MediaFilename = media.Filename
		pm.MediaMIMEType = media.MIMEType
		if pm.Text == "" {
			pm.Text = media.Caption
		}
		if pm.MediaFilename == "" {
			pm.MediaFilename = mediaFilename(media, mediaType)
		}
		if pm.MediaMIMEType == "" {
			pm.MediaMIMEType = mediaMIME(media, mediaType, pm.MediaFilename)
		}
		return []ParsedMessage{pm}
	}

	if pm.Text != "" {
		pm.Type = "text"
		return []ParsedMessage{pm}
	}

	if text := locationText(m.Location); text != "" {
		pm.Type = "location"
		pm.Text = text
		return []ParsedMessage{pm}
	}
	if text := contactText(m.Contact, m.Contacts); text != "" {
		pm.Type = "contact"
		pm.Text = text
		return []ParsedMessage{pm}
	}

	pm.Type = "unsupported"
	return []ParsedMessage{pm}
}

func (p *WebhookPayload) ExtractAcks() []ParsedAck {
	if p == nil || p.Event != "message.ack" {
		return nil
	}
	var a ackPayload
	if err := json.Unmarshal(p.Payload, &a); err != nil || len(a.IDs) == 0 {
		return nil
	}
	status := ""
	switch strings.ToLower(strings.TrimSpace(a.ReceiptType)) {
	case "delivered":
		status = "delivered"
	case "read", "played":
		status = "read"
	}
	if status == "" {
		return nil
	}
	return []ParsedAck{{
		MessageIDs: a.IDs,
		Status:     status,
		Timestamp:  firstTime(p.Timestamp, ""),
	}}
}

func VerifySignature(body []byte, signatureHeader, secret string) bool {
	if secret == "" || signatureHeader == "" {
		return false
	}
	parts := strings.SplitN(signatureHeader, "=", 2)
	if len(parts) != 2 || parts[0] != "sha256" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(parts[1]))
}

func isDirectChat(jid string) bool {
	lower := strings.ToLower(jid)
	return !strings.HasSuffix(lower, "@g.us") &&
		!strings.HasSuffix(lower, "@broadcast") &&
		!strings.HasSuffix(lower, "@newsletter")
}

// directIdentity prefers the phone JID WAGO normalizes LIDs to, and falls back to the raw chat user.
func directIdentity(chatID, from string) string {
	if strings.HasSuffix(strings.ToLower(chatID), "@s.whatsapp.net") {
		return jidUser(chatID)
	}
	if strings.HasSuffix(strings.ToLower(from), "@s.whatsapp.net") {
		return jidUser(from)
	}
	return jidUser(chatID)
}

func jidUser(jid string) string {
	if i := strings.Index(jid, "@"); i > 0 {
		jid = jid[:i]
	}
	if i := strings.Index(jid, ":"); i > 0 {
		jid = jid[:i]
	}
	return strings.TrimSpace(jid)
}

func firstTime(values ...string) time.Time {
	for _, v := range values {
		if v == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC()
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(n, 0).UTC()
		}
	}
	return time.Now().UTC()
}

func pickMedia(m messagePayload) (mediaPayload, string) {
	candidates := []struct {
		raw  json.RawMessage
		typ  string
		mime string
	}{
		{m.Image, "image", ""},
		{m.Video, "video", ""},
		{m.Audio, "audio", ""},
		{m.Document, "document", ""},
		{m.Sticker, "sticker", "image/webp"},
		{m.VideoNote, "video", "video/mp4"},
	}
	for _, c := range candidates {
		media, ok := parseMedia(c.raw)
		if !ok || (media.Path == "" && media.URL == "") {
			continue
		}
		if media.MIMEType == "" {
			media.MIMEType = c.mime
		}
		return media, c.typ
	}
	return mediaPayload{}, ""
}

func parseMedia(raw json.RawMessage) (mediaPayload, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return mediaPayload{}, false
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return mediaPayload{}, false
		}
		return mediaPayload{Path: s}, true
	}
	var m mediaPayload
	var obj struct {
		Path     string `json:"path"`
		URL      string `json:"url"`
		Caption  string `json:"caption"`
		Filename string `json:"filename"`
		MIMEType string `json:"mime_type"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return mediaPayload{}, false
	}
	m.Path = obj.Path
	m.URL = obj.URL
	m.Caption = obj.Caption
	m.Filename = obj.Filename
	m.MIMEType = obj.MIMEType
	return m, true
}

func mediaFilename(m mediaPayload, typ string) string {
	if m.Path != "" {
		if base := path.Base(m.Path); base != "." && base != "/" {
			return base
		}
	}
	ext := extensionForMIME(firstNonEmpty(m.MIMEType, defaultMIME(typ)))
	return typ + ext
}

func mediaMIME(m mediaPayload, typ, filename string) string {
	if m.MIMEType != "" {
		return normalizeMIME(m.MIMEType)
	}
	if ext := path.Ext(filename); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return normalizeMIME(t)
		}
	}
	return defaultMIME(typ)
}

func defaultMIME(typ string) string {
	switch typ {
	case "image":
		return "image/jpeg"
	case "video":
		return "video/mp4"
	case "audio":
		return "audio/ogg"
	case "sticker":
		return "image/webp"
	}
	return "application/octet-stream"
}

func extensionForMIME(m string) string {
	switch normalizeMIME(m) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "video/mp4":
		return ".mp4"
	case "audio/ogg", "audio/opus":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "application/pdf":
		return ".pdf"
	}
	if exts, _ := mime.ExtensionsByType(normalizeMIME(m)); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

func normalizeMIME(m string) string {
	if i := strings.Index(m, ";"); i >= 0 {
		m = m[:i]
	}
	return strings.ToLower(strings.TrimSpace(m))
}

func locationText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var loc struct {
		Name      string  `json:"name"`
		Address   string  `json:"address"`
		Latitude  float64 `json:"degreesLatitude"`
		Longitude float64 `json:"degreesLongitude"`
	}
	if err := json.Unmarshal(raw, &loc); err != nil {
		return ""
	}
	var lines []string
	if label := strings.TrimSpace(strings.Trim(loc.Name+", "+loc.Address, ", ")); label != "" {
		lines = append(lines, label)
	}
	if loc.Latitude != 0 || loc.Longitude != 0 {
		lines = append(lines, "https://www.google.com/maps?q="+
			strconv.FormatFloat(loc.Latitude, 'f', -1, 64)+","+
			strconv.FormatFloat(loc.Longitude, 'f', -1, 64))
	}
	return strings.Join(lines, "\n")
}

func contactText(single, many json.RawMessage) string {
	var lines []string
	appendContact := func(raw json.RawMessage) {
		var c struct {
			DisplayName string `json:"displayName"`
			PhoneNumber string `json:"phone_number"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return
		}
		line := strings.TrimSpace(c.DisplayName + " " + c.PhoneNumber)
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(single) > 0 && string(single) != "null" {
		appendContact(single)
	}
	if len(many) > 0 && string(many) != "null" {
		var contacts []json.RawMessage
		if err := json.Unmarshal(many, &contacts); err == nil {
			for _, c := range contacts {
				appendContact(c)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// MediaURL is exported for tests and diagnostics; it only returns paths WAGO exposes locally.
func (m mediaPayload) MediaURL() string {
	if m.Path != "" {
		return m.Path
	}
	return m.URL
}
