package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

func sttFinalTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("STT_FINAL_TIMEOUT_MS"))
	if raw == "" {
		return modulestt.DefaultFinalTimeout
	}
	ms, err := strconv.Atoi(raw)
	if err != nil {
		return modulestt.DefaultFinalTimeout
	}
	return modulestt.FinalTimeoutFromMilliseconds(ms)
}

func sttSilenceAbsThresholdFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("STT_SILENCE_ABS_THRESHOLD"))
	if raw == "" {
		return modulestt.DefaultSilenceAbsThreshold
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return modulestt.DefaultSilenceAbsThreshold
	}
	return modulestt.SilenceAbsThreshold(v)
}

func isLikelySilentWAV(wav []byte, absThreshold int) bool {
	return modulestt.IsLikelySilentWAV(wav, absThreshold)
}

func normalizeSTTAudioPayload(payload []byte) []byte {
	return modulestt.NormalizeAudioPayload(payload)
}

func pcm16LEToWAV(pcm []byte, sampleRate int) []byte {
	return modulestt.PCM16LEToWAV(pcm, sampleRate)
}
