package asr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CorpusEntry is one recorded sample in the benchmark corpus.
//
// The corpus is parents' actual voice, so it must never be committed. It lives
// on disk only, is gitignored, and the whole point of collecting it is to
// measure the providers against real speech rather than a demo.
type CorpusEntry struct {
	// ID identifies the sample. It becomes the file name stem.
	ID string `json:"id"`
	// Path is the audio file, relative to the corpus root.
	Path string `json:"path"`
	// Reference is the hand-written correct transcript.
	Reference string `json:"reference"`
	// MimeType describes the audio format.
	MimeType string `json:"mime_type,omitempty"`
	// Tags describe conditions such as "noisy", "slow", "accents" so results
	// can be broken down. A provider that wins overall may still lose on
	// noisy samples, which is exactly the case that matters for a parent
	// speaking in a kitchen.
	Tags []string `json:"tags,omitempty"`
}

// Corpus is a loaded set of samples.
type Corpus struct {
	Entries []CorpusEntry
}

// LoadCorpus reads a JSONL manifest.
//
// JSONL, one entry per line, because a hand-edited reference transcript is
// much easier to fix line by line than inside a JSON array where a trailing
// comma breaks the whole file.
func LoadCorpus(path string) (Corpus, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Corpus{}, fmt.Errorf("asr: read corpus manifest: %w", err)
	}

	var corpus Corpus
	for lineNo, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") {
			continue
		}

		var entry CorpusEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return Corpus{}, fmt.Errorf("asr: manifest line %d: %w", lineNo+1, err)
		}
		if entry.ID == "" || entry.Path == "" {
			return Corpus{}, fmt.Errorf("asr: manifest line %d: id and path are required", lineNo+1)
		}
		if strings.TrimSpace(entry.Reference) == "" {
			// A missing reference would silently distort every aggregate.
			return Corpus{}, fmt.Errorf("asr: manifest line %d (%s): reference transcript is required", lineNo+1, entry.ID)
		}

		corpus.Entries = append(corpus.Entries, entry)
	}

	if len(corpus.Entries) == 0 {
		return Corpus{}, errors.New("asr: corpus manifest is empty")
	}
	return corpus, nil
}

// BenchmarkResult is the outcome for one provider.
type BenchmarkResult struct {
	Provider string

	// Samples attempted and SamplesOK count successes. A provider that errors
	// is recorded as a failure rather than silently excluded, because a high
	// success rate matters as much as a low WER.
	Samples    int
	SamplesOK  int
	SamplesErr int

	WER    AggregateWER
	MeanMS int64
	P95MS  int64

	// Failures records per-sample errors, so a provider that times out on
	// long clips can be told apart from one that is simply inaccurate.
	Failures []string

	// ByTag holds the aggregate WER for each tag.
	ByTag map[string]AggregateWER

	// BelowThreshold counts transcripts that would be rejected by the
	// ask-to-repeat rule. A provider can have a good WER and still be unusable
	// if most of its transcripts fall below the confidence gate.
	BelowThreshold int

	// PerSample holds every measurement, for inspecting specific clips.
	PerSample []WER
}

// RunBenchmark sends every corpus sample through a transcriber and measures
// accuracy and latency.
//
// minConfidence gates the "ask the parent to repeat" path. A transcript below
// it is still counted in the WER, because hiding bad results would make the
// benchmark lie. It is additionally counted in BelowThreshold.
func RunBenchmark(ctx context.Context, root string, corpus Corpus, t Transcriber, minConfidence float64) (BenchmarkResult, error) {
	result := BenchmarkResult{
		Provider: t.Name(),
		Samples:  len(corpus.Entries),
		ByTag:    map[string]AggregateWER{},
	}

	var durations []time.Duration
	var belowThreshold int
	tagResults := map[string][]WER{}

	for _, entry := range corpus.Entries {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		audio, err := readSample(root, entry)
		if err != nil {
			result.SamplesErr++
			result.Failures = append(result.Failures, fmt.Sprintf("%s: read: %v", entry.ID, err))
			continue
		}

		start := time.Now()
		res, err := t.Transcribe(ctx, audio)
		elapsed := time.Since(start)

		// Wipe immediately. This is the privacy guarantee in code form.
		audio.Wipe()

		if err != nil {
			result.SamplesErr++
			result.Failures = append(result.Failures, fmt.Sprintf("%s: %v", entry.ID, err))
			continue
		}
		if res.Text == "" {
			result.SamplesErr++
			result.Failures = append(result.Failures, fmt.Sprintf("%s: empty transcript", entry.ID))
			continue
		}

		result.SamplesOK++
		durations = append(durations, elapsed)

		wer := WordErrorRate(entry.Reference, res.Text, DefaultWEROptions)
		result.PerSample = append(result.PerSample, wer)

		for _, tag := range entry.Tags {
			tagResults[tag] = append(tagResults[tag], wer)
		}

		if !res.IsUsable(minConfidence) {
			belowThreshold++
		}
	}

	result.WER = MeanWER(result.PerSample)
	for tag, wers := range tagResults {
		result.ByTag[tag] = MeanWER(wers)
	}

	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		var total time.Duration
		for _, d := range durations {
			total += d
		}
		result.MeanMS = total.Milliseconds() / int64(len(durations))
		result.P95MS = durations[percentileIndex(len(durations), 95)].Milliseconds()
	}

	result.BelowThreshold = belowThreshold
	return result, nil
}

// readSample loads one audio file into memory.
func readSample(root string, entry CorpusEntry) (Audio, error) {
	path := entry.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Audio{}, err
	}

	mimeType := entry.MimeType
	if mimeType == "" {
		mimeType = mimeTypeFromExtension(path)
	}

	return Audio{
		Data:     data,
		MimeType: mimeType,
		Format:   strings.TrimPrefix(filepath.Ext(path), "."),
	}, nil
}

func mimeTypeFromExtension(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ogg", ".oga":
		return "audio/ogg; codecs=opus"
	case ".opus":
		return "audio/ogg; codecs=opus"
	case ".wav":
		return "audio/wav"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".webm":
		return "audio/webm"
	case ".flac":
		return "audio/flac"
	default:
		return "application/octet-stream"
	}
}

// percentileIndex returns the index of the requested percentile, nearest-rank.
func percentileIndex(n, percentile int) int {
	if n == 0 {
		return 0
	}
	idx := (n*percentile + 99) / 100
	if idx < 1 {
		idx = 1
	}
	if idx > n {
		idx = n
	}
	return idx - 1
}

// FormatTable renders results as a Markdown table, suitable for committing to
// the repository as a dated record of provider quality.
func FormatTable(results []BenchmarkResult, corpusName string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# ASR benchmark: %s\n\n", corpusName)
	fmt.Fprintf(&b, "Measured %s.\n\n", time.Now().Format("2006-01-02"))

	b.WriteString("| Provider | WER | Sub | Del | Ins | Samples | Errors | Mean ms | p95 ms | Below threshold |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")

	for _, r := range results {
		fmt.Fprintf(&b, "| %s | %.3f | %d | %d | %d | %d | %d | %d | %d | %d |\n",
			r.Provider, r.WER.Value,
			r.WER.Substitutions, r.WER.Deletions, r.WER.Insertions,
			r.SamplesOK, r.SamplesErr, r.MeanMS, r.P95MS, r.BelowThreshold)
	}

	// Per-tag breakdown, which is where the interesting differences hide.
	tags := map[string]bool{}
	for _, r := range results {
		for tag := range r.ByTag {
			tags[tag] = true
		}
	}

	if len(tags) > 0 {
		sorted := make([]string, 0, len(tags))
		for tag := range tags {
			sorted = append(sorted, tag)
		}
		sort.Strings(sorted)

		b.WriteString("\n## By tag\n\n| Provider |")
		for _, tag := range sorted {
			fmt.Fprintf(&b, " %s |", tag)
		}
		b.WriteString("\n| --- |")
		for range sorted {
			b.WriteString(" --- |")
		}
		b.WriteString("\n")

		for _, r := range results {
			fmt.Fprintf(&b, "| %s |", r.Provider)
			for _, tag := range sorted {
				if agg, ok := r.ByTag[tag]; ok {
					fmt.Fprintf(&b, " %.3f |", agg.Value)
				} else {
					b.WriteString(" - |")
				}
			}
			b.WriteString("\n")
		}
	}

	var failures []string
	for _, r := range results {
		for _, f := range r.Failures {
			failures = append(failures, fmt.Sprintf("- **%s** %s", r.Provider, f))
		}
	}
	if len(failures) > 0 {
		b.WriteString("\n## Failures\n\n")
		b.WriteString(strings.Join(failures, "\n"))
		b.WriteString("\n")
	}

	return b.String()
}

// FSEntryCount is a small helper for reporting corpus size.
func FSEntryCount(fsys fs.FS) (int, error) {
	count := 0
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	return count, err
}
