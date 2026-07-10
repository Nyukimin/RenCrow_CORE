package dci

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
)

// SessionLogFormat はセッションログのフォーマット種別
type SessionLogFormat string

const (
	SessionLogFormatRenCrow SessionLogFormat = "rencrow" // internal/infrastructure/logging/session_log_writer.go
	SessionLogFormatCodex   SessionLogFormat = "codex"   // ~/.codex/sessions/**/*.jsonl
	SessionLogFormatClaude  SessionLogFormat = "claude"  // ~/.claude/projects/**/*.jsonl
)

// SessionLogSource はDCIが参照するセッションログの参照先定義
type SessionLogSource struct {
	Name    string // 表示名 ("rencrow", "codex", "claude" 等)
	PathDir string // GLOBで検索するベースディレクトリ
	Format  SessionLogFormat
}

// SessionLogCandidateProvider は複数のセッションログソースからDCI候補ファイルを提供する
type SessionLogCandidateProvider struct {
	sources []SessionLogSource
}

// NewSessionLogCandidateProvider は指定されたソース群からプロバイダーを生成する
func NewSessionLogCandidateProvider(sources []SessionLogSource) *SessionLogCandidateProvider {
	return &SessionLogCandidateProvider{sources: sources}
}

// CandidateFiles はクエリ・タームにマッチするセッションログファイルを返す
func (p *SessionLogCandidateProvider) CandidateFiles(ctx context.Context, query string, terms []string, allowlist []string, limit int) ([]domaindci.SourceMetadataRank, error) {
	if limit <= 0 {
		limit = 10
	}

	lowerTerms := make([]string, len(terms))
	for i, t := range terms {
		lowerTerms[i] = strings.ToLower(t)
	}
	queryLower := strings.ToLower(query)

	var candidates []domaindci.SourceMetadataRank

	for _, src := range p.sources {
		files, err := collectJSONLFiles(src.PathDir, 90) // 直近90日のファイルのみ
		if err != nil {
			continue
		}
		for _, f := range files {
			score := scoreSessionFile(f, src.Format, queryLower, lowerTerms)
			if score > 0 {
				candidates = append(candidates, domaindci.SourceMetadataRank{
					FilePath: f,
					SourceID: "session:" + src.Name,
					Score:    score,
					Reason:   "session log matched query terms (" + src.Name + ")",
				})
			}
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

// collectJSONLFiles は指定ディレクトリ以下のJSONLファイルのうち、recentDays日以内に更新されたものを返す
func collectJSONLFiles(baseDir string, recentDays int) ([]string, error) {
	expanded := os.ExpandEnv(baseDir)
	if expanded == "" {
		return nil, nil
	}
	cutoff := time.Now().AddDate(0, 0, -recentDays)

	var files []string
	err := filepath.Walk(expanded, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if info.ModTime().After(cutoff) {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// scoreSessionFile はファイル内容をサンプリングしてスコアを計算する
func scoreSessionFile(path string, format SessionLogFormat, queryLower string, terms []string) float64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<16), 1<<16) // 64KB
	hitCount := 0
	lineCount := 0

	for scanner.Scan() && lineCount < 200 {
		lineCount++
		text := strings.ToLower(extractTextFromLine(scanner.Bytes(), format))
		if text == "" {
			continue
		}
		for _, term := range terms {
			if strings.Contains(text, term) {
				hitCount++
			}
		}
		if strings.Contains(text, queryLower) {
			hitCount += 2
		}
	}
	if hitCount == 0 {
		return 0
	}
	if lineCount == 0 {
		lineCount = 1
	}
	return float64(hitCount) / float64(lineCount)
}

// extractTextFromLine はフォーマット別にJSONLの1行からテキストを取り出す
func extractTextFromLine(data []byte, format SessionLogFormat) string {
	switch format {
	case SessionLogFormatRenCrow:
		var entry struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(data, &entry) == nil {
			return entry.Content
		}
	case SessionLogFormatCodex:
		// {"type":"user_turn"/"assistant_turn","payload":{"content":"..."}}
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				Content string `json:"content"`
				Text    string `json:"text"`
				Message string `json:"message"`
			} `json:"payload"`
		}
		if json.Unmarshal(data, &entry) == nil {
			if entry.Payload.Content != "" {
				return entry.Payload.Content
			}
			if entry.Payload.Text != "" {
				return entry.Payload.Text
			}
			return entry.Payload.Message
		}
	case SessionLogFormatClaude:
		// {"type":"user"/"assistant","message":{"content":[{"type":"text","text":"..."}]}}
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(data, &entry) == nil {
			// content can be string or array
			var text string
			if json.Unmarshal(entry.Message.Content, &text) == nil {
				return text
			}
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(entry.Message.Content, &parts) == nil {
				var sb strings.Builder
				for _, p := range parts {
					if p.Type == "text" {
						sb.WriteString(p.Text)
					}
				}
				return sb.String()
			}
		}
	}
	return ""
}
