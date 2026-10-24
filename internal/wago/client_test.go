package wago

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zerodha/logf"
)

func testLogger() *logf.Logger {
	lo := logf.New(logf.Opts{Level: logf.FatalLevel})
	return &lo
}

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	return New(testLogger(), Opts{BaseURL: baseURL, DeviceID: "dev-1", Username: "user", Password: "pass"})
}

func TestStatusAndDevice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Device-Id") != "dev-1" {
			t.Errorf("missing device header")
		}
		if u, p, ok := r.BasicAuth(); !ok || u != "user" || p != "pass" {
			t.Errorf("missing basic auth")
		}
		switch r.URL.Path {
		case "/devices/dev-1/status":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"device_id":"dev-1","is_connected":true,"is_logged_in":true}}`))
		case "/devices/dev-1":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"id":"dev-1","phone_number":"628111","display_name":"Desk","state":"logged_in","jid":"628111@s.whatsapp.net"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	st, err := client.Status(context.Background())
	if err != nil || !st.IsConnected || !st.IsLoggedIn || st.DeviceID != "dev-1" {
		t.Fatalf("unexpected status: %+v, %v", st, err)
	}
	info, err := client.DeviceInfo(context.Background())
	if err != nil || info.PhoneNumber != "628111" || info.DisplayName != "Desk" {
		t.Fatalf("unexpected device: %+v, %v", info, err)
	}
	state, err := client.DeviceState(context.Background())
	if err != nil || !state.IsLoggedIn || state.ID != "dev-1" {
		t.Fatalf("unexpected device state: %+v, %v", state, err)
	}
}

func TestSendText(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send/message" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"message_id":"wamid.1"}}`))
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	id, err := client.SendText(context.Background(), "628111", "Hello", "reply-1")
	if err != nil || id != "wamid.1" {
		t.Fatalf("unexpected send: %q, %v", id, err)
	}
	if payload["phone"] != "628111" || payload["message"] != "Hello" || payload["reply_message_id"] != "reply-1" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestSendMedia(t *testing.T) {
	var gotPath, gotField, gotCaption, gotFilename, gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		f, header, err := r.FormFile("image")
		if err != nil {
			t.Fatalf("reading part: %v", err)
		}
		defer f.Close()
		body, _ := io.ReadAll(f)
		gotField = string(body)
		gotCaption = r.FormValue("caption")
		gotFilename = header.Filename
		gotContentType = header.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"message_id":"wamid.2"}}`))
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	id, err := client.SendMedia(context.Background(), "628111", "image", "a.jpg", "image/jpeg", "Look", []byte("bytes"))
	if err != nil || id != "wamid.2" {
		t.Fatalf("unexpected send: %q, %v", id, err)
	}
	if gotPath != "/send/image" || gotField != "bytes" || gotCaption != "Look" || gotFilename != "a.jpg" || gotContentType != "image/jpeg" {
		t.Fatalf("unexpected media request: %s %s %s %s %s", gotPath, gotField, gotCaption, gotFilename, gotContentType)
	}
}

func TestLoginReturnsDataURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/login":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"qr_link":"` + "http://example.invalid" + `/statics/qr.png","qr_duration":30}}`))
		case "/statics/qr.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	qr, err := client.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(qr.DataURL, "data:image/png;base64,") || qr.DurationSec != 30 {
		t.Fatalf("unexpected QR: %+v", qr)
	}
}

func TestDownloadMedia(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("media"))
	}))
	defer server.Close()

	client := testClient(t, server.URL+"/gowa")
	body, err := client.DownloadMedia(context.Background(), "statics/media/x.jpg", 0)
	if err != nil || string(body) != "media" {
		t.Fatalf("unexpected download: %q, %v", body, err)
	}
	if gotPath != "/gowa/statics/media/x.jpg" {
		t.Fatalf("unexpected path %s", gotPath)
	}

	if _, err := client.DownloadMedia(context.Background(), "statics/media/x.jpg", 3); err == nil {
		t.Fatal("expected an oversize error")
	}
}

func TestDownloadMediaRejectsForeignHost(t *testing.T) {
	client := testClient(t, "https://wago.example.com")
	if _, err := client.DownloadMedia(context.Background(), "https://evil.example.com/x", 100); err == nil {
		t.Fatal("expected a foreign host to be rejected")
	}
}

func TestAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"INVALID_REQUEST","message":"bad phone"}`))
	}))
	defer server.Close()
	_, err := testClient(t, server.URL).SendText(context.Background(), "x", "hi", "")
	var apiErr *APIError
	if err == nil || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest || apiErr.Message != "bad phone" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMarkReadAndDeviceActions(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{"code":"SUCCESS","results":null}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL)
	ctx := context.Background()
	if err := client.MarkRead(ctx, "wamid.1", "628111"); err != nil {
		t.Fatal(err)
	}
	if err := client.Reconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /message/wamid.1/read", "POST /devices/dev-1/reconnect", "POST /devices/dev-1/logout"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected calls: %v", paths)
	}
}

func TestTimeoutsAreFinite(t *testing.T) {
	client := testClient(t, "https://wago.example.com")
	if client.httpClient.Timeout <= 0 || client.mediaHTTPClient.Timeout <= client.httpClient.Timeout {
		t.Fatal("expected finite request timeouts with a longer media timeout")
	}
}
