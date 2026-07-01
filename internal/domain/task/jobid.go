package task

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// JobID はジョブの一意識別子を表す値オブジェクト
type JobID struct {
	value string
}

// NewJobID は新しいJobIDを生成
func NewJobID() JobID {
	// フォーマット: YYYYMMDD-HHMMSS-{UUID先頭8文字}
	now := time.Now()
	datePrefix := now.Format("20060102-150405")
	uuidStr := uuid.New().String()[:8]

	return JobID{
		value: fmt.Sprintf("%s-%s", datePrefix, uuidStr),
	}
}

// FromString は文字列からJobIDを復元
func JobIDFromString(s string) JobID {
	return JobID{value: s}
}

// String はJobIDの文字列表現を返す
func (j JobID) String() string {
	return j.value
}

// Equals は2つのJobIDが等しいかを判定
func (j JobID) Equals(other JobID) bool {
	return j.value == other.value
}

// IsZero はJobIDがゼロ値かを判定
func (j JobID) IsZero() bool {
	return j.value == ""
}

// ParseJobID は文字列からJobIDを復元（バリデーション付き）
// 空文字列はエラーを返す
func ParseJobID(s string) (JobID, error) {
	if s == "" {
		return JobID{}, fmt.Errorf("job ID cannot be empty")
	}
	return JobID{value: s}, nil
}
