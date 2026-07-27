package logging

import (
	"os"
	"path/filepath"
	"testing"
)

// 世代交代の判定と手順は純粋関数として検証する。
//
// ファイル作成・改名・削除を高頻度で繰り返すテストは、振る舞いとしては
// ランサムウェアと区別がつかず、セキュリティソフトの検知対象になり得る。
// ロジックを I/O から切り離すことで、検証内容を落とさずにファイル操作を
// 最小限にする。

// TestShouldRotate はサイズ上限の判定を確認する
func TestShouldRotate(t *testing.T) {
	tests := []struct {
		name        string
		currentSize int64
		incoming    int
		maxBytes    int64
		want        bool
	}{
		{name: "empty file always accepts", currentSize: 0, incoming: 1000, maxBytes: 100, want: false},
		{name: "fits", currentSize: 50, incoming: 40, maxBytes: 100, want: false},
		{name: "exactly at limit", currentSize: 60, incoming: 40, maxBytes: 100, want: false},
		{name: "exceeds limit", currentSize: 61, incoming: 40, maxBytes: 100, want: true},
		{name: "single write larger than limit", currentSize: 1, incoming: 1000, maxBytes: 100, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRotate(tc.currentSize, tc.incoming, tc.maxBytes); got != tc.want {
				t.Fatalf("shouldRotate(%d, %d, %d) = %v, want %v", tc.currentSize, tc.incoming, tc.maxBytes, got, tc.want)
			}
		})
	}
}

// TestRotationPlanOrdersOldestFirst は世代交代の手順が
// 「最古を削除してから古い順に繰り下げる」であることを確認する
//
// 順序を誤ると新しい世代が古い世代を上書きして内容を失う。
func TestRotationPlanOrdersOldestFirst(t *testing.T) {
	steps := rotationPlan("app.log", 3)

	want := []rotationStep{
		{From: "", To: "app.log.3"},
		{From: "app.log.2", To: "app.log.3"},
		{From: "app.log.1", To: "app.log.2"},
		{From: "app.log", To: "app.log.1"},
	}
	if len(steps) != len(want) {
		t.Fatalf("steps = %+v, want %d steps", steps, len(want))
	}
	for i, step := range steps {
		if step != want[i] {
			t.Fatalf("steps[%d] = %+v, want %+v", i, step, want[i])
		}
	}
}

// TestRotationPlanKeepsBackupsBounded は保持世代数を超えるファイルを
// 作らないことを確認する
func TestRotationPlanKeepsBackupsBounded(t *testing.T) {
	for _, maxBackups := range []int{1, 2, 5} {
		steps := rotationPlan("app.log", maxBackups)
		for _, step := range steps {
			if step.To > "app.log."+string(rune('0'+maxBackups)) && step.To != "app.log" {
				t.Fatalf("maxBackups=%d produced %q, which exceeds the bound", maxBackups, step.To)
			}
		}
		if steps[0].From != "" {
			t.Fatalf("maxBackups=%d: first step should delete the oldest, got %+v", maxBackups, steps[0])
		}
	}
}

// TestRotationPlanUsesDefaultForInvalidBackups は不正な世代数で既定値を
// 使うことを確認する
func TestRotationPlanUsesDefaultForInvalidBackups(t *testing.T) {
	steps := rotationPlan("app.log", 0)
	// 削除1 + 繰り下げ(DefaultMaxLogBackups-1) + 現行退避1
	if want := DefaultMaxLogBackups + 1; len(steps) != want {
		t.Fatalf("steps = %d, want %d", len(steps), want)
	}
}

// TestRotatingWriterWritesAndRotatesOnce は実ファイルでの最小の動作確認
//
// ファイル操作は「作成・1回の世代交代・読み取り」に限定する。
func TestRotatingWriterWritesAndRotatesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")

	w, err := NewRotatingWriter(path, 16, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}

	if _, err := w.Write([]byte("first line\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := w.Write([]byte("second line\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current log: %v", err)
	}
	if string(current) != "second line\n" {
		t.Fatalf("current log = %q, want %q", string(current), "second line\n")
	}

	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read rotated log: %v (世代交代していない)", err)
	}
	if string(rotated) != "first line\n" {
		t.Fatalf("rotated log = %q, want %q (内容が失われた)", string(rotated), "first line\n")
	}
}

// TestRotatingWriterCreatesParentDir は親ディレクトリが無くても書けることを
// 確認する
func TestRotatingWriterCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "app.log")

	w, err := NewRotatingWriter(path, 1024, 1)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("ok\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("log file should exist: %v", err)
	}
}
