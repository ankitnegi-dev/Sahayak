// Package whatsapp wraps the Meta WhatsApp Cloud API: webhook payload
// parsing, request signature verification, and the Graph API client used for
// sending replies and downloading media.
//
// Everything Meta-specific lives here so that a policy change or API version
// bump touches one package rather than the whole codebase.
package whatsapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Errors returned by signature verification. They are sentinels so callers can
// branch with errors.Is.
var (
	// ErrMissingSignature means the X-Hub-Signature-256 header was absent.
	ErrMissingSignature = errors.New("whatsapp: missing X-Hub-Signature-256 header")

	// ErrMalformedSignature means the header was present but is not a
	// well-formed sha256 digest, for example the prefix is missing, the hex
	// does not decode, or the digest is the wrong length.
	ErrMalformedSignature = errors.New("whatsapp: malformed X-Hub-Signature-256 header")

	// ErrInvalidSignature means the signature was well formed but did not
	// match the payload. Either the body was tampered with or the app secret
	// is wrong.
	ErrInvalidSignature = errors.New("whatsapp: signature verification failed")
)

// signaturePrefix is what Meta prefixes the hex digest with.
const signaturePrefix = "sha256="

// Sign computes the X-Hub-Signature-256 header value for a payload.
//
// This exists for two reasons: it lets tests generate valid signatures without
// duplicating the algorithm, and it documents the expected format in one place.
// Production code calls VerifySignature, not Sign.
func Sign(payload []byte, appSecret string) string {
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(payload)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks Meta's X-Hub-Signature-256 header against the raw
// request body.
//
// Three rules make this correct, and each one has caused real webhook
// vulnerabilities in the wild:
//
//  1. The HMAC is computed over the RAW body bytes. Decoding the JSON and
//     re-encoding it changes key order and whitespace, producing a different
//     digest. Never verify against a re-serialised struct.
//
//  2. The comparison uses hmac.Equal, which is constant time. A byte-by-byte
//     comparison leaks the correct prefix through timing, letting an attacker
//     forge a signature one byte at a time.
//
//  3. Every failure returns a distinct error so callers can tell "Meta sent
//     something malformed" apart from "this request is forged".
//
// appSecret is the App Secret from Meta's dashboard, NOT the access token. They
// are different credentials and confusing them silently breaks verification.
func VerifySignature(payload []byte, header, appSecret string) error {
	if header == "" {
		return ErrMissingSignature
	}

	if !strings.HasPrefix(header, signaturePrefix) {
		return fmt.Errorf("%w: expected sha256= prefix", ErrMalformedSignature)
	}

	provided, err := hex.DecodeString(strings.TrimPrefix(header, signaturePrefix))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedSignature, err)
	}

	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(payload)
	expected := mac.Sum(nil)

	// A digest that is not exactly sha256.Size bytes cannot be a valid
	// SHA-256 signature, so it is malformed rather than merely mismatched.
	// Checking the length first keeps the error meaningful instead of
	// reporting a wrong-length value as a forged signature.
	if len(provided) != len(expected) {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrMalformedSignature, len(provided), len(expected))
	}

	// hmac.Equal is constant time. Never replace it with == or bytes.Equal.
	if !hmac.Equal(provided, expected) {
		return ErrInvalidSignature
	}

	return nil
}
