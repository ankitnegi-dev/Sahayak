package whatsapp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
)

const (
	testSecret  = "test-app-secret"
	testPayload = `{"object":"whatsapp_business_account","entry":[]}`
)

func TestSignProducesExpectedFormat(t *testing.T) {
	got := whatsapp.Sign([]byte(testPayload), testSecret)

	if !strings.HasPrefix(got, "sha256=") {
		t.Errorf("Sign() = %q, want sha256= prefix", got)
	}
	if len(got) != len("sha256=")+64 {
		t.Errorf("Sign() length = %d, want %d", len(got), len("sha256=")+64)
	}
}

func TestVerifySignature_Valid(t *testing.T) {
	header := whatsapp.Sign([]byte(testPayload), testSecret)

	if err := whatsapp.VerifySignature([]byte(testPayload), header, testSecret); err != nil {
		t.Errorf("VerifySignature() error = %v, want nil", err)
	}
}

func TestVerifySignature_RejectsTamperedBody(t *testing.T) {
	// Signature computed over the original payload.
	header := whatsapp.Sign([]byte(testPayload), testSecret)

	// Attacker alters the body, keeping the original signature.
	tampered := []byte(`{"object":"whatsapp_business_account","entry":[{"hacked":true}]}`)

	err := whatsapp.VerifySignature(tampered, header, testSecret)
	if !errors.Is(err, whatsapp.ErrInvalidSignature) {
		t.Errorf("VerifySignature() error = %v, want ErrInvalidSignature", err)
	}
}

func TestVerifySignature_RejectsWrongSecret(t *testing.T) {
	header := whatsapp.Sign([]byte(testPayload), testSecret)

	err := whatsapp.VerifySignature([]byte(testPayload), header, "attacker-secret")
	if !errors.Is(err, whatsapp.ErrInvalidSignature) {
		t.Errorf("VerifySignature() error = %v, want ErrInvalidSignature", err)
	}
}

// JSON re-encoding changes key order and whitespace, so a signature over the
// reserialised form must NOT match. This is why the handler verifies the raw
// bytes rather than a decoded struct.
func TestVerifySignature_RejectsReencodedBody(t *testing.T) {
	original := `{"object":"whatsapp_business_account","entry":[]}`
	header := whatsapp.Sign([]byte(original), testSecret)

	// Same data, different byte sequence.
	reencoded := `{ "object": "whatsapp_business_account", "entry": [] }`

	if err := whatsapp.VerifySignature([]byte(reencoded), header, testSecret); !errors.Is(err, whatsapp.ErrInvalidSignature) {
		t.Errorf("VerifySignature() error = %v, want ErrInvalidSignature for reencoded body", err)
	}
}

func TestVerifySignature_MissingHeader(t *testing.T) {
	err := whatsapp.VerifySignature([]byte(testPayload), "", testSecret)
	if !errors.Is(err, whatsapp.ErrMissingSignature) {
		t.Errorf("VerifySignature() error = %v, want ErrMissingSignature", err)
	}
}

func TestVerifySignature_MalformedHeaders(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"no prefix", "abc123"},
		{"sha1 prefix", "sha1=abc123"},
		{"empty after prefix", "sha256="},
		{"not hex", "sha256=zzzz!!"},
		{"truncated hex", "sha256=abcd"},
		{"uppercase prefix", "SHA256=abcd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := whatsapp.VerifySignature([]byte(testPayload), tt.header, testSecret)
			if err == nil {
				t.Fatalf("VerifySignature(%q) error = nil, want error", tt.header)
			}
			if !errors.Is(err, whatsapp.ErrMalformedSignature) {
				t.Errorf("VerifySignature(%q) error = %v, want ErrMalformedSignature", tt.header, err)
			}
		})
	}
}

// A digest of the wrong length can never be a valid SHA-256, so it is
// reported as malformed rather than as a forged signature.
func TestVerifySignature_WrongLengthDigest(t *testing.T) {
	tests := []struct {
		name   string
		digest string
	}{
		{"too short", strings.Repeat("ab", 31)},  // 31 bytes
		{"too long", strings.Repeat("ab", 33)},   // 33 bytes
		{"empty", ""},                            // 0 bytes
		{"single nibble", "a"},                   // does not decode
		{"odd length", strings.Repeat("abc", 1)}, // 3 hex chars
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := whatsapp.VerifySignature([]byte(testPayload), "sha256="+tt.digest, testSecret)
			if !errors.Is(err, whatsapp.ErrMalformedSignature) {
				t.Errorf("VerifySignature() error = %v, want ErrMalformedSignature", err)
			}
		})
	}
}

// An empty body still verifies if the signature matches the empty body, which
// is correct behaviour. The handler rejects empty payloads separately.
func TestVerifySignature_EmptyPayloadWithMatchingSignature(t *testing.T) {
	header := whatsapp.Sign(nil, testSecret)

	if err := whatsapp.VerifySignature(nil, header, testSecret); err != nil {
		t.Errorf("VerifySignature() error = %v, want nil", err)
	}
}

func TestVerifySignature_EmptySecretAlwaysFails(t *testing.T) {
	header := whatsapp.Sign([]byte(testPayload), testSecret)

	err := whatsapp.VerifySignature([]byte(testPayload), header, "")
	if !errors.Is(err, whatsapp.ErrInvalidSignature) {
		t.Errorf("VerifySignature() error = %v, want ErrInvalidSignature", err)
	}
}

// Every single-character mutation of a valid signature must be rejected.
func TestVerifySignature_EveryBitFlipRejected(t *testing.T) {
	header := whatsapp.Sign([]byte(testPayload), testSecret)
	digest := strings.TrimPrefix(header, "sha256=")

	for i := 0; i < len(digest); i++ {
		for _, replacement := range []byte{'0', 'f', 'a'} {
			if digest[i] == replacement {
				continue
			}
			mutated := digest[:i] + string(replacement) + digest[i+1:]
			err := whatsapp.VerifySignature([]byte(testPayload), "sha256="+mutated, testSecret)
			if !errors.Is(err, whatsapp.ErrInvalidSignature) {
				t.Fatalf("mutation at %d accepted: %v", i, err)
			}
		}
	}
}
