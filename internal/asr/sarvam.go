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

// sarvamEndpoint is Sarvam's speech-to-text REST endpoint.
//
// Verified against docs.sarvam.ai/api-reference/speech-to-text/transcribe on
// 2026-10-19. Sarvam ships SDKs and frequent model changes, so re-check this
// before trusting it in production.
const sarvamEndpoint = "https://api.sarvam.ai/speech-to-text"

// SarvamConfig configures the Sarvam client.
type SarvamConfig struct {
	// APIKey is the Sarvam API subscription key. Sent as the
	// api-subscription-key header, which is NOT the Authorization bearer
	// style used by most providers.
	APIKey string

	// Endpoint overrides the API URL. Tests set this to an httptest server.
	Endpoint string

	// HTTPClient overrides the HTTP client.
	HTTPClient *http.Client

	// LanguageCode is a BCP-47 code such as "hi-IN". Leave empty to let
	// Sarvam auto-detect.
	//
	// Auto-detection costs accuracy on code-mixed Hinglish, because most of a
	// sentence like "mere liye cab book karo" is Latin script with a Hindi
	// accent. Setting this explicitly is the safer default once the language
	// is decided.
	LanguageCode string

	// Model selects the Sarvam model. "saaras:v4" is the current default and
	// the only one supporting keyterms.
	Model string

	// Keyterms biases recognition toward domain words such as "New Delhi" or
	// a saved place name. Up to 50 terms, 64 characters each.
	//
	// This is the single highest-leverage setting for this product. Family
	// place names are exactly the vocabulary a general model gets wrong, and
	// a wrong "mandir" is a cab to the wrong place.
	Keyterms []string

	// Timeout bounds a single call.
	Timeout time.Duration
}

// Sarvam is a Transcriber backed by Sarvam AI.
type Sarvam struct {
	cfg  SarvamConfig
	http *http.Client
}

// compile-time assertion that Sarvam satisfies the interface.
var _ Transcriber = (*Sarvam)(nil)

// NewSarvam returns a Sarvam transcriber.
func NewSarvam(cfg SarvamConfig) *Sarvam {
	if cfg.Endpoint == "" {
		cfg.Endpoint = sarvamEndpoint
	}
	if cfg.Model == "" {
		cfg.Model = "saaras:v4"
	}
	if cfg.Timeout == 0 {
		// Voice notes are seconds long. Thirty seconds is generous, and a
		// bounded call is what stops a stalled provider from pinning a worker.
		cfg.Timeout = 30 * time.Second
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}

	return &Sarvam{cfg: cfg, http: httpClient}
}

// Name implements Transcriber.
func (s *Sarvam) Name() string { return "sarvam" }

// sarvamResponse is the documented response shape.
type sarvamResponse struct {
	RequestID string `json:"request_id"`
	// Transcript is the transcribed text.
	Transcript string `json:"transcript"`
	// LanguageCode is the BCP-47 code of the language spoken.
	LanguageCode string `json:"language_code"`
	// LanguageProbability is the provider's confidence in the detected
	// language, not in the transcription itself.
	//
	// This distinction matters: Sarvam does not return a per-utterance ASR
	// confidence, so Result.Confidence stays ConfidenceUnknown unless
	// LanguageCode was left unset.
	LanguageProbability *float64 `json:"language_probability"`
}

// sarvamError is the documented error envelope.
type sarvamError struct {
	Error struct {
		RequestID string `json:"request_id"`
		Message   string `json:"message"`
		Code      string `json:"code"`
	} `json:"error"`
}

// Transcribe implements Transcriber.
func (s *Sarvam) Transcribe(ctx context.Context, audio Audio) (Result, error) {
	if err := audio.validate(); err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	body, contentType, err := s.buildForm(audio)
	if err != nil {
		return Result{}, fmt.Errorf("sarvam: build request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoint, body)
	if err != nil {
		return Result{}, fmt.Errorf("sarvam: build request: %w", err)
	}
	req.Header.Set("api-subscription-key", s.cfg.APIKey)
	req.Header.Set("Content-Type", contentType)

	start := time.Now()
	resp, err := s.http.Do(req)
	if err != nil {
		// Transport errors are the strongest signal that the provider is
		// temporarily unavailable rather than permanently broken.
		return Result{}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("sarvam: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Result{}, sarvamStatusError(resp.StatusCode, raw)
	}

	var decoded sarvamResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Result{}, fmt.Errorf("sarvam: decode response: %w", err)
	}

	result := Result{
		Text:       strings.TrimSpace(decoded.Transcript),
		Language:   decoded.LanguageCode,
		Confidence: ConfidenceUnknown,
		Provider:   s.Name(),
		Latency:    time.Since(start),
	}

	if result.Text == "" {
		return result, ErrNoSpeech
	}

	// Only use language_probability when we asked Sarvam to detect the
	// language. When a specific code was supplied it is a constant the caller
	// chose, and its probability carries no information about the transcript.
	if s.cfg.LanguageCode == "" && decoded.LanguageProbability != nil {
		result.Confidence = *decoded.LanguageProbability
	}

	return result, nil
}

// buildForm assembles the multipart request.
//
// The file field is named "file", per the documented API.
func (s *Sarvam) buildForm(audio Audio) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	filename := "voice." + audio.formatOrDefault()
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(audio.Data); err != nil {
		return nil, "", err
	}

	if s.cfg.Model != "" {
		_ = writer.WriteField("model", s.cfg.Model)
	}
	if s.cfg.LanguageCode != "" {
		_ = writer.WriteField("language_code", s.cfg.LanguageCode)
	}
	if len(s.cfg.Keyterms) > 0 {
		// keyterms is a JSON-encoded array in a single form field. Sending
		// comma-separated text, which older docs suggested, is ignored.
		encoded, err := json.Marshal(s.cfg.Keyterms)
		if err != nil {
			return nil, "", err
		}
		_ = writer.WriteField("keyterms", string(encoded))
	}

	if err := writer.Close(); err != nil {
		return nil, "", err
	}

	return &buf, writer.FormDataContentType(), nil
}

// sarvamStatusError maps an HTTP failure onto a sentinel.
func sarvamStatusError(status int, raw []byte) error {
	var envelope sarvamError
	_ = json.Unmarshal(raw, &envelope)

	message := envelope.Error.Message
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}

	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("%w: sarvam status %d: %s", ErrProviderUnavailable, status, message)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: sarvam status %d: %s", ErrUnsupportedAudio, status, message)
	default:
		return fmt.Errorf("sarvam: unexpected status %d: %s", status, message)
	}
}

// formatOrDefault guesses a file extension from the MIME type or format field.
func (a Audio) formatOrDefault() string {
	if a.Format != "" {
		return a.Format
	}
	lower := strings.ToLower(a.MimeType)
	switch {
	case strings.Contains(lower, "ogg"):
		return "ogg"
	case strings.Contains(lower, "mpeg"), strings.Contains(lower, "mp3"):
		return "mp3"
	case strings.Contains(lower, "wav"):
		return "wav"
	case strings.Contains(lower, "m4a"), strings.Contains(lower, "mp4"):
		return "m4a"
	case strings.Contains(lower, "webm"):
		return "webm"
	case strings.Contains(lower, "amr"):
		return "amr"
	default:
		return "ogg"
	}
}
