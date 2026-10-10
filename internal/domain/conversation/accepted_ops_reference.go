package conversation

// AcceptedOPSInputReference is the compact durable reference a Task keeps to
// the Conversation-owned accepted input. The accepted body remains owned by
// Conversation's Raw record.
type AcceptedOPSInputReference struct {
	OwnerID       string `json:"owner_id"`
	RequestID     string `json:"request_id"`
	PayloadSHA256 string `json:"payload_sha256"`
}

// AcceptedOPSInputReferenceFromReceipt validates an owner receipt before
// deriving the compact Task reference. Replay-only receipt fields are omitted.
func AcceptedOPSInputReferenceFromReceipt(receipt AcceptedOPSInputReceipt) (AcceptedOPSInputReference, error) {
	if err := receipt.Validate(); err != nil {
		return AcceptedOPSInputReference{}, err
	}
	return AcceptedOPSInputReference{
		OwnerID:       receipt.OwnerID,
		RequestID:     receipt.RequestID,
		PayloadSHA256: receipt.PayloadSHA256,
	}, nil
}

func (reference AcceptedOPSInputReference) Validate() error {
	if _, err := NormalizeAcceptedOPSInputReadRequest(AcceptedOPSInputReadRequest{
		RequestID: reference.RequestID,
		OwnerID:   reference.OwnerID,
	}); err != nil || !validSHA256(reference.PayloadSHA256) {
		return ErrAcceptedOPSInputInvalid
	}
	return nil
}
