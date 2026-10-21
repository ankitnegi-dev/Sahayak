package asr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ErrFFmpegUnavailable means the ffmpeg binary could not be found.
var ErrFFmpegUnavailable = errors.New("asr: ffmpeg is not available")

// Converter changes audio into a format a provider accepts.
//
// Declared as an interface so the ffmpeg subprocess can be faked in tests.
// Running a real external binary is exactly the kind of thing that should not
// run inside a unit test.
type Converter interface {
	Convert(ctx context.Context, audio Audio, target TargetFormat) (Audio, error)
}

// TargetFormat describes what a provider needs.
type TargetFormat struct {
	// Container is the output extension, such as "wav".
	Container string
	// SampleRate is the output sample rate in hertz. Google performs best at
	// 16000 for Indian languages, which is also what most ASR training data
	// uses.
	SampleRate int
	// Channels is the output channel count. Google wants mono.
	Channels int
}

// Linear16Mono16k is what Google Speech-to-Text wants for Indian languages.
var Linear16Mono16k = TargetFormat{
	Container:  "wav",
	SampleRate: 16000,
	Channels:   1,
}

// FFmpeg converts audio by shelling out to the ffmpeg binary.
//
// ffmpeg is an external dependency, not a Go library, because no pure-Go
// decoder handles Opus well. It is the standard tool for this and is already
// what the roadmap specifies.
type FFmpeg struct {
	// BinaryPath is the ffmpeg executable. Empty means "ffmpeg" on PATH.
	BinaryPath string

	// Timeout bounds the conversion. Voice notes take well under a second, so
	// ten seconds is a generous ceiling that still catches a hung process.
	Timeout time.Duration
}

// compile-time assertion that FFmpeg satisfies the interface.
var _ Converter = (*FFmpeg)(nil)

// NewFFmpeg returns a converter using ffmpeg from PATH.
func NewFFmpeg() *FFmpeg {
	return &FFmpeg{BinaryPath: "ffmpeg", Timeout: 10 * time.Second}
}

// Convert implements Converter.
func (f *FFmpeg) Convert(ctx context.Context, audio Audio, target TargetFormat) (Audio, error) {
	if err := audio.validate(); err != nil {
		return Audio{}, err
	}

	binary := f.BinaryPath
	if binary == "" {
		binary = "ffmpeg"
	}

	timeout := f.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	// input is pipe:0, output is pipe:1, so audio never touches disk. That
	// matters because the roadmap's privacy rule is that audio is deleted right
	// after processing, and writing it to a temp file would create a second
	// copy to clean up, and a window where it exists on disk at all.
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-i", "pipe:0",
		"-ar", fmt.Sprintf("%d", target.SampleRate),
		"-ac", fmt.Sprintf("%d", target.Channels),
		"-f", target.Container,
		"pipe:1",
	}

	cmd := exec.CommandContext(cmdCtx, binary, args...)
	cmd.Stdin = bytes.NewReader(audio.Data)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	// Wipe the source buffer regardless of outcome. On the error path this
	// still runs because it is a defer.
	defer audio.Wipe()

	if runErr != nil {
		// exec.ErrNotFound wrapped: ffmpeg is not installed.
		if errors.Is(runErr, exec.ErrNotFound) || strings.Contains(stderr.String(), "not recognized") {
			return Audio{}, fmt.Errorf("%w: %v", ErrFFmpegUnavailable, runErr)
		}

		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			return Audio{}, fmt.Errorf("asr: ffmpeg timed out after %s: %s", timeout, tail(stderr.String()))
		}

		return Audio{}, fmt.Errorf("asr: ffmpeg failed: %w: %s", runErr, tail(stderr.String()))
	}

	converted := stdout.Bytes()
	if len(converted) == 0 {
		return Audio{}, errors.New("asr: ffmpeg produced no output")
	}

	mimeType := map[string]string{
		"wav":  "audio/wav",
		"mp3":  "audio/mpeg",
		"flac": "audio/flac",
	}[target.Container]

	return Audio{
		Data:       converted,
		MimeType:   mimeType,
		Format:     target.Container,
		SampleRate: target.SampleRate,
	}, nil
}

// CheckFFmpeg reports whether ffmpeg can be invoked.
func CheckFFmpeg(ctx context.Context, binary string) error {
	if binary == "" {
		binary = "ffmpeg"
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, binary, "-version")
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%w: install ffmpeg and ensure it is on PATH", ErrFFmpegUnavailable)
		}
		return fmt.Errorf("asr: run ffmpeg -version: %w", err)
	}
	return nil
}

// tail trims long process stderr so an error message stays readable.
func tail(s string) string {
	s = strings.TrimSpace(s)
	const max = 400
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
