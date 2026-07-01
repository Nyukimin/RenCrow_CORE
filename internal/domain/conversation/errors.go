package conversation

import "errors"

var (
	// ErrThreadNotFound はThreadが見つからない場合のエラー
	ErrThreadNotFound = errors.New("thread not found")

	// ErrSessionNotFound はSessionが見つからない場合のエラー
	ErrSessionNotFound = errors.New("session not found")

	// ErrInvalidThreadStatus はThread状態が不正な場合のエラー
	ErrInvalidThreadStatus = errors.New("invalid thread status")
)
