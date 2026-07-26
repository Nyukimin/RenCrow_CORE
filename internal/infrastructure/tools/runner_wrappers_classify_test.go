package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// TestClassifyV1ErrorUsesSentinelErrors はエラー分類が sentinel error に基づく
// ことを確認する
//
// OSごとにエラー文言が異なる（Windows は "Access is denied." /
// "The system cannot find the file specified."、Unix は "permission denied" /
// "no such file or directory"）。文言マッチだけでは Windows で誤分類される。
func TestClassifyV1ErrorUsesSentinelErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want tool.ErrorCode
	}{
		{
			// Windows の実文言。英語の "no such file" を含まない
			name: "not-exist with Windows wording",
			err:  osStyleError{msg: "open C:\\missing: The system cannot find the file specified.", is: fs.ErrNotExist},
			want: tool.ErrNotFound,
		},
		{
			// Windows の実文言。英語の "permission denied" を含まない
			name: "permission with Windows wording",
			err:  osStyleError{msg: "open C:\\secret: Access is denied.", is: fs.ErrPermission},
			want: tool.ErrPermissionDenied,
		},
		{
			name: "wrapped os.ErrDeadlineExceeded",
			err:  fmt.Errorf("read: %w", os.ErrDeadlineExceeded),
			want: tool.ErrTimeout,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := classifyV1Error(tc.err)
			if resp == nil || resp.Error == nil {
				t.Fatalf("classifyV1Error(%v) returned no error response", tc.err)
			}
			if resp.Error.Code != tc.want {
				t.Fatalf("error code = %s, want %s", resp.Error.Code, tc.want)
			}
		})
	}
}

// TestClassifyV1ErrorKeepsPathErrorClassification は *os.PathError を
// そのまま渡した場合も分類できることを確認する
func TestClassifyV1ErrorKeepsPathErrorClassification(t *testing.T) {
	_, err := os.Open(pathThatMustNotExist)
	if err == nil {
		t.Fatalf("expected open error for %q", pathThatMustNotExist)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}
	resp := classifyV1Error(err)
	if resp.Error.Code != tool.ErrNotFound {
		t.Fatalf("error code = %s, want %s (err=%v)", resp.Error.Code, tool.ErrNotFound, err)
	}
}

const pathThatMustNotExist = "rencrow-nonexistent-path-for-classification-test"

// osStyleError は OS 固有文言を持ちつつ sentinel error に一致するエラーを再現する
//
// Windows の syscall エラーは英語の "no such file" や "permission denied" を
// 含まないが、errors.Is では fs.ErrNotExist / fs.ErrPermission に一致する。
type osStyleError struct {
	msg string
	is  error
}

func (e osStyleError) Error() string { return e.msg }

func (e osStyleError) Is(target error) bool { return target == e.is }
