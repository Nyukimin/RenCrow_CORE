package jsonlutil

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const appendAckCrashRootEnv = "RENCROW_JSONL_APPEND_ACK_CRASH_ROOT"

func TestJSONLAppendAcknowledgementSurvivesChildProcessKill(t *testing.T) {
	if root := os.Getenv(appendAckCrashRootEnv); root != "" {
		runAppendAcknowledgementCrashChild(root)
		return
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary root: %v", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatalf("make temporary root absolute: %v", err)
	}
	path := filepath.Join(root, "acknowledged.jsonl")
	command := exec.Command(os.Args[0], "-test.run=^TestJSONLAppendAcknowledgementSurvivesChildProcessKill$")
	command.Env = append(os.Environ(), appendAckCrashRootEnv+"="+root)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		t.Fatal(err)
	}
	var stderr appendCrashBuffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		t.Fatalf("start append child: %v", err)
	}
	childRunning := true
	defer func() {
		if childRunning {
			_ = command.Process.Kill()
			_ = stdin.Close()
			_ = command.Wait()
		}
	}()

	ackResult := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		ackResult <- struct {
			line string
			err  error
		}{line: strings.TrimSpace(line), err: err}
	}()
	var wantRows int
	var wantHash string
	select {
	case result := <-ackResult:
		if result.err != nil {
			t.Fatalf("read child acknowledgement: %v; stderr=%s", result.err, stderr.String())
		}
		fields := strings.Fields(result.line)
		if len(fields) != 3 || fields[0] != "ACK" || !strings.HasPrefix(fields[1], "rows=") || !strings.HasPrefix(fields[2], "sha256=") {
			t.Fatalf("unexpected child acknowledgement %q; stderr=%s", result.line, stderr.String())
		}
		wantRows, err = strconv.Atoi(strings.TrimPrefix(fields[1], "rows="))
		if err != nil || wantRows <= 0 {
			t.Fatalf("invalid row count in child acknowledgement %q", result.line)
		}
		wantHash = strings.TrimPrefix(fields[2], "sha256=")
		if len(wantHash) != sha256.Size*2 {
			t.Fatalf("invalid content hash in child acknowledgement %q", result.line)
		}
		if _, err := hex.DecodeString(wantHash); err != nil {
			t.Fatalf("invalid content hash in child acknowledgement %q: %v", result.line, err)
		}
		t.Logf("child_ack=%s", result.line)
	case <-time.After(15 * time.Second):
		t.Fatalf("child did not acknowledge completed appends; stderr=%s", stderr.String())
	}

	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill acknowledged child pid=%d: %v", command.Process.Pid, err)
	}
	_ = stdin.Close()
	waitErr := command.Wait()
	childRunning = false
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("child wait error=%v, want killed-process exit; stderr=%s", waitErr, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("child wrote stderr after successful acknowledgement: %s", stderr.String())
	}
	t.Logf("killed_child_pid=%d wait_error=%v stderr=%q", command.Process.Pid, waitErr, stderr.String())

	data, err := readAcknowledgedJSONL(path)
	if err != nil {
		t.Fatalf("reopen/read/close acknowledged JSONL after child kill: %v", err)
	}
	rows := splitNonEmptyLines(data)
	if len(rows) != wantRows {
		t.Fatalf("reopened rows=%d, child acknowledged %d", len(rows), wantRows)
	}
	ids := make(map[int]bool, wantRows)
	for _, line := range rows {
		var record testRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode row after child kill: %v", err)
		}
		if record.ID < 0 || record.ID >= wantRows || ids[record.ID] {
			t.Fatalf("missing-range or duplicate row id=%d", record.ID)
		}
		ids[record.ID] = true
	}
	actualLines := make([][]byte, len(rows))
	for i, row := range rows {
		actualLines[i] = append(append([]byte(nil), row...), '\n')
	}
	actualHash := sortedJSONLHash(actualLines)
	if actualHash != wantHash {
		t.Fatalf("reopened content hash=%s, child acknowledged %s", actualHash, wantHash)
	}
	t.Logf("reopened_rows=%d sorted_content_sha256=%s", len(rows), actualHash)
}

func readAcknowledgedJSONL(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return data, nil
}

func runAppendAcknowledgementCrashChild(root string) {
	const count = 128
	path := filepath.Join(root, "acknowledged.jsonl")
	start := make(chan struct{})
	errCh := make(chan error, count)
	var wg sync.WaitGroup
	for id := 0; id < count; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			errCh <- Append(path, testRecord{ID: id})
		}(id)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "append failed before ACK: %v\n", err)
			os.Exit(3)
		}
	}
	expected := make([][]byte, count)
	for id := 0; id < count; id++ {
		line, err := json.Marshal(testRecord{ID: id})
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "marshal expected row before ACK: %v\n", err)
			os.Exit(4)
		}
		expected[id] = append(line, '\n')
	}
	_, _ = fmt.Fprintf(os.Stdout, "ACK rows=%d sha256=%s\n", count, sortedJSONLHash(expected))
	var keepAlive [1]byte
	_, _ = os.Stdin.Read(keepAlive[:])
	os.Exit(0)
}

func sortedJSONLHash(lines [][]byte) string {
	sorted := append([][]byte(nil), lines...)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i], sorted[j]) < 0 })
	hash := sha256.New()
	for _, line := range sorted {
		_, _ = hash.Write(line)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type appendCrashBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *appendCrashBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *appendCrashBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func (b *appendCrashBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Len()
}
