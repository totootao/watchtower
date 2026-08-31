package config

import (
	"crypto/sha256"
	"crypto/subtle"
)

// CookieName is the cookie used to carry the API token for browser sessions.
//
// The HTTP API accepts it as an alternative to the Authorization header so the
// web dashboard can authenticate same-origin fetch requests without exposing
// the token to page scripts.
const CookieName = "access_token"

// HashToken returns the SHA-256 digest of a token.
//
// Parameters:
//   - token: Raw token value.
//
// Returns:
//   - [sha256.Size]byte: Digest of the token.
func HashToken(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

// TokenHashMatches compares a provided secret against a precomputed token hash
// using constant-time comparison.
//
// Parameters:
//   - expectedHash: Precomputed SHA-256 of the configured token.
//   - provided: Candidate token value from a request.
//
// Returns:
//   - bool: True when the digests match.
func TokenHashMatches(expectedHash [sha256.Size]byte, provided string) bool {
	providedHash := sha256.Sum256([]byte(provided))

	return subtle.ConstantTimeCompare(expectedHash[:], providedHash[:]) == 1
}

// TokenValid reports whether a provided token equals the expected token.
//
// The comparison hashes both values and compares digests in constant time so
// neither the result nor the shared length leaks through timing.
//
// Parameters:
//   - expected: Configured token. An empty expected token never matches.
//   - provided: Candidate token value from a request.
//
// Returns:
//   - bool: True when both values are non-empty and identical.
func TokenValid(expected, provided string) bool {
	if expected == "" || provided == "" {
		return false
	}

	return TokenHashMatches(HashToken(expected), provided)
}
