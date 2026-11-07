package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func boardCommunicationTestConfig() *Config {
	cfg := &Config{
		Server:  ServerConfig{Port: 8080},
		Session: SessionConfig{StorageDir: "./data"},
	}
	cfg.setDefaults()
	return cfg
}

func validBoardCommunicationConfig(t *testing.T) BoardCommunicationConfig {
	t.Helper()
	return BoardCommunicationConfig{
		Enabled:     true,
		URL:         "https://board.example.invalid/",
		TokenFile:   filepath.Join(t.TempDir(), "board-token-canary"),
		PrincipalID: "rencrow-comm",
		ChannelIDs:  []string{"rencrow"},
		PeerIDs:     []string{"dots-comm"},
		CutoverAt:   "2099-01-01T00:00:00Z",
	}
}

func TestBoardCommunicationConfigDefaultsOffAndIgnoresDisabledFields(t *testing.T) {
	cfg := boardCommunicationTestConfig()
	if cfg.BoardCommunication.Enabled {
		t.Fatal("board communication must default to disabled")
	}

	cfg.BoardCommunication = BoardCommunicationConfig{
		URL:         "http://remote.example.invalid/not-a-root?secret=canary",
		UnixSocket:  "relative/socket",
		TokenFile:   "relative/token",
		PrincipalID: "INVALID PRINCIPAL",
		ChannelIDs:  []string{"invalid channel"},
		PeerIDs:     []string{"invalid peer"},
		CutoverAt:   "not-a-time",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled board communication fields must be inactive: %v", err)
	}
}

func TestBoardCommunicationConfigAcceptsEnabledTransportShapes(t *testing.T) {
	cases := []struct {
		name   string
		change func(*BoardCommunicationConfig)
	}{
		{
			name: "https root endpoint",
			change: func(cfg *BoardCommunicationConfig) {
				cfg.URL = "https://board.example.invalid/"
			},
		},
		{
			name: "http loopback endpoint",
			change: func(cfg *BoardCommunicationConfig) {
				cfg.URL = "http://127.0.0.1:18792/"
			},
		},
		{
			name: "unix socket",
			change: func(cfg *BoardCommunicationConfig) {
				cfg.URL = ""
				cfg.UnixSocket = filepath.Join(t.TempDir(), "board.sock")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := boardCommunicationTestConfig()
			board := validBoardCommunicationConfig(t)
			tc.change(&board)
			cfg.BoardCommunication = board
			if err := cfg.Validate(); err != nil {
				t.Fatalf("valid enabled board communication shape: %v", err)
			}
		})
	}
}

func TestBoardCommunicationConfigAcceptsDistinctScopedCHATResponderActors(t *testing.T) {
	cfg := boardCommunicationTestConfig()
	board := validBoardCommunicationConfig(t)
	board.Responders = []BoardCommunicationResponderConfig{
		{
			ActorID: "shiro", TokenFile: filepath.Join(t.TempDir(), "shiro-token"),
			ChannelIDs: []string{"rencrow"}, PeerIDs: []string{"dots-comm"}, CutoverAt: "2099-01-01T00:00:00Z",
		},
		{
			ActorID: "midori", TokenFile: filepath.Join(t.TempDir(), "midori-token"),
			ChannelIDs: []string{"rencrow"}, PeerIDs: []string{"dots-comm"}, CutoverAt: "2099-01-02T00:00:00Z",
		},
		{
			ActorID: "kuro", TokenFile: filepath.Join(t.TempDir(), "kuro-token"),
			ChannelIDs: []string{"rencrow"}, PeerIDs: []string{"dots-comm"}, CutoverAt: "2099-01-03T00:00:00Z",
		},
	}
	cfg.BoardCommunication = board
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid distinct responder scopes rejected: %v", err)
	}
}

func TestBoardCommunicationConfigRejectsUnsafeResponderScopes(t *testing.T) {
	tests := []struct {
		name   string
		change func(*BoardCommunicationConfig)
	}{
		{name: "responders require enabled config", change: func(board *BoardCommunicationConfig) { board.Enabled = false }},
		{name: "Heavy alias is not a responder actor", change: func(board *BoardCommunicationConfig) { board.Responders[0].ActorID = "heavy" }},
		{name: "duplicate actor", change: func(board *BoardCommunicationConfig) {
			board.Responders = append(board.Responders, board.Responders[0])
		}},
		{name: "legacy principal reuse", change: func(board *BoardCommunicationConfig) { board.Responders[0].ActorID = board.PrincipalID }},
		{name: "legacy token path reuse", change: func(board *BoardCommunicationConfig) { board.Responders[0].TokenFile = board.TokenFile }},
		{name: "responder token path reuse", change: func(board *BoardCommunicationConfig) { board.Responders[1].TokenFile = board.Responders[0].TokenFile }},
		{name: "relative responder token", change: func(board *BoardCommunicationConfig) { board.Responders[0].TokenFile = "shiro.token" }},
		{name: "empty responder channels", change: func(board *BoardCommunicationConfig) { board.Responders[0].ChannelIDs = nil }},
		{name: "duplicate responder peer", change: func(board *BoardCommunicationConfig) {
			board.Responders[0].PeerIDs = []string{"dots-comm", "dots-comm"}
		}},
		{name: "self peer", change: func(board *BoardCommunicationConfig) { board.Responders[0].PeerIDs = []string{"shiro"} }},
		{name: "malformed responder cutover", change: func(board *BoardCommunicationConfig) { board.Responders[0].CutoverAt = "2099-01-01T00:00:00+00:00" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := boardCommunicationTestConfig()
			board := validBoardCommunicationConfig(t)
			board.Responders = []BoardCommunicationResponderConfig{
				{
					ActorID: "shiro", TokenFile: filepath.Join(t.TempDir(), "shiro-token"),
					ChannelIDs: []string{"rencrow"}, PeerIDs: []string{"dots-comm"}, CutoverAt: "2099-01-01T00:00:00Z",
				},
				{
					ActorID: "midori", TokenFile: filepath.Join(t.TempDir(), "midori-token"),
					ChannelIDs: []string{"rencrow"}, PeerIDs: []string{"dots-comm"}, CutoverAt: "2099-01-02T00:00:00Z",
				},
			}
			test.change(&board)
			cfg.BoardCommunication = board
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "board_communication") {
				t.Fatalf("Validate() error = %v, want responder scope rejection", err)
			}
		})
	}
}

func TestBoardCommunicationConfigValidationDoesNotRequireTokenOrSocketAvailability(t *testing.T) {
	cfg := boardCommunicationTestConfig()
	board := validBoardCommunicationConfig(t)
	board.URL = ""
	board.UnixSocket = filepath.Join(t.TempDir(), "missing-board-socket-canary")
	board.TokenFile = filepath.Join(t.TempDir(), "missing-board-token-canary")
	cfg.BoardCommunication = board

	for _, path := range []string{board.TokenFile, board.UnixSocket} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("test fixture path unexpectedly exists: %q", path)
		}
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("shape validation must not require token or socket availability: %v", err)
	}
}

func TestBoardCommunicationConfigRejectsInvalidEnabledShapes(t *testing.T) {
	type invalidCase struct {
		name   string
		change func(*BoardCommunicationConfig)
	}
	uncleanPath := func(path string) string {
		return filepath.Dir(path) + string(os.PathSeparator) + "." + string(os.PathSeparator) + filepath.Base(path)
	}
	cases := []invalidCase{
		{name: "transport missing", change: func(cfg *BoardCommunicationConfig) { cfg.URL = "" }},
		{name: "both transports", change: func(cfg *BoardCommunicationConfig) { cfg.UnixSocket = filepath.Join(t.TempDir(), "board.sock") }},
		{name: "whitespace socket with url", change: func(cfg *BoardCommunicationConfig) { cfg.UnixSocket = " " }},
		{name: "whitespace url with socket", change: func(cfg *BoardCommunicationConfig) {
			cfg.URL, cfg.UnixSocket = " ", filepath.Join(t.TempDir(), "board.sock")
		}},
		{name: "unsupported scheme", change: func(cfg *BoardCommunicationConfig) { cfg.URL = "ftp://board.example.invalid" }},
		{name: "http non-loopback", change: func(cfg *BoardCommunicationConfig) { cfg.URL = "http://board.example.invalid" }},
		{name: "userinfo", change: func(cfg *BoardCommunicationConfig) { cfg.URL = "https://user:pass@board.example.invalid" }},
		{name: "query", change: func(cfg *BoardCommunicationConfig) { cfg.URL = "https://board.example.invalid/?x=1" }},
		{name: "fragment", change: func(cfg *BoardCommunicationConfig) { cfg.URL = "https://board.example.invalid/#board" }},
		{name: "non-root path", change: func(cfg *BoardCommunicationConfig) { cfg.URL = "https://board.example.invalid/api/v1" }},
		{name: "relative socket", change: func(cfg *BoardCommunicationConfig) { cfg.URL, cfg.UnixSocket = "", "board.sock" }},
		{name: "unclean socket", change: func(cfg *BoardCommunicationConfig) {
			path := filepath.Join(t.TempDir(), "board.sock")
			cfg.URL, cfg.UnixSocket = "", uncleanPath(path)
		}},
		{name: "relative token file", change: func(cfg *BoardCommunicationConfig) { cfg.TokenFile = "board-token" }},
		{name: "unclean token file", change: func(cfg *BoardCommunicationConfig) {
			cfg.TokenFile = uncleanPath(filepath.Join(t.TempDir(), "board-token"))
		}},
		{name: "empty principal", change: func(cfg *BoardCommunicationConfig) { cfg.PrincipalID = "" }},
		{name: "noncanonical principal", change: func(cfg *BoardCommunicationConfig) { cfg.PrincipalID = "Rencrow-Comm" }},
		{name: "empty channel allowlist", change: func(cfg *BoardCommunicationConfig) { cfg.ChannelIDs = nil }},
		{name: "noncanonical channel id", change: func(cfg *BoardCommunicationConfig) { cfg.ChannelIDs = []string{"channel alpha"} }},
		{name: "duplicate channel id", change: func(cfg *BoardCommunicationConfig) { cfg.ChannelIDs = []string{"rencrow", "rencrow"} }},
		{name: "empty peer allowlist", change: func(cfg *BoardCommunicationConfig) { cfg.PeerIDs = nil }},
		{name: "noncanonical peer id", change: func(cfg *BoardCommunicationConfig) { cfg.PeerIDs = []string{"dots:comm"} }},
		{name: "duplicate peer id", change: func(cfg *BoardCommunicationConfig) { cfg.PeerIDs = []string{"dots-comm", "dots-comm"} }},
		{name: "self peer", change: func(cfg *BoardCommunicationConfig) { cfg.PeerIDs = []string{cfg.PrincipalID} }},
		{name: "cutover missing", change: func(cfg *BoardCommunicationConfig) { cfg.CutoverAt = "" }},
		{name: "cutover requires literal utc z", change: func(cfg *BoardCommunicationConfig) { cfg.CutoverAt = "2026-11-05T00:00:00+00:00" }},
		{name: "invalid cutover", change: func(cfg *BoardCommunicationConfig) { cfg.CutoverAt = "not-a-time" }},
		{name: "zero cutover", change: func(cfg *BoardCommunicationConfig) { cfg.CutoverAt = "0001-01-01T00:00:00Z" }},
	}
	if runtime.GOOS != "windows" {
		cases = append(cases,
			invalidCase{name: "windows drive socket is relative on unix", change: func(cfg *BoardCommunicationConfig) {
				cfg.URL, cfg.UnixSocket = "", "C:/board.sock"
			}},
			invalidCase{name: "windows drive token is relative on unix", change: func(cfg *BoardCommunicationConfig) {
				cfg.TokenFile = "C:/board-token"
			}},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := boardCommunicationTestConfig()
			board := validBoardCommunicationConfig(t)
			tc.change(&board)
			cfg.BoardCommunication = board
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "board_communication") {
				t.Fatalf("Validate() error = %v, want a board_communication shape error", err)
			}
		})
	}
}

func TestReadBoardCommunicationBearerTokenRejectsWindowsDriveRelativePathOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows drive paths are absolute on Windows")
	}

	workingDir := t.TempDir()
	relativePath := filepath.Join("C:", "board-token")
	fixturePath := filepath.Join(workingDir, relativePath)
	if err := os.MkdirAll(filepath.Dir(fixturePath), 0o700); err != nil {
		t.Fatalf("create private fixture directory: %v", err)
	}
	if err := os.Chmod(filepath.Dir(fixturePath), 0o700); err != nil {
		t.Fatalf("protect private fixture directory: %v", err)
	}
	const token = "relative-path-token-canary"
	if err := os.WriteFile(fixturePath, []byte(token), 0o600); err != nil {
		t.Fatalf("write private fixture: %v", err)
	}
	if err := os.Chmod(fixturePath, 0o600); err != nil {
		t.Fatalf("protect private fixture: %v", err)
	}
	t.Chdir(workingDir)

	got, err := ReadBoardCommunicationBearerToken(relativePath)
	if err == nil {
		t.Fatal("reader accepted a Windows-style drive-relative path on Unix")
	}
	if strings.Contains(err.Error(), relativePath) || strings.Contains(err.Error(), token) {
		t.Fatal("reader diagnostic leaked the relative path or token contents")
	}
	if strings.Contains(string(got), token) {
		t.Fatal("reader returned token bytes for a relative path")
	}
}

func TestLoadConfigReadsBoardCommunicationSection(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "missing-board-token-canary")
	content := fmt.Sprintf(`
server:
  port: 8080
board_communication:
  enabled: true
  url: https://board.example.invalid/
  token_file: %q
  principal_id: rencrow-comm
  channel_ids: [rencrow]
  peer_ids: [dots-comm]
  cutover_at: "2026-11-05T00:00:00Z"
`, tokenPath)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.BoardCommunication; !got.Enabled || got.URL != "https://board.example.invalid/" || got.TokenFile != tokenPath ||
		got.PrincipalID != "rencrow-comm" || len(got.ChannelIDs) != 1 || got.ChannelIDs[0] != "rencrow" ||
		len(got.PeerIDs) != 1 || got.PeerIDs[0] != "dots-comm" || got.CutoverAt != "2026-11-05T00:00:00Z" {
		t.Fatalf("loaded board_communication = %+v", got)
	}
}

func TestLoadConfigReadsBoardCommunicationResponderList(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "kuro-token")
	content := fmt.Sprintf(`
server:
  port: 8080
board_communication:
  enabled: true
  url: https://board.example.invalid/
  token_file: %q
  principal_id: rencrow-comm
  channel_ids: [rencrow]
  peer_ids: [dots-comm]
  cutover_at: "2026-11-05T00:00:00Z"
  responders:
    - actor_id: kuro
      token_file: %q
      channel_ids: [rencrow]
      peer_ids: [dots-comm]
      cutover_at: "2026-11-06T00:00:00Z"
`, filepath.Join(t.TempDir(), "owner-token"), tokenPath)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.BoardCommunication.Responders) != 1 {
		t.Fatalf("loaded responders = %#v, want one", cfg.BoardCommunication.Responders)
	}
	got := cfg.BoardCommunication.Responders[0]
	if got.ActorID != "kuro" || got.TokenFile != tokenPath || len(got.ChannelIDs) != 1 || got.ChannelIDs[0] != "rencrow" ||
		len(got.PeerIDs) != 1 || got.PeerIDs[0] != "dots-comm" || got.CutoverAt != "2026-11-06T00:00:00Z" {
		t.Fatalf("loaded responder = %+v", got)
	}
}

func TestReadBoardCommunicationBearerTokenReadsProtectedFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board-token-canary")
	const token = "board-fixture-token_123=="
	if err := os.WriteFile(path, []byte(token+"\r\n"), 0o600); err != nil {
		t.Fatalf("write token fixture: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("protect token fixture: %v", err)
	}
	got, err := ReadBoardCommunicationBearerToken(path)
	if err != nil {
		t.Fatalf("ReadBoardCommunicationBearerToken: %v", err)
	}
	if string(got) != token {
		t.Fatalf("token bytes differ from the bounded bearer fixture")
	}
}

func TestReadBoardCommunicationBearerTokenRejectsAndRedactsUnsafeFixtures(t *testing.T) {
	type fixture struct {
		name    string
		write   bool
		mode    os.FileMode
		content []byte
	}
	cases := []fixture{
		{name: "missing"},
		{name: "unsafe permissions", write: true, mode: 0o644, content: []byte("unsafe-board-token-canary")},
		{name: "empty", write: true, mode: 0o600},
		{name: "invalid bearer grammar", write: true, mode: 0o600, content: []byte("invalid board token canary")},
		{name: "oversized", write: true, mode: 0o600, content: []byte(strings.Repeat("oversized-board-token-canary", maxStorageHostBearerTokenBytes+1))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "board-token-path-canary")
			if tc.write {
				if err := os.WriteFile(path, tc.content, tc.mode); err != nil {
					t.Fatalf("write token fixture: %v", err)
				}
				if err := os.Chmod(path, tc.mode); err != nil {
					t.Fatalf("set token fixture mode: %v", err)
				}
			}
			if _, err := ReadBoardCommunicationBearerToken(path); err == nil {
				t.Fatal("unsafe or invalid token fixture was accepted")
			} else {
				if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), filepath.Base(path)) {
					t.Fatalf("error leaked token-file path: %v", err)
				}
				if len(tc.content) > 0 && strings.Contains(err.Error(), string(tc.content)) {
					t.Fatalf("error leaked token contents: %v", err)
				}
			}
		})
	}
}
