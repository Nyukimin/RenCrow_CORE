package conversation

import (
	"context"
	"errors"
)

// ProfileExtractionResult はプロファイル抽出の結果
type ProfileExtractionResult struct {
	NewPreferences map[string]string `json:"preferences"`
	NewFacts       []string          `json:"facts"`
}

// HasData は抽出結果にデータがあるかを返す
func (r *ProfileExtractionResult) HasData() bool {
	return len(r.NewPreferences) > 0 || len(r.NewFacts) > 0
}

// ProfileExtractor はスレッド内の会話からユーザープロファイルを抽出する
type ProfileExtractor interface {
	Extract(ctx context.Context, thread *Thread, existing UserProfile) (*ProfileExtractionResult, error)
}

// ProfileExtractionErrorCode is the bounded failure category exposed by a
// ProfileExtractor. The underlying provider/response detail is deliberately
// not part of this domain contract.
type ProfileExtractionErrorCode string

const (
	ProfileExtractionErrorUnavailable ProfileExtractionErrorCode = "unavailable"
	ProfileExtractionErrorInvalid     ProfileExtractionErrorCode = "invalid"
)

// ProfileExtractionError is a typed, operator-safe extraction failure. The
// private cause is retained only so callers can preserve cancellation and
// other in-process error checks; Error never includes provider/raw content.
type ProfileExtractionError struct {
	Code  ProfileExtractionErrorCode
	cause error
}

func (e *ProfileExtractionError) Error() string {
	if e == nil {
		return "profile extractor error"
	}
	switch e.Code {
	case ProfileExtractionErrorUnavailable:
		return "profile extractor unavailable"
	case ProfileExtractionErrorInvalid:
		return "profile extractor invalid response"
	default:
		return "profile extractor error"
	}
}

func (e *ProfileExtractionError) Is(target error) bool {
	t, ok := target.(*ProfileExtractionError)
	return ok && e != nil && t != nil && e.Code == t.Code
}

func (e *ProfileExtractionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// NewProfileExtractionError creates a typed extraction error without exposing
// its provider/raw cause in the returned Error string.
func NewProfileExtractionError(code ProfileExtractionErrorCode, cause error) error {
	return &ProfileExtractionError{Code: code, cause: cause}
}

func NewProfileExtractionUnavailableError(cause error) error {
	return NewProfileExtractionError(ProfileExtractionErrorUnavailable, cause)
}

func NewProfileExtractionInvalidError(cause error) error {
	return NewProfileExtractionError(ProfileExtractionErrorInvalid, cause)
}

func ProfileExtractionErrorCodeOf(err error) ProfileExtractionErrorCode {
	var typed *ProfileExtractionError
	if errors.As(err, &typed) && typed != nil {
		return typed.Code
	}
	return ""
}

var (
	ErrProfileExtractorUnavailable = &ProfileExtractionError{Code: ProfileExtractionErrorUnavailable}
	ErrProfileExtractorInvalid     = &ProfileExtractionError{Code: ProfileExtractionErrorInvalid}
)
