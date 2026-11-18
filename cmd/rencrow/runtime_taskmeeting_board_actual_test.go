package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskMeetingActualBoardTaskID = "taskmeeting-board-actual-publication"
	taskMeetingActualBoardPort   = 28792
	taskMeetingActualBoardSender = "rencrow-comm"
)

type taskMeetingActualBoardManifest struct {
	TaskID                  string `json:"task_id"`
	Binary                  string `json:"binary"`
	BinarySHA256            string `json:"binary_sha256"`
	DataDir                 string `json:"data_dir"`
	BaseURL                 string `json:"base_url"`
	Port                    int    `json:"port"`
	ChannelID               string `json:"channel_id"`
	SenderPrincipalID       string `json:"sender_principal_id"`
	ThreadID                string `json:"thread_id"`
	ExpectedTextFile        string `json:"expected_text_file"`
	ExpectedTextSHA256      string `json:"expected_text_sha256"`
	ExpectedMessageCount    int    `json:"expected_message_count"`
	ExpectedTranscriptFile  string `json:"expected_transcript_file"`
	AdminKeyFile            string `json:"admin_key_file"`
	SessionEndpoint         string `json:"session_endpoint"`
	BootstrapEndpoint       string `json:"bootstrap_endpoint"`
	BrowserTopicSelector    string `json:"browser_topic_selector"`
	BrowserMessageSelector  string `json:"browser_message_selector"`
	JournalInstanceID       string `json:"journal_instance_id"`
	RestartReplayVerified   bool   `json:"restart_replay_verified"`
	InProcessReplayVerified bool   `json:"in_process_replay_verified"`
}

func TestTaskMeetingActualBOARDPublication(t *testing.T) {
	binary, artifactRoot, port, enabled := taskMeetingActualBoardTestConfig()
	if !enabled {
		t.Skip("set all RENCROW_TASKMEETING_BOARD_ACTUAL_* variables to run the isolated actual BOARD test")
	}
	if port != taskMeetingActualBoardPort {
		t.Fatalf("actual BOARD fixture port must be reserved port %d", taskMeetingActualBoardPort)
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatalf("resolve actual BOARD fixture binary path: %v", err)
	}
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatalf("create actual BOARD artifact root: %v", err)
	}
	artifactRoot, err = filepath.Abs(artifactRoot)
	if err != nil {
		t.Fatalf("resolve actual BOARD artifact root: %v", err)
	}
	dataDir := filepath.Join(artifactRoot, "data")
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			t.Fatalf("refusing to reuse existing actual BOARD data directory: %s", dataDir)
		}
		t.Fatalf("inspect actual BOARD data directory: %v", err)
	}
	if err := requireTaskMeetingActualBoardPortFree(port); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		t.Fatalf("actual BOARD fixture binary is unavailable or not executable: %s", binary)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tokensDir := filepath.Join(artifactRoot, "credentials")
	homeDir := filepath.Join(artifactRoot, "home")
	if err := os.MkdirAll(tokensDir, 0o700); err != nil {
		t.Fatalf("create isolated credential directory: %v", err)
	}
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		t.Fatalf("create isolated process HOME: %v", err)
	}
	for _, directory := range []string{"tmp", "data", "config"} {
		if err := os.MkdirAll(filepath.Join(homeDir, directory), 0o700); err != nil {
			t.Fatalf("create isolated HOME subdirectory %s: %v", directory, err)
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create isolated BOARD data directory: %v", err)
	}
	senderTokenPath := filepath.Join(tokensDir, "mio.token")
	if err := taskMeetingActualBoardProvision(ctx, binary, dataDir, taskMeetingActualBoardSender, senderTokenPath, homeDir); err != nil {
		t.Fatalf("provision isolated BOARD principal mio: %v", err)
	}
	adminKeyPath := filepath.Join(dataDir, "admin.key")
	if info, err := os.Stat(adminKeyPath); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("isolated BOARD admin key is missing or not private: %s", adminKeyPath)
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	service, err := startTaskMeetingActualBoard(ctx, t, binary, dataDir, port, 1, homeDir)
	if err != nil {
		t.Fatal(err)
	}
	client, identity, tokenBytes, err := newTaskMeetingActualBoardClient(ctx, baseURL, senderTokenPath, nil)
	if err != nil {
		service.stop(t)
		t.Fatalf("create pinned actual BOARD client: %v", err)
	}
	firstIdentity := identity
	defer func() {
		for i := range tokenBytes {
			tokenBytes[i] = 0
		}
		client.Close()
	}()
	if identity.PrincipalID != taskMeetingActualBoardSender {
		service.stop(t)
		t.Fatalf("pinned BOARD principal = %q, want %s", identity.PrincipalID, taskMeetingActualBoardSender)
	}
	firstJournalInstanceID := identity.JournalInstanceID
	if err := taskMeetingActualBoardHasChannel(ctx, client, "rencrow"); err != nil {
		service.stop(t)
		t.Fatalf("isolated BOARD channel check: %v", err)
	}
	if topics, err := taskMeetingActualBoardThreads(ctx, client); err != nil || len(topics) != 0 {
		service.stop(t)
		t.Fatalf("fresh isolated BOARD should have no threads: count=%d err=%v", len(topics), err)
	}

	transcript := taskMeetingActualBoardTranscript()
	route := domainmeeting.BoardPublication{
		ChannelID: "rencrow", SenderPrincipalID: taskMeetingActualBoardSender, Recipients: []string{},
	}
	if err := route.Validate(); err != nil {
		service.stop(t)
		t.Fatalf("invalid actual BOARD publication route fixture: %v", err)
	}
	title, expectedChunks, err := renderTaskMeetingBoardMinutes(transcript)
	if err != nil {
		service.stop(t)
		t.Fatalf("render expected actual BOARD transcript: %v", err)
	}
	expectedText := strings.Join(expectedChunks, "")
	if len(expectedChunks) < 2 {
		service.stop(t)
		t.Fatalf("fixture must cross the BOARD message rune limit; chunks=%d", len(expectedChunks))
	}
	if err := writeTaskMeetingActualBoardArtifact(filepath.Join(artifactRoot, "expected-text.txt"), []byte(expectedText)); err != nil {
		service.stop(t)
		t.Fatalf("write expected full transcript: %v", err)
	}
	transcriptRaw, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		service.stop(t)
		t.Fatalf("encode expected transcript: %v", err)
	}
	if err := writeTaskMeetingActualBoardArtifact(filepath.Join(artifactRoot, "expected-transcript.json"), transcriptRaw); err != nil {
		service.stop(t)
		t.Fatalf("write expected transcript fixture: %v", err)
	}

	publisher := runtimeTaskMeetingBoardPublisher{client: client}
	if err := publisher.Publish(ctx, route, transcript); err != nil {
		service.stop(t)
		t.Fatalf("first actual BOARD minutes publication: %v", err)
	}
	firstThread, err := taskMeetingActualBoardVerifyPublished(ctx, client, route, title, expectedChunks)
	if err != nil {
		service.stop(t)
		t.Fatalf("first actual BOARD readback: %v", err)
	}
	if err := publisher.Publish(ctx, route, transcript); err != nil {
		service.stop(t)
		t.Fatalf("same-process idempotent actual BOARD replay: %v", err)
	}
	if err := taskMeetingActualBoardVerifySameThread(ctx, client, route, title, expectedChunks, firstThread); err != nil {
		service.stop(t)
		t.Fatalf("same-process replay changed actual BOARD page: %v", err)
	}
	client.Close()
	for i := range tokenBytes {
		tokenBytes[i] = 0
	}
	tokenBytes = nil

	service.stop(t)
	service, err = startTaskMeetingActualBoard(ctx, t, binary, dataDir, port, 2, homeDir)
	if err != nil {
		t.Fatal(err)
	}
	client, identity, tokenBytes, err = newTaskMeetingActualBoardClient(ctx, baseURL, senderTokenPath, &firstIdentity)
	if err != nil {
		service.stop(t)
		t.Fatalf("reconnect to replayed BOARD journal: %v", err)
	}
	defer func() {
		for i := range tokenBytes {
			tokenBytes[i] = 0
		}
		client.Close()
	}()
	if identity.PrincipalID != taskMeetingActualBoardSender || identity.JournalInstanceID != firstJournalInstanceID {
		service.stop(t)
		t.Fatalf("reopened BOARD identity differs from pinned principal/journal: %+v", identity)
	}
	publisher = runtimeTaskMeetingBoardPublisher{client: client}
	if err := publisher.Publish(ctx, route, transcript); err != nil {
		service.stop(t)
		t.Fatalf("post-restart idempotent actual BOARD replay: %v", err)
	}
	if err := taskMeetingActualBoardVerifySameThread(ctx, client, route, title, expectedChunks, firstThread); err != nil {
		service.stop(t)
		t.Fatalf("post-restart replay changed actual BOARD page: %v", err)
	}
	service.stop(t)

	binaryHash, err := taskMeetingActualBoardFileSHA256(binary)
	if err != nil {
		t.Fatalf("hash actual BOARD fixture binary: %v", err)
	}
	textHash := sha256.Sum256([]byte(expectedText))
	manifest := taskMeetingActualBoardManifest{
		TaskID: taskMeetingActualBoardTaskID, Binary: binary, BinarySHA256: binaryHash,
		DataDir: dataDir, BaseURL: baseURL, Port: port, ChannelID: route.ChannelID,
		SenderPrincipalID: route.SenderPrincipalID, ThreadID: firstThread,
		ExpectedTextFile:   filepath.Join(artifactRoot, "expected-text.txt"),
		ExpectedTextSHA256: hex.EncodeToString(textHash[:]), ExpectedMessageCount: len(expectedChunks),
		ExpectedTranscriptFile: filepath.Join(artifactRoot, "expected-transcript.json"),
		AdminKeyFile:           adminKeyPath, SessionEndpoint: baseURL + "/api/session",
		BootstrapEndpoint: baseURL + "/api/bootstrap", BrowserTopicSelector: "[data-topic=\"" + firstThread + "\"]",
		BrowserMessageSelector: "#threadScroll .message-text", JournalInstanceID: identity.JournalInstanceID,
		RestartReplayVerified: true, InProcessReplayVerified: true,
	}
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode actual BOARD verification manifest: %v", err)
	}
	if err := writeTaskMeetingActualBoardArtifact(filepath.Join(artifactRoot, "manifest.json"), manifestRaw); err != nil {
		t.Fatalf("write actual BOARD verification manifest: %v", err)
	}
}

func taskMeetingActualBoardTestConfig() (binary, artifactRoot string, port int, enabled bool) {
	binary = strings.TrimSpace(os.Getenv("RENCROW_TASKMEETING_BOARD_ACTUAL_BINARY"))
	artifactRoot = strings.TrimSpace(os.Getenv("RENCROW_TASKMEETING_BOARD_ACTUAL_ARTIFACT_ROOT"))
	taskID := strings.TrimSpace(os.Getenv("RENCROW_TASKMEETING_BOARD_ACTUAL_TASK_ID"))
	portRaw := strings.TrimSpace(os.Getenv("RENCROW_TASKMEETING_BOARD_ACTUAL_PORT"))
	if binary == "" && artifactRoot == "" && taskID == "" && portRaw == "" {
		return "", "", 0, false
	}
	if binary == "" || artifactRoot == "" || taskID == "" || portRaw == "" {
		return binary, artifactRoot, 0, true
	}
	if taskID != taskMeetingActualBoardTaskID {
		return binary, artifactRoot, 0, true
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		return binary, artifactRoot, 0, true
	}
	return binary, artifactRoot, port, true
}

func requireTaskMeetingActualBoardPortFree(port int) error {
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("reserved actual BOARD test port 127.0.0.1:%d is busy; refusing to scan or use another port: %w", port, err)
	}
	return listener.Close()
}

func taskMeetingActualBoardProvision(ctx context.Context, binary, dataDir, principalID, tokenPath, homeDir string) error {
	command := exec.CommandContext(ctx, binary, "provision", "--data", dataDir, "--id", principalID,
		"--name", "Mio", "--channels", "rencrow", "--token-out", tokenPath)
	command.Env = taskMeetingActualBoardEnvironment(os.Environ(), homeDir)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("BOARD provision CLI failed: %w", err)
	}
	info, err := os.Stat(tokenPath)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return errors.New("BOARD provision CLI did not create a protected scoped token file")
	}
	return nil
}

func taskMeetingActualBoardEnvironment(source []string, homeDir string) []string {
	environment := make([]string, 0, len(source))
	for _, value := range source {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "BOARD_") || key == "HOME" || key == "TMPDIR" || key == "TMP" ||
			key == "XDG_DATA_HOME" || key == "XDG_CONFIG_HOME" {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, "HOME="+homeDir, "TMPDIR="+filepath.Join(homeDir, "tmp"),
		"TMP="+filepath.Join(homeDir, "tmp"), "XDG_DATA_HOME="+filepath.Join(homeDir, "data"),
		"XDG_CONFIG_HOME="+filepath.Join(homeDir, "config"))
}

type taskMeetingActualBoardProcess struct {
	command *exec.Cmd
	done    chan struct{}
	waitErr error
	stopMu  sync.Mutex
	stopped bool
}

func startTaskMeetingActualBoard(ctx context.Context, t *testing.T, binary, dataDir string, port, run int, homeDir string) (*taskMeetingActualBoardProcess, error) {
	t.Helper()
	if err := requireTaskMeetingActualBoardPortFree(port); err != nil {
		return nil, err
	}
	stdout, err := os.OpenFile(filepath.Join(filepath.Dir(dataDir), fmt.Sprintf("board-run-%d.stdout.log", run)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create isolated BOARD stdout log: %w", err)
	}
	stderr, err := os.OpenFile(filepath.Join(filepath.Dir(dataDir), fmt.Sprintf("board-run-%d.stderr.log", run)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("create isolated BOARD stderr log: %w", err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	command := exec.Command(binary, "-listen", address, "-data", dataDir, "-open=false")
	command.Env = taskMeetingActualBoardEnvironment(os.Environ(), homeDir)
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("start owned isolated BOARD process: %w", err)
	}
	if err := stdout.Close(); err != nil {
		_ = stderr.Close()
		_ = command.Process.Kill()
		return nil, fmt.Errorf("close BOARD stdout log: %w", err)
	}
	if err := stderr.Close(); err != nil {
		_ = command.Process.Kill()
		return nil, fmt.Errorf("close BOARD stderr log: %w", err)
	}
	process := &taskMeetingActualBoardProcess{command: command, done: make(chan struct{})}
	go func() {
		process.waitErr = command.Wait()
		close(process.done)
	}()
	t.Cleanup(func() { process.stop(t) })
	if err := taskMeetingActualBoardWaitHealthy(ctx, process, "http://"+address); err != nil {
		process.stop(t)
		return nil, err
	}
	return process, nil
}

func (p *taskMeetingActualBoardProcess) stop(t *testing.T) {
	t.Helper()
	if p == nil {
		return
	}
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	if p.stopped || p.command == nil || p.command.Process == nil {
		return
	}
	select {
	case <-p.done:
		p.stopped = true
		return
	default:
	}
	if err := p.command.Process.Signal(os.Interrupt); err != nil {
		_ = p.command.Process.Kill()
		select {
		case <-p.done:
			p.stopped = true
		case <-time.After(10 * time.Second):
			t.Errorf("owned isolated BOARD process did not stop after kill")
			p.stopped = true
		}
		return
	}
	select {
	case <-p.done:
		if p.waitErr != nil {
			t.Errorf("owned isolated BOARD process stopped with error: %v", p.waitErr)
		}
		p.stopped = true
	case <-time.After(20 * time.Second):
		_ = p.command.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Errorf("owned isolated BOARD process could not be reaped")
		}
		p.stopped = true
	}
}

func taskMeetingActualBoardWaitHealthy(ctx context.Context, process *taskMeetingActualBoardProcess, baseURL string) error {
	client := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health/live", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-process.done:
			return fmt.Errorf("isolated BOARD process exited before health check: %w", process.waitErr)
		default:
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for isolated BOARD health: %w", ctx.Err())
		case <-deadline.C:
			return errors.New("isolated BOARD did not become healthy within 15 seconds")
		case <-ticker.C:
		}
	}
}

func newTaskMeetingActualBoardClient(ctx context.Context, baseURL, tokenPath string, expected *boardcommunication.ConnectionIdentity) (*boardcommunication.Client, boardcommunication.ConnectionIdentity, []byte, error) {
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, boardcommunication.ConnectionIdentity{}, nil, errors.New("cannot read isolated BOARD token file")
	}
	token = []byte(strings.TrimSpace(string(token)))
	if len(token) == 0 {
		return nil, boardcommunication.ConnectionIdentity{}, token, errors.New("isolated BOARD token file is empty")
	}
	tokenSource := func(context.Context) ([]byte, error) { return append([]byte(nil), token...), nil }
	options := boardcommunication.Options{URL: baseURL, TokenSource: tokenSource}
	var identity boardcommunication.ConnectionIdentity
	if expected == nil {
		identity, err = boardcommunication.DiscoverConnection(ctx, options)
		if err != nil {
			return nil, boardcommunication.ConnectionIdentity{}, token, fmt.Errorf("discover isolated BOARD principal identity: %w", err)
		}
	} else {
		identity = *expected
	}
	if identity.PrincipalID != taskMeetingActualBoardSender {
		return nil, boardcommunication.ConnectionIdentity{}, token, fmt.Errorf("isolated BOARD returned principal %q, want %s", identity.PrincipalID, taskMeetingActualBoardSender)
	}
	options.ExpectedConnection = identity
	client, err := boardcommunication.NewClient(options)
	if err != nil {
		return nil, identity, token, fmt.Errorf("create pinned isolated BOARD client: %w", err)
	}
	readback, err := client.ReadConnection(ctx)
	if err != nil || readback != identity {
		client.Close()
		return nil, identity, token, fmt.Errorf("read back pinned isolated BOARD identity: %w", err)
	}
	return client, identity, token, nil
}

func taskMeetingActualBoardHasChannel(ctx context.Context, client *boardcommunication.Client, channelID string) error {
	contexts, err := client.ListContexts(ctx)
	if err != nil {
		return err
	}
	for _, boardContext := range contexts {
		if boardContext.ID == channelID {
			return nil
		}
	}
	return fmt.Errorf("isolated BOARD context %q was not provisioned", channelID)
}

func taskMeetingActualBoardThreads(ctx context.Context, client *boardcommunication.Client) ([]boardcommunication.ThreadSummary, error) {
	page, err := client.ListThreads(ctx, "rencrow", boardcommunication.PageRequest{Limit: 100})
	if err != nil {
		return nil, err
	}
	if page.NextCursor != "" {
		return nil, errors.New("isolated BOARD fixture unexpectedly has more than 100 topics")
	}
	return page.Threads, nil
}

func taskMeetingActualBoardVerifyPublished(ctx context.Context, client *boardcommunication.Client, route domainmeeting.BoardPublication, title string, expectedChunks []string) (string, error) {
	topics, err := taskMeetingActualBoardThreads(ctx, client)
	if err != nil {
		return "", err
	}
	if len(topics) != 1 {
		return "", fmt.Errorf("published topic count=%d, want one", len(topics))
	}
	threadID := topics[0].ThreadID
	if err := taskMeetingActualBoardVerifySameThread(ctx, client, route, title, expectedChunks, threadID); err != nil {
		return "", err
	}
	return threadID, nil
}

func taskMeetingActualBoardVerifySameThread(ctx context.Context, client *boardcommunication.Client, route domainmeeting.BoardPublication, title string, expectedChunks []string, expectedThreadID string) error {
	topics, err := taskMeetingActualBoardThreads(ctx, client)
	if err != nil {
		return err
	}
	if len(topics) != 1 || topics[0].ThreadID != expectedThreadID || topics[0].Title != title || topics[0].ChannelID != route.ChannelID {
		return fmt.Errorf("BOARD thread list changed: %+v", topics)
	}
	var messages []boardcommunication.BoardMessage
	cursor := ""
	seen := make(map[string]bool)
	for pageNumber := 0; pageNumber <= len(expectedChunks); pageNumber++ {
		page, err := client.ReadThread(ctx, expectedThreadID, boardcommunication.PageRequest{Limit: 100, Cursor: cursor})
		if err != nil {
			return err
		}
		if page.Thread.ID != expectedThreadID || page.Thread.Space != route.ChannelID || page.Thread.Title != title {
			return errors.New("BOARD readback thread identity/title differs from the expected occurrence")
		}
		messages = append(messages, page.Thread.Messages...)
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor || seen[page.NextCursor] {
			return errors.New("BOARD thread pagination cursor repeated")
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
		if pageNumber == len(expectedChunks) {
			return errors.New("BOARD thread pagination exceeded the expected message count")
		}
	}
	if len(messages) != len(expectedChunks) {
		return fmt.Errorf("BOARD message count=%d, want %d", len(messages), len(expectedChunks))
	}
	for index, message := range messages {
		if message.Body != expectedChunks[index] || message.By != route.SenderPrincipalID || message.ChannelID != route.ChannelID ||
			message.ThreadID != expectedThreadID || message.Kind != "" || message.ReplyTo != "" || len(message.Mentions) != 0 ||
			!sameTaskMeetingBoardRecipients(message.Recipients, route.Recipients) {
			return fmt.Errorf("BOARD message %d differs in order, text, identity, or audience", index)
		}
	}
	return nil
}

func taskMeetingActualBoardTranscript() workstreampersistence.TaskMeetingMinutesTranscript {
	actors := []string{"mio", "shiro", "midori", "kuro", "aka", "ao", "kin", "gin"}
	key := domainmeeting.OccurrenceKey{
		ScheduleID: modulecore.NewScheduleID(), LocalDate: "2026-11-18", Slot: "08:00", Timezone: domainmeeting.TimezoneJST,
	}
	transcript := workstreampersistence.TaskMeetingMinutesTranscript{
		OwnerUserID: "meeting-board-minutes-20261118", CoordinatorActorID: "mio", OccurrenceKey: key,
		Outcome: domainmeeting.StatusCompleted, Audience: append([]string(nil), actors...),
		MessageOrder: make([]modulecore.MessageID, 0, len(actors)*2),
		Reports:      make([]workstreampersistence.TaskMeetingMinutesReport, 0, len(actors)),
		Answers:      make([]workstreampersistence.TaskMeetingMinutesAnswer, 0, len(actors)),
	}
	for index, actor := range actors {
		reportID := modulecore.MessageID(fmt.Sprintf("minute_report_%s", actor))
		answerID := modulecore.MessageID(fmt.Sprintf("minute_answer_%s", actor))
		body := fmt.Sprintf("%s の進捗、次の作業、確認事項を記録します。", actor)
		if index == 0 {
			body += strings.Repeat("長いUTF-8本文です🙂。BOARDの上限を越えても全ての記録を順番どおり表示します。", 300)
		}
		transcript.Reports = append(transcript.Reports, workstreampersistence.TaskMeetingMinutesReport{
			MessageID: reportID, ActorID: actor, Body: body,
			Current:  domainmeeting.ReportField{State: "known", Text: actor + " の現在状況"},
			Next:     domainmeeting.ReportField{State: "known", Text: actor + " の次の作業"},
			Concerns: []domainmeeting.Concern{{Description: actor + " の確認事項"}},
		})
		transcript.MessageOrder = append(transcript.MessageOrder, reportID)
		answerActor := actors[(index+1)%len(actors)]
		transcript.Answers = append(transcript.Answers, workstreampersistence.TaskMeetingMinutesAnswer{
			MessageID: answerID, ActorID: answerActor, ReporterID: actor,
			Body: answerActor + " から " + actor + " への回答です。",
		})
		transcript.MessageOrder = append(transcript.MessageOrder, answerID)
	}
	return transcript
}

func taskMeetingActualBoardWriteArtifact(path string, value []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(value); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writeTaskMeetingActualBoardArtifact(path string, value []byte) error {
	return taskMeetingActualBoardWriteArtifact(path, value)
}

func taskMeetingActualBoardFileSHA256(path string) (string, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}
