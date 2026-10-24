package wago

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestParseTextMessage(t *testing.T) {
	body := []byte(`{
		"event":"message",
		"device_id":"dev-1",
		"payload":{
			"id":"wamid.1",
			"chat_id":"628111@s.whatsapp.net",
			"from":"628111@s.whatsapp.net",
			"from_name":"Alice",
			"timestamp":"2026-01-02T03:04:05Z",
			"is_from_me":false,
			"body":"Hello"
		}
	}`)
	p, err := ParsePayload(body)
	if err != nil {
		t.Fatal(err)
	}
	msgs := p.ExtractMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.ID != "wamid.1" || m.From != "628111" || m.ContactName != "Alice" || m.Type != "text" || m.Text != "Hello" {
		t.Fatalf("unexpected message: %+v", m)
	}
	if m.Timestamp.Year() != 2026 {
		t.Fatalf("unexpected timestamp: %s", m.Timestamp)
	}
}

func TestParseMediaMessage(t *testing.T) {
	cases := []struct {
		name     string
		payload  string
		wantType string
		wantPath string
		wantMIME string
	}{
		{
			name:     "auto-downloaded image",
			payload:  `{"id":"i1","chat_id":"628111@s.whatsapp.net","from":"628111@s.whatsapp.net","timestamp":"2026-01-02T03:04:05Z","image":"statics/media/a.jpeg"}`,
			wantType: "image",
			wantPath: "statics/media/a.jpeg",
			wantMIME: "image/jpeg",
		},
		{
			name:     "image with caption object",
			payload:  `{"id":"i2","chat_id":"628111@s.whatsapp.net","from":"628111@s.whatsapp.net","body":"Look","image":{"path":"statics/media/b.png","caption":"Look"}}`,
			wantType: "image",
			wantPath: "statics/media/b.png",
			wantMIME: "image/png",
		},
		{
			name:     "document strips original name",
			payload:  `{"id":"d1","chat_id":"628111@s.whatsapp.net","from":"628111@s.whatsapp.net","document":{"path":"statics/media/c.pdf","caption":"Report"}}`,
			wantType: "document",
			wantPath: "statics/media/c.pdf",
			wantMIME: "application/pdf",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParsePayload([]byte(`{"event":"message","payload":` + tc.payload + `}`))
			if err != nil {
				t.Fatal(err)
			}
			msgs := p.ExtractMessages()
			if len(msgs) != 1 {
				t.Fatalf("expected one message, got %d", len(msgs))
			}
			m := msgs[0]
			if m.Type != tc.wantType || m.MediaPath != tc.wantPath || m.MediaMIMEType != tc.wantMIME {
				t.Fatalf("unexpected media message: %+v", m)
			}
			if m.MediaFilename == "" {
				t.Fatal("expected a filename")
			}
		})
	}
}

func TestIgnoresSelfAndGroups(t *testing.T) {
	cases := []string{
		`{"event":"message","payload":{"id":"1","chat_id":"628111@s.whatsapp.net","from":"628111@s.whatsapp.net","is_from_me":true,"body":"hi"}}`,
		`{"event":"message","payload":{"id":"2","chat_id":"120363@g.us","from":"628111@s.whatsapp.net","body":"hi"}}`,
		`{"event":"message.reaction","payload":{"id":"3","chat_id":"628111@s.whatsapp.net","from":"628111@s.whatsapp.net","reaction":"x"}}`,
	}
	for _, raw := range cases {
		p, err := ParsePayload([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := p.ExtractMessages(); len(got) != 0 {
			t.Fatalf("%s: expected no messages, got %+v", raw, got)
		}
	}
}

func TestParseLocationAndContact(t *testing.T) {
	p, err := ParsePayload([]byte(`{"event":"message","payload":{"id":"l1","chat_id":"628111@s.whatsapp.net","from":"628111@s.whatsapp.net","location":{"degreesLatitude":-6.2,"degreesLongitude":106.8,"name":"Office"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	msgs := p.ExtractMessages()
	if len(msgs) != 1 || msgs[0].Type != "location" || msgs[0].Text == "" {
		t.Fatalf("unexpected location: %+v", msgs)
	}

	p, err = ParsePayload([]byte(`{"event":"message","payload":{"id":"c1","chat_id":"628111@s.whatsapp.net","from":"628111@s.whatsapp.net","contact":{"displayName":"Bob","phone_number":"628222"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	msgs = p.ExtractMessages()
	if len(msgs) != 1 || msgs[0].Type != "contact" || msgs[0].Text != "Bob 628222" {
		t.Fatalf("unexpected contact: %+v", msgs)
	}
}

func TestUnsupportedMessage(t *testing.T) {
	p, err := ParsePayload([]byte(`{"event":"message","payload":{"id":"u1","chat_id":"628111@s.whatsapp.net","from":"628111@s.whatsapp.net"}}`))
	if err != nil {
		t.Fatal(err)
	}
	msgs := p.ExtractMessages()
	if len(msgs) != 1 || msgs[0].Type != "unsupported" {
		t.Fatalf("unexpected unsupported message: %+v", msgs)
	}
}

func TestExtractAcks(t *testing.T) {
	p, err := ParsePayload([]byte(`{"event":"message.ack","timestamp":"2026-01-02T03:04:05Z","payload":{"ids":["a","b"],"receipt_type":"read"}}`))
	if err != nil {
		t.Fatal(err)
	}
	acks := p.ExtractAcks()
	if len(acks) != 1 || acks[0].Status != "read" || len(acks[0].MessageIDs) != 2 {
		t.Fatalf("unexpected acks: %+v", acks)
	}

	p, _ = ParsePayload([]byte(`{"event":"message.ack","payload":{"ids":["a"],"receipt_type":"sender"}}`))
	if got := p.ExtractAcks(); len(got) != 0 {
		t.Fatalf("sender receipts must be ignored, got %+v", got)
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"event":"message"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !VerifySignature(body, sig, "secret") {
		t.Fatal("expected a valid signature")
	}
	if VerifySignature(body, sig, "other") {
		t.Fatal("wrong secret must fail")
	}
	if VerifySignature(body, "", "secret") {
		t.Fatal("empty signature must fail")
	}
	if VerifySignature(body, sig, "") {
		t.Fatal("empty secret must fail")
	}
}
