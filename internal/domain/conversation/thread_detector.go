package conversation

// ThreadBoundaryReason はスレッド境界検出の理由
type ThreadBoundaryReason string

const (
	BoundaryKeyword    ThreadBoundaryReason = "keyword"
	BoundarySimilarity ThreadBoundaryReason = "similarity"
	BoundaryInactivity ThreadBoundaryReason = "inactivity"
	BoundaryDomain     ThreadBoundaryReason = "domain_change"
)

// ThreadBoundaryResult はスレッド境界検出の結果
type ThreadBoundaryResult struct {
	ShouldCreateNew bool
	Reason          ThreadBoundaryReason
	Score           float32 // cosine similarity score（similarity の場合のみ有効）
}

// ThreadBoundaryDetector は新しいメッセージが新スレッドに属するかを判定する
type ThreadBoundaryDetector interface {
	Detect(currentThread *Thread, newMessage string, newDomain string) ThreadBoundaryResult
}
