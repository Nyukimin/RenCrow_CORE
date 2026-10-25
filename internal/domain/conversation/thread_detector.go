package conversation

// ThreadBoundaryProposal is a semantic proposal about whether a message
// starts a new thread. The canonical conversation owner resolves it against
// the prepared head and any trusted Mio proposal before committing.
type ThreadBoundaryProposal string

const (
	ThreadBoundaryUnknown ThreadBoundaryProposal = "unknown"
	ThreadBoundaryKeep    ThreadBoundaryProposal = "keep"
	ThreadBoundaryNew     ThreadBoundaryProposal = "new"
)

// ThreadBoundaryReason is a fixed code stored with a canonical new-thread
// decision. It never contains caller or model prose.
type ThreadBoundaryReason string

const (
	BoundaryExplicitTopicChange ThreadBoundaryReason = "explicit_topic_change"
	BoundaryMioTopicChange      ThreadBoundaryReason = "mio_topic_change"
)

// ThreadBoundaryResult is the bounded deterministic rule result. Unknown is
// resolved to keep unless a valid, turn-bound Mio proposal is available.
type ThreadBoundaryResult struct {
	Proposal ThreadBoundaryProposal
	Reason   ThreadBoundaryReason
}

// ThreadBoundaryDetector derives lightweight cues from the already prepared
// head and current user message. It performs no embedding or storage access.
type ThreadBoundaryDetector interface {
	Detect(currentThread *Thread, newMessage string) ThreadBoundaryResult
}
