package conversation

import "time"

// Speaker は発話者の種別
type Speaker string

const (
	SpeakerUser   Speaker = "user"
	SpeakerMio    Speaker = "mio"
	SpeakerShiro  Speaker = "shiro"
	SpeakerAka    Speaker = "aka"
	SpeakerAo     Speaker = "ao"
	SpeakerGin    Speaker = "gin"
	SpeakerSystem Speaker = "system"
	SpeakerTool   Speaker = "tool"
	SpeakerMemory Speaker = "memory"
)

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
