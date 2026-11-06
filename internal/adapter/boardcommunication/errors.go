package boardcommunication

// ErrorCode is a sanitized category for client failures.
type ErrorCode string

const (
	ErrorCodeUnavailable                ErrorCode = "unavailable"
	ErrorCodeUnsupported                ErrorCode = "unsupported"
	ErrorCodeInvalidConfiguration       ErrorCode = "invalid_configuration"
	ErrorCodeInvalidRequest             ErrorCode = "invalid_request"
	ErrorCodeInvalidPreparedSend        ErrorCode = "invalid_prepared_send"
	ErrorCodeAuthentication             ErrorCode = "authentication_failed"
	ErrorCodeProtocol                   ErrorCode = "protocol_error"
	ErrorCodeConnectionIdentityMismatch ErrorCode = "connection_identity_mismatch"
	ErrorCodeUnknownOutcome             ErrorCode = "outcome_unknown"
	ErrorCodeConflict                   ErrorCode = "idempotency_conflict"
	ErrorCodeClaimFence                 ErrorCode = "claim_fence_rejected"
	ErrorCodeClaimActive                ErrorCode = "claim_active"
	ErrorCodeClosed                     ErrorCode = "client_closed"
)

// ClientError carries only a fixed error category. It never wraps transport,
// token, or response details.
type ClientError struct {
	code ErrorCode
}

func (e *ClientError) Error() string {
	if e == nil {
		return string(ErrorCodeUnavailable)
	}
	return string(e.code)
}

// Code returns the fixed category for this client error.
func (e *ClientError) Code() ErrorCode {
	if e == nil {
		return ErrorCodeUnavailable
	}
	return e.code
}

var (
	ErrUnavailable                = &ClientError{code: ErrorCodeUnavailable}
	ErrUnsupported                = &ClientError{code: ErrorCodeUnsupported}
	ErrInvalidConfiguration       = &ClientError{code: ErrorCodeInvalidConfiguration}
	ErrInvalidRequest             = &ClientError{code: ErrorCodeInvalidRequest}
	ErrInvalidPreparedSend        = &ClientError{code: ErrorCodeInvalidPreparedSend}
	ErrAuthentication             = &ClientError{code: ErrorCodeAuthentication}
	ErrProtocol                   = &ClientError{code: ErrorCodeProtocol}
	ErrConnectionIdentityMismatch = &ClientError{code: ErrorCodeConnectionIdentityMismatch}
	ErrUnknownOutcome             = &ClientError{code: ErrorCodeUnknownOutcome}
	ErrConflict                   = &ClientError{code: ErrorCodeConflict}
	ErrClaimFence                 = &ClientError{code: ErrorCodeClaimFence}
	ErrClaimActive                = &ClientError{code: ErrorCodeClaimActive}
	ErrClosed                     = &ClientError{code: ErrorCodeClosed}
)
