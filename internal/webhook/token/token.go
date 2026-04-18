// Package token provides HMAC-SHA256 signed tokens for Kapro approval webhooks.
//
// Tokens are self-contained: all claims needed to create the Approval CR or
// reject the Promotion are encoded in the token itself. No server-side session state.
//
// Format: base64url(json_claims) + "." + base64url(hmac_sha256_sig)
//
// Claims are bound to the Promotion UID, preventing replay attacks when
// Promotion names are reused across releases.
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Claims encodes the context for a single approve or reject action.
type Claims struct {
	// PromotionName is the Promotion object name.
	PromotionName string `json:"n"`
	// Namespace is the Promotion's namespace.
	Namespace string `json:"ns"`
	// Release is the ReleaseRef from the PromotionSpec.
	Release string `json:"r"`
	// Environment is the EnvironmentRef from the PromotionSpec.
	Environment string `json:"e"`
	// Version is the artifact version being promoted.
	Version string `json:"v"`
	// UID is the Promotion object UID — prevents replay across name reuse.
	UID string `json:"uid"`
	// Action is "approve" or "reject".
	Action string `json:"a"`
	// ApprovedBy is the identity of the approver (email, username, service account).
	// Set by the notification system from SSO/OIDC context when the token is minted.
	// The webhook server uses this to populate Approval.spec.approvedBy — it cannot
	// be forged because the token is HMAC-signed.
	ApprovedBy string `json:"by,omitempty"`
	// Exp is the Unix timestamp after which the token is invalid.
	Exp int64 `json:"exp"`
}

// DefaultTTL is how long approval tokens remain valid. 48 hours covers most
// corporate approval workflows while limiting window for token leak abuse.
const DefaultTTL = 48 * time.Hour

// Sign encodes claims as JSON and appends an HMAC-SHA256 signature.
// Returns a URL-safe token with no padding.
func Sign(c Claims, secret []byte) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("token: marshal claims: %w", err)
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)
	sig := computeHMAC([]byte(enc), secret)
	return enc + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify parses and validates a token. Returns the claims if the signature
// is valid and the token has not expired.
func Verify(token string, secret []byte) (*Claims, error) {
	dot := strings.LastIndex(token, ".")
	if dot < 0 {
		return nil, fmt.Errorf("token: malformed — missing separator")
	}

	enc := token[:dot]
	gotSig, err := base64.RawURLEncoding.DecodeString(token[dot+1:])
	if err != nil {
		return nil, fmt.Errorf("token: decode signature: %w", err)
	}

	expected := computeHMAC([]byte(enc), secret)
	if !hmac.Equal(gotSig, expected) {
		return nil, fmt.Errorf("token: invalid signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, fmt.Errorf("token: decode payload: %w", err)
	}

	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("token: unmarshal claims: %w", err)
	}

	if time.Now().Unix() > c.Exp {
		return nil, fmt.Errorf("token: expired")
	}
	return &c, nil
}

func computeHMAC(data, secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(data)
	return mac.Sum(nil)
}
