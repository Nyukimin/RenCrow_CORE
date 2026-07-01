package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
	"golang.org/x/crypto/ssh"
)

// failingWriter はWrite時に常にエラーを返すモック
type failingWriter struct{}

func (w *failingWriter) Write(p []byte) (int, error) { return 0, fmt.Errorf("write error") }
func (w *failingWriter) Close() error                { return nil }

// === sshDialer / sshClient / sshSession モック ===

type mockDialer struct {
	dialErr error
	client  sshClient
}

func (d *mockDialer) Dial(network, addr string, config *ssh.ClientConfig) (sshClient, error) {
	if d.dialErr != nil {
		return nil, d.dialErr
	}
	return d.client, nil
}

type mockSSHClient struct {
	sessionErr error
	session    sshSession
	closed     bool
}

func (c *mockSSHClient) NewSession() (sshSession, error) {
	if c.sessionErr != nil {
		return nil, c.sessionErr
	}
	return c.session, nil
}

func (c *mockSSHClient) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	return true, nil, nil
}

func (c *mockSSHClient) Close() error {
	c.closed = true
	return nil
}

type mockSSHSession struct {
	stdinPipeErr  error
	stdoutPipeErr error
	startErr      error
	stdin         io.WriteCloser
	stdout        io.Reader
	closed        bool
}

func (s *mockSSHSession) StdinPipe() (io.WriteCloser, error) {
	if s.stdinPipeErr != nil {
		return nil, s.stdinPipeErr
	}
	return s.stdin, nil
}

func (s *mockSSHSession) StdoutPipe() (io.Reader, error) {
	if s.stdoutPipeErr != nil {
		return nil, s.stdoutPipeErr
	}
	return s.stdout, nil
}

func (s *mockSSHSession) Start(cmd string) error {
	return s.startErr
}

func (s *mockSSHSession) Close() error {
	s.closed = true
	return nil
}

// generateTestKeyFile はテスト用のSSH秘密鍵ファイルを生成
func generateTestKeyFile(t *testing.T, dir string) string {
	t.Helper()
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	pemBlock, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	keyPath := filepath.Join(dir, "id_ed25519_test")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return keyPath
}

func TestNewSSHTransport(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "picoclaw", "/home/user/.ssh/id_ed25519", "worker")

	if st.host != "192.168.1.100:22" {
		t.Errorf("Expected host '192.168.1.100:22', got '%s'", st.host)
	}
	if st.user != "picoclaw" {
		t.Errorf("Expected user 'picoclaw', got '%s'", st.user)
	}
	if st.agentType != "worker" {
		t.Errorf("Expected agentType 'worker', got '%s'", st.agentType)
	}
	if st.closed {
		t.Error("Should not be closed initially")
	}
}

func TestSSHTransport_IsHealthy_BeforeConnect(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "picoclaw", "/path/to/key", "worker")
	defer st.Close()

	// 接続前はnot healthy
	if st.IsHealthy() {
		t.Error("Should not be healthy before Connect()")
	}
}

func TestSSHTransport_Close(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "picoclaw", "/path/to/key", "worker")

	if err := st.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if st.IsHealthy() {
		t.Error("Should not be healthy after Close()")
	}

	// Double close should be idempotent
	if err := st.Close(); err != nil {
		t.Fatalf("Second close failed: %v", err)
	}
}

func TestSSHTransport_SendAfterClose(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "picoclaw", "/path/to/key", "worker")
	st.Close()

	msg := domaintransport.NewMessage("A", "B", "s1", "j1", "test")
	err := st.Send(context.Background(), msg)
	if err == nil {
		t.Error("Expected error on send after close")
	}
}

func TestSSHTransport_ReceiveAfterClose(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "picoclaw", "/path/to/key", "worker")
	st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := st.Receive(ctx)
	if err == nil {
		t.Error("Expected error on receive after close")
	}
}

func TestSSHTransport_ConnectFailsWithBadKey(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "picoclaw", "/nonexistent/key", "worker")
	defer st.Close()

	err := st.Connect()
	if err == nil {
		t.Error("Expected error with non-existent key file")
	}
}

func TestSSHTransport_SendWithoutConnection(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "picoclaw", "/path/to/key", "worker")
	defer st.Close()

	msg := domaintransport.NewMessage("A", "B", "s1", "j1", "test")
	err := st.Send(context.Background(), msg)
	if err == nil {
		t.Error("Expected error on send without connection")
	}
}

// === Step 2: getHostKeyCallback / receiveLoop / tryReconnect テスト ===

func TestSSHTransport_GetHostKeyCallback_NoKnownHosts(t *testing.T) {
	// HOME を known_hosts が無いtmpdirに変更
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	defer st.Close()

	callback, err := st.getHostKeyCallback()
	if err != nil {
		t.Fatalf("getHostKeyCallback should not error: %v", err)
	}
	if callback == nil {
		t.Error("callback should not be nil (InsecureIgnoreHostKey)")
	}
}

func TestSSHTransport_GetHostKeyCallback_WithKnownHosts(t *testing.T) {
	// HOME をtmpdirに変更し、.ssh/known_hosts を作成
	tmpHome := t.TempDir()
	sshDir := filepath.Join(tmpHome, ".ssh")
	os.MkdirAll(sshDir, 0700)

	// 有効な known_hosts を作成（空でもパース可能）
	knownHostsPath := filepath.Join(sshDir, "known_hosts")
	os.WriteFile(knownHostsPath, []byte(""), 0600)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	defer st.Close()

	callback, err := st.getHostKeyCallback()
	if err != nil {
		t.Fatalf("getHostKeyCallback should succeed with empty known_hosts: %v", err)
	}
	if callback == nil {
		t.Error("callback should not be nil")
	}
}

func TestSSHTransport_ReceiveLoop(t *testing.T) {
	pr, pw := io.Pipe()

	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	st.stdout = pr
	st.receiveLoopDone = make(chan struct{})

	go st.receiveLoop()

	// JSONメッセージを送信
	msg := domaintransport.NewMessage("A", "B", "s1", "j1", "hello-receive-loop")
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	pw.Write(data)

	// 受信確認
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	received, err := st.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}
	if received.Content != "hello-receive-loop" {
		t.Errorf("Expected 'hello-receive-loop', got '%s'", received.Content)
	}

	// クリーンアップ
	pw.Close()
	close(st.done)
	<-st.receiveLoopDone
}

func TestSSHTransport_ReceiveLoop_InvalidJSON(t *testing.T) {
	pr, pw := io.Pipe()

	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	st.stdout = pr
	st.receiveLoopDone = make(chan struct{})

	go st.receiveLoop()

	// 不正JSON → スキップされる
	pw.Write([]byte("this is not json\n"))

	// 有効なメッセージ → 受信される
	msg := domaintransport.NewMessage("A", "B", "s1", "j1", "valid-after-invalid")
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	pw.Write(data)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	received, err := st.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}
	if received.Content != "valid-after-invalid" {
		t.Errorf("Expected 'valid-after-invalid', got '%s'", received.Content)
	}

	pw.Close()
	close(st.done)
	<-st.receiveLoopDone
}

func TestSSHTransport_ReceiveLoop_DoneSignal(t *testing.T) {
	pr, pw := io.Pipe()

	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	st.stdout = pr
	st.receiveLoopDone = make(chan struct{})

	go st.receiveLoop()

	// done をクローズしてループ終了
	close(st.done)
	pw.Close()

	// receiveLoopDone が閉じられるのを待つ
	select {
	case <-st.receiveLoopDone:
		// OK
	case <-time.After(2 * time.Second):
		t.Error("receiveLoop did not terminate after done signal")
	}
}

func TestSSHTransport_TryReconnect_AlreadyClosed(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	st.closed = true

	// closed状態では tryReconnect は即座にリターン
	st.tryReconnect()

	// reconnecting フラグが立っていないことを確認
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.reconnecting {
		t.Error("reconnecting should not be set when already closed")
	}
}

func TestSSHTransport_TryReconnect_AlreadyReconnecting(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	st.reconnecting = true

	// reconnecting中は tryReconnect は即座にリターン
	st.tryReconnect()

	// まだreconnecting状態のはず（変更なし）
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.reconnecting {
		t.Error("reconnecting should remain true")
	}
}

func TestSSHTransport_IsHealthy_WhileReconnecting(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	defer st.Close()

	st.reconnecting = true

	if st.IsHealthy() {
		t.Error("Should not be healthy while reconnecting")
	}
}

func TestSSHTransport_StrictHostKey_NoKnownHosts(t *testing.T) {
	// HOME を known_hosts が無いtmpdirに変更
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	st := NewSSHTransportStrict("192.168.1.100:22", "user", "/path/to/key", "worker", true)
	defer st.Close()

	_, err := st.getHostKeyCallback()
	if err == nil {
		t.Error("Expected error with strict_host_key=true and no known_hosts")
	}
	if !strings.Contains(err.Error(), "strict_host_key=true") {
		t.Errorf("Error should mention strict_host_key, got: %v", err)
	}
}

func TestSSHTransport_StrictHostKey_WithKnownHosts(t *testing.T) {
	tmpHome := t.TempDir()
	sshDir := filepath.Join(tmpHome, ".ssh")
	os.MkdirAll(sshDir, 0700)
	os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte(""), 0600)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	st := NewSSHTransportStrict("192.168.1.100:22", "user", "/path/to/key", "worker", true)
	defer st.Close()

	callback, err := st.getHostKeyCallback()
	if err != nil {
		t.Fatalf("Should succeed with known_hosts present: %v", err)
	}
	if callback == nil {
		t.Error("callback should not be nil")
	}
}

func TestNewSSHTransportStrict(t *testing.T) {
	st := NewSSHTransportStrict("host:22", "user", "/key", "worker", true)
	if !st.strictHostKey {
		t.Error("strictHostKey should be true")
	}

	st2 := NewSSHTransportStrict("host:22", "user", "/key", "worker", false)
	if st2.strictHostKey {
		t.Error("strictHostKey should be false")
	}
}

func TestSSHTransport_ReceiveLoop_ChannelFull(t *testing.T) {
	pr, pw := io.Pipe()

	// バッファサイズ1のinboundチャネルで作成
	st := &SSHTransport{
		host:            "192.168.1.100:22",
		user:            "user",
		keyPath:         "/path/to/key",
		agentType:       "worker",
		inbound:         make(chan domaintransport.Message, 1),
		receiveLoopDone: make(chan struct{}),
		done:            make(chan struct{}),
		stdout:          pr,
	}

	go st.receiveLoop()

	// 2メッセージ送信 → 1つ目は受信、2つ目はチャネルフル（ドロップ）
	for i := 0; i < 2; i++ {
		msg := domaintransport.NewMessage("A", "B", "s1", "j1", "msg")
		data, _ := json.Marshal(msg)
		data = append(data, '\n')
		pw.Write(data)
	}

	// 少し待ってチャネルフルのログが出るのを確認
	time.Sleep(100 * time.Millisecond)

	// 1件は受信できるはず
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err := st.Receive(ctx)
	if err != nil {
		t.Fatalf("Should receive at least 1 message: %v", err)
	}

	pw.Close()
	close(st.done)
	<-st.receiveLoopDone
}

// === Step 1: Send() カバレッジ改善 ===

func TestSSHTransport_Send_WriteError(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	defer st.Close()

	// stdinをfailingWriterに差し替え
	st.stdin = &failingWriter{}

	msg := domaintransport.NewMessage("A", "B", "s1", "j1", "test")
	err := st.Send(context.Background(), msg)
	if err == nil {
		t.Error("Expected error on write failure")
	}
	if !strings.Contains(err.Error(), "write to SSH stdin") {
		t.Errorf("Expected 'write to SSH stdin' error, got: %v", err)
	}
}

func TestSSHTransport_Send_ContextCancelled(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	defer st.Close()

	// stdinを設定（到達前にctxキャンセル）
	st.stdin = &failingWriter{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 即座にキャンセル

	msg := domaintransport.NewMessage("A", "B", "s1", "j1", "test")
	err := st.Send(ctx, msg)
	if err == nil {
		t.Error("Expected error on cancelled context")
	}
}

// === Step 2: closeConnection() カバレッジ改善 ===

func TestSSHTransport_CloseConnection_AllNil(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	// stdin/session/client全てnil → パニックしないことを確認
	st.closeConnection()
}

func TestSSHTransport_CloseConnection_WithStdin(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	// stdinのみ非nil
	st.stdin = &failingWriter{}
	st.closeConnection()
}

// === Step 3: Receive() 追加パス ===

func TestSSHTransport_Receive_InboundClosed(t *testing.T) {
	st := NewSSHTransport("192.168.1.100:22", "user", "/path/to/key", "worker")
	// inboundチャネルをcloseして !ok パスをテスト
	close(st.inbound)

	_, err := st.Receive(context.Background())
	if err == nil {
		t.Error("Expected error on closed inbound channel")
	}
	if !strings.Contains(err.Error(), "transport is closed") {
		t.Errorf("Expected 'transport is closed' error, got: %v", err)
	}
}

// === Step 5: establishConnection() DI テスト ===

func TestEstablishConnection_ParseKeyError(t *testing.T) {
	tmpDir := t.TempDir()
	// 不正な鍵ファイル
	badKeyPath := filepath.Join(tmpDir, "bad_key")
	os.WriteFile(badKeyPath, []byte("not a valid key"), 0600)

	st := NewSSHTransport("192.168.1.100:22", "user", badKeyPath, "worker")
	defer st.Close()

	err := st.establishConnection()
	if err == nil {
		t.Error("Expected error on invalid key")
	}
	if !strings.Contains(err.Error(), "parse SSH key") {
		t.Errorf("Expected 'parse SSH key' error, got: %v", err)
	}
}

func TestEstablishConnection_DialError(t *testing.T) {
	tmpDir := t.TempDir()
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	keyPath := generateTestKeyFile(t, tmpDir)

	st := NewSSHTransport("192.168.1.100:22", "user", keyPath, "worker")
	defer st.Close()

	st.dialer = &mockDialer{dialErr: fmt.Errorf("connection refused")}

	err := st.establishConnection()
	if err == nil {
		t.Error("Expected error on dial failure")
	}
	if !strings.Contains(err.Error(), "SSH dial") {
		t.Errorf("Expected 'SSH dial' error, got: %v", err)
	}
}

func TestEstablishConnection_NewSessionError(t *testing.T) {
	tmpDir := t.TempDir()
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	keyPath := generateTestKeyFile(t, tmpDir)
	mockClient := &mockSSHClient{sessionErr: fmt.Errorf("session creation failed")}

	st := NewSSHTransport("192.168.1.100:22", "user", keyPath, "worker")
	defer st.Close()

	st.dialer = &mockDialer{client: mockClient}

	err := st.establishConnection()
	if err == nil {
		t.Error("Expected error on session creation failure")
	}
	if !strings.Contains(err.Error(), "SSH session") {
		t.Errorf("Expected 'SSH session' error, got: %v", err)
	}
	if !mockClient.closed {
		t.Error("Client should be closed on session error")
	}
}

func TestEstablishConnection_StdinPipeError(t *testing.T) {
	tmpDir := t.TempDir()
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	keyPath := generateTestKeyFile(t, tmpDir)
	mockSession := &mockSSHSession{stdinPipeErr: fmt.Errorf("stdin pipe failed")}
	mockClient := &mockSSHClient{session: mockSession}

	st := NewSSHTransport("192.168.1.100:22", "user", keyPath, "worker")
	defer st.Close()

	st.dialer = &mockDialer{client: mockClient}

	err := st.establishConnection()
	if err == nil {
		t.Error("Expected error on stdin pipe failure")
	}
	if !strings.Contains(err.Error(), "stdin pipe") {
		t.Errorf("Expected 'stdin pipe' error, got: %v", err)
	}
	if !mockSession.closed {
		t.Error("Session should be closed on stdin pipe error")
	}
	if !mockClient.closed {
		t.Error("Client should be closed on stdin pipe error")
	}
}

func TestEstablishConnection_StdoutPipeError(t *testing.T) {
	tmpDir := t.TempDir()
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	keyPath := generateTestKeyFile(t, tmpDir)
	mockSession := &mockSSHSession{
		stdin:         &failingWriter{}, // StdinPipe succeeds
		stdoutPipeErr: fmt.Errorf("stdout pipe failed"),
	}
	mockClient := &mockSSHClient{session: mockSession}

	st := NewSSHTransport("192.168.1.100:22", "user", keyPath, "worker")
	defer st.Close()

	st.dialer = &mockDialer{client: mockClient}

	err := st.establishConnection()
	if err == nil {
		t.Error("Expected error on stdout pipe failure")
	}
	if !strings.Contains(err.Error(), "stdout pipe") {
		t.Errorf("Expected 'stdout pipe' error, got: %v", err)
	}
	if !mockSession.closed {
		t.Error("Session should be closed on stdout pipe error")
	}
	if !mockClient.closed {
		t.Error("Client should be closed on stdout pipe error")
	}
}

func TestEstablishConnection_StartError(t *testing.T) {
	tmpDir := t.TempDir()
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	keyPath := generateTestKeyFile(t, tmpDir)
	pr, _ := io.Pipe()
	mockSession := &mockSSHSession{
		stdin:    &failingWriter{},
		stdout:   pr,
		startErr: fmt.Errorf("command start failed"),
	}
	mockClient := &mockSSHClient{session: mockSession}

	st := NewSSHTransport("192.168.1.100:22", "user", keyPath, "worker")
	defer st.Close()

	st.dialer = &mockDialer{client: mockClient}

	err := st.establishConnection()
	if err == nil {
		t.Error("Expected error on start failure")
	}
	if !strings.Contains(err.Error(), "start remote command") {
		t.Errorf("Expected 'start remote command' error, got: %v", err)
	}
	if !mockSession.closed {
		t.Error("Session should be closed on start error")
	}
	if !mockClient.closed {
		t.Error("Client should be closed on start error")
	}
}

func TestEstablishConnection_Success(t *testing.T) {
	tmpDir := t.TempDir()
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	keyPath := generateTestKeyFile(t, tmpDir)
	pr, pw := io.Pipe()
	mockSession := &mockSSHSession{
		stdin:  &failingWriter{},
		stdout: pr,
	}
	mockClient := &mockSSHClient{session: mockSession}

	st := NewSSHTransport("192.168.1.100:22", "user", keyPath, "worker")

	st.dialer = &mockDialer{client: mockClient}

	err := st.establishConnection()
	if err != nil {
		t.Fatalf("Expected success, got: %v", err)
	}

	if st.client == nil {
		t.Error("client should be set")
	}
	if st.session == nil {
		t.Error("session should be set")
	}
	if st.stdin == nil {
		t.Error("stdin should be set")
	}

	// クリーンアップ
	pw.Close()
	st.Close()
}
