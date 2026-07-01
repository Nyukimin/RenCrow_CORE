package line

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// verifySignature verifies the HMAC-SHA256 signature from LINE webhook
func verifySignature(body []byte, signature, channelSecret string) bool {
	if signature == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(channelSecret))
	mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}
