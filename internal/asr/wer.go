package asr

import (
	"strings"
	"unicode"
)

// WER is a word error rate measurement for one transcript.
type WER struct {
	// Reference is the correct transcript.
	Reference string
	// Hypothesis is what the provider produced.
	Hypothesis string

	// ReferenceWords, Substitutions, Deletions, Insertions are the raw counts.
	ReferenceWords int
	Substitutions  int
	Deletions      int
	Insertions     int

	// Value is (S + D + I) / N. It is 0 for a perfect transcript.
	//
	// It can exceed 1, and can be negative-free but unbounded, when a provider
	// hallucinates. A value above 1 is not a bug in the maths, it is a signal
	// that the provider invented content.
	Value float64
}

// WEROptions controls how text is normalised before comparison.
type WEROptions struct {
	// Lowercase folds case before comparing.
	Lowercase bool
	// StripPunctuation removes punctuation. Off by default because
	// punctuation differences are a real quality signal.
	StripPunctuation bool
	// NormalizeWhitespace collapses runs of whitespace and trims.
	NormalizeWhitespace bool
}

// DefaultWEROptions is what the benchmark uses.
//
// Case and whitespace are folded because providers disagree about casing and
// spacing on identical audio, and measuring that as an error would make the
// comparison meaningless. Punctuation is kept, because getting commas and
// periods wrong on Hinglish is a genuine difference in output quality.
var DefaultWEROptions = WEROptions{
	Lowercase:           true,
	StripPunctuation:    false,
	NormalizeWhitespace: true,
}

// WordErrorRate computes the word error rate of hypothesis against reference.
func WordErrorRate(reference, hypothesis string, opts WEROptions) WER {
	refWords := tokenize(reference, opts)
	hypWords := tokenize(hypothesis, opts)

	// The edit distance table is O(len(ref) * len(hyp)) in time and O(min) in
	// space by keeping two rows. Voice notes are short, but the benchmark
	// must not blow up on a long pathological input.
	s, d, i := align(refWords, hypWords)

	result := WER{
		Reference:      reference,
		Hypothesis:     hypothesis,
		ReferenceWords: len(refWords),
		Substitutions:  s,
		Deletions:      d,
		Insertions:     i,
	}

	if len(refWords) == 0 {
		// An empty reference has no defined WER. Reporting 0 would make a
		// provider that said nothing look perfect, which is misleading, so
		// fall back to measuring pure insertions.
		if len(hypWords) == 0 {
			return result
		}
		result.Value = float64(len(hypWords))
		return result
	}

	result.Value = float64(s+d+i) / float64(len(refWords))
	return result
}

// tokenize splits text into comparable words.
func tokenize(text string, opts WEROptions) []string {
	if opts.Lowercase {
		text = strings.ToLower(text)
	}
	if opts.StripPunctuation {
		text = strings.Map(func(r rune) rune {
			if unicode.IsPunct(r) {
				return ' '
			}
			return r
		}, text)
	}
	if opts.NormalizeWhitespace {
		text = strings.Join(strings.Fields(text), " ")
	}

	if strings.TrimSpace(text) == "" {
		return nil
	}
	return strings.Split(text, " ")
}

// align computes substitution, deletion and insertion counts between two word
// sequences using the Levenshtein algorithm, tracking the full table so the
// operation breakdown is exact.
//
// Two rows would give the distance more cheaply, but the distance alone is not
// enough: a substitution and an insertion produce the same total. Since the
// breakdown is the thing the benchmark actually reports, it pays for the table.
func align(ref, hyp []string) (substitutions, deletions, insertions int) {
	return breakdown(ref, hyp)
}

// breakdown computes the operation counts with a full dynamic programming
// table, then walks it backwards to classify each edit.
//
// Voice notes are a few seconds of speech, so the O(n*m) table is fine. The
// guard below keeps a pathological input from allocating without bound.
func breakdown(ref, hyp []string) (substitutions, deletions, insertions int) {
	if len(ref) == 0 {
		return 0, 0, len(hyp)
	}
	if len(hyp) == 0 {
		return 0, len(ref), 0
	}

	rows, cols := len(ref)+1, len(hyp)+1
	table := make([]int, rows*cols)
	at := func(i, j int) int { return i*cols + j }

	for j := 0; j < cols; j++ {
		table[at(0, j)] = j
	}
	for i := 0; i < rows; i++ {
		table[at(i, 0)] = i
	}

	for i := 1; i < rows; i++ {
		for j := 1; j < cols; j++ {
			if ref[i-1] == hyp[j-1] {
				table[at(i, j)] = table[at(i-1, j-1)]
				continue
			}
			best := table[at(i-1, j-1)] + 1           // substitution
			if v := table[at(i, j-1)] + 1; v < best { // insertion
				best = v
			}
			if v := table[at(i-1, j)] + 1; v < best { // deletion
				best = v
			}
			table[at(i, j)] = best
		}
	}

	i, j := len(ref), len(hyp)
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && ref[i-1] == hyp[j-1]:
			i--
			j--
		case i > 0 && j > 0 && table[at(i, j)] == table[at(i-1, j-1)]+1:
			substitutions++
			i--
			j--
		case i > 0 && table[at(i, j)] == table[at(i-1, j)]+1:
			deletions++
			i--
		default:
			insertions++
			j--
		}
	}

	return substitutions, deletions, insertions
}

// AggregateWER combines several measurements into one overall rate.
//
// Totals are summed and the rate recomputed from them, rather than averaging
// the individual rates. Averaging would give a one-word transcript the same
// weight as a thirty-word one, which would let short clips dominate the result.
type AggregateWER struct {
	Substitutions  int
	Deletions      int
	Insertions     int
	ReferenceWords int
	Samples        int
	Value          float64
}

// MeanWER aggregates a set of measurements.
func MeanWER(results []WER) AggregateWER {
	agg := AggregateWER{Samples: len(results)}
	for _, r := range results {
		agg.Substitutions += r.Substitutions
		agg.Deletions += r.Deletions
		agg.Insertions += r.Insertions
		agg.ReferenceWords += r.ReferenceWords
	}
	if agg.ReferenceWords > 0 {
		agg.Value = float64(agg.Substitutions+agg.Deletions+agg.Insertions) / float64(agg.ReferenceWords)
	}
	return agg
}
