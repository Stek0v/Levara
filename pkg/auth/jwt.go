// Package auth holds JWT primitives shared between the HTTP server
// (internal/http) and the gRPC server (internal/grpc). Lives outside
// internal/ so both transports can import it without creating a cycle
// through internal/http.
//
// Only the verify side is here — the sign side stays in internal/http
// where AuthConfig and loginHandler already own the token lifecycle.
// Verification is what gRPC interceptors need.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// Payload is the JWT claim set Levara issues. Field names match the
// internal/http.jwtPayload it was extracted from; changing them would
// silently invalidate tokens already in the wild.
type Payload struct {
	Sub   string `json:"sub"`   // user ID
	Email string `json:"email"` // may be empty for API-key derived tokens
	Exp   int64  `json:"exp"`   // expiry (unix seconds)
	Iat   int64  `json:"iat"`   // issued at (unix seconds)
}

// VerifyJWT validates an HS256-signed JWT against secret and returns the
// payload + true when the signature matches AND the token hasn't expired.
// Returns nil + false otherwise.
//
// Three-part token format (base64url-encoded header.payload.signature)
// with no padding — matches internal/http.createJWT. Callers MUST NOT
// trust the payload on a false return value.
func VerifyJWT(token, secret string) (*Payload, bool) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return nil, false
	}

	// Signature check — constant-time compare to avoid timing leaks.
	sigInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sigInput))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(expected)) {
		return nil, false
	}

	pJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var p Payload
	if err := json.Unmarshal(pJSON, &p); err != nil {
		return nil, false
	}
	if p.Exp < time.Now().Unix() {
		return nil, false
	}
	return &p, true
}
