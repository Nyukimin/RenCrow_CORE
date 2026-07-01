package memory

import (
	"fmt"
	"strings"
	"time"
)

const CutoverHour = 4
const CutoverTimezone = "Asia/Tokyo"

// Store はエージェントの永続メモリを管理するインターフェース
type Store interface {
	// ReadLongTerm は長期記憶（MEMORY.md）を読み込む
	ReadLongTerm() string
	// WriteLongTerm は長期記憶に書き込む
	WriteLongTerm(content string) error
	// ReadToday は今日の日次ノートを読み込む
	ReadToday() string
	// AppendToday は今日の日次ノートに追記する
	AppendToday(content string) error
	// GetRecentDailyNotes は直近N日分の日次ノートを返す
	GetRecentDailyNotes(days int) string
	// SaveDailyNoteForDate は指定日の日次ノートに書き込む
	SaveDailyNoteForDate(date time.Time, content string) error
	// GetMemoryContext はエージェントプロンプト用のメモリコンテキストを返す
	GetMemoryContext() string
}

// cutoverLocation はカットオーバー計算に使うタイムゾーンを返す
// 設定されたタイムゾーンが無効な場合はUTCにフォールバック
func cutoverLocation() *time.Location {
	loc, err := time.LoadLocation(CutoverTimezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// GetCutoverBoundary は直近の日次カットオーバー境界を返す
// カットオーバー境界は当日または前日のCutoverHour（04:00 JST）のうち、
// 直近の過去の時刻。全計算はCutoverTimezoneで行う。
func GetCutoverBoundary(now time.Time) time.Time {
	loc := cutoverLocation()
	nowLocal := now.In(loc)
	today := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), CutoverHour, 0, 0, 0, loc)
	if nowLocal.Before(today) {
		return today.AddDate(0, 0, -1)
	}
	return today
}

// GetLogicalDate は指定時刻の「論理日付」を返す
// CutoverHour（JST）より前の活動は前日に属する。
func GetLogicalDate(t time.Time) time.Time {
	loc := cutoverLocation()
	tLocal := t.In(loc)
	if tLocal.Hour() < CutoverHour {
		tLocal = tLocal.AddDate(0, 0, -1)
	}
	return time.Date(tLocal.Year(), tLocal.Month(), tLocal.Day(), 0, 0, 0, 0, loc)
}

// FormatCutoverNote はセッション要約と直近メッセージから日次ノートを構築する
func FormatCutoverNote(summary string, recentMessages []string) string {
	var parts []string

	if summary != "" {
		parts = append(parts, fmt.Sprintf("## Session Summary\n\n%s", summary))
	}

	if len(recentMessages) > 0 {
		parts = append(parts, fmt.Sprintf("## Last Messages\n\n%s", strings.Join(recentMessages, "\n")))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}
