package conversation

import "time"

// Speaker は発話者の種別
type Speaker string

const (
	SpeakerUser   Speaker = "user"
	SpeakerMio    Speaker = "mio"
	SpeakerShiro  Speaker = "shiro"
	SpeakerKuro   Speaker = "kuro"
	SpeakerMidori Speaker = "midori"
	SpeakerAka    Speaker = "aka"
	SpeakerAo     Speaker = "ao"
	SpeakerGin    Speaker = "gin"
	SpeakerSystem Speaker = "system"
	SpeakerTool   Speaker = "tool"
	SpeakerMemory Speaker = "memory"
)

// IsChatAgentSpeaker reports whether speaker is one of the user-facing CORE Agents.
func IsChatAgentSpeaker(speaker Speaker) bool {
	_, ok := CanonicalChatAgentSpeaker(speaker)
	return ok
}

// CanonicalChatAgentSpeaker maps legacy execution-role labels to Agent identities.
func CanonicalChatAgentSpeaker(speaker Speaker) (Speaker, bool) {
	switch speaker {
	case SpeakerMio, SpeakerShiro, SpeakerKuro, SpeakerMidori:
		return speaker, true
	case Speaker("heavy"):
		return SpeakerKuro, true
	case Speaker("wild"):
		return SpeakerMidori, true
	default:
		return "", false
	}
}

// Message は発話の最小単位
type Message struct {
	Speaker   Speaker                `json:"speaker"`
	Msg       string                 `json:"msg"`
	Timestamp time.Time              `json:"ts"`
	Meta      map[string]interface{} `json:"meta,omitempty"`
}

// NewMessage はMessageを生成
func NewMessage(speaker Speaker, msg string, meta map[string]interface{}) Message {
	if meta == nil {
		meta = make(map[string]interface{})
	}
	return Message{
		Speaker:   speaker,
		Msg:       msg,
		Timestamp: time.Now(),
		Meta:      meta,
	}
}
