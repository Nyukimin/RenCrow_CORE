package line

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestVerifySignature_Valid(t *testing.T) {
	channelSecret := "test-channel-secret"
	body := []byte(`{"events":[{"type":"message"}]}`)

	// Generate valid signature
	mac := hmac.New(sha256.New, []byte(channelSecret))
	mac.Write(body)
	validSignature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !verifySignature(body, validSignature, channelSecret) {
		t.Error("Valid signature should be verified as true")
	}
}

func TestVerifySignature_Invalid(t *testing.T) {
	channelSecret := "test-channel-secret"
	body := []byte(`{"events":[{"type":"message"}]}`)
	invalidSignature := "invalid-signature"

	if verifySignature(body, invalidSignature, channelSecret) {
		t.Error("Invalid signature should be verified as false")
	}
}

func TestVerifySignature_EmptySignature(t *testing.T) {
	channelSecret := "test-channel-secret"
	body := []byte(`{"events":[{"type":"message"}]}`)

	if verifySignature(body, "", channelSecret) {
		t.Error("Empty signature should be verified as false")
	}
}

func TestVerifySignature_TamperedBody(t *testing.T) {
	channelSecret := "test-channel-secret"
	originalBody := []byte(`{"events":[{"type":"message"}]}`)
	tamperedBody := []byte(`{"events":[{"type":"tampered"}]}`)

	// Generate signature for original body
	mac := hmac.New(sha256.New, []byte(channelSecret))
	mac.Write(originalBody)
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// Verify with tampered body should fail
	if verifySignature(tamperedBody, signature, channelSecret) {
		t.Error("Tampered body should fail signature verification")
	}
}

func TestVerifySignature_DifferentSecret(t *testing.T) {
	channelSecret := "test-channel-secret"
	body := []byte(`{"events":[{"type":"message"}]}`)

	// Generate signature with one secret
	mac := hmac.New(sha256.New, []byte(channelSecret))
	mac.Write(body)
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// Verify with different secret should fail
	if verifySignature(body, signature, "different-secret") {
		t.Error("Different channel secret should fail signature verification")
	}
}

func TestVerifySignature_EmptyBody(t *testing.T) {
	channelSecret := "test-channel-secret"
	body := []byte{}

	// Generate signature for empty body
	mac := hmac.New(sha256.New, []byte(channelSecret))
	mac.Write(body)
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !verifySignature(body, signature, channelSecret) {
		t.Error("Empty body with valid signature should be verified as true")
	}
}
