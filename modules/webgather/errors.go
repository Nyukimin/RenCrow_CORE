package webgather

type ErrorCode string

const (
	ErrInvalidURL             ErrorCode = "invalid_url"
	ErrUnsupportedScheme      ErrorCode = "unsupported_scheme"
	ErrBlockedByPolicy        ErrorCode = "blocked_by_policy"
	ErrRobotsDisallowed       ErrorCode = "robots_disallowed"
	ErrRateLimited            ErrorCode = "rate_limited"
	ErrFetchTimeout           ErrorCode = "fetch_timeout"
	ErrFetchFailed            ErrorCode = "fetch_failed"
	ErrHTTPStatus             ErrorCode = "http_status_error"
	ErrBodyTooLarge           ErrorCode = "body_too_large"
	ErrUnsupportedContentType ErrorCode = "unsupported_content_type"
	ErrExtractFailed          ErrorCode = "extract_failed"
	ErrEmptyContent           ErrorCode = "empty_content"
	ErrSecurityWarning        ErrorCode = "security_warning"
	ErrStagingFailed          ErrorCode = "staging_failed"
	ErrCacheError             ErrorCode = "cache_error"
)

type Error struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return string(e.Code) + ": " + e.Message
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func NewError(code ErrorCode, message string) *Error {
	return &Error{Code: code, Message: message}
}

func WrapError(code ErrorCode, message string, err error) *Error {
	return &Error{Code: code, Message: message, Cause: err}
}
