package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/toolharness"
	modulewebgather "github.com/Nyukimin/picoclaw_multiLLM/modules/webgather"
)

var validToolName = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// ErrUnknownTool はツール名が登録されていない場合に返すセンチネルエラー
var ErrUnknownTool = errors.New("unknown tool")

// ToolFuncV2 は構造化レスポンスを返すツール実行関数の型
type ToolFuncV2 func(ctx context.Context, args map[string]interface{}) (*tool.ToolResponse, error)

type WebSearchCache interface {
	GetFreshWebSearchCache(ctx context.Context, query string) ([]GoogleSearchItem, bool, error)
	SaveWebSearchCache(ctx context.Context, query string, items []GoogleSearchItem, ttl time.Duration) error
}

type WebGatherFetcher interface {
	FetchURL(ctx context.Context, req modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error)
}

type WebGatherSearcher interface {
	Search(ctx context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error)
}

type WebGatherSearchAndFetcher interface {
	SearchAndFetch(ctx context.Context, req modulewebgather.SearchAndFetchRequest) (modulewebgather.SearchAndFetchResponse, error)
}

// ToolRunner はツール実行の実装（V1 + V2 対応）
type ToolRunner struct {
	tools    map[string]ToolFunc
	toolsV2  map[string]ToolFuncV2
	metadata map[string]tool.ToolMetadata
	config   ToolRunnerConfig
	harness  *toolharness.Harness
}

// ToolRunnerConfig はToolRunnerの設定
type ToolRunnerConfig struct {
	GoogleAPIKey         string
	GoogleSearchEngineID string
	HTTPClient           *http.Client            // テスト用注入（nilの場合はデフォルトを使用）
	Subagents            map[string]SubagentFunc // サブエージェントマップ（nil許容）
	AllowedShellCommands []string                // 許可コマンドプレフィックス（空=全許可）
	AllowedWritePaths    []string                // file_write 許可パス（空=全許可）
	DisableWebSearch     bool                    // web_search を登録しない（会話モード安全ポリシー）
	WebSearchCache       WebSearchCache          // nil = web_search cache 無効
	WebGatherFetcher     WebGatherFetcher        // nil = web_gather.fetch 無効
	WebGatherSearcher    WebGatherSearcher       // nil = web_gather.search 無効
	WebGatherSearchFetch WebGatherSearchAndFetcher
	BrowserActorRunner   BrowserActorRunner
	ToolHarnessRecorder  toolharness.Recorder // nil = mediation event 永続化なし
	DisableToolHarness   bool                 // true = ToolRunner内の入力調停を無効化する

	// Phase 4: Shiro ツール共有
	ToolRegistry capability.ToolRegistry // nil = register_tool 無効
	WorkspaceDir string                  // workspace/tools/<name>.sh のベースディレクトリ
}

// ToolFunc はツール実行関数の型
type ToolFunc func(ctx context.Context, args map[string]interface{}) (string, error)

// NewToolRunner は新しいToolRunnerを作成
func NewToolRunner(config ToolRunnerConfig) *ToolRunner {
	runner := &ToolRunner{
		tools:    make(map[string]ToolFunc),
		toolsV2:  make(map[string]ToolFuncV2),
		metadata: make(map[string]tool.ToolMetadata),
		config:   config,
		harness:  toolharness.New(),
	}

	// ツール登録
	runner.registerTools()

	return runner
}

func (r *ToolRunner) WithWebSearchCache(cache WebSearchCache) *ToolRunner {
	r.config.WebSearchCache = cache
	return r
}

func (r *ToolRunner) WithWebGatherFetcher(fetcher WebGatherFetcher) *ToolRunner {
	r.config.WebGatherFetcher = fetcher
	r.registerTools()
	return r
}

func (r *ToolRunner) WithWebGatherSearcher(searcher WebGatherSearcher) *ToolRunner {
	r.config.WebGatherSearcher = searcher
	r.registerTools()
	return r
}

func (r *ToolRunner) WithWebGatherSearchAndFetcher(searchAndFetcher WebGatherSearchAndFetcher) *ToolRunner {
	r.config.WebGatherSearchFetch = searchAndFetcher
	r.registerTools()
	return r
}

// Execute はツールを実行
func (r *ToolRunner) Execute(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
	toolFunc, exists := r.tools[toolName]
	if !exists {
		return "", fmt.Errorf("unknown tool: %s: %w", toolName, ErrUnknownTool)
	}

	mediated := r.mediateToolInput(toolName, args)
	return toolFunc(ctx, mediated.Input)
}

// List は利用可能なツール一覧を返す
func (r *ToolRunner) List(ctx context.Context) ([]string, error) {
	tools := make([]string, 0, len(r.tools))
	for name := range r.tools {
		tools = append(tools, name)
	}
	return tools, nil
}

// ExecuteV2 はツールを実行して構造化レスポンスを返す
func (r *ToolRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	v2Func, exists := r.toolsV2[toolName]
	if !exists {
		return nil, fmt.Errorf("unknown tool: %s: %w", toolName, ErrUnknownTool)
	}
	mediated := r.mediateToolInput(toolName, args)
	resp, err := v2Func(ctx, mediated.Input)
	if err != nil || resp == nil || !mediated.Repaired() {
		return resp, err
	}
	if resp.Metadata == nil {
		resp.Metadata = map[string]any{}
	}
	for k, v := range mediated.Metadata() {
		resp.Metadata[k] = v
	}
	return resp, nil
}

// ListTools はツールのメタデータ一覧を返す
func (r *ToolRunner) ListTools(ctx context.Context) ([]tool.ToolMetadata, error) {
	metas := make([]tool.ToolMetadata, 0, len(r.metadata))
	for _, m := range r.metadata {
		metas = append(metas, m)
	}
	return metas, nil
}

// shellMetachars はコマンドチェーニングや注入に使われるシェルメタ文字列
var shellMetachars = []string{";", "&&", "||", "|", "`", "$(", "\n"}

func (r *ToolRunner) mediateToolInput(toolName string, args map[string]any) toolharness.Result {
	if r.harness == nil || r.config.DisableToolHarness {
		return toolharness.Result{Input: args}
	}
	result := r.harness.Mediate(toolName, args)
	r.recordToolMediation(toolName, args, result)
	return result
}

func (r *ToolRunner) recordToolMediation(toolName string, args map[string]any, result toolharness.Result) {
	if r.config.ToolHarnessRecorder == nil {
		return
	}
	event := toolharness.NewEvent(
		fmt.Sprintf("evt_tool_%d", time.Now().UTC().UnixNano()),
		toolName,
		args,
		result,
		time.Now().UTC(),
	)
	_ = r.config.ToolHarnessRecorder.RecordToolMediationEvent(event)
}
