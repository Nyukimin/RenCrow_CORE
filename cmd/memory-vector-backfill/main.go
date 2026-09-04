// memory-vector-backfill is an owner CLI that projects existing memories into
// the Qdrant thread-memory collection through the canonical embedding route
// (RenCrow_LLM Gateway /v1/embeddings). It is deterministic: no generative
// LLM is used, summaries come from stored archive rows and from fixed-format
// ChatGPT conversation digests. Qdrant point identity is derived by the store
// from canonical ThreadID so re-runs converge without a second identity rule.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/rencrowllm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/vectordb"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func truncateRunes(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}

// sanitizeUTF8 replaces invalid byte sequences; legacy archive rows contain a
// few, and Qdrant's gRPC payload marshaling rejects them outright.
func sanitizeUTF8(value string) string {
	return strings.ToValidUTF8(value, "\uFFFD")
}

func sanitizeKeywords(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, sanitizeUTF8(v))
	}
	return out
}

type embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

type archiveThreadIdentity struct {
	threadID   modulecore.ThreadID
	threadSeq  modulecore.ThreadSeq
	threadKind modulecore.ThreadKind
}

func validateArchiveThreadIdentity(identity archiveThreadIdentity) error {
	if err := identity.threadID.Validate(); err != nil {
		return fmt.Errorf("thread_id: %w", err)
	}
	if err := identity.threadSeq.Validate(); err != nil {
		return fmt.Errorf("thread_seq: %w", err)
	}
	if err := identity.threadKind.Validate(); err != nil {
		return fmt.Errorf("thread_kind: %w", err)
	}
	return nil
}

func chatGPTConversationIdentity(conversationID string) (modulecore.SessionID, modulecore.ThreadID, error) {
	sessionRaw, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "l1_raw_record", "session_id", conversationID)
	if err != nil {
		return "", "", fmt.Errorf("derive ChatGPT session ID for conversation %q: %w", conversationID, err)
	}
	sessionID := modulecore.SessionID(sessionRaw)
	if err := sessionID.Validate(); err != nil {
		return "", "", fmt.Errorf("validate ChatGPT session ID for conversation %q: %w", conversationID, err)
	}

	threadRaw, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "l1_raw_record", "thread_id", conversationID)
	if err != nil {
		return "", "", fmt.Errorf("derive ChatGPT thread ID for conversation %q: %w", conversationID, err)
	}
	threadID := modulecore.ThreadID(threadRaw)
	if err := threadID.Validate(); err != nil {
		return "", "", fmt.Errorf("validate ChatGPT thread ID for conversation %q: %w", conversationID, err)
	}
	if string(sessionID) == string(threadID) {
		return "", "", fmt.Errorf("ChatGPT session and thread IDs must differ for conversation %q", conversationID)
	}
	return sessionID, threadID, nil
}

func main() {
	archiveDB := flag.String("archive-db", "/srv/rencrow/db/core/databases/conversation/memory_archive.db", "conversation archive SQLite path")
	l1DB := flag.String("l1-db", "/srv/rencrow/db/core/databases/conversation/l1_memory.db", "conversation L1 SQLite path")
	qdrantURL := flag.String("qdrant", "localhost:6334", "Qdrant gRPC address")
	collection := flag.String("collection", "rencrow_memory_1024", "Qdrant collection name")
	dimension := flag.Uint64("dimension", 1024, "embedding dimension")
	gatewayURL := flag.String("gateway", "http://127.0.0.1:8090", "RenCrow_LLM Gateway base URL")
	embedModel := flag.String("embed-model", "embedding", "gateway embedding execution alias")
	dryRun := flag.Bool("dry-run", false, "list work without embedding or writing")
	limit := flag.Int("limit", 0, "stop after N items per source (0 = all)")
	flag.Parse()

	ctx := context.Background()
	var emb embedder
	var store *vectordb.VectorDBStore
	if !*dryRun {
		emb = rencrowllm.NewGatewayEmbedderWithOptions("", *embedModel, *gatewayURL, 60*time.Second)
		var err error
		store, err = vectordb.NewVectorDBStoreWithDimension(*qdrantURL, *collection, *dimension)
		if err != nil {
			log.Fatalf("[NG] qdrant: %v", err)
		}
		defer store.Close()
	}

	archived, archiveErr := backfillArchiveThreads(ctx, *archiveDB, emb, store, *dryRun, *limit)
	if archiveErr != nil {
		log.Fatalf("[NG] archive backfill: %v", archiveErr)
	}
	chatgpt, chatgptErr := backfillChatGPTConversations(ctx, *l1DB, emb, store, *dryRun, *limit)
	if chatgptErr != nil {
		log.Fatalf("[NG] chatgpt backfill: %v", chatgptErr)
	}
	fmt.Printf("[OK] backfill complete: archive_threads=%d chatgpt_conversations=%d dry_run=%v\n", archived, chatgpt, *dryRun)
	os.Exit(0)
}

func backfillArchiveThreads(ctx context.Context, dbPath string, emb embedder, store *vectordb.VectorDBStore, dryRun bool, limit int) (int, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `
SELECT thread_id, thread_seq, thread_kind, session_id, ts_start, ts_end, domain, summary, keywords, is_novel
FROM session_thread
WHERE summary IS NOT NULL AND trim(summary) != ''
ORDER BY thread_id ASC, thread_seq ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var rawThreadID, rawThreadKind string
		var rawThreadSeq int64
		var threadID modulecore.ThreadID
		var threadSeq modulecore.ThreadSeq
		var threadKind modulecore.ThreadKind
		var sessionID, domain, summaryText, keywordsJSON string
		var tsStart, tsEnd time.Time
		var isNovel bool
		if err := rows.Scan(&rawThreadID, &rawThreadSeq, &rawThreadKind, &sessionID, &tsStart, &tsEnd, &domain, &summaryText, &keywordsJSON, &isNovel); err != nil {
			return count, err
		}
		threadID = modulecore.ThreadID(rawThreadID)
		threadSeq = modulecore.ThreadSeq(rawThreadSeq)
		threadKind = modulecore.ThreadKind(rawThreadKind)
		if err := validateArchiveThreadIdentity(archiveThreadIdentity{threadID: threadID, threadSeq: threadSeq, threadKind: threadKind}); err != nil {
			return count, fmt.Errorf("validate archive session_thread identity %q: %w", rawThreadID, err)
		}
		var keywords []string
		_ = json.Unmarshal([]byte(keywordsJSON), &keywords)
		if dryRun {
			count++
			continue
		}
		vector, err := emb.Embed(ctx, sanitizeUTF8(summaryText))
		if err != nil {
			return count, fmt.Errorf("embed archive thread %s: %w", threadID, err)
		}
		summary := &domconv.ThreadSummary{
			ThreadID: threadID, ThreadSeq: threadSeq, ThreadKind: threadKind, SessionID: sanitizeUTF8(sessionID), Domain: sanitizeUTF8(domain),
			Summary: sanitizeUTF8(summaryText), Keywords: sanitizeKeywords(keywords), Embedding: vector,
			StartTime: tsStart.UTC(), EndTime: tsEnd.UTC(),
			IsNovel: isNovel,
		}
		if err := store.SaveThreadSummary(ctx, summary); err != nil {
			return count, fmt.Errorf("save archive thread %s: %w", threadID, err)
		}
		count++
		if count%25 == 0 {
			log.Printf("archive threads: %d done", count)
		}
		if limit > 0 && count >= limit {
			break
		}
	}
	return count, rows.Err()
}

func backfillChatGPTConversations(ctx context.Context, dbPath string, emb embedder, store *vectordb.VectorDBStore, dryRun bool, limit int) (int, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	// Single ordered scan; per-conversation aggregates are built in memory
	// (about 2K conversations). A correlated per-group subquery over the 54K
	// message rows was quadratic and unusable.
	rows, err := db.QueryContext(ctx, `
SELECT
  json_extract(meta_json,'$.conversation_id'),
  COALESCE(json_extract(meta_json,'$.conversation_title'), ''),
  created_at,
  message,
  COALESCE(json_extract(meta_json,'$.original_role'), ''),
  COALESCE(json_extract(meta_json,'$.on_current_branch'), 0)
FROM l1_memory_event
WHERE layer='L3' AND source='chatgpt_export'
ORDER BY created_at ASC, rowid ASC`)
	if err != nil {
		return 0, err
	}
	type convAgg struct {
		title     string
		start     time.Time
		end       time.Time
		firstUser string
	}
	aggregates := map[string]*convAgg{}
	order := []string{}
	for rows.Next() {
		var convID, title, message, role string
		var created time.Time
		var onBranch bool
		if err := rows.Scan(&convID, &title, &created, &message, &role, &onBranch); err != nil {
			rows.Close()
			return 0, err
		}
		if strings.TrimSpace(convID) == "" {
			continue
		}
		agg, ok := aggregates[convID]
		if !ok {
			agg = &convAgg{start: created, end: created}
			aggregates[convID] = agg
			order = append(order, convID)
		}
		if title != "" {
			agg.title = title
		}
		if created.Before(agg.start) {
			agg.start = created
		}
		if created.After(agg.end) {
			agg.end = created
		}
		if agg.firstUser == "" && role == "user" && onBranch {
			agg.firstUser = message
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	count := 0
	for _, convID := range order {
		agg := aggregates[convID]
		digest := sanitizeUTF8(buildChatGPTDigest(agg.title, agg.firstUser, agg.start.UTC()))
		if digest == "" {
			continue
		}
		sessionID, threadID, err := chatGPTConversationIdentity(convID)
		if err != nil {
			return count, err
		}
		if dryRun {
			count++
			continue
		}
		vector, err := emb.Embed(ctx, digest)
		if err != nil {
			return count, fmt.Errorf("embed chatgpt conversation %s: %w", convID, err)
		}
		keywords := []string{"chatgpt", "過去の会話"}
		if t := sanitizeUTF8(truncateRunes(agg.title, 64)); t != "" {
			keywords = append([]string{t}, keywords...)
		}
		summary := &domconv.ThreadSummary{
			ThreadID:   threadID,
			ThreadSeq:  1,
			ThreadKind: modulecore.ThreadKindUserConversation,
			SessionID:  string(sessionID),
			Domain:     "chatgpt",
			Summary:    digest,
			Keywords:   keywords,
			Embedding:  vector,
			StartTime:  agg.start.UTC(), EndTime: agg.end.UTC(),
		}
		if err := store.SaveThreadSummary(ctx, summary); err != nil {
			return count, fmt.Errorf("save chatgpt conversation %s: %w", convID, err)
		}
		count++
		if count%100 == 0 {
			log.Printf("chatgpt conversations: %d done", count)
		}
		if limit > 0 && count >= limit {
			break
		}
	}
	return count, nil
}

func buildChatGPTDigest(title, firstUser string, start time.Time) string {
	title = strings.TrimSpace(title)
	body := truncateRunes(firstUser, 300)
	if title == "" && body == "" {
		return ""
	}
	date := ""
	if !start.IsZero() {
		date = start.Format("2006-01")
	}
	var sb strings.Builder
	if title != "" {
		fmt.Fprintf(&sb, "『%s』についての過去のChatGPT会話", title)
	} else {
		sb.WriteString("過去のChatGPT会話")
	}
	if date != "" {
		fmt.Fprintf(&sb, "（%s）", date)
	}
	if body != "" {
		sb.WriteString(": ")
		sb.WriteString(body)
	}
	return sb.String()
}
