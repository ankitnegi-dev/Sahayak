package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultBaseURL is the Graph API endpoint. Overridable in Client for tests.
const defaultBaseURL = "https://graph.facebook.com/v21.0"

// maxMediaBytes caps a downloaded audio file. WhatsApp voice notes are tiny,
// so anything larger means something is wrong or hostile.
const maxMediaBytes = 25 << 20 // 25 MiB

// Client talks to the WhatsApp Cloud API.
//
// There is no official Go SDK for this API. It is a small JSON-over-HTTP
// surface, so a hand-written client is less code than the SDK would pull in,
// and it keeps Meta specifics inside this package.
type Client struct {
	baseURL       string
	phoneNumberID string
	accessToken   string
	http          *http.Client
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithBaseURL overrides the API endpoint. Used by tests to point at an
// httptest server.
func WithBaseURL(u string) ClientOption {
	return func(c *Client) { c.baseURL = u }
}

// WithHTTPClient overrides the underlying HTTP client.
func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *Client) { c.http = h }
}

// NewClient returns a Client for the given business phone number.
func NewClient(phoneNumberID, accessToken string, opts ...ClientOption) *Client {
	c := &Client{
		baseURL:       defaultBaseURL,
		phoneNumberID: phoneNumberID,
		accessToken:   accessToken,
		http:          &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// APIError is a structured error returned by the Graph API.
type APIError struct {
	StatusCode int
	Code       int
	Type       string
	Message    string
	Subcode    int
	UserTitle  string
}

func (e *APIError) Error() string {
	parts := []string{fmt.Sprintf("whatsapp api: status %d", e.StatusCode)}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Code != 0 {
		parts = append(parts, fmt.Sprintf("(code %d", e.Code))
		if e.Subcode != 0 {
			parts = append(parts, fmt.Sprintf(", subcode %d", e.Subcode))
		}
		parts = append(parts, ")")
	}
	return strings.Join(parts, " ")
}

// sendRequest is the shared request path for the Graph API.
func (c *Client) sendRequest(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("whatsapp: marshal request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}

	endpoint := fmt.Sprintf("%s/%s?access_token=%s",
		strings.TrimRight(c.baseURL, "/"),
		strings.TrimLeft(path, "/"),
		url.QueryEscape(c.accessToken),
	)

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("whatsapp: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	// Cap the response so a misbehaving endpoint cannot exhaust memory.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("whatsapp: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp.StatusCode, raw)
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("whatsapp: decode response: %w", err)
	}
	return nil
}

func parseAPIError(statusCode int, raw []byte) error {
	apiErr := &APIError{StatusCode: statusCode}

	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    int    `json:"code"`
			Subcode int    `json:"error_subcode"`
			Data    struct {
				Details string `json:"details"`
			} `json:"error_data"`
		} `json:"error"`
	}

	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Message != "" {
		apiErr.Message = envelope.Error.Message
		apiErr.Type = envelope.Error.Type
		apiErr.Code = envelope.Error.Code
		apiErr.Subcode = envelope.Error.Subcode
		apiErr.UserTitle = envelope.Error.Data.Details
	} else {
		apiErr.Message = strings.TrimSpace(string(raw))
	}

	return apiErr
}

// textMessageRequest is the body for sending a text reply.
type textMessageRequest struct {
	MessagingProduct string `json:"messaging_product"`
	To               string `json:"to"`
	Type             string `json:"type"`
	Text             struct {
		Body string `json:"body"`
	} `json:"text"`
}

// SendTextMessage replies to a parent with plain text.
//
// to is the parent's WhatsApp ID in international form without a leading plus,
// for example "919812345678".
func (c *Client) SendTextMessage(ctx context.Context, to, body string) error {
	if strings.TrimSpace(to) == "" {
		return errors.New("whatsapp: recipient is required")
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("whatsapp: message body is required")
	}

	req := textMessageRequest{
		MessagingProduct: "whatsapp",
		To:               to,
		Type:             "text",
	}
	req.Text.Body = body

	path := fmt.Sprintf("%s/messages", c.phoneNumberID)
	if err := c.sendRequest(ctx, http.MethodPost, path, req, nil); err != nil {
		return fmt.Errorf("send text to %s: %w", to, err)
	}
	return nil
}

// mediaInfo is the response from GET /{media-id}.
type mediaInfo struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
	SHA256   string `json:"sha256"`
	FileSize int64  `json:"file_size"`
	URL      string `json:"url"`
}

// GetMediaURL resolves a Meta media ID into a temporary download URL.
//
// The URL is short lived and requires the access token to fetch, so it cannot
// be stored or sent to a worker as-is. Phase 3 uses this inside the worker.
func (c *Client) GetMediaURL(ctx context.Context, mediaID string) (string, error) {
	if strings.TrimSpace(mediaID) == "" {
		return "", errors.New("whatsapp: media id is required")
	}

	var info mediaInfo
	if err := c.sendRequest(ctx, http.MethodGet, mediaID, nil, &info); err != nil {
		return "", fmt.Errorf("get media %s: %w", mediaID, err)
	}

	if info.URL == "" {
		return "", fmt.Errorf("whatsapp: media %s returned no url: %w", mediaID, ErrNoMediaURL)
	}
	return info.URL, nil
}

// ErrNoMediaURL means Meta resolved a media ID but gave no download URL.
var ErrNoMediaURL = errors.New("no media url returned")

// DownloadMedia fetches audio bytes from a URL obtained from GetMediaURL.
//
// Two protections matter here. The size is capped so a hostile or broken
// response cannot exhaust memory, and context cancellation propagates so a
// cancelled job stops reading immediately.
func (c *Client) DownloadMedia(ctx context.Context, mediaURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: build media request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: download media: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{StatusCode: resp.StatusCode, Message: resp.Status}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes))
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read media: %w", err)
	}
	return data, nil
}
