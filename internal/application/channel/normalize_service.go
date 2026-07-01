package channel

import (
	"fmt"
	"strings"
	"time"
)

// BuildSessionID はチャネル共通のセッションID規約を組み立てる
// format: YYYYMMDD-{channel}-{chatID}
func BuildSessionID(now time.Time, channel, chatID string) string {
	return fmt.Sprintf("%s-%s-%s", now.Format("20060102"), channel, chatID)
}

// NormalizeEntryPlatformChannel normalizes unified entry platform/channel values.
// platform: line|viewer|cli|chrome (default: viewer)
// channel: line|telegram|discord|slack|viewer|local
func NormalizeEntryPlatformChannel(platform, channel string) (string, string) {
	p := strings.ToLower(strings.TrimSpace(platform))
	c := strings.ToLower(strings.TrimSpace(channel))

	switch p {
	case "line", "viewer", "cli", "chrome":
	default:
		p = "viewer"
	}
	if c != "" {
		switch c {
		case "line", "telegram", "discord", "slack", "viewer", "local":
			return p, c
		default:
			// Unknown channel falls back to platform-derived default.
		}
	}

	switch p {
	case "line":
		return p, "line"
	case "cli", "chrome":
		return p, "local"
	default:
		return p, "viewer"
	}
}
