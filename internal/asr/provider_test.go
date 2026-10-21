package asr_test

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/asr"
)

// sampleAudio is a stand-in for an OGG/Opus voice note. Its content is
// irrelevant; what matters is that the right bytes reach the provider.
var sampleAudio = asr.Audio{
	Data:     []byte("OggS-fake-audio-bytes"),
	MimeType: "audio/ogg; codecs=opus",
	Format:   "ogg",
}

// ---------------------------------------------------------------------------
// Result.IsUsable
// ---------------------------------------------------------------------------

func TestResult_IsUsable(t *testing.T) {
	tests := []struct {
		name          string
		result        asr.Result
		minConfidence float64
		want          bool
	}{
		{
			name:          "high confidence is usable",
			result:        asr.Result{Text: "ghar bhejo", Confidence: 0.92},
			minConfidence: 0.6, want: true,
		},
		{
			name:          "low confidence is not usable",
			result:        asr.Result{Text: "ghar bhejo", Confidence: 0.31},
			minConfidence: 0.6, want: false,
		},
		{
			name:          "confidence exactly at threshold is usable",
			result:        asr.Result{Text: "ghar bhejo", Confidence: 0.6},
			minConfidence: 0.6, want: true,
		},
		{
			name:          "empty text is never usable",
			result:        asr.Result{Text: "   ", Confidence: 0.99},
			minConfidence: 0.6, want: false,
		},
		{
			name:          "punctuation only is never usable",
			result:        asr.Result{Text: "... !!!", Confidence: 0.99},
			minConfidence: 0.6, want: false,
		},
		{
			// Sarvam and Whisper report no confidence. A real transcript
			// must still be accepted, otherwise those providers are unusable.
			name:          "unknown confidence accepts a real transcript",
			result:        asr.Result{Text: "ghar bhejo", Confidence: asr.ConfidenceUnknown},
			minConfidence: 0.6, want: true,
		},
		{
			name:          "unknown confidence rejects a very short transcript",
			result:        asr.Result{Text: "ha", Confidence: asr.ConfidenceUnknown},
			minConfidence: 0.6, want: false,
		},
		{
			name:          "devanagari counts as real text",
			result:        asr.Result{Text: "घर भेजो", Confidence: asr.ConfidenceUnknown},
			minConfidence: 0.6, want: true,
		},
		{
			name:          "numbers count as real text",
			result:        asr.Result{Text: "123", Confidence: asr.ConfidenceUnknown},
			minConfidence: 0.6, want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.IsUsable(tt.minConfidence); got != tt.want {
				t.Errorf("IsUsable(%v) = %v, want %v", tt.result, got, tt.want)
			}
		})
	}
}

func TestResult_HasConfidence(t *testing.T) {
	// Zero is a real confidence, meaning the provider is certain the
	// transcript is bad. Absence is represented by ConfidenceUnknown, not by
	// the zero value, precisely so this distinction survives.
	if !(asr.Result{Confidence: 0}).HasConfidence() {
		t.Error("confidence 0 reported as unknown; it is a real value")
	}
	if !(asr.Result{Confidence: 0.85}).HasConfidence() {
		t.Error("confidence 0.85 reported as unknown")
	}
	if (asr.Result{Confidence: asr.ConfidenceUnknown}).HasConfidence() {
		t.Error("ConfidenceUnknown reported as known")
	}
}

// Wipe must actually zero the buffer. This is the privacy guarantee.
func TestAudio_WipeZeroesBuffer(t *testing.T) {
	audio := asr.Audio{Data: []byte("sensitive voice content")}
	audio.Wipe()

	for i, b := range audio.Data {
		if b != 0 {
			t.Fatalf("byte %d = %d after Wipe, want 0", i, b)
		}
	}
}

// ---------------------------------------------------------------------------
// Sarvam
// ---------------------------------------------------------------------------

// inspectMultipart parses the request body so tests can assert on the fields
// the documented API expects.
func inspectMultipart(t *testing.T, r *http.Request) (fields map[string]string, fileName string, fileBytes []byte) {
	t.Helper()

	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}

	mr := multipart.NewReader(r.Body, params["boundary"])
	fields = map[string]string{}

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next part: %v", err)
		}

		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}

		if part.FileName() != "" {
			fileName = part.FileName()
			fileBytes = data
			continue
		}
		fields[part.FormName()] = string(data)
	}
	return fields, fileName, fileBytes
}

func newSarvam(t *testing.T, handler http.HandlerFunc) *asr.Sarvam {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return asr.NewSarvam(asr.SarvamConfig{
		APIKey:       "test-sarvam-key",
		Endpoint:     srv.URL,
		HTTPClient:   srv.Client(),
		LanguageCode: "hi-IN",
	})
}

func TestSarvam_TranscribeSuccess(t *testing.T) {
	var gotKey, gotFileName, gotFile string
	var gotFields map[string]string

	client := newSarvam(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("api-subscription-key")
		fields, fileName, file := inspectMultipart(t, r)
		gotFields, gotFileName, gotFile = fields, fileName, string(file)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"request_id":"req1","transcript":"मेरे लिए कैब बुक करो","language_code":"hi-IN"}`))
	})

	res, err := client.Transcribe(context.Background(), sampleAudio)
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}

	// Sarvam uses a custom header, not Authorization: Bearer.
	if gotKey != "test-sarvam-key" {
		t.Errorf("api-subscription-key = %q", gotKey)
	}
	if gotFields["model"] != "saaras:v4" {
		t.Errorf("model = %q, want saaras:v4", gotFields["model"])
	}
	if gotFields["language_code"] != "hi-IN" {
		t.Errorf("language_code = %q", gotFields["language_code"])
	}
	if gotFileName != "voice.ogg" {
		t.Errorf("filename = %q, want voice.ogg", gotFileName)
	}
	if gotFile != string(sampleAudio.Data) {
		t.Error("uploaded bytes do not match the input audio")
	}

	if res.Text != "मेरे लिए कैब बुक करो" {
		t.Errorf("Text = %q", res.Text)
	}
	if res.Provider != "sarvam" {
		t.Errorf("Provider = %q", res.Provider)
	}
	// A fixed language_code means Sarvam did no detection, so confidence must
	// stay unknown rather than being faked from a constant.
	if res.HasConfidence() {
		t.Errorf("Confidence = %v, want unknown when language_code is fixed", res.Confidence)
	}
}

// keyterms is the highest-leverage setting for this product, because family
// place names are exactly what a general model mishears. Sarvam expects a
// JSON-encoded array in a single field, not comma-separated text.
func TestSarvam_SendsKeytermsAsJSONArray(t *testing.T) {
	var gotKeyterms string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields, _, _ := inspectMultipart(t, r)
		gotKeyterms = fields["keyterms"]
		_, _ = w.Write([]byte(`{"transcript":"mandir","language_code":"hi-IN"}`))
	}))
	defer srv.Close()

	client := asr.NewSarvam(asr.SarvamConfig{
		APIKey:     "k",
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Keyterms:   []string{"Mandir", "New Delhi"},
	})

	if _, err := client.Transcribe(context.Background(), sampleAudio); err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}

	var decoded []string
	if err := json.Unmarshal([]byte(gotKeyterms), &decoded); err != nil {
		t.Fatalf("keyterms is not valid JSON: %q", gotKeyterms)
	}
	if len(decoded) != 2 || decoded[0] != "Mandir" || decoded[1] != "New Delhi" {
		t.Errorf("keyterms = %v, want [Mandir New Delhi]", decoded)
	}
}

func TestSarvam_LanguageDetectionYieldsConfidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"transcript":"ghar bhejo","language_code":"hi-IN","language_probability":0.87}`))
	}))
	defer srv.Close()

	client := asr.NewSarvam(asr.SarvamConfig{
		APIKey: "k", Endpoint: srv.URL, HTTPClient: srv.Client(),
		// LanguageCode intentionally unset, so Sarvam auto-detects.
	})

	res, err := client.Transcribe(context.Background(), sampleAudio)
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}
	if !res.HasConfidence() {
		t.Fatal("Confidence unknown, want language_probability to be used when auto-detecting")
	}
	if res.Confidence < 0.86 || res.Confidence > 0.88 {
		t.Errorf("Confidence = %v, want about 0.87", res.Confidence)
	}
}

func TestSarvam_EmptyTranscriptIsNoSpeech(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"transcript":"   ","language_code":"hi-IN"}`))
	}))
	defer srv.Close()

	client := asr.NewSarvam(asr.SarvamConfig{APIKey: "k", Endpoint: srv.URL, HTTPClient: srv.Client()})

	_, err := client.Transcribe(context.Background(), sampleAudio)
	if err == nil {
		t.Fatal("error = nil, want ErrNoSpeech")
	}
	if !strings.Contains(err.Error(), "no speech detected") {
		t.Errorf("error = %v, want ErrNoSpeech", err)
	}
}

func TestSarvam_ErrorStatusMapping(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantSubstr string
	}{
		{"rate limited", 429, `{"error":{"message":"Quota exceeded","code":"rate_limit_exceeded_error"}}`, "provider unavailable"},
		{"overloaded", 503, `{"error":{"message":"Service overloaded","code":"internal_server_error"}}`, "provider unavailable"},
		{"bad request", 400, `{"error":{"message":"Unsupported audio","code":"invalid_request_error"}}`, "unsupported audio format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			client := asr.NewSarvam(asr.SarvamConfig{APIKey: "k", Endpoint: srv.URL, HTTPClient: srv.Client()})

			_, err := client.Transcribe(context.Background(), sampleAudio)
			if err == nil {
				t.Fatal("error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantSubstr)
			}
		})
	}
}

func TestProviders_RejectEmptyAudio(t *testing.T) {
	empty := asr.Audio{}

	if _, err := asr.NewSarvam(asr.SarvamConfig{APIKey: "k"}).Transcribe(context.Background(), empty); err == nil {
		t.Error("sarvam accepted empty audio")
	}
	if _, err := asr.NewOpenAI(asr.OpenAIConfig{APIKey: "k"}).Transcribe(context.Background(), empty); err == nil {
		t.Error("openai accepted empty audio")
	}
	if _, err := asr.NewGoogle(asr.GoogleConfig{APIKey: "k"}).Transcribe(context.Background(), empty); err == nil {
		t.Error("google accepted empty audio")
	}
}

// ---------------------------------------------------------------------------
// OpenAI Whisper
// ---------------------------------------------------------------------------

func TestOpenAI_TranscribeSuccess(t *testing.T) {
	var gotAuth, gotFileName string
	var gotFields map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotFields, gotFileName, _ = inspectMultipart(t, r)
		_, _ = w.Write([]byte(`{"text":"mere liye cab book karo","language":"hinglish"}`))
	}))
	defer srv.Close()

	client := asr.NewOpenAI(asr.OpenAIConfig{
		APIKey: "sk-test", Endpoint: srv.URL, HTTPClient: srv.Client(),
		Language: "hi", Prompt: "Mandir, Railway Station",
	})

	res, err := client.Transcribe(context.Background(), sampleAudio)
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}

	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	if gotFields["model"] != "whisper-1" {
		t.Errorf("model = %q", gotFields["model"])
	}
	if gotFields["response_format"] != "json" {
		t.Errorf("response_format = %q", gotFields["response_format"])
	}
	if gotFields["prompt"] != "Mandir, Railway Station" {
		t.Errorf("prompt = %q", gotFields["prompt"])
	}
	if gotFileName != "voice.ogg" {
		t.Errorf("filename = %q", gotFileName)
	}
	if res.Text != "mere liye cab book karo" {
		t.Errorf("Text = %q", res.Text)
	}
	if res.HasConfidence() {
		t.Error("Whisper json format reports no confidence, but one was set")
	}
}

func TestOpenAI_ContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	client := asr.NewOpenAI(asr.OpenAIConfig{APIKey: "k", Endpoint: srv.URL, HTTPClient: srv.Client()})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := client.Transcribe(ctx, sampleAudio); err == nil {
		t.Error("error = nil, want a deadline error")
	}
}

// ---------------------------------------------------------------------------
// Google
// ---------------------------------------------------------------------------

func TestGoogle_TranscribeSuccess(t *testing.T) {
	var gotKey string
	// googleRequest is unexported, so the test decodes into its own shape.
	// This is deliberate: an external test package should only depend on the
	// public API, and duplicating the shape is what proves the wire format.
	var gotBody struct {
		Config struct {
			Encoding     string `json:"encoding"`
			LanguageCode string `json:"languageCode"`
		} `json:"config"`
		Audio struct {
			Content string `json:"content"`
		} `json:"audio"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"results":[{"alternatives":[{"transcript":"ghar bhejo","confidence":0.88}]}]}`))
	}))
	defer srv.Close()

	client := asr.NewGoogle(asr.GoogleConfig{
		APIKey: "google-key", Endpoint: srv.URL, HTTPClient: srv.Client(),
		LanguageCode: "hi-IN",
	})

	res, err := client.Transcribe(context.Background(), sampleAudio)
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}

	if gotKey != "google-key" {
		t.Errorf("key = %q", gotKey)
	}
	if gotBody.Config.LanguageCode != "hi-IN" {
		t.Errorf("languageCode = %q", gotBody.Config.LanguageCode)
	}
	if gotBody.Audio.Content == "" {
		t.Error("audio content was not base64 encoded into the request")
	}
	if res.Text != "ghar bhejo" {
		t.Errorf("Text = %q", res.Text)
	}
	// Google is the one provider that does report per-utterance confidence,
	// which is what makes the ask-to-repeat threshold meaningful here.
	if !res.HasConfidence() {
		t.Error("Confidence unknown, want the alternative confidence")
	}
	if res.Confidence < 0.87 || res.Confidence > 0.89 {
		t.Errorf("Confidence = %v, want about 0.88", res.Confidence)
	}
}

// Google omits confidence when there is a single result in some responses.
// That must map to unknown, not to zero, or every Google transcript would be
// rejected as low confidence.
func TestGoogle_MissingConfidenceBecomesUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"alternatives":[{"transcript":"ghar bhejo"}]}]}`))
	}))
	defer srv.Close()

	client := asr.NewGoogle(asr.GoogleConfig{APIKey: "k", Endpoint: srv.URL, HTTPClient: srv.Client()})

	res, err := client.Transcribe(context.Background(), sampleAudio)
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}
	if res.HasConfidence() {
		t.Errorf("Confidence = %v, want unknown", res.Confidence)
	}
	if !res.IsUsable(0.6) {
		t.Error("a real transcript with unknown confidence should still be usable")
	}
}

func TestGoogle_RejectsOversizedAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server called with oversized audio")
	}))
	defer srv.Close()

	client := asr.NewGoogle(asr.GoogleConfig{APIKey: "k", Endpoint: srv.URL, HTTPClient: srv.Client()})

	huge := asr.Audio{Data: make([]byte, 6<<20), MimeType: "audio/wav"}
	if _, err := client.Transcribe(context.Background(), huge); err == nil {
		t.Error("error = nil, want a size rejection")
	}
}

// ---------------------------------------------------------------------------
// Interface conformance
// ---------------------------------------------------------------------------

func TestProviders_SatisfyTranscriber(t *testing.T) {
	providers := []asr.Transcriber{
		asr.NewSarvam(asr.SarvamConfig{}),
		asr.NewOpenAI(asr.OpenAIConfig{}),
		asr.NewGoogle(asr.GoogleConfig{}),
	}

	wantNames := []string{"sarvam", "openai-whisper", "google-stt"}
	for i, p := range providers {
		if p.Name() != wantNames[i] {
			t.Errorf("Name() = %q, want %q", p.Name(), wantNames[i])
		}
	}
}
