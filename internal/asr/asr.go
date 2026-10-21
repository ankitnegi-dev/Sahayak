// Package asr turns a voice note into text.
//
// The package is organised around one interface and several providers, so the
// choice of provider is configuration, not code. That matters because the
// whole point of the roadmap's Phase 3 is to measure which provider actually
// understands parents' speech, rather than guessing.
package asr

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Errors returned by providers.
var (
	// ErrNoAudio means the audio input was empty.
	ErrNoAudio = errors.New("asr: audio is empty")

	// ErrNoSpeech means the provider processed the audio but detected no
	// speech in it. This is different from an error: silence is a valid
	// outcome, and the right reply is to ask the parent to try again.
	ErrNoSpeech = errors.New("asr: no speech detected")

	// ErrLowConfidence means a transcript came back but is not trustworthy
	// enough to act on. The right reply is to ask the parent to repeat.
	ErrLowConfidence = errors.New("asr: transcript confidence too low")

	// ErrProviderUnavailable means the provider failed in a way that suggests
	// a temporary problem. Phase 9 wraps this in a circuit breaker.
	ErrProviderUnavailable = errors.New("asr: provider unavailable")

	// ErrUnsupportedAudio means the audio format is not something this
	// provider accepts.
	ErrUnsupportedAudio = errors.New("asr: unsupported audio format")
)

// ConfidenceUnknown marks a Result whose Confidence is not a real probability.
//
// This is not hypothetical. Sarvam, the leading Indian-language option, returns
// a language_probability but no per-utterance ASR confidence. OpenAI Whisper
// returns none either. Any threshold logic must therefore handle a genuinely
// absent confidence rather than treating 0 as "very unlikely", which would
// reject every Sarvam transcript.
const ConfidenceUnknown = -1.0

// Result is a completed transcription.
type Result struct {
	// Text is the transcript. Never log this at info level; it is a parent's
	// spoken words.
	Text string

	// Language is a BCP-47 code such as "hi-IN", or empty if unknown.
	Language string

	// Confidence is a 0 to 1 probability, or ConfidenceUnknown when the
	// provider does not report one.
	Confidence float64

	// Provider is the name that produced this result, for benchmark output.
	Provider string

	// Latency is how long the provider call took.
	Latency time.Duration
}

// HasConfidence reports whether Confidence is a real probability.
func (r Result) HasConfidence() bool { return r.Confidence >= 0 }

// IsUsable reports whether the transcript is good enough to act on.
//
// The rules, in order:
//
//  1. Empty or whitespace-only text is never usable. A blank transcript from a
//     voice note means the parent said nothing intelligible.
//  2. Text with no letters or digits is never usable. Providers occasionally
//     return punctuation or a lone filler token for noise.
//  3. If the provider reports confidence, it must clear minConfidence.
//  4. If the provider reports no confidence, fall back to a length heuristic,
//     because refusing every Sarvam transcript would make the bot useless.
//
// Rule 4 is the compromise the roadmap forces. The roadmap says to ask the
// parent to repeat on low confidence, but for providers that never report
// confidence, "low confidence" has to mean "suspiciously short or has no
// letters", which is a much weaker signal. Benchmarking the providers is what
// tells us whether that compromise is safe.
func (r Result) IsUsable(minConfidence float64) bool {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return false
	}

	if !hasAlphanumeric(text) {
		return false
	}

	if r.HasConfidence() {
		return r.Confidence >= minConfidence
	}

	// No confidence available. A transcript this short is more likely a noise
	// artefact than a real request, since parents say things like "ghar bhijao".
	return len([]rune(text)) >= minPlausibleRunes
}

// minPlausibleRunes is the shortest transcript we accept when the provider
// gives no confidence. Two characters or fewer is almost never a request.
const minPlausibleRunes = 3

func hasAlphanumeric(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return true
		}
		// Devanagari and other Indic ranges, plus any other letter.
		if r > 0x80 && !isPunctuation(r) {
			return true
		}
	}
	return false
}

func isPunctuation(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r',
		'.', ',', '!', '?', ';', ':', '-', '_',
		'"', '\'', '(', ')', '[', ']', '{', '}',
		'।', '॥', '،', '؟':
		return true
	}
	return false
}

// Transcriber converts audio into text.
//
// Declared in the package that uses it, per the roadmap rule. The conversation
// engine and worker depend on this capability, never on a concrete provider.
type Transcriber interface {
	// Transcribe converts audio bytes into text.
	//
	// It must honour ctx for both cancellation and deadline. A voice note
	// arrives through a job queue with a time budget, and an ASR call that
	// ignores context will hold a worker slot open indefinitely.
	Transcribe(ctx context.Context, audio Audio) (Result, error)

	// Name identifies the provider in logs and benchmark output.
	Name() string
}

// Audio is a voice note held in memory.
//
// Audio is never written to disk by this package and never placed in a queue
// payload. It exists as a []byte for the duration of one call and is then
// dropped, which upholds the promise to parents that voice data is not
// retained.
type Audio struct {
	// Data is the encoded audio.
	Data []byte

	// MimeType is the source format, such as "audio/ogg; codecs=opus".
	MimeType string

	// Format is the container or codec name passed to the provider, such as
	// "ogg" or "wav". Some providers need this separately from the MIME type.
	Format string

	// SampleRate is the sample rate in hertz when known, otherwise 0.
	SampleRate int
}

// validate performs the provider-independent checks every transcriber needs.
func (a Audio) validate() error {
	if len(a.Data) == 0 {
		return ErrNoAudio
	}
	return nil
}

// Wipe zeroes the audio buffer.
//
// Call this in a defer immediately after downloading, so the bytes are cleared
// as soon as processing finishes, including on the error paths. Go's garbage
// collector does not promise to zero memory, so without this a core dump or
// heap profile could contain a parent's voice.
func (a Audio) Wipe() {
	for i := range a.Data {
		a.Data[i] = 0
	}
}
