package core

import "testing"

// TestIsMissingCommandTextCoversWindowsWording は「コマンドが見つからない」の
// 判定が OS 固有文言を網羅することを確認する
//
// shell が返す文言は OS ごとに異なる。Unix 系は "command not found" と
// exit status 127 を返すが、Windows の cmd.exe は
// "'x' is not recognized as an internal or external command" と exit code 1 / 9009、
// PowerShell は "is not recognized as the name of a cmdlet" を返す。
func TestIsMissingCommandTextCoversWindowsWording(t *testing.T) {
	missing := []string{
		"sh: 1: foo: command not found",
		"bash: foo: command not found",
		"exit status 127",
		"'foo' is not recognized as an internal or external command,\noperable program or batch file.",
		"foo : The term 'foo' is not recognized as the name of a cmdlet, function, script file, or operable program.",
		"exec: \"foo\": executable file not found in %PATH%",
		"exec: \"foo\": executable file not found in $PATH",
		"exit status 9009",
	}
	for _, text := range missing {
		if !IsMissingCommandText(text) {
			t.Errorf("IsMissingCommandText(%q) = false, want true", text)
		}
	}

	notMissing := []string{
		"",
		"test failed: expected 1 got 2",
		"permission denied",
		"exit status 1",
	}
	for _, text := range notMissing {
		if IsMissingCommandText(text) {
			t.Errorf("IsMissingCommandText(%q) = true, want false", text)
		}
	}
}

// TestIsTransientNetworkTextCoversWindowsWording は一時的なネットワーク障害の
// 判定が OS 固有文言を網羅することを確認する
//
// Windows の dial 失敗は "No connection could be made because the target
// machine actively refused it."、reset は "An existing connection was forcibly
// closed by the remote host." であり、Unix の文言と一致しない。
func TestIsTransientNetworkTextCoversWindowsWording(t *testing.T) {
	transient := []string{
		"dial tcp 127.0.0.1:8080: connect: connection refused",
		"read tcp 127.0.0.1:8080: connection reset by peer",
		"dial tcp 127.0.0.1:8080: connectex: No connection could be made because the target machine actively refused it.",
		"read tcp 127.0.0.1:8080: wsarecv: An existing connection was forcibly closed by the remote host.",
		"Client.Timeout exceeded while awaiting headers",
		"context deadline exceeded",
	}
	for _, text := range transient {
		if !IsTransientNetworkText(text) {
			t.Errorf("IsTransientNetworkText(%q) = false, want true", text)
		}
	}

	notTransient := []string{
		"",
		"400 Bad Request",
		"invalid character 'x' looking for beginning of value",
	}
	for _, text := range notTransient {
		if IsTransientNetworkText(text) {
			t.Errorf("IsTransientNetworkText(%q) = true, want false", text)
		}
	}
}

// TestIsMissingPathTextCoversWindowsWording は「パスが見つからない」の判定が
// OS 固有文言を網羅することを確認する
func TestIsMissingPathTextCoversWindowsWording(t *testing.T) {
	missing := []string{
		"open /tmp/x: no such file or directory",
		"open C:\\tmp\\x: The system cannot find the file specified.",
		"open C:\\tmp\\x: The system cannot find the path specified.",
	}
	for _, text := range missing {
		if !IsMissingPathText(text) {
			t.Errorf("IsMissingPathText(%q) = false, want true", text)
		}
	}

	if IsMissingPathText("permission denied") {
		t.Error("IsMissingPathText(\"permission denied\") = true, want false")
	}
}
