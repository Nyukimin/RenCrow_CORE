package task

import (
	"strings"
	"testing"
)

func TestNewJobID(t *testing.T) {
	jobID1 := NewJobID()
	jobID2 := NewJobID()

	// JobIDは一意である
	if jobID1.String() == jobID2.String() {
		t.Errorf("JobID should be unique, got same ID: %s", jobID1.String())
	}

	// フォーマットチェック: YYYYMMDD-HHMMSS-{UUID}
	parts := strings.Split(jobID1.String(), "-")
	if len(parts) != 3 {
		t.Errorf("JobID format should be YYYYMMDD-HHMMSS-UUID, got: %s", jobID1.String())
	}

	// 日付部分は8文字
	if len(parts[0]) != 8 {
		t.Errorf("Date part should be 8 chars, got: %s", parts[0])
	}

	// 時刻部分は6文字
	if len(parts[1]) != 6 {
		t.Errorf("Time part should be 6 chars, got: %s", parts[1])
	}

	// UUID部分は8文字
	if len(parts[2]) != 8 {
		t.Errorf("UUID part should be 8 chars, got: %s", parts[2])
	}
}

func TestJobIDFromString(t *testing.T) {
	original := "20260301-120000-abcd1234"
	jobID := JobIDFromString(original)

	if jobID.String() != original {
		t.Errorf("Expected %s, got %s", original, jobID.String())
	}
}

func TestJobIDEquals(t *testing.T) {
	jobID1 := JobIDFromString("20260301-120000-abcd1234")
	jobID2 := JobIDFromString("20260301-120000-abcd1234")
	jobID3 := JobIDFromString("20260301-120001-efgh5678")

	if !jobID1.Equals(jobID2) {
		t.Error("Same JobIDs should be equal")
	}

	if jobID1.Equals(jobID3) {
		t.Error("Different JobIDs should not be equal")
	}
}

func TestJobIDIsZero(t *testing.T) {
	var zeroJobID JobID
	normalJobID := NewJobID()

	if !zeroJobID.IsZero() {
		t.Error("Zero JobID should return true for IsZero()")
	}

	if normalJobID.IsZero() {
		t.Error("Normal JobID should return false for IsZero()")
	}
}

func TestParseJobID(t *testing.T) {
	// 正常系
	jobID, err := ParseJobID("20260301-120000-abcd1234")
	if err != nil {
		t.Fatalf("ParseJobID failed: %v", err)
	}
	if jobID.String() != "20260301-120000-abcd1234" {
		t.Errorf("Expected '20260301-120000-abcd1234', got '%s'", jobID.String())
	}

	// 空文字列はエラー
	_, err = ParseJobID("")
	if err == nil {
		t.Error("ParseJobID should return error for empty string")
	}
}
