package asr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// googleSTTEndpoint is Google Cloud Speech-to-Text v1.
//
// The v1 endpoint is used rather than v2 because v1 accepts inline base64
// audio with a simpler request shape, which is enough for short voice notes.
const googleSTTEndpoint = "https://speech.googleapis.com/v1/speech:recognize"

// maxInlineAudioBytes bounds inline base64 audio.
//
// The v1 endpoint caps inline payload at about 1 minute / 10 MB. Voice notes
// are far shorter, but an oversized clip means the request will be rejected, so
// failing early with a clear error is better than a confusing API error.
const maxInlineAudioBytes = 5 << 20 // 5 MiB

// GoogleConfig configures the Google Speech-to-Text transcriber.
type GoogleConfig struct {
	// APIKey is sent as a key query parameter.
	APIKey string

	// Endpoint overrides the API URL. Tests set this to an httptest server.
	Endpoint string

	HTTPClient *http.Client

	// LanguageCode is required by Google, such as "hi-IN" or "en-IN".
	//
	// Unlike Sarvam, Google has no auto-detect on the v1 endpoint used here,
	// so this must be set. "hi-IN" is the closest match for the Hinglish this
	// product targets; "en-IN" is the alternative to benchmark against.
	LanguageCode string

	// EnableAutomaticPunctuation asks for punctuation.
	EnableAutomaticPunctuation bool

	// Timeout bounds a single call.
	Timeout time.Duration
}

// Google is a Transcriber backed by Google Cloud Speech-to-Text.
type Google struct {
	cfg  GoogleConfig
	http *http.Client
}

// compile-time assertion that Google satisfies the interface.
var _ Transcriber = (*Google)(nil)

// NewGoogle returns a Google Speech-to-Text transcriber.
func NewGoogle(cfg GoogleConfig) *Google {
	if cfg.Endpoint == "" {
		cfg.Endpoint = googleSTTEndpoint
	}
	if cfg.LanguageCode == "" {
		cfg.LanguageCode = "hi-IN"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}

	return &Google{cfg: cfg, http: httpClient}
}

// Name implements Transcriber.
func (g *Google) Name() string { return "google-stt" }

// googleRequest is the documented request shape for inline audio.
type googleRequest struct {
	Config struct {
		Encoding                   string `json:"encoding"`
		SampleRateHertz            int    `json:"sampleRateHertz,omitempty"`
		LanguageCode               string `json:"languageCode"`
		EnableAutomaticPunctuation bool   `json:"enableAutomaticPunctuation"`
	} `json:"config"`
	Audio struct {
		Content string `json:"content"`
	} `json:"audio"`
}

// googleResponse is the documented response shape.
type googleResponse struct {
	Results []struct {
		Alternatives []struct {
			Transcript string  `json:"transcript"`
			Confidence float64 `json:"confidence"`
		} `json:"alternatives"`
	} `json:"results"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

// Transcribe implements Transcriber.
func (g *Google) Transcribe(ctx context.Context, audio Audio) (Result, error) {
	if err := audio.validate(); err != nil {
		return Result{}, err
	}

	if len(audio.Data) > maxInlineAudioBytes {
		return Result{}, fmt.Errorf("%w: audio is %d bytes, over the inline limit",
			ErrUnsupportedAudio, len(audio.Data))
	}

	ctx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	defer cancel()

	payload := googleRequest{}
	payload.Config.Encoding = audio.googleEncoding()
	payload.Config.SampleRateHertz = audio.SampleRate
	payload.Config.LanguageCode = g.cfg.LanguageCode
	payload.Config.EnableAutomaticPunctuation = g.cfg.EnableAutomaticPunctuation
	payload.Audio.Content = base64.StdEncoding.EncodeToString(audio.Data)

	encoded, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("google: marshal request: %w", err)
	}

	endpoint := g.cfg.Endpoint
	if g.cfg.APIKey != "" {
		endpoint += "?key=" + url.QueryEscape(g.cfg.APIKey)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return Result{}, fmt.Errorf("google: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := g.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("google: read response: %w", err)
	}

	var decoded googleResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Result{}, fmt.Errorf("google: decode response: %w", err)
	}

	if decoded.Error != nil && decoded.Error.Message != "" {
		return Result{}, fmt.Errorf("google: api error %d: %s", decoded.Error.Code, decoded.Error.Message)
	}

	if resp.StatusCode != http.StatusOK {
		return Result{}, providerStatusError("google", resp.StatusCode, raw)
	}

	text, confidence := firstAlternative(decoded)

	result := Result{
		Text:     strings.TrimSpace(text),
		Language: g.cfg.LanguageCode,
		// Unlike Sarvam and Whisper, Google does return a per-alternative
		// confidence. That makes the low-confidence ask-to-repeat path
		// actually work for this provider.
		Confidence: confidence,
		Provider:   g.Name(),
		Latency:    time.Since(start),
	}

	if result.Text == "" {
		return result, ErrNoSpeech
	}

	return result, nil
}

// firstAlternative returns the best alternative's text and confidence.
func firstAlternative(resp googleResponse) (string, float64) {
	for _, result := range resp.Results {
		for i, alt := range result.Alternatives {
			if i == 0 {
				// Google omits confidence when there is only one result in
				// some responses. Treat that as unknown, not zero.
				if alt.Confidence == 0 {
					return alt.Transcript, ConfidenceUnknown
				}
				return alt.Transcript, alt.Confidence
			}
		}
	}
	return "", ConfidenceUnknown
}

// googleEncoding maps a MIME type onto Google's LINEAR16 encoding name.
//
// Google requires an explicit encoding. WhatsApp sends OGG/Opus, which Google
// cannot decode directly, so this path in practice expects already-converted
// PCM. The ffmpeg helper exists to produce that.
func (a Audio) googleEncoding() string {
	lower := strings.ToLower(a.MimeType)
	if strings.Contains(lower, "wav") || strings.Contains(lower, "x-wav") {
		return "LINEAR16"
	}
	if a.SampleRate > 0 {
		return "LINEAR16"
	}
	return "WEBM_OPUS"
}
