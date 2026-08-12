package tool

// ErrorCode はツールエラーの分類コード（TOOL_CONTRACT 1.3）
type ErrorCode string

const (
	ErrValidationFailed          ErrorCode = "VALIDATION_FAILED"
	ErrNotFound                  ErrorCode = "NOT_FOUND"
	ErrPermissionDenied          ErrorCode = "PERMISSION_DENIED"
	ErrConflict                  ErrorCode = "CONFLICT"
	ErrTimeout                   ErrorCode = "TIMEOUT"
	ErrInternalError             ErrorCode = "INTERNAL_ERROR"
	ErrRateLimited               ErrorCode = "RATE_LIMITED"
	ErrDryRunOnly                ErrorCode = "DRY_RUN_ONLY"
	ErrToolExecutionScopeMissing ErrorCode = "TOOL_EXECUTION_SCOPE_MISSING"
	ErrToolExecutionScopeInvalid ErrorCode = "TOOL_EXECUTION_SCOPE_INVALID"
)
