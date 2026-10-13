package main

import (
	"os"
	"strconv"
	"strings"

	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

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
