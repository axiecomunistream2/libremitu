package whatsapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhinavxd/libredesk/internal/attachment"
	"github.com/abhinavxd/libredesk/internal/conversation/models"
	"github.com/abhinavxd/libredesk/internal/wago"
	"github.com/zerodha/logf"
)

func testLogger() *logf.Logger {
	lo := logf.New(logf.Opts{Level: logf.FatalLevel})
	return &lo
}

func testClient(t *testing.T, serverURL string) *wago.Client {
	t.Helper()
	return wago.New(testLogger(), wago.Opts{BaseURL: serverURL, DeviceID: "dev-1", Username: "u", Password: "p"})
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		opts    Opts
		wantErr string
	}{
		{"no client", Opts{Lo: testLogger()}, "base_url and device_id are required"},
		{"missing logger", Opts{Client: testClient(t, "https://wago.example.com")}, "logger is required"},
		{"ok", Opts{Client: testClient(t, "https://wago.example.com"), Lo: testLogger()}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(nil, tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestInboxAccessors(t *testing.T) {
	w, err := New(nil, Opts{ID: 7, Name: "Support", Config: Config{CSATMessage: "Rate us"}, Client: testClient(t, "https://wago.example.com"), Lo: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if w.Identifier() != 7 || w.Name() != "Support" || w.Channel() != ChannelWhatsApp || w.Config().CSATMessage != "Rate us" {
		t.Fatalf("unexpected inbox accessors: %+v", w)
	}
	if w.FromAddress() != "" || w.ReplyToAddress() != "" || w.FromNameTemplate() != "" {
		t.Fatal("whatsapp inbox must not expose email addresses")
	}
	if err := w.Receive(context.Background()); err != nil || w.Close() != nil {
		t.Fatal("receive/close must be no-ops")
	}
}

type fakeSourceUpdater struct {
	uuid, sourceID string
	err            error
}

func (f *fakeSourceUpdater) UpdateMessageSourceID(uuid, sourceID string) error {
	f.uuid, f.sourceID = uuid, sourceID
	return f.err
}

func TestSendText(t *testing.T) {
	var gotPath, gotDevice, gotUser, gotPass string
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotDevice = r.Header.Get("X-Device-Id")
		gotUser, gotPass, _ = r.BasicAuth()
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"code":"SUCCESS","message":"ok","results":{"message_id":"wamid.out"}}`))
	}))
	defer server.Close()

	updater := &fakeSourceUpdater{}
	w, err := New(nil, Opts{ID: 1, Client: testClient(t, server.URL), Lo: testLogger(), SourceUpdater: updater})
	if err != nil {
		t.Fatal(err)
	}
	err = w.Send(models.OutboundMessage{
		UUID:        "msg-1",
		Content:     "Hello",
		ContentType: models.ContentTypeText,
		Meta:        json.RawMessage(`{"whatsapp":{"to_phone":"628111"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/send/message" || gotDevice != "dev-1" || gotUser != "u" || gotPass != "p" {
		t.Fatalf("unexpected request: path=%s device=%s auth=%s:%s", gotPath, gotDevice, gotUser, gotPass)
	}
	if payload["phone"] != "628111" || payload["message"] != "Hello" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if updater.uuid != "msg-1" || updater.sourceID != "wamid.out" {
		t.Fatalf("source id not persisted: %+v", updater)
	}
}

func TestSendReplyCarriesReplyID(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"message_id":"x"}}`))
	}))
	defer server.Close()
	w, _ := New(nil, Opts{Client: testClient(t, server.URL), Lo: testLogger()})
	err := w.Send(models.OutboundMessage{
		Content: "Hi", ContentType: models.ContentTypeText,
		Meta: json.RawMessage(`{"whatsapp":{"to_phone":"628111","reply_to_wa_message_id":"in-1"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload["reply_message_id"] != "in-1" {
		t.Fatalf("reply id missing: %+v", payload)
	}
}

func TestSendAttachment(t *testing.T) {
	var gotPath, gotField, gotCaption, gotFilename string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		f, header, err := r.FormFile("image")
		if err != nil {
			t.Fatalf("reading multipart: %v", err)
		}
		defer f.Close()
		body, _ := io.ReadAll(f)
		gotField = string(body)
		gotCaption = r.FormValue("caption")
		gotFilename = header.Filename
		_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"message_id":"wamid.media"}}`))
	}))
	defer server.Close()

	w, _ := New(nil, Opts{Client: testClient(t, server.URL), Lo: testLogger()})
	err := w.Send(models.OutboundMessage{
		Content:     "Look",
		ContentType: models.ContentTypeText,
		Attachments: attachment.Attachments{{Name: "photo.jpg", ContentType: "image/jpeg", Content: []byte("image-bytes"), Size: 11}},
		Meta:        json.RawMessage(`{"whatsapp":{"to_phone":"628111"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/send/image" || gotField != "image-bytes" || gotCaption != "Look" || gotFilename != "photo.jpg" {
		t.Fatalf("unexpected media request: path=%s body=%s caption=%s filename=%s", gotPath, gotField, gotCaption, gotFilename)
	}
}

func TestSendRejectsUnusableMessages(t *testing.T) {
	w, _ := New(nil, Opts{Client: testClient(t, "https://wago.example.com"), Lo: testLogger()})
	tests := []struct {
		name string
		msg  models.OutboundMessage
	}{
		{"no content", models.OutboundMessage{Meta: json.RawMessage(`{"whatsapp":{"to_phone":"1"}}`)}},
		{"no recipient", models.OutboundMessage{Content: "hi", ContentType: models.ContentTypeText}},
		{"many attachments", models.OutboundMessage{
			Meta: json.RawMessage(`{"whatsapp":{"to_phone":"1"}}`),
			Attachments: attachment.Attachments{
				{Name: "a", ContentType: "image/png", Content: []byte("a")},
				{Name: "b", ContentType: "image/png", Content: []byte("b")},
			},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := w.Send(tc.msg); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestSendRetriesWhenWagoRefuses(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"INTERNAL_SERVER_ERROR","message":"busy"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"message_id":"ok"}}`))
	}))
	defer server.Close()
	w, _ := New(nil, Opts{Client: testClient(t, server.URL), Lo: testLogger()})
	w.retryBackoff = time.Millisecond
	err := w.Send(models.OutboundMessage{Content: "hi", ContentType: models.ContentTypeText, Meta: json.RawMessage(`{"whatsapp":{"to_phone":"1"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestSendSurfacesPermanentWagoError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"INVALID_REQUEST","message":"bad phone"}`))
	}))
	defer server.Close()
	w, _ := New(nil, Opts{Client: testClient(t, server.URL), Lo: testLogger()})
	err := w.Send(models.OutboundMessage{Content: "hi", ContentType: models.ContentTypeText, Meta: json.RawMessage(`{"whatsapp":{"to_phone":"1"}}`)})
	if err == nil || !strings.Contains(err.Error(), "bad phone") {
		t.Fatalf("expected the WAGO error, got %v", err)
	}
}

func TestMediaKind(t *testing.T) {
	cases := map[string]string{
		"image/jpeg": "image", "image/webp": "image",
		"video/mp4": "video", "audio/ogg": "audio",
		"application/pdf": "file", "": "file",
	}
	for mime, want := range cases {
		if got := mediaKind(attachment.Attachment{ContentType: mime}); got != want {
			t.Errorf("%s: expected %s, got %s", mime, want, got)
		}
	}
}

func TestRejectMediaReason(t *testing.T) {
	if got := RejectMediaReason("a.pdf", "application/pdf", 1024); got != "" {
		t.Fatalf("expected a small PDF to pass, got %q", got)
	}
	if got := RejectMediaReason("big.pdf", "application/pdf", maxDocumentBytes); !strings.Contains(got, "exceeds") {
		t.Fatalf("expected a size rejection, got %q", got)
	}
}

func TestParseSendMeta(t *testing.T) {
	for _, raw := range []string{
		`{"whatsapp":{"to_phone":"1","reply_to_wa_message_id":"2"}}`,
		`{"to_phone":"1","reply_to_wa_message_id":"2"}`,
	} {
		meta, err := parseSendMeta(json.RawMessage(raw))
		if err != nil || meta.ToPhone != "1" || meta.ReplyToWAMessageID != "2" {
			t.Fatalf("%s: unexpected meta %+v, err %v", raw, meta, err)
		}
	}
}

func TestTextBody(t *testing.T) {
	if got := textBody(models.OutboundMessage{ContentType: models.ContentTypeHTML, Content: "<p>Hello <b>there</b></p>"}); got != "Hello *there*" {
		t.Fatalf("unexpected HTML rendering: %q", got)
	}
	if got := textBody(models.OutboundMessage{TextContent: "plain"}); got != "plain" {
		t.Fatalf("unexpected text body: %q", got)
	}
}
