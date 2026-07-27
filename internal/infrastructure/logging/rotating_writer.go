package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// サイズ上限付きのログファイルライター。
//
// docs/10_ログ仕様.md「保持と上限」に従う。すべての出力先は上限か保持期間の
// いずれかを持ち、無制限に増える出力先を作らない。
//
// LLM の生応答ログは不具合調査で最も役立つため出力自体は止めない。上限を
// 設けることと、書いたログを失うことは別である。上限到達時は世代を退避し、
// 最新の内容を優先して残す。

const (
	// DefaultMaxLogBytes は1ファイルあたりの既定上限
	DefaultMaxLogBytes int64 = 64 << 20 // 64MiB
	// DefaultMaxLogBackups は保持する世代数の既定値
	DefaultMaxLogBackups = 3
)

// RotatingWriter はサイズ上限で世代交代するファイルライター
type RotatingWriter struct {
	mu          sync.Mutex
	path        string
	maxBytes    int64
	maxBackups  int
	file        *os.File
	currentSize int64
}

// NewRotatingWriter はローテーション付きライターを生成する
//
// maxBytes 以下の値や maxBackups が0以下の場合は既定値を使う。
func NewRotatingWriter(path string, maxBytes int64, maxBackups int) (*RotatingWriter, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxLogBytes
	}
	if maxBackups <= 0 {
		maxBackups = DefaultMaxLogBackups
	}
	w := &RotatingWriter{path: path, maxBytes: maxBytes, maxBackups: maxBackups}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	w.file = f
	w.currentSize = info.Size()
	return nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, fmt.Errorf("log file is closed")
	}
	// 1回の書き込みが上限を超える場合でも分割しない。行の途中で切ると
	// 解析できなくなるため、先に世代交代してから書く。
	if shouldRotate(w.currentSize, len(p), w.maxBytes) {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.currentSize += int64(n)
	return n, err
}

// Sync はバッファをディスクへ反映する
func (w *RotatingWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	return w.file.Sync()
}

// Close はファイルを閉じる
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// shouldRotate は書き込み前に世代交代が必要かを返す
//
// 1回の書き込みが上限を超える場合でも分割しない。行の途中で切ると解析でき
// なくなるため、先に世代交代してから書く。空ファイルへは常に書く。
func shouldRotate(currentSize int64, incoming int, maxBytes int64) bool {
	if currentSize <= 0 {
		return false
	}
	return currentSize+int64(incoming) > maxBytes
}

// rotationStep は世代交代で行う1つのファイル操作
type rotationStep struct {
	// From が空の場合は To を削除する。それ以外は From を To へ改名する。
	From string
	To   string
}

// rotationPlan は世代交代の手順を返す
//
// 上限を超える最古の世代を削除してから、古い順に繰り下げる。手順を純粋関数
// として切り出すことで、ファイル操作を伴わずに順序を検証できる。
func rotationPlan(path string, maxBackups int) []rotationStep {
	if maxBackups <= 0 {
		maxBackups = DefaultMaxLogBackups
	}
	steps := make([]rotationStep, 0, maxBackups+1)
	steps = append(steps, rotationStep{To: fmt.Sprintf("%s.%d", path, maxBackups)})
	for i := maxBackups - 1; i >= 1; i-- {
		steps = append(steps, rotationStep{
			From: fmt.Sprintf("%s.%d", path, i),
			To:   fmt.Sprintf("%s.%d", path, i+1),
		})
	}
	steps = append(steps, rotationStep{From: path, To: path + ".1"})
	return steps
}

// rotate は現行ファイルを .1 へ退避し、古い世代を繰り下げる
func (w *RotatingWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close log file for rotation: %w", err)
	}
	w.file = nil

	for _, step := range rotationPlan(w.path, w.maxBackups) {
		if step.From == "" {
			if err := os.Remove(step.To); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove oldest log backup: %w", err)
			}
			continue
		}
		if err := os.Rename(step.From, step.To); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rotate log backup: %w", err)
		}
	}
	return w.open()
}
