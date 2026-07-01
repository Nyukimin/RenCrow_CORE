package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

// registerTools は利用可能なツールを登録（ミドルウェアで安全レール適用）
func (r *ToolRunner) registerTools() {
	r.registerCoreTools()
	r.registerOptionalTools()
	r.registerToolMetadata()
}

func (r *ToolRunner) registerCoreTools() {
	r.tools["shell"] = withTimeout(
		withStringValidation(r.executeShell, "command", 10000),
		30*time.Second,
	)
	r.tools["file_read"] = withTimeout(
		withPathValidation(r.executeFileRead, "path"),
		10*time.Second,
	)
	r.tools["file_write"] = withTimeout(
		withPathValidation(r.executeFileWrite, "path"),
		10*time.Second,
	)
	r.tools["file_list"] = withTimeout(
		withPathValidation(r.executeFileList, "path"),
		10*time.Second,
	)

	r.toolsV2["shell"] = v2Wrap(r.tools["shell"])
	r.toolsV2["file_read"] = v2Wrap(r.tools["file_read"])
	r.toolsV2["file_write"] = v2Wrap(r.tools["file_write"])
	r.toolsV2["file_list"] = v2Wrap(r.tools["file_list"])
}

func (r *ToolRunner) registerOptionalTools() {
	if !r.config.DisableWebSearch {
		r.tools["web_search"] = withTimeout(
			withRetry(
				withStringValidation(r.executeWebSearch, "query", 500),
				DefaultRetryConfig,
			),
			15*time.Second,
		)
	}
	if len(r.config.Subagents) > 0 {
		r.registerSubagentTool()
	}

	if !r.config.DisableWebSearch {
		r.toolsV2["web_search"] = r.executeWebSearchV2 // 構造化データ対応
	}
	if r.config.WebGatherFetcher != nil {
		r.toolsV2["web_gather.fetch"] = r.executeWebGatherFetchV2
	}
	if r.config.WebGatherSearcher != nil {
		r.toolsV2["web_gather.search"] = r.executeWebGatherSearchV2
	}
	if r.config.WebGatherSearchFetch != nil {
		r.toolsV2["web_gather.search_and_fetch"] = r.executeWebGatherSearchAndFetchV2
	}
	if r.config.BrowserActorRunner != nil {
		r.toolsV2["browser.run"] = r.executeBrowserRunV2
	}

	// Phase 4: register_tool（ToolRegistry が有効な場合のみ登録）
	if r.config.ToolRegistry != nil {
		r.registerToolRegistryTool()
	}
}

func (r *ToolRunner) registerToolMetadata() {
	r.metadata["shell"] = tool.ToolMetadata{
		ToolID: "shell", Version: "1.0.0", Category: "mutation",
		DryRun:      true,
		Description: "シェルコマンドを実行する",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "実行するシェルコマンド"},
			},
			"required": []any{"command"},
		},
	}
	r.metadata["file_read"] = tool.ToolMetadata{
		ToolID: "file_read", Version: "1.0.0", Category: "query",
		Description: "ファイルの内容を読み込む",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "読み込むファイルパス"},
				"limit":  map[string]any{"type": "integer", "description": "読み込む行数（省略時は全行）"},
				"offset": map[string]any{"type": "integer", "description": "読み込み開始行（0始まり）"},
			},
			"required": []any{"path"},
		},
	}
	r.metadata["file_write"] = tool.ToolMetadata{
		ToolID: "file_write", Version: "1.0.0", Category: "mutation",
		DryRun:      true,
		Description: "ファイルに内容を書き込む",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "書き込み先ファイルパス"},
				"content": map[string]any{"type": "string", "description": "書き込む内容"},
			},
			"required": []any{"path", "content"},
		},
	}
	r.metadata["file_list"] = tool.ToolMetadata{
		ToolID: "file_list", Version: "1.0.0", Category: "query",
		Description: "ディレクトリ内のファイル一覧を取得する",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "一覧を取得するディレクトリパス"},
			},
			"required": []any{"path"},
		},
	}
	if !r.config.DisableWebSearch {
		r.metadata["web_search"] = tool.ToolMetadata{
			ToolID: "web_search", Version: "1.0.0", Category: "query",
			Description: "Web検索を実行して結果を返す",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "検索クエリ"},
				},
				"required": []any{"query"},
			},
		}
	}
	if r.config.WebGatherFetcher != nil {
		r.metadata["web_gather.fetch"] = tool.ToolMetadata{
			ToolID: "web_gather.fetch", Version: "0.1.0", Category: "query",
			Description: "公開 URL を fetch / extract し、必要に応じて pending L1 staging に保存する",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url":            map[string]any{"type": "string"},
					"fetch_provider": map[string]any{"type": "string", "enum": []any{"http", "webwright"}},
					"extractor":      map[string]any{"type": "string", "enum": []any{"go_readability", "html_basic", "plain_text", "json_text"}},
					"namespace":      map[string]any{"type": "string"},
					"source_id":      map[string]any{"type": "string"},
					"store_staging":  map[string]any{"type": "boolean"},
					"refresh":        map[string]any{"type": "boolean"},
					"policy": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"request_timeout_ms": map[string]any{"type": "integer"},
							"max_body_bytes":     map[string]any{"type": "integer"},
							"max_redirects":      map[string]any{"type": "integer"},
						},
					},
				},
				"required": []any{"url"},
			},
		}
	}
	if r.config.WebGatherSearcher != nil {
		r.metadata["web_gather.search"] = tool.ToolMetadata{
			ToolID: "web_gather.search", Version: "0.1.0", Category: "query",
			Description: "Web Gather の検索候補を返す。local_cache / SearXNG / RSS / Atom / sitemap provider を扱う。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":     map[string]any{"type": "string"},
					"provider":  map[string]any{"type": "string", "enum": []any{"local_cache", "searxng", "rss_atom", "sitemap", "yacy"}},
					"limit":     map[string]any{"type": "integer"},
					"language":  map[string]any{"type": "string"},
					"freshness": map[string]any{"type": "string", "enum": []any{"any", "day", "week", "month"}},
					"namespace": map[string]any{"type": "string"},
					"refresh":   map[string]any{"type": "boolean"},
				},
				"required": []any{"query"},
			},
		}
	}
	if r.config.WebGatherSearchFetch != nil {
		r.metadata["web_gather.search_and_fetch"] = tool.ToolMetadata{
			ToolID: "web_gather.search_and_fetch", Version: "0.1.0", Category: "query",
			Description: "検索候補を取得し、上位 URL を fetch / extract して必要に応じて pending L1 staging に保存する。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":          map[string]any{"type": "string"},
					"provider":       map[string]any{"type": "string", "enum": []any{"local_cache", "searxng", "rss_atom", "sitemap", "yacy"}},
					"limit":          map[string]any{"type": "integer"},
					"max_fetches":    map[string]any{"type": "integer"},
					"language":       map[string]any{"type": "string"},
					"freshness":      map[string]any{"type": "string", "enum": []any{"any", "day", "week", "month"}},
					"namespace":      map[string]any{"type": "string"},
					"refresh":        map[string]any{"type": "boolean"},
					"fetch_provider": map[string]any{"type": "string", "enum": []any{"http", "webwright"}},
					"extractor":      map[string]any{"type": "string", "enum": []any{"go_readability", "html_basic", "plain_text", "json_text"}},
					"store_staging":  map[string]any{"type": "boolean"},
					"policy": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"request_timeout_ms": map[string]any{"type": "integer"},
							"max_body_bytes":     map[string]any{"type": "integer"},
							"max_redirects":      map[string]any{"type": "integer"},
						},
					},
				},
				"required": []any{"query"},
			},
		}
	}
	if r.config.BrowserActorRunner != nil {
		r.metadata["browser.run"] = tool.ToolMetadata{
			ToolID: "browser.run", Version: "0.1.0", Category: "query",
			Description: "Headless browser 操作を 1 run として実行し、screenshot / snapshot / network / console artifact を保存する。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"run_id":             map[string]any{"type": "string"},
					"goal":               map[string]any{"type": "string"},
					"start_url":          map[string]any{"type": "string"},
					"profile_id":         map[string]any{"type": "string"},
					"storage_state_path": map[string]any{"type": "string"},
					"headless":           map[string]any{"type": "boolean"},
					"artifact_dir":       map[string]any{"type": "string"},
					"timeout_ms":         map[string]any{"type": "integer"},
					"max_actions":        map[string]any{"type": "integer"},
					"allowed_origins":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"viewport": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"width":  map[string]any{"type": "integer"},
							"height": map[string]any{"type": "integer"},
						},
					},
					"actions": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "object"},
					},
				},
				"required": []any{"start_url", "actions"},
			},
		}
	}
	if len(r.config.Subagents) > 0 {
		r.metadata["subagent"] = subagentToolMetadata()
	}

	if r.config.ToolRegistry != nil {
		r.metadata["register_tool"] = registerToolMetadata()
	}
}

func (r *ToolRunner) registerSubagentTool() {
	r.tools["subagent"] = withTimeout(r.executeSubagent, 30*time.Second)
	r.toolsV2["subagent"] = v2Wrap(r.tools["subagent"])
}

func (r *ToolRunner) registerToolRegistryTool() {
	r.tools["register_tool"] = withTimeout(r.executeRegisterTool, 10*time.Second)
	r.toolsV2["register_tool"] = v2Wrap(r.tools["register_tool"])
}

func subagentToolMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "subagent", Version: "1.0.0", Category: "query",
		Description: "サブエージェントにタスクを委譲する",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent":   map[string]any{"type": "string", "description": "サブエージェント名"},
				"message": map[string]any{"type": "string", "description": "タスク指示文"},
			},
			"required": []any{"agent", "message"},
		},
	}
}

func registerToolMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "register_tool", Version: "1.0.0", Category: "mutation",
		Description: "シェルスクリプトを再利用可能なツールとして ToolRegistry に登録する。スクリプトは事前に workspace/tools/<name>.sh に保存しておくこと。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":              map[string]any{"type": "string", "description": "ツール名（英数字とアンダースコアのみ）"},
				"description":       map[string]any{"type": "string", "description": "ツールの説明文（LLM に渡す）"},
				"parameters_schema": map[string]any{"type": "object", "description": "ツール引数の JSON Schema（省略可）"},
			},
			"required": []any{"name", "description"},
		},
	}
}

// RegisterSubagent はサブエージェントを後から登録する（循環依存回避用）
func (r *ToolRunner) RegisterSubagent(name string, fn SubagentFunc) {
	if r.config.Subagents == nil {
		r.config.Subagents = make(map[string]SubagentFunc)
	}
	r.config.Subagents[name] = fn

	// ツールも登録
	r.registerSubagentTool()
	r.metadata["subagent"] = subagentToolMetadata()
}

// executeRegisterTool はシェルスクリプトを ToolRegistry に登録する（Phase 4）
func (r *ToolRunner) executeRegisterTool(ctx context.Context, args map[string]interface{}) (string, error) {
	name, ok := args["name"].(string)
	if !ok || name == "" {
		return "", &tool.ToolError{Code: tool.ErrValidationFailed, Message: "'name' is required"}
	}
	if !validToolName.MatchString(name) {
		return "", &tool.ToolError{Code: tool.ErrValidationFailed, Message: "tool name must contain only alphanumeric characters and underscores"}
	}

	description, ok := args["description"].(string)
	if !ok || description == "" {
		return "", &tool.ToolError{Code: tool.ErrValidationFailed, Message: "'description' is required"}
	}

	// スクリプト存在確認
	scriptPath := filepath.Join(r.config.WorkspaceDir, "tools", name+".sh")
	if _, err := os.Stat(scriptPath); err != nil {
		return "", &tool.ToolError{
			Code:    tool.ErrNotFound,
			Message: fmt.Sprintf("script not found at %s: create it with file_write first", scriptPath),
		}
	}

	// parameters_schema の処理（省略時はシンプルな {input: string} スキーマ）
	paramsSchema, _ := args["parameters_schema"].(map[string]any)
	if paramsSchema == nil {
		paramsSchema = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{"type": "string", "description": "Input to the tool"},
			},
		}
	}

	// llm.ToolDefinition を構築して SchemaJSON に変換
	toolDef := llm.ToolDefinition{
		Type: "function",
		Function: llm.ToolFunctionDef{
			Name:        name,
			Description: description,
			Parameters:  paramsSchema,
		},
	}
	schemaBytes, err := json.Marshal(toolDef)
	if err != nil {
		return "", fmt.Errorf("failed to marshal tool schema: %w", err)
	}

	entry := capability.ToolEntry{
		Name:        name,
		Description: description,
		SchemaJSON:  string(schemaBytes),
		Platforms:   []string{"linux"},
		Source:      capability.ToolSourceShiroGenerated,
		CreatedBy:   "shiro",
	}

	if err := r.config.ToolRegistry.Register(ctx, entry); err != nil {
		return "", fmt.Errorf("failed to register tool %q: %w", name, err)
	}

	return fmt.Sprintf("Tool %q registered. It is now available in the next invocation.", name), nil
}
