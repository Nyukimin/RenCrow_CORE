package userhome

import (
	"path/filepath"
	"testing"
)

// TestDirUsesUserHomeDir はホームディレクトリの解決が os.UserHomeDir と
// 同じ環境変数を参照することを確認する
//
// os.Getenv("HOME") の直接参照は Windows で空になる。Windows が参照するのは
// USERPROFILE であるため、HOME だけを見る実装はドライブ直下などの
// 意図しないパスを生む。
func TestDirUsesUserHomeDir(t *testing.T) {
	want := t.TempDir()
	t.Setenv("HOME", want)
	t.Setenv("USERPROFILE", want)

	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	if got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestJoinBuildsPathUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got, err := Join(".rencrow", "logs", "sessions")
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	want := filepath.Join(home, ".rencrow", "logs", "sessions")
	if got != want {
		t.Fatalf("Join() = %q, want %q", got, want)
	}
}

// TestDirFailsWhenUnresolvable はホームディレクトリが解決できない場合に
// 空文字を黙って返さずエラーにすることを確認する
func TestDirFailsWhenUnresolvable(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	if _, err := Dir(); err == nil {
		t.Fatal("Dir() = nil error, want error when home is unresolvable")
	}
}
