package boardcommunication

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// ConnectionIdentity pins a BOARD client to one principal and journal instance.
type ConnectionIdentity struct {
	PrincipalID       string `json:"principal_id"`
	JournalInstanceID string `json:"journal_instance_id"`
}

// DiscoverConnection performs a one-shot, unbound connection identity probe.
func DiscoverConnection(ctx context.Context, options Options) (ConnectionIdentity, error) {
	if ctx == nil {
		return ConnectionIdentity{}, ErrInvalidRequest
	}
	if options.ExpectedConnection != (ConnectionIdentity{}) {
		return ConnectionIdentity{}, ErrInvalidConfiguration
	}
	client, err := newClient(options)
	if err != nil {
		return ConnectionIdentity{}, err
	}
	defer client.Close()
	return client.readConnection(ctx, nil)
}

// ReadConnection reads the identity of this client's pinned BOARD connection.
func (c *Client) ReadConnection(ctx context.Context) (ConnectionIdentity, error) {
	if c == nil || ctx == nil {
		return ConnectionIdentity{}, ErrInvalidRequest
	}
	c.mu.RLock()
	expected := c.expectedConnection
	c.mu.RUnlock()
	if !validConnectionIdentity(expected) {
		return ConnectionIdentity{}, ErrInvalidConfiguration
	}
	return c.readConnection(ctx, &expected)
}

type connectionResponse struct {
	PrincipalID       string `json:"principal_id"`
	JournalInstanceID string `json:"journal_instance_id"`
	CorrelationID     string `json:"correlation_id"`
}

func (c *Client) readConnection(ctx context.Context, expected *ConnectionIdentity) (ConnectionIdentity, error) {
	var wire connectionResponse
	header, err := c.getJSON(ctx, "/api/v1/connection", &wire)
	if err != nil {
		return ConnectionIdentity{}, err
	}
	identity := ConnectionIdentity{PrincipalID: wire.PrincipalID, JournalInstanceID: wire.JournalInstanceID}
	if !validConnectionIdentity(identity) || !validResponseCorrelation(wire.CorrelationID) ||
		!correlationHeaderMatches(header, wire.CorrelationID) {
		return ConnectionIdentity{}, ErrProtocol
	}
	if expected != nil && identity != *expected {
		return ConnectionIdentity{}, ErrConnectionIdentityMismatch
	}
	return identity, nil
}

func validConnectionIdentity(identity ConnectionIdentity) bool {
	principal := identity.PrincipalID
	if len(principal) < 1 || len(principal) > 64 {
		return false
	}
	for index, value := range principal {
		valid := value >= 'a' && value <= 'z' || value >= '0' && value <= '9' ||
			(index > 0 && (value == '_' || value == '-'))
		if !valid {
			return false
		}
	}
	const journalPrefix = "journal_"
	journalID := identity.JournalInstanceID
	if len(journalID) != len(journalPrefix)+32 || !strings.HasPrefix(journalID, journalPrefix) {
		return false
	}
	for _, value := range journalID[len(journalPrefix):] {
		if !((value >= '0' && value <= '9') || (value >= 'a' && value <= 'f')) {
			return false
		}
	}
	return true
}

func setConnectionIdentityHeaders(header http.Header, identity ConnectionIdentity) {
	if !validConnectionIdentity(identity) {
		return
	}
	header.Set("X-BOARD-Principal-ID", identity.PrincipalID)
	header.Set("X-BOARD-Journal-Instance-ID", identity.JournalInstanceID)
}

func connectionIdentityResponseError(status int, body []byte, header http.Header) error {
	if status != http.StatusConflict {
		return contextResponseError(status)
	}
	errorCode, valid := decodeConnectionIdentityError(body, header)
	if valid && errorCode == "connection_identity_mismatch" {
		return ErrConnectionIdentityMismatch
	}
	return contextResponseError(status)
}

func connectionResponseStatusError(response *http.Response) error {
	if response == nil || response.StatusCode != http.StatusConflict {
		if response == nil {
			return ErrProtocol
		}
		return contextResponseError(response.StatusCode)
	}
	body, err := readBounded(response.Body, maxResponseBodyBytes)
	if err != nil {
		if errors.Is(err, errResponseRead) {
			return ErrUnavailable
		}
		return ErrProtocol
	}
	return connectionIdentityResponseError(response.StatusCode, body, response.Header)
}

func decodeConnectionIdentityError(body []byte, header http.Header) (string, bool) {
	var envelope struct {
		Error         string `json:"error"`
		CorrelationID string `json:"correlation_id,omitempty"`
	}
	if decodeStrictJSON(body, &envelope) != nil || (envelope.CorrelationID != "" &&
		(!validResponseCorrelation(envelope.CorrelationID) || !correlationHeaderMatches(header, envelope.CorrelationID))) {
		return "", false
	}
	return envelope.Error, true
}
