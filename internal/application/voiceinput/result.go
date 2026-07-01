package voiceinput

import (
	"errors"
	"strings"
	"time"
)

type Mode string

const (
	ModeSTT Mode = "stt"
	ModeLLM Mode = "llm"
)

type Result struct {
	Mode        Mode
	UtteranceID string
	SessionID   string
	Channel     string
	ChatID      string
	UserText    string
	Reply       string
	RawFinal    string
	Source      string
	Timings     Timings
}

type Timings struct {
	StartedAt    time.Time
	CommitAt     time.Time
	FirstTokenAt time.Time
	FinalAt      time.Time
	PublishedAt  time.Time
}

func (r Result) Validate() error {
	if r.Mode == "" {
		return errors.New("voice input mode is required")
	}
	if strings.TrimSpace(r.UtteranceID) == "" {
		return errors.New("voice input utterance_id is required")
	}
	if strings.TrimSpace(r.SessionID) == "" {
		return errors.New("voice input session_id is required")
	}
	if strings.TrimSpace(r.Channel) == "" {
		return errors.New("voice input channel is required")
	}
	if strings.TrimSpace(r.ChatID) == "" {
		return errors.New("voice input chat_id is required")
	}
	if strings.TrimSpace(r.Source) == "" {
		return errors.New("voice input source is required")
	}
	switch r.Mode {
	case ModeLLM:
		if strings.TrimSpace(r.Reply) == "" {
			return errors.New("llm voice reply is required")
		}
		if strings.TrimSpace(r.RawFinal) == "" {
			return errors.New("llm voice raw final is required")
		}
	case ModeSTT:
		if strings.TrimSpace(r.UserText) == "" {
			return errors.New("stt voice user_text is required")
		}
		if strings.TrimSpace(r.Reply) == "" {
			return errors.New("stt voice reply is required")
		}
	default:
		return errors.New("unsupported voice input mode")
	}
	return nil
}
