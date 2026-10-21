package asr_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/asr"
)

// scriptedTranscriber returns canned transcripts, so the benchmark harness can
// be tested without any provider or network.
type scriptedTranscriber struct {
	name        string
	transcripts map[string]string // keyed by sample ID embedded in the audio
	failFor     map[string]error
	delay       time.Duration
}

func (s scriptedTranscriber) Name() string { return s.name }

func (s scriptedTranscriber) Transcribe(ctx context.Context, audio asr.Audio) (asr.Result, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return asr.Result{}, ctx.Err()
		}
	}

	// The ID is smuggled in through the audio bytes.
	id := string(audio.Data)

	if err, ok := s.failFor[id]; ok {
		return asr.Result{}, err
	}

	text, ok := s.transcripts[id]
	if !ok {
		return asr.Result{}, asr.ErrNoSpeech
	}

	return asr.Result{Text: text, Provider: s.name, Confidence: asr.ConfidenceUnknown}, nil
}

func TestLoadCorpus(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.jsonl")

	content := strings.Join([]string{
		`{"id":"s1","path":"s1.ogg","reference":"mere liye cab book karo","tags":["quiet","male"]}`,
		``,
		`// a comment line`,
		`{"id":"s2","path":"s2.ogg","reference":"ghar bhejo","tags":["noisy"]}`,
	}, "\n")

	if err := os.WriteFile(manifest, []byte(content), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	corpus, err := asr.LoadCorpus(manifest)
	if err != nil {
		t.Fatalf("LoadCorpus() error = %v", err)
	}

	if len(corpus.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(corpus.Entries))
	}
	if corpus.Entries[0].ID != "s1" || corpus.Entries[1].ID != "s2" {
		t.Errorf("unexpected ids: %+v", corpus.Entries)
	}
	if len(corpus.Entries[0].Tags) != 2 {
		t.Errorf("tags = %v, want 2", corpus.Entries[0].Tags)
	}
}

func TestLoadCorpus_Errors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty file", "", "empty"},
		{"missing reference", `{"id":"s1","path":"s1.ogg"}`, "reference transcript is required"},
		{"missing id", `{"path":"s1.ogg","reference":"hi"}`, "id and path are required"},
		{"bad json", `{"id":"s1",`, "manifest line"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			manifest := filepath.Join(dir, "manifest.jsonl")
			if err := os.WriteFile(manifest, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			_, err := asr.LoadCorpus(manifest)
			if err == nil {
				t.Fatal("error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestLoadCorpus_MissingFile(t *testing.T) {
	if _, err := asr.LoadCorpus(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Error("error = nil, want a read failure")
	}
}

// writeCorpus builds a small on-disk corpus for the benchmark tests.
func writeCorpus(t *testing.T, refs map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	var lines []string
	for id, ref := range refs {
		if err := os.WriteFile(filepath.Join(dir, id+".ogg"), []byte(id), 0o600); err != nil {
			t.Fatalf("write audio: %v", err)
		}
		lines = append(lines, `{"id":"`+id+`","path":"`+id+`.ogg","reference":"`+ref+`"}`)
	}

	manifest := filepath.Join(dir, "manifest.jsonl")
	if err := os.WriteFile(manifest, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

func TestRunBenchmark(t *testing.T) {
	dir := writeCorpus(t, map[string]string{
		"s1": "mere liye cab book karo",
		"s2": "ghar bhejo",
		"s3": "mandir chalo",
	})

	manifest := filepath.Join(dir, "manifest.jsonl")
	corpus, err := asr.LoadCorpus(manifest)
	if err != nil {
		t.Fatalf("LoadCorpus() error = %v", err)
	}

	// s3 is wrong on purpose, so the aggregate must be non-zero but small.
	provider := scriptedTranscriber{
		name: "scripted",
		transcripts: map[string]string{
			"s1": "mere liye cab book karo",
			"s2": "ghar bhejo",
			"s3": "mandir chalo jaldi",
		},
	}

	result, err := asr.RunBenchmark(context.Background(), dir, corpus, provider, 0.6)
	if err != nil {
		t.Fatalf("RunBenchmark() error = %v", err)
	}

	if result.Samples != 3 {
		t.Errorf("Samples = %d, want 3", result.Samples)
	}
	if result.SamplesOK != 3 {
		t.Errorf("SamplesOK = %d, want 3", result.SamplesOK)
	}
	if result.SamplesErr != 0 {
		t.Errorf("SamplesErr = %d, want 0", result.SamplesErr)
	}
	if result.WER.Insertions != 1 {
		t.Errorf("Insertions = %d, want 1", result.WER.Insertions)
	}
	if result.WER.Value <= 0 || result.WER.Value > 0.2 {
		t.Errorf("Value = %v, want a small non-zero rate", result.WER.Value)
	}
}

// A provider that fails on some samples must report failures, not silently
// drop them. A high error rate makes a provider unusable regardless of WER.
func TestRunBenchmark_RecordsFailures(t *testing.T) {
	dir := writeCorpus(t, map[string]string{"s1": "haan", "s2": "nahi"})

	corpus, err := asr.LoadCorpus(filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		t.Fatalf("LoadCorpus() error = %v", err)
	}

	provider := scriptedTranscriber{
		name: "flaky",
		transcripts: map[string]string{
			"s1": "haan",
			"s2": "nahi",
		},
		failFor: map[string]error{"s2": errors.New("provider timed out")},
	}

	result, err := asr.RunBenchmark(context.Background(), dir, corpus, provider, 0.6)
	if err != nil {
		t.Fatalf("RunBenchmark() error = %v", err)
	}

	if result.SamplesErr != 1 {
		t.Errorf("SamplesErr = %d, want 1", result.SamplesErr)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("Failures = %v, want 1 entry", result.Failures)
	}
	if !strings.Contains(result.Failures[0], "s2") {
		t.Errorf("failure = %q, want it to name the sample", result.Failures[0])
	}
}

func TestRunBenchmark_MissingAudioFile(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.jsonl")
	content := `{"id":"s1","path":"missing.ogg","reference":"haan"}`
	if err := os.WriteFile(manifest, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	corpus, err := asr.LoadCorpus(manifest)
	if err != nil {
		t.Fatalf("LoadCorpus() error = %v", err)
	}

	result, err := asr.RunBenchmark(context.Background(), dir, corpus,
		scriptedTranscriber{name: "x"}, 0.6)
	if err != nil {
		t.Fatalf("RunBenchmark() error = %v", err)
	}

	if result.SamplesErr != 1 {
		t.Errorf("SamplesErr = %d, want 1 for an unreadable sample", result.SamplesErr)
	}
}

func TestRunBenchmark_HonoursContextCancellation(t *testing.T) {
	dir := writeCorpus(t, map[string]string{"s1": "haan", "s2": "nahi"})
	corpus, err := asr.LoadCorpus(filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		t.Fatalf("LoadCorpus() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = asr.RunBenchmark(ctx, dir, corpus, scriptedTranscriber{name: "x"}, 0.6)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// The benchmark table is committed to the repo as a dated record, so its
// shape matters.
func TestFormatTable(t *testing.T) {
	results := []asr.BenchmarkResult{
		{
			Provider: "sarvam", Samples: 30, SamplesOK: 30,
			WER:    asr.AggregateWER{Substitutions: 12, Deletions: 4, Insertions: 2, ReferenceWords: 400, Value: 0.045},
			MeanMS: 820, P95MS: 1400,
			ByTag: map[string]asr.AggregateWER{
				"quiet": {ReferenceWords: 200, Value: 0.02},
				"noisy": {ReferenceWords: 200, Value: 0.07},
			},
		},
		{
			Provider: "google-stt", Samples: 30, SamplesOK: 28, SamplesErr: 2,
			WER:    asr.AggregateWER{Substitutions: 30, ReferenceWords: 400, Value: 0.075},
			MeanMS: 510, P95MS: 900,
			Failures: []string{"s7: provider unavailable"},
		},
	}

	table := asr.FormatTable(results, "family voice notes")

	for _, want := range []string{
		"# ASR benchmark", "| Provider |", "sarvam", "google-stt",
		"0.045", "0.075", "## By tag", "quiet", "noisy",
		"## Failures", "provider unavailable",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("table missing %q\n%s", want, table)
		}
	}
}
