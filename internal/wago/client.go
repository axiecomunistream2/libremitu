package wago

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/zerodha/logf"
)

const (
	defaultTimeout = 30 * time.Second
	mediaTimeout   = 5 * time.Minute
	maxJSONBytes   = 4 * 1024 * 1024
	maxQRBytes     = 4 * 1024 * 1024
)

type Opts struct {
	BaseURL  string
	DeviceID string
	Username string
	Password string
}

type Client struct {
	baseURL         string
	deviceID        string
	username        string
	password        string
	httpClient      *http.Client
	mediaHTTPClient *http.Client
	lo              *logf.Logger
}

func New(lo *logf.Logger, opts Opts) *Client {
	return &Client{
		baseURL:         strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"),
		deviceID:        strings.TrimSpace(opts.DeviceID),
		username:        opts.Username,
		password:        opts.Password,
		httpClient:      &http.Client{Timeout: defaultTimeout},
		mediaHTTPClient: &http.Client{Timeout: mediaTimeout},
		lo:              lo,
	}
}

func (c *Client) Configured() bool {
	return c != nil && c.baseURL != "" && c.deviceID != ""
}

func (c *Client) DeviceID() string {
	if c == nil {
		return ""
	}
	return c.deviceID
}

func (c *Client) endpoint(path string) string {
	return c.baseURL + path
}

func (c *Client) newRequest(ctx context.Context, method, rawURL string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	if c.deviceID != "" {
		req.Header.Set("X-Device-Id", c.deviceID)
	}
	return req, nil
}

type envelope struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Results json.RawMessage `json:"results"`
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding wago request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := c.newRequest(ctx, method, c.endpoint(path), reader)
	if err != nil {
		return nil, fmt.Errorf("building wago request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling wago api: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
	if err != nil {
		return nil, fmt.Errorf("reading wago response: %w", err)
	}

	var env envelope
	if len(respBody) > 0 {
		_ = json.Unmarshal(respBody, &env)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{StatusCode: resp.StatusCode, Code: env.Code, Message: env.Message}
	}
	if env.Code != "" && !strings.EqualFold(env.Code, "SUCCESS") {
		return nil, &APIError{StatusCode: resp.StatusCode, Code: env.Code, Message: env.Message}
	}
	return env.Results, nil
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	raw, err := c.doJSON(ctx, http.MethodGet, "/devices/"+url.PathEscape(c.deviceID)+"/status", nil)
	if err != nil {
		return Status{}, err
	}
	var st Status
	if err := decodeResults(raw, &st); err != nil {
		return Status{}, fmt.Errorf("decoding wago device status: %w", err)
	}
	if st.DeviceID == "" {
		st.DeviceID = c.deviceID
	}
	return st, nil
}

func (c *Client) DeviceInfo(ctx context.Context) (Device, error) {
	raw, err := c.doJSON(ctx, http.MethodGet, "/devices/"+url.PathEscape(c.deviceID), nil)
	if err != nil {
		return Device{}, err
	}
	var d Device
	if err := decodeResults(raw, &d); err != nil {
		return Device{}, fmt.Errorf("decoding wago device: %w", err)
	}
	return d, nil
}

func (c *Client) DeviceState(ctx context.Context) (DeviceState, error) {
	st, err := c.Status(ctx)
	if err != nil {
		return DeviceState{}, err
	}
	info, err := c.DeviceInfo(ctx)
	if err != nil {
		return DeviceState{IsConnected: st.IsConnected, IsLoggedIn: st.IsLoggedIn}, err
	}
	return DeviceState{Device: info, IsConnected: st.IsConnected, IsLoggedIn: st.IsLoggedIn}, nil
}

func (c *Client) Login(ctx context.Context) (LoginQR, error) {
	raw, err := c.doJSON(ctx, http.MethodGet, "/app/login", nil)
	if err != nil {
		return LoginQR{}, err
	}
	var out struct {
		QRLink   string `json:"qr_link"`
		Duration int    `json:"qr_duration"`
	}
	if err := decodeResults(raw, &out); err != nil {
		return LoginQR{}, fmt.Errorf("decoding wago login response: %w", err)
	}
	if out.QRLink == "" {
		return LoginQR{}, fmt.Errorf("wago login returned no qr link")
	}
	dataURL, err := c.fetchQR(ctx, out.QRLink)
	if err != nil {
		return LoginQR{}, err
	}
	return LoginQR{DataURL: dataURL, DurationSec: out.Duration}, nil
}

// fetchQR reads the QR image server-side so the browser never needs WAGO credentials.
func (c *Client) fetchQR(ctx context.Context, qrLink string) (string, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("parsing wago base url: %w", err)
	}
	qr, err := url.Parse(qrLink)
	if err != nil || qr.Path == "" {
		return "", fmt.Errorf("wago returned malformed qr link %q", qrLink)
	}
	// WAGO builds qr_link from the request host, which may differ behind a proxy.
	target := *base
	target.Path = qr.Path
	target.RawQuery = qr.RawQuery
	req, err := c.newRequest(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching wago qr image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("wago qr image returned status %d", resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxQRBytes))
	if err != nil {
		return "", fmt.Errorf("reading wago qr image: %w", err)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/png"
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(body), nil
}

func (c *Client) Logout(ctx context.Context) error {
	_, err := c.doJSON(ctx, http.MethodPost, "/devices/"+url.PathEscape(c.deviceID)+"/logout", nil)
	return err
}

func (c *Client) Reconnect(ctx context.Context) error {
	_, err := c.doJSON(ctx, http.MethodPost, "/devices/"+url.PathEscape(c.deviceID)+"/reconnect", nil)
	return err
}

func (c *Client) SendText(ctx context.Context, phone, message, replyToID string) (string, error) {
	payload := map[string]any{
		"phone":   phone,
		"message": message,
	}
	if replyToID != "" {
		payload["reply_message_id"] = replyToID
	}
	raw, err := c.doJSON(ctx, http.MethodPost, "/send/message", payload)
	if err != nil {
		return "", err
	}
	return decodeMessageID(raw)
}

// SendMedia sends one file. kind is image, video, audio, or file.
func (c *Client) SendMedia(ctx context.Context, phone, kind, filename, contentType, caption string, content []byte) (string, error) {
	if kind != "image" && kind != "video" && kind != "audio" && kind != "file" {
		return "", fmt.Errorf("unsupported wago media kind %q", kind)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("phone", phone); err != nil {
		return "", err
	}
	if caption != "" && kind != "audio" {
		if err := mw.WriteField("caption", caption); err != nil {
			return "", err
		}
	}

	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, kind, filename))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	partHeader.Set("Content-Type", contentType)
	fw, err := mw.CreatePart(partHeader)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(content); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := c.newRequest(ctx, http.MethodPost, c.endpoint("/send/"+kind), &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.mediaHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending wago media: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
	if err != nil {
		return "", fmt.Errorf("reading wago media response: %w", err)
	}
	var env envelope
	if len(respBody) > 0 {
		_ = json.Unmarshal(respBody, &env)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (env.Code != "" && !strings.EqualFold(env.Code, "SUCCESS")) {
		return "", &APIError{StatusCode: resp.StatusCode, Code: env.Code, Message: env.Message}
	}
	return decodeMessageID(env.Results)
}

func (c *Client) MarkRead(ctx context.Context, messageID, phone string) error {
	_, err := c.doJSON(ctx, http.MethodPost, "/message/"+url.PathEscape(messageID)+"/read", map[string]any{"phone": phone})
	return err
}

// DownloadMedia fetches a file from WAGO's statics directory. Only the configured host is allowed.
func (c *Client) DownloadMedia(ctx context.Context, mediaPath string, maxBytes int64) ([]byte, error) {
	target, err := c.resolveMediaURL(mediaPath)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.mediaHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading wago media: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		var env envelope
		_ = json.Unmarshal(body, &env)
		return nil, &APIError{StatusCode: resp.StatusCode, Code: env.Code, Message: env.Message}
	}

	limit := int64(100 * 1024 * 1024)
	if maxBytes > 0 && maxBytes < limit {
		limit = maxBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading wago media: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: %d bytes", ErrMediaTooLarge, limit)
	}
	return body, nil
}

func (c *Client) resolveMediaURL(mediaPath string) (string, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("parsing wago base url: %w", err)
	}
	ref, err := url.Parse(mediaPath)
	if err != nil {
		return "", fmt.Errorf("parsing wago media path: %w", err)
	}
	if ref.IsAbs() {
		if !strings.EqualFold(ref.Host, base.Host) || ref.Scheme != base.Scheme {
			return "", fmt.Errorf("refusing to download wago media from unexpected host %q", ref.Host)
		}
		return ref.String(), nil
	}
	return base.JoinPath(ref.Path).String(), nil
}

func decodeResults(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func decodeMessageID(raw json.RawMessage) (string, error) {
	var out struct {
		MessageID string `json:"message_id"`
	}
	if err := decodeResults(raw, &out); err != nil {
		return "", fmt.Errorf("decoding wago send response: %w", err)
	}
	if out.MessageID == "" {
		return "", fmt.Errorf("no message id in wago send response")
	}
	return out.MessageID, nil
}
