package worldread

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"
)

// WORLD R07 transports {world} and {timeline} as ASCII path segments. The
// canonical ID stays exact UTF-8; only the transport form is encoded. This is
// CORE's implementation of WORLD ID.md "utf8-base64url-tilde-v1", checked
// against the WORLD fixture.
const transportEscape = '~'

var errInvalidTransportID = errors.New("world read: invalid transport ID")

func encodeTransportID(id string) (string, error) {
	if !validID(id) {
		return "", errInvalidTransportID
	}
	if isTransportSafe(id) && id[0] != transportEscape {
		return id, nil
	}
	return string(transportEscape) + base64.RawURLEncoding.EncodeToString([]byte(id)), nil
}

func decodeTransportID(transport string) (string, error) {
	if transport == "" {
		return "", errInvalidTransportID
	}
	if transport[0] == transportEscape {
		payload := transport[1:]
		if payload == "" {
			return "", errInvalidTransportID
		}
		raw, err := base64.RawURLEncoding.DecodeString(payload)
		if err != nil {
			return "", errInvalidTransportID
		}
		if base64.RawURLEncoding.EncodeToString(raw) != payload {
			return "", errInvalidTransportID
		}
		id := string(raw)
		if !validID(id) {
			return "", errInvalidTransportID
		}
		return id, nil
	}
	if !isTransportSafe(transport) || !validID(transport) {
		return "", errInvalidTransportID
	}
	return transport, nil
}

func validID(id string) bool {
	if id == "" || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func isTransportSafe(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '-', '.', '_', '~':
			continue
		}
		return false
	}
	return true
}
