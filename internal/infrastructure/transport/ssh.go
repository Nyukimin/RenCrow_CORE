package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	sshMaxRetries      = 3
	sshBaseBackoff     = 1 * time.Second
	sshInboundBufSize  = 100
	sshRemoteCommand   = "picoclaw-agent --standalone"
)

// sshDialer はSSHクライアント生成の抽象化（テスト用DI）
type sshDialer interface {
	Dial(network, addr string, config *ssh.ClientConfig) (sshClient, error)
}

// sshClient はssh.Clientの抽象化（テスト用DI）
type sshClient interface {
	NewSession() (sshSession, error)
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
	Close() error
}

// sshSession はssh.Sessionの抽象化（テスト用DI）
type sshSession interface {
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.Reader, error)
	Start(cmd string) error
	Close() error
}

// defaultDialer は本番用のSSHクライアント生成
type defaultDialer struct{}

func (d *defaultDialer) Dial(network, addr string, config *ssh.ClientConfig) (sshClient, error) {
	client, err := ssh.Dial(network, addr, config)
	if err != nil {
		return nil, err
	}
	return &realSSHClient{client: client}, nil
}

// realSSHClient は*ssh.ClientのアダプタでsshClientインターフェースを実装
type realSSHClient struct {
	client *ssh.Client
}

func (c *realSSHClient) NewSession() (sshSession, error) {
	return c.client.NewSession()
}

func (c *realSSHClient) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	return c.client.SendRequest(name, wantReply, payload)
}

func (c *realSSHClient) Close() error {
	return c.client.Close()
}

// SSHTransport はSSH経由のAgent間通信
// stdin/stdout上のJSON通信（1行1メッセージ）
type SSHTransport struct {
	host          string
	user          string
	keyPath       string
	agentType     string // "worker", "coder1", "coder2", "coder3"
	strictHostKey bool   // true: known_hosts必須（本番用）
	agentPath     string // リモートのpicoclaw-agentパス（空の場合はデフォルト）
	configPath    string // リモートのconfig.yamlパス（空の場合は省略）

	dialer  sshDialer
	client  sshClient
	session sshSession
	stdin   io.WriteCloser
	stdout  io.Reader

	inbound         chan domaintransport.Message
	sendMu          sync.Mutex     // エンコーダ保護
	receiveLoopDone chan struct{}   // receiveLoopの完了通知

	done         chan struct{}
	mu           sync.Mutex
	closed       bool
	reconnecting bool
}

// NewSSHTransport は新しいSSHTransportを作成
func NewSSHTransport(host, user, keyPath, agentType string) *SSHTransport {
	return &SSHTransport{
		host:            host,
		user:            user,
		keyPath:         keyPath,
		agentType:       agentType,
		dialer:          &defaultDialer{},
		inbound:         make(chan domaintransport.Message, sshInboundBufSize),
		receiveLoopDone: make(chan struct{}),
		done:            make(chan struct{}),
	}
}

// NewSSHTransportStrict は本番用SSHTransportを作成（known_hosts必須）
func NewSSHTransportStrict(host, user, keyPath, agentType string, strictHostKey bool) *SSHTransport {
	return &SSHTransport{
		host:            host,
		user:            user,
		keyPath:         keyPath,
		agentType:       agentType,
		strictHostKey:   strictHostKey,
		dialer:          &defaultDialer{},
		inbound:         make(chan domaintransport.Message, sshInboundBufSize),
		receiveLoopDone: make(chan struct{}),
		done:            make(chan struct{}),
	}
}

// WithRemotePaths はリモートのagentパスとconfigパスを設定
func (t *SSHTransport) WithRemotePaths(agentPath, configPath string) *SSHTransport {
	t.agentPath = agentPath
	t.configPath = configPath
	return t
}

// Connect はSSH接続を確立しリモートAgentを起動
func (t *SSHTransport) Connect() error {
	return t.connectWithRetry()
}

func (t *SSHTransport) connectWithRetry() error {
	var lastErr error
	for i := 0; i < sshMaxRetries; i++ {
		if i > 0 {
			backoff := sshBaseBackoff * time.Duration(1<<(i-1))
			log.Printf("[SSHTransport] Retry %d/%d after %v", i+1, sshMaxRetries, backoff)
			time.Sleep(backoff)
		}

		if err := t.establishConnection(); err != nil {
			lastErr = err
			log.Printf("[SSHTransport] Connection attempt %d failed: %v", i+1, err)
			continue
		}
		return nil
	}
	return fmt.Errorf("SSH connection failed after %d retries: %w", sshMaxRetries, lastErr)
}

func (t *SSHTransport) establishConnection() error {
	key, err := os.ReadFile(t.keyPath)
	if err != nil {
		return fmt.Errorf("read SSH key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("parse SSH key: %w", err)
	}

	hostKeyCallback, err := t.getHostKeyCallback()
	if err != nil {
		return fmt.Errorf("host key callback: %w", err)
	}

	config := &ssh.ClientConfig{
		User: t.user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}

	client, err := t.dialer.Dial("tcp", t.host, config)
	if err != nil {
		return fmt.Errorf("SSH dial: %w", err)
	}

	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return fmt.Errorf("SSH session: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		client.Close()
		return fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		client.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	agentBin := sshRemoteCommand
	if t.agentPath != "" {
		agentBin = t.agentPath + " --standalone"
	}
	cmd := fmt.Sprintf("%s --agent %s", agentBin, t.agentType)
	if t.configPath != "" {
		cmd += fmt.Sprintf(" --config \"%s\"", t.configPath)
	}
	if err := session.Start(cmd); err != nil {
		session.Close()
		client.Close()
		return fmt.Errorf("start remote command: %w", err)
	}

	t.client = client
	t.session = session
	t.stdin = stdin
	t.stdout = stdout

	// 受信ループ開始（完了通知チャネルを更新）
	t.receiveLoopDone = make(chan struct{})
	go t.receiveLoop()

	log.Printf("[SSHTransport] Connected to %s@%s (agent: %s)", t.user, t.host, t.agentType)
	return nil
}

func (t *SSHTransport) getHostKeyCallback() (ssh.HostKeyCallback, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}

	knownHostsPath := home + "/.ssh/known_hosts"
	if _, err := os.Stat(knownHostsPath); os.IsNotExist(err) {
		if t.strictHostKey {
			return nil, fmt.Errorf("known_hosts not found at %s (strict_host_key=true)", knownHostsPath)
		}
		// known_hostsが無い場合は警告付きでInsecure許可（開発環境用）
		log.Println("[SSHTransport] WARN: known_hosts not found, using insecure host key callback")
		return ssh.InsecureIgnoreHostKey(), nil
	}

	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("parse known_hosts: %w", err)
	}
	return callback, nil
}

// receiveLoop はstdoutからJSONメッセージを受信
func (t *SSHTransport) receiveLoop() {
	defer close(t.receiveLoopDone)

	scanner := bufio.NewScanner(t.stdout)
	// 大きなメッセージに対応（最大1MB）
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-t.done:
			return
		default:
		}

		line := scanner.Bytes()
		var msg domaintransport.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Printf("[SSHTransport] Failed to decode message: %v", err)
			continue
		}
		log.Printf("[SSHTransport] inbound from=%s to=%s type=%s job=%s", msg.From, msg.To, msg.Type, msg.JobID)

		select {
		case t.inbound <- msg:
		case <-t.done:
			return
		default:
			log.Printf("[SSHTransport] WARN: inbound channel full, message dropped")
		}
	}

	if err := scanner.Err(); err != nil {
		select {
		case <-t.done:
			// 正常終了
		default:
			log.Printf("[SSHTransport] Receive loop error: %v", err)
			t.tryReconnect()
		}
	}
}

// tryReconnect はロックフリーの再接続
func (t *SSHTransport) tryReconnect() {
	t.mu.Lock()
	if t.closed || t.reconnecting {
		t.mu.Unlock()
		return
	}
	t.reconnecting = true
	loopDone := t.receiveLoopDone
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		t.reconnecting = false
		t.mu.Unlock()
	}()

	log.Println("[SSHTransport] Attempting reconnection...")
	t.closeConnection()

	// 旧receiveLoopの完了を待つ（5秒タイムアウト）
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		log.Println("[SSHTransport] WARN: old receive loop did not stop in time")
	}

	if err := t.connectWithRetry(); err != nil {
		log.Printf("[SSHTransport] Reconnection failed: %v", err)
	}
}

func (t *SSHTransport) closeConnection() {
	if t.stdin != nil {
		t.stdin.Close()
	}
	if t.session != nil {
		t.session.Close()
	}
	if t.client != nil {
		t.client.Close()
	}
}

// Send はメッセージをSSH経由で送信（JSON 1行）
func (t *SSHTransport) Send(ctx context.Context, msg domaintransport.Message) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("transport is closed")
	}
	t.mu.Unlock()

	t.sendMu.Lock()
	defer t.sendMu.Unlock()

	if t.stdin == nil {
		return fmt.Errorf("SSH connection not established")
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if _, err := t.stdin.Write(data); err != nil {
		return fmt.Errorf("write to SSH stdin: %w", err)
	}
	log.Printf("[SSHTransport] sent to_agent=%s from=%s to=%s type=%s job=%s", t.agentType, msg.From, msg.To, msg.Type, msg.JobID)
	return nil
}

// Receive はメッセージを受信
func (t *SSHTransport) Receive(ctx context.Context) (domaintransport.Message, error) {
	select {
	case msg, ok := <-t.inbound:
		if !ok {
			return domaintransport.Message{}, fmt.Errorf("transport is closed")
		}
		log.Printf("[SSHTransport] recv from=%s to=%s type=%s job=%s", msg.From, msg.To, msg.Type, msg.JobID)
		return msg, nil
	case <-ctx.Done():
		log.Printf("[SSHTransport] recv canceled agent=%s err=%v", t.agentType, ctx.Err())
		return domaintransport.Message{}, ctx.Err()
	case <-t.done:
		return domaintransport.Message{}, fmt.Errorf("transport is closed")
	}
}

// Close はTransportを閉じる
func (t *SSHTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}

	t.closed = true
	close(t.done)
	t.closeConnection()

	log.Printf("[SSHTransport] Closed connection to %s@%s", t.user, t.host)
	return nil
}

// IsHealthy はTransportの健全性を返す
// SSH keepalive でリモートとの接続状態を確認
func (t *SSHTransport) IsHealthy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed || t.reconnecting {
		return false
	}
	if t.client == nil {
		return false
	}

	// OpenSSH keepalive で接続確認
	_, _, err := t.client.SendRequest("keepalive@openssh.com", true, nil)
	if err != nil {
		log.Printf("[SSHTransport] Keepalive failed: %v", err)
		return false
	}
	return true
}
