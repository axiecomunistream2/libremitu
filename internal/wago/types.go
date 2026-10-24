// Package wago talks to a go-whatsapp-web-multidevice (WAGO) instance.
package wago

import (
	"errors"
	"fmt"
	"time"
)

// ErrMediaTooLarge is permanent; retrying cannot help.
var ErrMediaTooLarge = errors.New("media exceeds the configured size limit")

// APIError is a non-2xx response from WAGO.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("wago api returned status %d", e.StatusCode)
}

type Status struct {
	DeviceID    string `json:"device_id"`
	IsConnected bool   `json:"is_connected"`
	IsLoggedIn  bool   `json:"is_logged_in"`
}

type Device struct {
	ID          string `json:"id"`
	PhoneNumber string `json:"phone_number"`
	DisplayName string `json:"display_name"`
	State       string `json:"state"`
	JID         string `json:"jid"`
}

// DeviceState combines the device record and its live connection status.
type DeviceState struct {
	Device
	IsConnected bool `json:"is_connected"`
	IsLoggedIn  bool `json:"is_logged_in"`
}

type LoginQR struct {
	DataURL     string `json:"qr_data_url"`
	DurationSec int    `json:"qr_duration"`
}

type ParsedMessage struct {
	ID            string
	From          string
	ChatID        string
	ContactName   string
	Timestamp     time.Time
	Type          string
	Text          string
	ReplyToID     string
	MediaPath     string
	MediaMIMEType string
	MediaFilename string
}

type ParsedAck struct {
	MessageIDs []string
	Status     string
	Timestamp  time.Time
	Error      string
}
