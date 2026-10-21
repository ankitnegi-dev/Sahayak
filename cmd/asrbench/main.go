// Command asrbench measures speech-to-text providers against a corpus of
// recorded voice notes.
//
// This is a development tool, not part of the product. Its output is committed
// to the repository as a dated record of which provider actually understands
// parents' speech, because that is a decision you cannot make from a demo.
//
// The corpus itself is never committed. It contains parents' voices.
//
// Usage:
//
//	asrbench -manifest path/to/manifest.jsonl -provider sarvam
//	asrbench -manifest manifest.jsonl -provider sarvam -provider openai -provider google
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/asr"
)

// multiFlag collects a repeated string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "asrbench: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var providers multiFlag
	var manifestPath, corpusRoot, outputPath, language string
	var minConfidence float64
	var keytermsCSV string

	flag.Var(&providers, "provider", "provider to benchmark: sarvam, openai, google. Repeatable.")
	flag.StringVar(&manifestPath, "manifest", "", "path to the JSONL corpus manifest (required)")
	flag.StringVar(&corpusRoot, "corpus", "", "root directory for relative audio paths. Defaults to the manifest's directory.")
	flag.StringVar(&outputPath, "out", "", "write the Markdown report here instead of stdout.")
	flag.StringVar(&language, "language", "hi-IN", "BCP-47 language code.")
	flag.Float64Var(&minConfidence, "min-confidence", 0.6, "confidence below which the bot asks the parent to repeat.")
	flag.StringVar(&keytermsCSV, "keyterms", "", "comma-separated biasing terms, e.g. 'Mandir,New Delhi'.")
	flag.Parse()

	if manifestPath == "" {
		return fmt.Errorf("-manifest is required")
	}
	if len(providers) == 0 {
		return fmt.Errorf("at least one -provider is required")
	}

	if corpusRoot == "" {
		corpusRoot = filepath.Dir(manifestPath)
	}

	corpus, err := asr.LoadCorpus(manifestPath)
	if err != nil {
		return err
	}

	var keyterms []string
	if keytermsCSV != "" {
		for _, term := range strings.Split(keytermsCSV, ",") {
			if trimmed := strings.TrimSpace(term); trimmed != "" {
				keyterms = append(keyterms, trimmed)
			}
		}
	}

	// A single deadline for the whole run. Without one, a provider that hangs
	// on the first sample blocks the benchmark indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	var results []asr.BenchmarkResult
	for _, name := range providers {
		transcriber, err := buildProvider(name, language, keyterms)
		if err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "running %s over %d samples...\n", transcriber.Name(), len(corpus.Entries))

		result, runErr := asr.RunBenchmark(ctx, corpusRoot, corpus, transcriber, minConfidence)
		if runErr != nil {
			// A cancelled or failed run still reports what it measured, which
			// is more useful than aborting with nothing.
			return fmt.Errorf("benchmark %s: %w", name, runErr)
		}

		results = append(results, result)
		fmt.Fprintf(os.Stderr, "  %s: WER %.3f, %d/%d ok, mean %dms\n",
			result.Provider, result.WER.Value, result.SamplesOK, result.Samples, result.MeanMS)
	}

	report := asr.FormatTable(results, filepath.Base(filepath.Dir(manifestPath)))

	if outputPath == "" {
		fmt.Println(report)
		return nil
	}

	if err := os.WriteFile(outputPath, []byte(report), 0o600); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	fmt.Fprintf(os.Stderr, "report written to %s\n", outputPath)
	return nil
}

// buildProvider constructs a transcriber from its name and environment config.
func buildProvider(name, language string, keyterms []string) (asr.Transcriber, error) {
	switch strings.ToLower(name) {
	case "sarvam":
		apiKey := os.Getenv("SARVAM_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("SARVAM_API_KEY is not set")
		}
		return asr.NewSarvam(asr.SarvamConfig{
			APIKey:       apiKey,
			LanguageCode: language,
			Keyterms:     keyterms,
		}), nil

	case "openai", "whisper":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY is not set")
		}
		return asr.NewOpenAI(asr.OpenAIConfig{
			APIKey: apiKey,
			// Google-style BCP-47 is not what OpenAI expects.
			Language: shortLanguage(language),
			Prompt:   strings.Join(keyterms, ", "),
		}), nil

	case "google":
		apiKey := os.Getenv("GOOGLE_STT_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("GOOGLE_STT_API_KEY is not set")
		}
		return asr.NewGoogle(asr.GoogleConfig{
			APIKey:       apiKey,
			LanguageCode: language,
		}), nil

	default:
		return nil, fmt.Errorf("unknown provider %q: want sarvam, openai or google", name)
	}
}

// shortLanguage converts "hi-IN" to the ISO-639-1 form OpenAI expects.
func shortLanguage(language string) string {
	if idx := strings.Index(language, "-"); idx > 0 {
		return language[:idx]
	}
	return language
}
