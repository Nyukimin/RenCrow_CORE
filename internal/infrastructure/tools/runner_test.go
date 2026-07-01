package tools

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewToolRunner(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	if runner == nil {
		t.Fatal("NewToolRunner should not return nil")
	}
}

func TestToolRunner_List(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tools, err := runner.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	// 最低限のツールが登録されているか
	expectedTools := []string{"shell", "file_read", "file_write", "file_list", "web_search"}
	for _, expected := range expectedTools {
		found := false
		for _, tool := range tools {
			if tool == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected tool '%s' not found in list: %v", expected, tools)
		}
	}
}

func TestToolRunner_DisableWebSearch(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{DisableWebSearch: true})

	tools, err := runner.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	for _, name := range tools {
		if name == "web_search" {
			t.Fatalf("web_search should be disabled, got tools: %v", tools)
		}
	}

	_, err = runner.Execute(context.Background(), "web_search", map[string]interface{}{"query": "test"})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("expected unknown tool error, got: %v", err)
	}
}

func TestToolRunner_Execute_Shell_Success(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	args := map[string]interface{}{
		"command": "echo 'Hello, World!'",
	}

	result, err := runner.Execute(context.Background(), "shell", args)
	if err != nil {
		t.Fatalf("Execute shell failed: %v", err)
	}

	if !strings.Contains(result, "Hello, World!") {
		t.Errorf("Expected 'Hello, World!' in result, got: %s", result)
	}
}

func TestToolRunner_Execute_Shell_MissingCommand(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	args := map[string]interface{}{}

	_, err := runner.Execute(context.Background(), "shell", args)
	if err == nil {
		t.Error("Expected error when command is missing")
	}
}

func TestToolRunner_Execute_FileRead_Success(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	// テスト用ファイル作成
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := "This is a test file"
	os.WriteFile(testFile, []byte(testContent), 0644)

	args := map[string]interface{}{
		"path": testFile,
	}

	result, err := runner.Execute(context.Background(), "file_read", args)
	if err != nil {
		t.Fatalf("Execute file_read failed: %v", err)
	}

	if result != testContent {
		t.Errorf("Expected '%s', got '%s'", testContent, result)
	}
}

func TestToolRunner_Execute_FileRead_NotFound(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	args := map[string]interface{}{
		"path": "/nonexistent/file.txt",
	}

	_, err := runner.Execute(context.Background(), "file_read", args)
	if err == nil {
		t.Error("Expected error when file not found")
	}
}

func TestToolRunner_Execute_FileWrite_Success(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "output.txt")
	testContent := "Written content"

	args := map[string]interface{}{
		"path":    testFile,
		"content": testContent,
	}

	result, err := runner.Execute(context.Background(), "file_write", args)
	if err != nil {
		t.Fatalf("Execute file_write failed: %v", err)
	}

	// ファイルが作成されたか確認
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Error("File was not created")
	}

	// 内容確認
	content, _ := os.ReadFile(testFile)
	if string(content) != testContent {
		t.Errorf("Expected '%s', got '%s'", testContent, string(content))
	}

	if !strings.Contains(strings.ToLower(result), "success") {
		t.Errorf("Expected success message, got: %s", result)
	}
}

func TestToolRunner_Execute_FileList_Success(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	// テスト用ファイル作成
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("test"), 0644)
	os.Mkdir(filepath.Join(tmpDir, "subdir"), 0755)

	args := map[string]interface{}{
		"path": tmpDir,
	}

	result, err := runner.Execute(context.Background(), "file_list", args)
	if err != nil {
		t.Fatalf("Execute file_list failed: %v", err)
	}

	// 作成したファイルが含まれているか
	if !strings.Contains(result, "file1.txt") {
		t.Error("Expected 'file1.txt' in result")
	}
	if !strings.Contains(result, "file2.txt") {
		t.Error("Expected 'file2.txt' in result")
	}
	if !strings.Contains(result, "subdir") {
		t.Error("Expected 'subdir' in result")
	}
}

func TestToolRunner_Execute_UnknownTool(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	args := map[string]interface{}{}

	_, err := runner.Execute(context.Background(), "unknown_tool", args)
	if err == nil {
		t.Error("Expected error for unknown tool")
	}
}

func TestToolRunner_Execute_Shell_Timeout(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	// 長時間かかるコマンド（sleepは避け、実際にはすぐ終わるが概念的なテスト）
	args := map[string]interface{}{
		"command": "echo 'quick'",
	}

	// タイムアウト付きコンテキスト
	ctx := context.Background()

	result, err := runner.Execute(ctx, "shell", args)
	if err != nil {
		t.Fatalf("Execute should succeed for quick command: %v", err)
	}

	if !strings.Contains(result, "quick") {
		t.Errorf("Expected 'quick' in result, got: %s", result)
	}
}

func TestToolRunner_Execute_FileWrite_CreateDirectory(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "subdir", "nested", "file.txt")
	testContent := "Nested content"

	args := map[string]interface{}{
		"path":    testFile,
		"content": testContent,
	}

	_, err := runner.Execute(context.Background(), "file_write", args)
	if err != nil {
		t.Fatalf("Execute file_write with nested path failed: %v", err)
	}

	// ファイルが作成されたか確認
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read created file: %v", err)
	}

	if string(content) != testContent {
		t.Errorf("Expected '%s', got '%s'", testContent, string(content))
	}
}

func TestToolRunner_Execute_FileRead_MissingPath(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	args := map[string]interface{}{}

	_, err := runner.Execute(context.Background(), "file_read", args)
	if err == nil {
		t.Error("Expected error when path is missing")
	}
}

func TestToolRunner_Execute_FileWrite_MissingContent(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	args := map[string]interface{}{
		"path": filepath.Join(tmpDir, "test.txt"),
	}

	_, err := runner.Execute(context.Background(), "file_write", args)
	if err == nil {
		t.Error("Expected error when content is missing")
	}
}

func TestToolRunner_Execute_FileList_MissingPath(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	args := map[string]interface{}{}

	_, err := runner.Execute(context.Background(), "file_list", args)
	if err == nil {
		t.Error("Expected error when path is missing")
	}
}

// mockRoundTripper はHTTPリクエストをインターセプトするモック
type mockRoundTripper struct {
	statusCode int
	body       string
	calls      int
}

func (m *mockRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	m.calls++
	return &http.Response{
		StatusCode: m.statusCode,
		Body:       io.NopCloser(strings.NewReader(m.body)),
	}, nil
}

func TestToolRunner_Execute_WebSearch_Success(t *testing.T) {
	mockBody := `{
		"items": [
			{"title": "Go Programming Language", "link": "https://go.dev", "snippet": "Go is an open source programming language."},
			{"title": "Golang Tutorial", "link": "https://example.com", "snippet": "Learn Go programming."}
		]
	}`
	runner := NewToolRunner(ToolRunnerConfig{
		GoogleAPIKey:         "test-api-key",
		GoogleSearchEngineID: "test-engine-id",
		HTTPClient: &http.Client{
			Transport: &mockRoundTripper{statusCode: 200, body: mockBody},
		},
	})

	args := map[string]interface{}{
		"query": "golang programming language",
	}

	result, err := runner.Execute(context.Background(), "web_search", args)
	if err != nil {
		t.Fatalf("Execute web_search failed: %v", err)
	}

	if len(result) == 0 {
		t.Error("Expected non-empty search result")
	}

	if !strings.Contains(result, "Go Programming Language") {
		t.Errorf("Expected result to contain search item title, got: %s", result)
	}
}

func TestToolRunner_Execute_WebSearch_UsesFreshCache(t *testing.T) {
	rt := &mockRoundTripper{statusCode: 500, body: `should not be called`}
	cache := &mockWebSearchCache{
		hit: true,
		items: []GoogleSearchItem{
			{Title: "Cached Result", Link: "https://example.com/cache", Snippet: "from cache"},
		},
	}
	runner := NewToolRunner(ToolRunnerConfig{
		GoogleAPIKey:         "test-api-key",
		GoogleSearchEngineID: "test-engine-id",
		HTTPClient:           &http.Client{Transport: rt},
		WebSearchCache:       cache,
	})

	result, err := runner.Execute(context.Background(), "web_search", map[string]interface{}{"query": "RenCrow 最新仕様"})
	if err != nil {
		t.Fatalf("Execute web_search failed: %v", err)
	}
	if rt.calls != 0 {
		t.Fatalf("expected HTTP not to be called on cache hit, got %d calls", rt.calls)
	}
	if cache.lastGetQuery != "RenCrow 最新仕様" {
		t.Fatalf("unexpected cache lookup query: %q", cache.lastGetQuery)
	}
	if !strings.Contains(result, "Cached Result") || !strings.Contains(result, "https://example.com/cache") {
		t.Fatalf("cached result not formatted: %s", result)
	}
}

func TestToolRunner_Execute_WebSearch_SavesCacheOnMiss(t *testing.T) {
	mockBody := `{
		"items": [
			{"title": "Live Result", "link": "https://example.com/live", "snippet": "from live search"}
		]
	}`
	cache := &mockWebSearchCache{}
	runner := NewToolRunner(ToolRunnerConfig{
		GoogleAPIKey:         "test-api-key",
		GoogleSearchEngineID: "test-engine-id",
		HTTPClient: &http.Client{
			Transport: &mockRoundTripper{statusCode: 200, body: mockBody},
		},
		WebSearchCache: cache,
	})

	result, err := runner.Execute(context.Background(), "web_search", map[string]interface{}{"query": "RenCrow 最新仕様"})
	if err != nil {
		t.Fatalf("Execute web_search failed: %v", err)
	}
	if !strings.Contains(result, "Live Result") {
		t.Fatalf("expected live search result, got: %s", result)
	}
	if !cache.saveCalled {
		t.Fatal("expected live search result to be saved to cache")
	}
	if cache.lastSaveQuery != "RenCrow 最新仕様" {
		t.Fatalf("unexpected saved query: %q", cache.lastSaveQuery)
	}
	if len(cache.savedItems) != 1 || cache.savedItems[0].Title != "Live Result" {
		t.Fatalf("unexpected saved items: %+v", cache.savedItems)
	}
	if cache.lastTTL <= 0 {
		t.Fatalf("expected positive ttl, got %s", cache.lastTTL)
	}
}

func TestToolRunner_WithWebSearchCache_AfterCreation(t *testing.T) {
	rt := &mockRoundTripper{statusCode: 500, body: `should not be called`}
	cache := &mockWebSearchCache{
		hit: true,
		items: []GoogleSearchItem{
			{Title: "Late Cache", Link: "https://example.com/late", Snippet: "injected later"},
		},
	}
	runner := NewToolRunner(ToolRunnerConfig{
		GoogleAPIKey:         "test-api-key",
		GoogleSearchEngineID: "test-engine-id",
		HTTPClient:           &http.Client{Transport: rt},
	})

	if got := runner.WithWebSearchCache(cache); got != runner {
		t.Fatal("WithWebSearchCache should return the same runner")
	}
	result, err := runner.Execute(context.Background(), "web_search", map[string]interface{}{"query": "late"})
	if err != nil {
		t.Fatalf("Execute web_search failed: %v", err)
	}
	if rt.calls != 0 {
		t.Fatalf("expected HTTP not to be called on late cache hit, got %d calls", rt.calls)
	}
	if !strings.Contains(result, "Late Cache") {
		t.Fatalf("expected late cache result, got: %s", result)
	}
}

func TestToolRunner_Execute_WebSearch_MissingQuery(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	args := map[string]interface{}{}

	_, err := runner.Execute(context.Background(), "web_search", args)
	if err == nil {
		t.Error("Expected error when query is missing")
	}
}

func TestToolRunner_Execute_WebSearch_EmptyQuery(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	args := map[string]interface{}{
		"query": "   ",
	}

	_, err := runner.Execute(context.Background(), "web_search", args)
	if err == nil {
		t.Error("Expected error when query is empty")
	}
}

type mockWebSearchCache struct {
	hit           bool
	items         []GoogleSearchItem
	lastGetQuery  string
	saveCalled    bool
	lastSaveQuery string
	savedItems    []GoogleSearchItem
	lastTTL       time.Duration
}

func (m *mockWebSearchCache) GetFreshWebSearchCache(_ context.Context, query string) ([]GoogleSearchItem, bool, error) {
	m.lastGetQuery = query
	if !m.hit {
		return nil, false, nil
	}
	return m.items, true, nil
}

func (m *mockWebSearchCache) SaveWebSearchCache(_ context.Context, query string, items []GoogleSearchItem, ttl time.Duration) error {
	m.saveCalled = true
	m.lastSaveQuery = query
	m.savedItems = append([]GoogleSearchItem{}, items...)
	m.lastTTL = ttl
	return nil
}
