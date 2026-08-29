package config

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config/agentcontrol"
	"gopkg.in/yaml.v3"
)

// LoadConfig は設定ファイルを読み込む
func LoadConfig(path string) (*Config, error) {
	// ファイル読み込み
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// 先に YAML をパースし、scalar 値として環境変数を展開する。
	// Windows パスのバックスラッシュ等を YAML 構文として再解釈させない。
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}
	if retired := retiredDatabaseConfigKey(&root); retired != "" {
		return nil, fmt.Errorf("config key %s is retired; CORE uses storage.databases SQLite owner stores", retired)
	}
	if yamlDocumentMapping(&root) != nil && yamlMappingValue(yamlDocumentMapping(&root), "viewer_log") != nil {
		return nil, fmt.Errorf("config key viewer_log is retired; CORE uses storage.databases.event_store as the only durable Event source")
	}
	if retired := retiredTTSEndpointConfigKey(&root); retired != "" {
		return nil, fmt.Errorf("config key %s is retired; CORE uses tts.gateway_base_url", retired)
	}
	expandConfigEnvironment(&root)
	var cfg Config
	if err := root.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	if err := cfg.applyCanonicalStoragePaths(); err != nil {
		return nil, fmt.Errorf("failed to resolve storage database paths: %w", err)
	}

	if err := cfg.resolveRuntimeTopologyReferences(); err != nil {
		return nil, fmt.Errorf("failed to resolve runtime topology references: %w", err)
	}

	// デフォルト値設定
	cfg.setDefaults()
	cfg.populateCanonicalStoragePaths()

	// バリデーション
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	// プロンプトファイル読み込み（prompts/ → workspace/ の順でオーバーライド）
	cfg.Prompts = LoadPrompts(cfg.PromptsDir, cfg.WorkspaceDir)
	cfg.AgentControl, err = agentcontrol.Load(cfg.WorkspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load shared agent control: %w", err)
	}
	if cfg.AgentControl != nil {
		ApplyAgentControl(cfg.Prompts, cfg.AgentControl)
		log.Printf("Loaded shared agent control from %s", cfg.WorkspaceDir)
	}

	return &cfg, nil
}

// retiredDatabaseConfigKey fails closed on settings from the removed DuckDB
// runtime. Keeping an ignored path in production config makes a retained
// rollback artifact look like an active owner store.
func retiredDatabaseConfigKey(root *yaml.Node) string {
	if mapping := yamlDocumentMapping(root); mapping != nil {
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			key, value := mapping.Content[i].Value, mapping.Content[i+1]
			switch key {
			case "duckdb_path":
				return "duckdb_path"
			case "storage":
				if child := yamlMappingValue(value, "legacy_databases"); child != nil {
					return "storage.legacy_databases"
				}
			}
		}
	}
	return ""
}

// retiredTTSEndpointConfigKey fails closed on direct TTS endpoints from the
// removed route. The CORE TTS boundary is the RenCrow_TTS Gateway only.
func retiredTTSEndpointConfigKey(root *yaml.Node) string {
	tts := yamlMappingValue(yamlDocumentMapping(root), "tts")
	if tts == nil {
		return ""
	}
	for _, key := range []string{"http_base_url", "base_url", "public_base_url", "audio_base_url"} {
		if yamlMappingValue(tts, key) != nil {
			return "tts." + key
		}
	}
	return ""
}

func yamlDocumentMapping(node *yaml.Node) *yaml.Node {
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	if node != nil && node.Kind == yaml.MappingNode {
		return node
	}
	return nil
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	mapping := yamlDocumentMapping(node)
	if mapping == nil {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func expandConfigEnvironment(node *yaml.Node) {
	if node.Kind == yaml.ScalarNode && strings.Contains(node.Value, "${") {
		node.Value = os.Expand(node.Value, func(key string) string {
			if strings.HasPrefix(key, "module:") {
				return "${" + key + "}"
			}
			return os.Getenv(key)
		})
		// Decode 時に展開後の値を対象フィールドの型へ変換させる。
		node.Tag = ""
	}
	for _, child := range node.Content {
		expandConfigEnvironment(child)
	}
}
