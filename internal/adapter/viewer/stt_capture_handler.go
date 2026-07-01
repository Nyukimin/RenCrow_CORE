package viewer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	modulestt "github.com/Nyukimin/picoclaw_multiLLM/modules/stt"
)

type sttLogSaveRequest struct {
	Content string `json:"content"`
}

type sttAutoTestRequest struct {
	ProviderURL    string  `json:"provider_url"`
	WSURL          string  `json:"ws_url"`
	ProviderRounds int     `json:"provider_rounds"`
	WSRounds       int     `json:"ws_rounds"`
	WSWait         float64 `json:"ws_wait"`
}

func HandleSTTClientLogSave(logPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		var req sttLogSaveRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		content := strings.TrimSpace(req.Content)
		if content == "" {
			http.Error(w, "content is required", http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			http.Error(w, "mkdir failed", http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(logPath, []byte(content+"\n"), 0o644); err != nil {
			http.Error(w, "write failed", http.StatusInternalServerError)
			return
		}
		writeMonitorJSON(w, map[string]any{
			"ok":   true,
			"path": logPath,
		})
	}
}

type sttInputArchivePathBuilder func(archiveDir string, capturedAt time.Time) string

func HandleSTTInputWAVSave(latestPath, archiveDir string) http.HandlerFunc {
	return handleSTTInputWAVSave(latestPath, archiveDir, modulestt.BuildViewerInputArchivePath)
}

func HandleSTTInputRawWAVSave(latestPath, archiveDir string) http.HandlerFunc {
	return handleSTTInputWAVSave(latestPath, archiveDir, modulestt.BuildViewerInputRawArchivePath)
}

func handleSTTInputWAVSave(latestPath, archiveDir string, buildArchivePath sttInputArchivePathBuilder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 50*1024*1024))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		if len(body) < 44 || !bytes.Equal(body[:4], []byte("RIFF")) {
			http.Error(w, "invalid wav", http.StatusBadRequest)
			return
		}
		latestPath, archivePath, err := writeSTTInputWAVFiles(body, latestPath, archiveDir, buildArchivePath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeMonitorJSON(w, map[string]any{
			"ok":           true,
			"latest_path":  latestPath,
			"archive_path": archivePath,
			"bytes":        len(body),
		})
	}
}

func writeSTTInputWAVFiles(body []byte, latestPath, archiveDir string, buildArchivePath sttInputArchivePathBuilder) (string, string, error) {
	if err := os.MkdirAll(filepath.Dir(latestPath), 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir failed")
	}
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir failed")
	}
	if err := os.WriteFile(latestPath, body, 0o644); err != nil {
		return "", "", fmt.Errorf("write failed")
	}
	archivePath := buildArchivePath(archiveDir, time.Now())
	if err := os.WriteFile(archivePath, body, 0o644); err != nil {
		return "", "", fmt.Errorf("archive write failed")
	}
	return latestPath, archivePath, nil
}

func HandleSTTAutoTest(scriptPath, wavPath, outputPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req sttAutoTestRequest
		body, err := io.ReadAll(io.LimitReader(r.Body, 32*1024))
		if err == nil && len(strings.TrimSpace(string(body))) > 0 {
			_ = json.Unmarshal(body, &req)
		}

		args := []string{scriptPath, "--wav", wavPath}
		if strings.TrimSpace(req.ProviderURL) != "" {
			args = append(args, "--provider-url", strings.TrimSpace(req.ProviderURL))
		}
		if strings.TrimSpace(req.WSURL) != "" {
			args = append(args, "--ws-url", strings.TrimSpace(req.WSURL))
		}
		if req.ProviderRounds > 0 {
			args = append(args, "--provider-rounds", strconv.Itoa(req.ProviderRounds))
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			args = append(args, "--ws-rounds", strconv.Itoa(req.WSRounds))
		} else if req.WSRounds > 0 {
			args = append(args, "--ws-rounds", strconv.Itoa(req.WSRounds))
		}
		if req.WSWait > 0 {
			args = append(args, "--ws-wait", strconv.FormatFloat(req.WSWait, 'f', 2, 64))
		}

		cmd := exec.Command("python3", args...)
		out, runErr := cmd.CombinedOutput()
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err == nil {
			_ = os.WriteFile(outputPath, out, 0o644)
		}
		if runErr != nil {
			http.Error(w, fmt.Sprintf("autotest failed: %v\n%s", runErr, strings.TrimSpace(string(out))), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}
}
