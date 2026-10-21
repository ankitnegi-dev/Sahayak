package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

const openAITranscriptionsEndpoint = "https://api.openai.com/v1/audio/transcriptions"

// OpenAIConfig configures the OpenAI Whisper transcriber.
type OpenAIConfig struct {
	// APIKey is sent as an Authorization bearer token.
	APIKey string

	// Endpoint overrides the API URL. Tests set this to an httptest server.
	Endpoint string

	HTTPClient *http.Client

	// Model selects the transcription model. "whisper-1" is the general model;
	// "gpt-4o-transcribe" and "gpt-4o-mini-transcribe" are newer options.
	Model string

	// Language is an ISO-639-1 code such as "hi" or "en". Leave empty to let
	// the model detect.
	Language string

	// Prompt biases the model with context. This is Whisper's equivalent of
	// Sarvam's keyterms, and is worth using for saved place names.
	Prompt string

	// ResponseFormat selects the response shape. Only "json" is supported here.
	ResponseFormat string

	Timeout time.Duration
}

// OpenAI is a Transcriber backed by OpenAI Whisper.
type OpenAI struct {
	cfg  OpenAIConfig
	http *http.Client
}

// compile-time assertion that OpenAI satisfies the interface.
var _ Transcriber = (*OpenAI)(nil)

// NewOpenAI returns an OpenAI Whisper transcriber.
func NewOpenAI(cfg OpenAIConfig) *OpenAI {
	if cfg.Endpoint == "" {
		cfg.Endpoint = openAITranscriptionsEndpoint
	}
	if cfg.Model == "" {
		cfg.Model = "whisper-1"
	}
	if cfg.ResponseFormat == "" {
		cfg.ResponseFormat = "json"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}

	return &OpenAI{cfg: cfg, http: httpClient}
}

// Name implements Transcriber.
func (o *OpenAI) Name() string { return "openai-whisper" }

// openAIResponse is the documented JSON response.
type openAIResponse struct {
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
	// Duration is present when response_format is verbose_json.
	Duration float64 `json:"duration,omitempty"`
	Error    *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Transcribe implements Transcriber.
func (o *OpenAI) Transcribe(ctx context.Context, audio Audio) (Result, error) {
	if err := audio.validate(); err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, o.cfg.Timeout)
	defer cancel()

	body, contentType, err := o.buildForm(audio)
	if err != nil {
		return Result{}, fmt.Errorf("openai: build request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.Endpoint, body)
	if err != nil {
		return Result{}, fmt.Errorf("openai: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	req.Header.Set("Content-Type", contentType)

	start := time.Now()
	resp, err := o.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("openai: read response: %w", err)
	}

	var decoded openAIResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Result{}, fmt.Errorf("openai: decode response: %w", err)
	}

	if decoded.Error != nil && decoded.Error.Message != "" {
		return Result{}, fmt.Errorf("openai: api error: %s", decoded.Error.Message)
	}

	if resp.StatusCode != http.StatusOK {
		return Result{}, providerStatusError("openai", resp.StatusCode, raw)
	}

	result := Result{
		Text: strings.TrimSpace(decoded.Text),
		// Whisper returns no confidence for the json response format.
		Language:   decoded.Language,
		Confidence: ConfidenceUnknown,
		Provider:   o.Name(),
		Latency:    time.Since(start),
	}

	if result.Text == "" {
		return result, ErrNoSpeech
	}

	return result, nil
}

func (o *OpenAI) buildForm(audio Audio) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("file", "voice."+audio.formatOrDefault())
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(audio.Data); err != nil {
		return nil, "", err
	}

	_ = writer.WriteField("model", o.cfg.Model)
	if o.cfg.Language != "" {
		_ = writer.WriteField("language", o.cfg.Language)
	}
	if o.cfg.Prompt != "" {
		_ = writer.WriteField("prompt", o.cfg.Prompt)
	}
	_ = writer.WriteField("response_format", o.cfg.ResponseFormat)

	if err := writer.Close(); err != nil {
		return nil, "", err
	}

	return &buf, writer.FormDataContentType(), nil
}

// providerStatusError maps an HTTP failure onto a sentinel for providers that
// share a generic JSON error envelope.
func providerStatusError(provider string, status int, raw []byte) error {
	message := strings.TrimSpace(string(raw))
	if len(message) > 300 {
		message = message[:300]
	}

	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("%w: %s status %d: %s", ErrProviderUnavailable, provider, status, message)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s status %d: %s", ErrUnsupportedAudio, provider, status, message)
	default:
		return fmt.Errorf("%s: unexpected status %d: %s", provider, status, message)
	}
}
