package asr_test

import (
	"math"
	"testing"

	"github.com/ankitnegi-dev/Sahayak/internal/asr"
)

func TestWordErrorRate_PerfectMatch(t *testing.T) {
	wer := asr.WordErrorRate(
		"mere liye cab book karo",
		"mere liye cab book karo",
		asr.DefaultWEROptions,
	)

	if wer.Value != 0 {
		t.Errorf("Value = %v, want 0", wer.Value)
	}
	if wer.ReferenceWords != 5 {
		t.Errorf("ReferenceWords = %d, want 5", wer.ReferenceWords)
	}
	if wer.Substitutions != 0 || wer.Deletions != 0 || wer.Insertions != 0 {
		t.Errorf("S/D/I = %d/%d/%d, want 0/0/0", wer.Substitutions, wer.Deletions, wer.Insertions)
	}
}

func TestWordErrorRate_Counts(t *testing.T) {
	tests := []struct {
		name       string
		reference  string
		hypothesis string
		wantSubs   int
		wantDels   int
		wantIns    int
		wantWords  int
	}{
		{
			name: "one substitution", reference: "ghar jaana hai",
			hypothesis: "ghar jana hai", wantSubs: 1, wantWords: 3,
		},
		{
			name: "one deletion", reference: "ghar jaana hai",
			hypothesis: "ghar jana hai", wantSubs: 1, wantWords: 3,
		},
		{
			name: "one insertion", reference: "ghar jana hai",
			hypothesis: "ghar jana chahiye hai", wantIns: 1, wantWords: 3,
		},
		{
			name: "deletion only", reference: "mujhe ghar bhejo",
			hypothesis: "ghar bhejo", wantDels: 1, wantWords: 3,
		},
		{
			name: "completely different", reference: "ek do teen",
			hypothesis: "satt aath nau", wantSubs: 3, wantWords: 3,
		},
		{
			name: "empty hypothesis is all deletions", reference: "ek do teen",
			hypothesis: "", wantDels: 3, wantWords: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wer := asr.WordErrorRate(tt.reference, tt.hypothesis, asr.DefaultWEROptions)

			if wer.Substitutions != tt.wantSubs {
				t.Errorf("Substitutions = %d, want %d", wer.Substitutions, tt.wantSubs)
			}
			if wer.Deletions != tt.wantDels {
				t.Errorf("Deletions = %d, want %d", wer.Deletions, tt.wantDels)
			}
			if wer.Insertions != tt.wantIns {
				t.Errorf("Insertions = %d, want %d", wer.Insertions, tt.wantIns)
			}
			if wer.ReferenceWords != tt.wantWords {
				t.Errorf("ReferenceWords = %d, want %d", wer.ReferenceWords, tt.wantWords)
			}

			// The total edits must always agree with the rate.
			total := wer.Substitutions + wer.Deletions + wer.Insertions
			wantValue := float64(total) / float64(tt.wantWords)
			if math.Abs(wer.Value-wantValue) > 1e-9 {
				t.Errorf("Value = %v, want %v", wer.Value, wantValue)
			}
		})
	}
}

// Case and whitespace differences are not transcription errors.
func TestWordErrorRate_Normalization(t *testing.T) {
	tests := []struct {
		name       string
		reference  string
		hypothesis string
		wantValue  float64
	}{
		{"case folded", "Ghar Jaana Hai", "ghar jaana hai", 0},
		{"extra whitespace", "ghar   jaana  hai", " ghar jaana hai ", 0},
		{"tabs and newlines", "ghar\tjaana\nhai", "ghar jaana hai", 0},
		// Default options keep punctuation, so the trailing comma and full
		// stop are two substitutions across three words.
		{"punctuation kept by default", "ghar, jaana hai.", "ghar jaana hai", 2.0 / 3.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wer := asr.WordErrorRate(tt.reference, tt.hypothesis, asr.DefaultWEROptions)
			if math.Abs(wer.Value-tt.wantValue) > 1e-9 {
				t.Errorf("Value = %v, want %v", wer.Value, tt.wantValue)
			}
		})
	}
}

func TestWordErrorRate_PunctuationStrippedWhenRequested(t *testing.T) {
	opts := asr.WEROptions{
		Lowercase:           true,
		StripPunctuation:    true,
		NormalizeWhitespace: true,
	}

	wer := asr.WordErrorRate("ghar, jaana hai.", "ghar jaana hai", opts)
	if wer.Value != 0 {
		t.Errorf("Value = %v, want 0 with punctuation stripped", wer.Value)
	}
}

// A provider that hallucinates produces a rate above 1. That is a meaningful
// signal, not a maths error, so it must not be clamped.
func TestWordErrorRate_HallucinationExceedsOne(t *testing.T) {
	wer := asr.WordErrorRate("haan", "haan ji main abhi aata hoon kal milte hain", asr.DefaultWEROptions)

	if wer.Value <= 1 {
		t.Errorf("Value = %v, want > 1 for a hallucinated transcript", wer.Value)
	}
}

func TestWordErrorRate_EmptyReference(t *testing.T) {
	wer := asr.WordErrorRate("", "unexpected words", asr.DefaultWEROptions)

	if wer.ReferenceWords != 0 {
		t.Errorf("ReferenceWords = %d, want 0", wer.ReferenceWords)
	}
	if wer.Value != 2 {
		t.Errorf("Value = %v, want 2 (two pure insertions)", wer.Value)
	}
}

func TestWordErrorRate_BothEmpty(t *testing.T) {
	wer := asr.WordErrorRate("", "", asr.DefaultWEROptions)
	if wer.Value != 0 {
		t.Errorf("Value = %v, want 0", wer.Value)
	}
}

// The aggregate must weight long transcripts more heavily than short ones,
// otherwise a one-word clip dominates the result.
func TestMeanWER_WeightsByReferenceLength(t *testing.T) {
	short := asr.WordErrorRate("haan", "nahi", asr.DefaultWEROptions)                              // 1 sub, 1 word
	long := asr.WordErrorRate("a b c d e f g h i j", "a b c d e f g h i j", asr.DefaultWEROptions) // perfect, 10 words

	agg := asr.MeanWER([]asr.WER{short, long})

	if agg.Samples != 2 {
		t.Errorf("Samples = %d, want 2", agg.Samples)
	}
	if agg.ReferenceWords != 11 {
		t.Errorf("ReferenceWords = %d, want 11", agg.ReferenceWords)
	}
	// 1 error across 11 reference words.
	if math.Abs(agg.Value-1.0/11.0) > 1e-9 {
		t.Errorf("Value = %v, want %v", agg.Value, 1.0/11.0)
	}

	// A plain average of the two rates would be 0.5, which is what we must avoid.
	if math.Abs(agg.Value-0.5) < 1e-9 {
		t.Error("Value equals a plain average; the aggregate must weight by reference length")
	}
}

func TestMeanWER_Empty(t *testing.T) {
	agg := asr.MeanWER(nil)
	if agg.Samples != 0 || agg.Value != 0 {
		t.Errorf("MeanWER(nil) = %+v, want zero value", agg)
	}
}

// Devanagari script is the actual target language, so it must tokenise.
func TestWordErrorRate_Devanagari(t *testing.T) {
	wer := asr.WordErrorRate("मेरे लिए कैब बुक करो", "मेरे लिए कैब बुक करो", asr.DefaultWEROptions)

	if wer.Value != 0 {
		t.Errorf("Value = %v, want 0 for identical Devanagari", wer.Value)
	}
	if wer.ReferenceWords != 5 {
		t.Errorf("ReferenceWords = %d, want 5", wer.ReferenceWords)
	}
}
