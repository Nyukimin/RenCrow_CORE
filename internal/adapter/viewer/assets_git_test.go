package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHandleAssetsGitStatusInitializesRepo(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "assets-repo")
	handler := HandleAssetsGitStatus(repoPath)

	req, err := http.NewRequest("GET", "/viewer/assets-git/status", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response AssetsGitStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal("Failed to unmarshal response:", err)
	}

	if response.RepoPath != repoPath {
		t.Errorf("expected repo_path %s, got %s", repoPath, response.RepoPath)
	}

	if !response.Initialized {
		t.Error("expected initialized to be true")
	}

	if response.LatestCommitHash != "" {
		t.Errorf("expected latest_commit_hash to be empty before the first commit, got %q", response.LatestCommitHash)
	}

	gitDir := filepath.Join(repoPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		t.Error("git repository was not initialized")
	}
}

func TestHandleAssetsGitStatusWithExistingRepo(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "assets-repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}

	runGitForTest(t, repoPath, "init")
	runGitForTest(t, repoPath, "config", "user.email", "test@example.com")
	runGitForTest(t, repoPath, "config", "user.name", "Test User")

	testFile := filepath.Join(repoPath, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, repoPath, "add", "test.txt")
	runGitForTest(t, repoPath, "commit", "-m", "initial commit")

	handler := HandleAssetsGitStatus(repoPath)

	req, err := http.NewRequest("GET", "/viewer/assets-git/status", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response AssetsGitStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal("Failed to unmarshal response:", err)
	}

	if !response.Initialized {
		t.Error("expected initialized to be true")
	}

	if response.Branch == "" {
		t.Error("expected branch to be non-empty")
	}

	if response.LatestCommitHash == "" {
		t.Error("expected latest_commit_hash to be non-empty")
	}
}

func TestHandleAssetsGitStatusMethodNotAllowed(t *testing.T) {
	handler := HandleAssetsGitStatus(filepath.Join(t.TempDir(), "assets-repo"))

	req, err := http.NewRequest("POST", "/viewer/assets-git/status", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusMethodNotAllowed {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusMethodNotAllowed)
	}
}

func runGitForTest(t *testing.T, repoPath string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}
