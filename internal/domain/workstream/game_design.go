package workstream

// GameProjectDesignResult contains the current Goal, including progress made
// after registration. Replaying this operation never writes the Goal again.
type GameProjectDesignResult struct {
	Goal       Goal   `json:"goal"`
	Replayed   bool   `json:"replayed"`
	ReceiptRef string `json:"receipt_ref"`
}
