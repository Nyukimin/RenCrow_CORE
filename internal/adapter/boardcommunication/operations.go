package boardcommunication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type threadPageResponse struct {
	Thread        BoardThread `json:"thread"`
	NextCursor    string      `json:"next_cursor"`
	CorrelationID string      `json:"correlation_id"`
}

type threadListResponse struct {
	Threads       []ThreadSummary `json:"threads"`
	NextCursor    string          `json:"next_cursor"`
	CorrelationID string          `json:"correlation_id"`
}

type inboxPageResponse struct {
	Items         []InboxItem `json:"items"`
	NextCursor    string      `json:"next_cursor"`
	CorrelationID string      `json:"correlation_id"`
}

type inboxReceiptResponse struct {
	ItemID        string `json:"item_id"`
	MessageID     string `json:"message_id"`
	AckReceipt    string `json:"ack_receipt"`
	CorrelationID string `json:"correlation_id"`
}

type claimResponse struct {
	MessageID     string     `json:"message_id"`
	ClaimID       string     `json:"claim_id"`
	ClaimReceipt  string     `json:"claim_receipt,omitempty"`
	Status        string     `json:"status"`
	LeaseUntil    *time.Time `json:"lease_until,omitempty"`
	CanProcess    bool       `json:"can_process,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	CorrelationID string     `json:"correlation_id"`
}

type messageReceiptResponse struct {
	Receipt       SendReceipt `json:"receipt"`
	CorrelationID string      `json:"correlation_id"`
}

type messageStatusResponse struct {
	MessageID     string               `json:"message_id"`
	Recipients    []BoardMessageStatus `json:"recipients"`
	CorrelationID string               `json:"correlation_id"`
}

type ackRequest struct {
	Receipt string `json:"receipt"`
}

type ackResponse struct {
	ItemID        string `json:"item_id"`
	Status        string `json:"status"`
	CorrelationID string `json:"correlation_id"`
}

// ListThreads reads one explicit canonical BOARD thread-summary page.
func (c *Client) ListThreads(ctx context.Context, channelID string, page PageRequest) (ThreadListPage, error) {
	if !validBoardID(channelID) || !validPageRequest(page) {
		return ThreadListPage{}, ErrInvalidRequest
	}
	query := pageValues(page)
	query.Set("channel_id", channelID)
	endpoint := "/api/v1/threads?" + query.Encode()
	var wire threadListResponse
	header, err := c.getJSON(ctx, endpoint, &wire)
	if err != nil {
		return ThreadListPage{}, err
	}
	if !validResponseCorrelation(wire.CorrelationID) || !correlationHeaderMatches(header, wire.CorrelationID) ||
		wire.Threads == nil || !validPageCursor(wire.NextCursor) {
		return ThreadListPage{}, ErrProtocol
	}
	for _, thread := range wire.Threads {
		if !validBoardID(thread.ThreadID) || thread.ChannelID != channelID || thread.UpdatedAt == "" {
			return ThreadListPage{}, ErrProtocol
		}
	}
	if wire.NextCursor != "" && (len(wire.Threads) == 0 || wire.Threads[len(wire.Threads)-1].ThreadID != wire.NextCursor) {
		return ThreadListPage{}, ErrProtocol
	}
	return ThreadListPage{Threads: wire.Threads, NextCursor: wire.NextCursor}, nil
}

// ReadThread reads one explicit canonical BOARD thread page.
func (c *Client) ReadThread(ctx context.Context, threadID string, page PageRequest) (ThreadPage, error) {
	if !validBoardID(threadID) || !validPageRequest(page) {
		return ThreadPage{}, ErrInvalidRequest
	}
	endpoint := "/api/v1/threads/" + url.PathEscape(threadID) + "?" + pageValues(page).Encode()
	var wire threadPageResponse
	header, err := c.getJSON(ctx, endpoint, &wire)
	if err != nil {
		return ThreadPage{}, err
	}
	result := ThreadPage{Thread: wire.Thread, NextCursor: wire.NextCursor}
	if !validResponseCorrelation(wire.CorrelationID) || !correlationHeaderMatches(header, wire.CorrelationID) || !validThreadPage(result, threadID) {
		return ThreadPage{}, ErrProtocol
	}
	return result, nil
}

// Inbox reads one explicit canonical flat BOARD inbox page.
func (c *Client) Inbox(ctx context.Context, page PageRequest) (InboxPage, error) {
	if !validPageRequest(page) {
		return InboxPage{}, ErrInvalidRequest
	}
	endpoint := "/api/v1/inbox?" + pageValues(page).Encode()
	var wire inboxPageResponse
	header, err := c.getJSON(ctx, endpoint, &wire)
	if err != nil {
		return InboxPage{}, err
	}
	if !validResponseCorrelation(wire.CorrelationID) || !correlationHeaderMatches(header, wire.CorrelationID) ||
		wire.Items == nil || !validPageCursor(wire.NextCursor) {
		return InboxPage{}, ErrProtocol
	}
	for _, item := range wire.Items {
		if !validInboxItem(item) {
			return InboxPage{}, ErrProtocol
		}
	}
	if wire.NextCursor != "" && (len(wire.Items) == 0 || wire.Items[len(wire.Items)-1].ItemID != wire.NextCursor) {
		return InboxPage{}, ErrProtocol
	}
	return InboxPage{Items: wire.Items, NextCursor: wire.NextCursor}, nil
}

// InboxReceipt requests the current private ACK receipt for one inbox item.
func (c *Client) InboxReceipt(ctx context.Context, itemID string) (InboxReceipt, error) {
	if !validBoardID(itemID) {
		return InboxReceipt{}, ErrInvalidRequest
	}
	endpoint := "/api/v1/inbox/" + url.PathEscape(itemID) + "/receipt"
	var wire inboxReceiptResponse
	header, err := c.getJSON(ctx, endpoint, &wire)
	if err != nil {
		return InboxReceipt{}, err
	}
	if !validResponseCorrelation(wire.CorrelationID) || !correlationHeaderMatches(header, wire.CorrelationID) || wire.ItemID != itemID ||
		!validBoardID(wire.MessageID) || !validReceipt(wire.AckReceipt) {
		return InboxReceipt{}, ErrProtocol
	}
	return InboxReceipt{ItemID: wire.ItemID, MessageID: wire.MessageID, AckReceipt: wire.AckReceipt}, nil
}

// ClaimProcessing makes one explicit caller-owned processing-claim request.
func (c *Client) ClaimProcessing(ctx context.Context, itemID string, claim ClaimRequest) (ClaimResult, error) {
	if !validBoardID(itemID) || !validReceipt(claim.Receipt) ||
		(claim.ClaimID != "" && !validBoardID(claim.ClaimID)) || claim.LeaseSeconds < 0 || claim.LeaseSeconds > 300 {
		return ClaimResult{}, ErrInvalidRequest
	}
	return c.postClaim(ctx, "/api/v1/inbox/"+url.PathEscape(itemID)+"/processing-claim", itemID, claim)
}

func (c *Client) postClaim(ctx context.Context, endpoint, itemID string, claim ClaimRequest) (ClaimResult, error) {
	body, err := json.Marshal(claim)
	if err != nil || len(body) > maxRequestBodyBytes {
		return ClaimResult{}, ErrInvalidRequest
	}
	response, release, err := c.do(ctx, http.MethodPost, endpoint, body, "")
	if err != nil {
		return ClaimResult{}, err
	}
	defer release()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, readErr := readBounded(response.Body, maxResponseBodyBytes)
		if readErr != nil {
			return ClaimResult{}, ErrUnknownOutcome
		}
		return ClaimResult{}, claimResponseError(response.StatusCode, body, response.Header)
	}
	responseBody, err := readBounded(response.Body, maxResponseBodyBytes)
	if err != nil {
		return ClaimResult{}, ErrUnknownOutcome
	}
	var wire claimResponse
	if err := decodeStrictJSON(responseBody, &wire); err != nil || !validResponseCorrelation(wire.CorrelationID) ||
		!correlationHeaderMatches(response.Header, wire.CorrelationID) || !validClaimResult(ClaimResult{
		MessageID: wire.MessageID, ClaimID: wire.ClaimID, ClaimReceipt: wire.ClaimReceipt,
		Status: wire.Status, LeaseUntil: wire.LeaseUntil, CanProcess: wire.CanProcess, CompletedAt: wire.CompletedAt,
	}, claim.ClaimID) {
		return ClaimResult{}, ErrUnknownOutcome
	}
	return ClaimResult{
		MessageID: wire.MessageID, ClaimID: wire.ClaimID, ClaimReceipt: wire.ClaimReceipt,
		Status: wire.Status, LeaseUntil: wire.LeaseUntil, CanProcess: wire.CanProcess, CompletedAt: wire.CompletedAt,
	}, nil
}

func claimResponseError(status int, body []byte, header http.Header) error {
	if status == http.StatusConflict {
		code, valid := decodeConnectionIdentityError(body, header)
		if valid && code == "processing_claim_active" {
			return ErrClaimActive
		}
	}
	return sendResponseError(status, body, header)
}

// AckInbox makes one explicit caller-owned inbox acknowledgement request.
func (c *Client) AckInbox(ctx context.Context, itemID, receipt string) error {
	if !validBoardID(itemID) || !validReceipt(receipt) {
		return ErrInvalidRequest
	}
	body, err := json.Marshal(ackRequest{Receipt: receipt})
	if err != nil || len(body) > maxRequestBodyBytes {
		return ErrInvalidRequest
	}
	response, release, err := c.do(ctx, http.MethodPost, "/api/v1/inbox/"+url.PathEscape(itemID)+"/ack", body, "")
	if err != nil {
		return err
	}
	defer release()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, readErr := readBounded(response.Body, maxResponseBodyBytes)
		if readErr != nil {
			return ErrUnknownOutcome
		}
		return sendResponseError(response.StatusCode, body, response.Header)
	}
	body, err = readBounded(response.Body, maxResponseBodyBytes)
	if err != nil {
		return ErrUnknownOutcome
	}
	var wire ackResponse
	if err := decodeStrictJSON(body, &wire); err != nil || !validResponseCorrelation(wire.CorrelationID) ||
		!correlationHeaderMatches(response.Header, wire.CorrelationID) || wire.ItemID != itemID || wire.Status != "acked" {
		return ErrUnknownOutcome
	}
	return nil
}

// MessageReceipt reads one explicit canonical BOARD message receipt.
func (c *Client) MessageReceipt(ctx context.Context, messageID string) (SendReceipt, error) {
	if !validBoardID(messageID) {
		return SendReceipt{}, ErrInvalidRequest
	}
	endpoint := "/api/v1/messages/" + url.PathEscape(messageID) + "/receipt"
	var wire messageReceiptResponse
	header, err := c.getJSON(ctx, endpoint, &wire)
	if err != nil {
		return SendReceipt{}, err
	}
	receipt := wire.Receipt
	if !validResponseCorrelation(wire.CorrelationID) || !correlationHeaderMatches(header, wire.CorrelationID) ||
		receipt.MessageID != messageID || !validBoardID(receipt.ThreadID) ||
		receipt.StoredAt.IsZero() || receipt.Status != "stored" {
		return SendReceipt{}, ErrProtocol
	}
	return receipt, nil
}

// MessageStatus reads one explicit canonical BOARD recipient-status view.
func (c *Client) MessageStatus(ctx context.Context, messageID string) ([]BoardMessageStatus, error) {
	if !validBoardID(messageID) {
		return nil, ErrInvalidRequest
	}
	endpoint := "/api/v1/messages/" + url.PathEscape(messageID) + "/status"
	var wire messageStatusResponse
	header, err := c.getJSON(ctx, endpoint, &wire)
	if err != nil {
		return nil, err
	}
	if !validResponseCorrelation(wire.CorrelationID) || !correlationHeaderMatches(header, wire.CorrelationID) ||
		wire.MessageID != messageID || wire.Recipients == nil || len(wire.Recipients) > 32 {
		return nil, ErrProtocol
	}
	seen := make(map[string]struct{}, len(wire.Recipients))
	for _, status := range wire.Recipients {
		if status.MessageID != messageID || !validBoardID(status.PrincipalID) || status.StoredAt.IsZero() {
			return nil, ErrProtocol
		}
		if _, exists := seen[status.PrincipalID]; exists {
			return nil, ErrProtocol
		}
		seen[status.PrincipalID] = struct{}{}
	}
	return wire.Recipients, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, output any) (http.Header, error) {
	response, release, err := c.do(ctx, http.MethodGet, endpoint, nil, "")
	if err != nil {
		return nil, err
	}
	defer release()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, connectionResponseStatusError(response)
	}
	body, err := readBounded(response.Body, maxResponseBodyBytes)
	if err != nil {
		if err == errResponseRead {
			return nil, ErrUnavailable
		}
		return nil, ErrProtocol
	}
	if err := decodeStrictJSON(body, output); err != nil {
		return nil, ErrProtocol
	}
	return response.Header.Clone(), nil
}

func pageValues(page PageRequest) url.Values {
	values := url.Values{"limit": []string{strconv.Itoa(page.Limit)}}
	if page.Cursor != "" {
		values.Set("cursor", page.Cursor)
	}
	return values
}

func validPageRequest(page PageRequest) bool {
	return page.Limit >= 1 && page.Limit <= 100 && validPageCursor(page.Cursor)
}

func validPageCursor(cursor string) bool {
	return cursor == "" || validBoardID(cursor)
}

func validResponseCorrelation(id string) bool {
	return id != "" && len(id) <= 128 && validHeaderValue(id)
}

func validThreadPage(page ThreadPage, threadID string) bool {
	thread := page.Thread
	if thread.ID != threadID || !validBoardID(thread.Space) || thread.CreatedAt == "" || thread.UpdatedAt == "" ||
		!validPageCursor(page.NextCursor) {
		return false
	}
	for _, participant := range thread.Participants {
		if !validBoardID(participant) {
			return false
		}
	}
	seenMessages := make(map[string]struct{}, len(thread.Messages))
	for _, message := range thread.Messages {
		if !validBoardID(message.ID) || message.Seq < 1 || !validBoardID(message.By) || message.At == "" ||
			(message.ChannelID != "" && message.ChannelID != thread.Space) ||
			(message.ThreadID != "" && message.ThreadID != threadID) ||
			!validOptionalBoardID(message.ReplyTo) || !validOptionalBoardID(message.CausationID) ||
			!validBoardIDs(message.Mentions) || !validBoardIDs(message.Recipients) {
			return false
		}
		if _, exists := seenMessages[message.ID]; exists {
			return false
		}
		seenMessages[message.ID] = struct{}{}
	}
	return page.NextCursor == "" || (len(thread.Messages) > 0 && thread.Messages[len(thread.Messages)-1].ID == page.NextCursor)
}

func validInboxItem(item InboxItem) bool {
	return validBoardID(item.ItemID) && validBoardID(item.MessageID) && validBoardID(item.ChannelID) &&
		validBoardID(item.ThreadID) && validBoardID(item.SenderID) && !item.CreatedAt.IsZero() &&
		validReceipt(item.AckReceipt) && validBoardIDs(item.Mentions) && validBoardIDs(item.Recipients)
}

func validClaimResult(result ClaimResult, requestedClaimID string) bool {
	if !validBoardID(result.MessageID) || !validBoardID(result.ClaimID) {
		return false
	}
	claimIDMatchesRequest := requestedClaimID == "" || result.ClaimID == requestedClaimID
	switch result.Status {
	case "claimed", "reacquired":
		return claimIDMatchesRequest && validReceipt(result.ClaimReceipt) && result.LeaseUntil != nil && !result.LeaseUntil.IsZero() &&
			result.CompletedAt == nil && result.CanProcess
	case "already_claimed":
		return claimIDMatchesRequest && validReceipt(result.ClaimReceipt) && result.LeaseUntil != nil && !result.LeaseUntil.IsZero() &&
			result.CompletedAt == nil && !result.CanProcess
	case "already_processed":
		return result.ClaimReceipt == "" && result.LeaseUntil == nil && result.CompletedAt != nil && !result.CompletedAt.IsZero() && !result.CanProcess
	default:
		return false
	}
}

func validOptionalBoardID(value string) bool {
	return value == "" || validBoardID(value)
}

func validBoardIDs(values []string) bool {
	for _, value := range values {
		if !validBoardID(value) {
			return false
		}
	}
	return true
}

func validReceipt(value string) bool {
	return value != "" && len(value) <= 4096 && validHeaderValue(value)
}

func correlationHeaderMatches(header http.Header, correlationID string) bool {
	value := header.Get("X-Correlation-ID")
	return value == "" || value == correlationID
}
