package conversation

import (
	"context"
	"math"
	"strings"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

// RealThreadBoundaryDetector はスレッド境界検出の実装
type RealThreadBoundaryDetector struct {
	similarityThreshold float32
	inactivityTimeout   time.Duration
	topicChangeKeywords []string
	embedder            domconv.EmbeddingProvider // nil の場合は類似度チェックをスキップ
}

// NewThreadBoundaryDetector は新しい RealThreadBoundaryDetector を作成
func NewThreadBoundaryDetector(embedder domconv.EmbeddingProvider) *RealThreadBoundaryDetector {
	return &RealThreadBoundaryDetector{
		similarityThreshold: 0.75,
		inactivityTimeout:   10 * time.Minute,
		topicChangeKeywords: []string{
			"ところで", "別件", "質問変えて", "話変わるけど", "別の話", "それとは別に",
		},
		embedder: embedder,
	}
}

// Detect はスレッド境界を検出する（first match wins）
func (d *RealThreadBoundaryDetector) Detect(currentThread *domconv.Thread, newMessage string, newDomain string) domconv.ThreadBoundaryResult {
	// 1. キーワード検出（最安）
	for _, kw := range d.topicChangeKeywords {
		if strings.Contains(newMessage, kw) {
			return domconv.ThreadBoundaryResult{
				ShouldCreateNew: true,
				Reason:          domconv.BoundaryKeyword,
			}
		}
	}

	// 2. 非活動タイムアウト
	if time.Since(currentThread.LastMessageTime()) > d.inactivityTimeout {
		return domconv.ThreadBoundaryResult{
			ShouldCreateNew: true,
			Reason:          domconv.BoundaryInactivity,
		}
	}

	// 3. Embedding 類似度（embedder がある場合のみ）
	if d.embedder != nil && len(currentThread.Turns) >= 2 {
		score := d.computeSimilarity(currentThread, newMessage)
		if score >= 0 && score < d.similarityThreshold {
			return domconv.ThreadBoundaryResult{
				ShouldCreateNew: true,
				Reason:          domconv.BoundarySimilarity,
				Score:           score,
			}
		}
	}

	// 4. ドメイン変更
	if newDomain != "" && currentThread.Domain != newDomain {
		return domconv.ThreadBoundaryResult{
			ShouldCreateNew: true,
			Reason:          domconv.BoundaryDomain,
		}
	}

	return domconv.ThreadBoundaryResult{ShouldCreateNew: false}
}

// computeSimilarity は新メッセージと Thread 直近メッセージの cosine 類似度を計算
// エラー時は -1 を返す（スキップ扱い）
func (d *RealThreadBoundaryDetector) computeSimilarity(thread *domconv.Thread, newMessage string) float32 {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 新メッセージの embedding
	newEmb, err := d.embedder.Embed(ctx, newMessage)
	if err != nil {
		return -1
	}

	// Thread 直近3件のテキストを連結して embedding
	recentText := thread.RecentMessagesText(3)
	if recentText == "" {
		return -1
	}

	recentEmb, err := d.embedder.Embed(ctx, recentText)
	if err != nil {
		return -1
	}

	return cosineSimilarity(newEmb, recentEmb)
}

// cosineSimilarity は2つのベクトルの cosine 類似度を計算
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / math.Sqrt(normA*normB))
}
