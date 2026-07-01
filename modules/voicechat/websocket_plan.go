package voicechat

import "encoding/json"

func IsWebSocketTextFramePayload(payload []byte) bool {
	if len(payload) == 0 {
		return true
	}
	switch payload[0] {
	case '{', '[', '"':
		return json.Valid(payload)
	default:
		return false
	}
}
