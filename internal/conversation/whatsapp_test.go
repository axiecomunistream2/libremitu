package conversation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/abhinavxd/libredesk/internal/conversation/models"
	"github.com/abhinavxd/libredesk/internal/dbutil"
	"github.com/abhinavxd/libredesk/internal/inbox"
	whatsappChannel "github.com/abhinavxd/libredesk/internal/inbox/channel/whatsapp"
	mmodels "github.com/abhinavxd/libredesk/internal/media/models"
	"github.com/abhinavxd/libredesk/internal/template"
	"github.com/abhinavxd/libredesk/internal/testutil"
	"github.com/abhinavxd/libredesk/internal/user"
	wmodels "github.com/abhinavxd/libredesk/internal/webhook/models"
	"github.com/abhinavxd/libredesk/internal/ws"
	"github.com/jmoiron/sqlx"
	"github.com/knadh/go-i18n"
	"github.com/zerodha/logf"
)

type whatsAppReplyMedia struct{ mediaStore }

type whatsAppReplyWebhook struct{ webhookStore }

func (whatsAppReplyMedia) LinkMessageMediaTx(*sqlx.Tx, int, []mmodels.Media, []string, int) error {
	return nil
}

func (whatsAppReplyWebhook) TriggerEvent(wmodels.WebhookEvent, any) {}

func TestQueueWhatsAppReplyVariables(t *testing.T) {
	db := testutil.NewDB(t, "whatsapp_reply_variables")
	lo := logf.New(logf.Opts{Level: logf.FatalLevel})
	lang := testutil.NewI18n(t)
	inboxes, err := inbox.New(&lo, db, lang, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.New(lang, user.Opts{DB: db, Lo: &lo})
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{
		db: db, lo: &lo, i18n: lang, inboxStore: inboxes, userStore: users,
		template: &template.Manager{}, mediaStore: whatsAppReplyMedia{},
		webhookStore: whatsAppReplyWebhook{}, wsHub: ws.NewHub(&lo, nil /** userStore **/),
	}
	if err := dbutil.ScanSQLFile("queries.sql", &m.q, db, efs); err != nil {
		t.Fatal(err)
	}
	var contactID, senderID, inboxID int
	if err := db.Get(&contactID, `INSERT INTO users (type, first_name, last_name)
		VALUES ('contact', 'Customer', 'A') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&senderID, `INSERT INTO users (type, email, first_name, last_name)
		VALUES ('agent', 'agent@example.com', 'Agent', '') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&inboxID, `INSERT INTO inboxes (channel, name, "from")
		VALUES ('whatsapp', 'Reply variables', '') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	if _, err := users.LinkChannelIdentity(contactID, "whatsapp", "15550000100"); err != nil {
		t.Fatal(err)
	}
	var conv models.Conversation
	if err := db.Get(&conv, `INSERT INTO conversations (contact_id, inbox_id, status_id, subject, last_inbound_at)
		VALUES ($1, $2, (SELECT id FROM conversation_statuses WHERE category = 'open' LIMIT 1), 'Support request', NOW())
		RETURNING id, uuid, reference_number`, contactID, inboxID); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{"contact and reference", "Hi {{ .Contact.FirstName }}, reference {{ .Conversation.ReferenceNumber }}", "Hi Customer, reference " + conv.ReferenceNumber},
		{"automated reply", "{{ .Recipient.FullName }}: {{ .Conversation.Subject }}", "Customer A: Support request"},
		{"repeated variables", "{{ .Contact.FirstName }} {{ .Contact.FirstName }}", "Customer Customer"},
		{"plain text", "Hello", "Hello"},
		{"rendered text at limit", strings.Repeat("x", whatsAppMaxTextLength-len("Customer")) + "{{ .Contact.FirstName }}", strings.Repeat("x", whatsAppMaxTextLength-len("Customer")) + "Customer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message, err := m.QueueReply(nil, /** media **/
				inboxID, senderID, contactID, conv.UUID, tc.content,
				nil, /** to **/
				nil, /** cc **/
				nil, /** bcc **/
				"",  /** sendFrom **/
				map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			if message.Content != tc.want || message.TextContent != tc.want {
				t.Fatalf("queued content = %q, text = %q, want %q", message.Content, message.TextContent, tc.want)
			}
			if tc.name == "plain text" {
				var meta struct {
					WhatsApp whatsappChannel.SendMeta `json:"whatsapp"`
				}
				if err := json.Unmarshal(message.Meta, &meta); err != nil {
					t.Fatal(err)
				}
				if meta.WhatsApp.ToPhone != "15550000100" {
					t.Fatalf("expected the linked WhatsApp identity, got %+v", meta.WhatsApp)
				}
			}
		})
	}

	t.Run("expanded content exceeds limit", func(t *testing.T) {
		if _, err := db.Exec(`UPDATE users SET first_name = $1 WHERE id = $2`, strings.Repeat("a", 64), contactID); err != nil {
			t.Fatal(err)
		}
		content := strings.Repeat("x", whatsAppMaxTextLength-32) + "{{ .Contact.FirstName }}"
		if _, err := m.QueueReply(nil, /** media **/
			inboxID, senderID, contactID, conv.UUID, content,
			nil, /** to **/
			nil, /** cc **/
			nil, /** bcc **/
			"",  /** sendFrom **/
			map[string]any{}); err == nil || !strings.Contains(err.Error(), "4096") {
			t.Fatalf("expected rendered content to exceed the limit, got %v", err)
		}
	})
}

func TestValidateWhatsAppContent(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		hasAttachments bool
		wantMatch      string
	}{
		{name: "attachment without caption", hasAttachments: true},
		{name: "caption at limit", content: strings.Repeat("a", 1024), hasAttachments: true},
		{name: "caption over limit", content: strings.Repeat("a", 1025), hasAttachments: true, wantMatch: "1024"},
		{name: "unicode caption counts runes", content: strings.Repeat("श", 1024), hasAttachments: true},
		{name: "text at limit", content: strings.Repeat("a", 4096)},
		{name: "text over limit", content: strings.Repeat("a", 4097), wantMatch: "4096"},
		{name: "empty text without attachment", wantMatch: "attach a file"},
	}

	manager := testManager(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := manager.validateWhatsAppContent(tc.content, tc.hasAttachments)
			if tc.wantMatch == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.wantMatch)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantMatch) {
				t.Fatalf("expected the error to mention %q, got %q", tc.wantMatch, err.Error())
			}
		})
	}
}

func TestCSATTokensHiddenFromAgentResponses(t *testing.T) {
	tests := []struct {
		name string
		meta string
		want string
	}{
		{"csat uuid removed", `{"is_csat":true,"csat_uuid":"secret","other":"keep"}`, `{"is_csat":true,"other":"keep"}`},
		{"token without flag", `{"csat_uuid":"secret","other":"keep"}`, `{"other":"keep"}`},
		{"ordinary message", `{"is_csat":false,"other":"keep"}`, `{"is_csat":false,"other":"keep"}`},
		{"null meta", `null`, `null`},
	}
	for _, tt := range tests {
		for name, redact := range map[string]func(json.RawMessage) json.RawMessage{
			"HTTP": func(meta json.RawMessage) json.RawMessage {
				msg := models.Message{Meta: meta}
				msg.StripCSATUUID()
				return msg.Meta
			},
			"WebSocket": stripCSATUUID,
		} {
			t.Run(tt.name+"/"+name, func(t *testing.T) {
				original := json.RawMessage(tt.meta)
				got := redact(original)
				var want any
				if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
					t.Fatal(err)
				}
				var result any
				if err := json.Unmarshal(got, &result); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(result, want) {
					t.Fatalf("got %s, want %s", got, tt.want)
				}
				if string(original) != tt.meta {
					t.Fatal("redaction mutated the stored metadata")
				}
			})
		}
	}
}

func TestStripCSATUUID(t *testing.T) {
	stripped := stripCSATUUID(json.RawMessage(`{"is_csat":true,"csat_uuid":"secret-uuid"}`))
	var out map[string]any
	if err := json.Unmarshal(stripped, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["csat_uuid"]; ok {
		t.Fatal("csat_uuid must not reach the websocket payload")
	}
	if out["is_csat"] != true {
		t.Fatalf("expected the rest of the meta to survive, got %+v", out)
	}

	same := json.RawMessage(`{"is_csat":true}`)
	if string(stripCSATUUID(same)) != string(same) {
		t.Fatal("meta without a csat_uuid must pass through unchanged")
	}
	if got := stripCSATUUID(nil); got != nil {
		t.Fatalf("expected nil meta to pass through, got %q", got)
	}
	malformed := json.RawMessage(`not json`)
	if string(stripCSATUUID(malformed)) != string(malformed) {
		t.Fatal("malformed meta must pass through unchanged")
	}
}

// testManager carries the real language file, so a renamed i18n key fails the test.
func testManager(t *testing.T) *Manager {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "i18n", "en-US.json"))
	if err != nil {
		t.Fatalf("reading the language file: %v", err)
	}
	lang, err := i18n.New(raw)
	if err != nil {
		t.Fatalf("loading i18n: %v", err)
	}
	return &Manager{i18n: lang}
}
