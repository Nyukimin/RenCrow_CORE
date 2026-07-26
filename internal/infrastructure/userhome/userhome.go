// Package userhome はホームディレクトリの解決を一箇所に集約する
//
// os.Getenv("HOME") の直接参照は Windows で空文字になる。Windows が参照するのは
// USERPROFILE であり、HOME だけを見る実装はドライブ直下などの意図しないパスを
// 生むか、機能を丸ごと無効化する。os.UserHomeDir は OS ごとに正しい環境変数を
// 参照するため、常にこちらを使う。
package userhome

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Dir はホームディレクトリを返す
//
// 解決できない場合は空文字を返さずエラーにする。呼び出し元が空文字のまま
// filepath.Join へ渡すと、ドライブ直下やカレント相対の誤ったパスになる。
func Dir() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return "", fmt.Errorf("resolve home directory: empty result")
	}
	return home, nil
}

// Join はホームディレクトリ配下のパスを組み立てる
func Join(elem ...string) (string, error) {
	home, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, elem...)...), nil
}
