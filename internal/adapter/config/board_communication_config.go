package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// BoardCommunicationConfig holds the opt-in private BOARD connection scope.
// CutoverAt is kept as its configured UTC RFC3339 literal; CORE's durable
// communication owner is responsible for pinning the initial cutover.
type BoardCommunicationConfig struct {
	Enabled     bool                                `yaml:"enabled"`
	URL         string                              `yaml:"url"`
	UnixSocket  string                              `yaml:"unix_socket"`
	TokenFile   string                              `yaml:"token_file"`
	PrincipalID string                              `yaml:"principal_id"`
	ChannelIDs  []string                            `yaml:"channel_ids"`
	PeerIDs     []string                            `yaml:"peer_ids"`
	CutoverAt   string                              `yaml:"cutover_at"`
	Responders  []BoardCommunicationResponderConfig `yaml:"responders"`
}

// BoardCommunicationResponderConfig defines one explicitly scoped BOARD CHAT
// connection. ActorID is also the principal ID authenticated by the responder's
// token and pinned by the private connection.
type BoardCommunicationResponderConfig struct {
	ActorID    string   `yaml:"actor_id"`
	TokenFile  string   `yaml:"token_file"`
	ChannelIDs []string `yaml:"channel_ids"`
	PeerIDs    []string `yaml:"peer_ids"`
	CutoverAt  string   `yaml:"cutover_at"`
}

var (
	errBoardCommunicationTokenUnavailable = errors.New("board_communication.token_file is unavailable or unreadable")
	errBoardCommunicationTokenInvalid     = errors.New("board_communication.token_file must contain a valid nonempty bearer token within the size limit")
)

func (c *Config) validateBoardCommunicationConfig() error {
	board := c.BoardCommunication
	if !board.Enabled {
		if len(board.Responders) != 0 {
			return errors.New("board_communication.responders requires enabled=true")
		}
		return nil
	}

	urlConfigured := board.URL != ""
	socketConfigured := board.UnixSocket != ""
	if urlConfigured == socketConfigured {
		return errors.New("board_communication must configure exactly one of url or unix_socket")
	}
	if urlConfigured {
		if err := validateBoardCommunicationURL(board.URL); err != nil {
			return err
		}
	} else if !validBoardCommunicationPath(board.UnixSocket) {
		return errors.New("board_communication.unix_socket must be an absolute clean path")
	}
	if !validBoardCommunicationPath(board.TokenFile) {
		return errors.New("board_communication.token_file must be an absolute clean path")
	}
	if !validBoardCommunicationID(board.PrincipalID) {
		return errors.New("board_communication.principal_id must be a canonical BOARD id")
	}
	if err := validateBoardCommunicationIDAllowlist("channel_ids", board.ChannelIDs); err != nil {
		return err
	}
	if err := validateBoardCommunicationIDAllowlist("peer_ids", board.PeerIDs); err != nil {
		return err
	}
	for _, peerID := range board.PeerIDs {
		if peerID == board.PrincipalID {
			return errors.New("board_communication.peer_ids must not contain principal_id")
		}
	}
	if err := validateBoardCommunicationCutover(board.CutoverAt); err != nil {
		return err
	}
	if err := validateBoardCommunicationResponders(board); err != nil {
		return err
	}
	return nil
}

func validateBoardCommunicationResponders(board BoardCommunicationConfig) error {
	seenActors := make(map[string]struct{}, len(board.Responders))
	seenTokenFiles := make(map[string]struct{}, len(board.Responders))
	for _, responder := range board.Responders {
		if !domainconversation.IsExternalPeerResponderActor(responder.ActorID) {
			return errors.New("board_communication.responders.actor_id must name a supported external peer responder")
		}
		if _, exists := seenActors[responder.ActorID]; exists {
			return errors.New("board_communication.responders must not contain duplicate actor_id values")
		}
		seenActors[responder.ActorID] = struct{}{}
		if responder.ActorID == board.PrincipalID {
			return errors.New("board_communication.responders.actor_id must not reuse the legacy principal_id")
		}
		if !validBoardCommunicationPath(responder.TokenFile) || responder.TokenFile == board.TokenFile {
			return errors.New("board_communication.responders.token_file must be an absolute clean path distinct from the legacy token_file")
		}
		if _, exists := seenTokenFiles[responder.TokenFile]; exists {
			return errors.New("board_communication.responders must not reuse token_file paths")
		}
		seenTokenFiles[responder.TokenFile] = struct{}{}
		if err := validateBoardCommunicationIDAllowlist("responders."+responder.ActorID+".channel_ids", responder.ChannelIDs); err != nil {
			return err
		}
		if err := validateBoardCommunicationIDAllowlist("responders."+responder.ActorID+".peer_ids", responder.PeerIDs); err != nil {
			return err
		}
		for _, peerID := range responder.PeerIDs {
			if peerID == responder.ActorID {
				return fmt.Errorf("board_communication.responders.%s.peer_ids must not contain its actor_id", responder.ActorID)
			}
		}
		if err := validateBoardCommunicationCutover(responder.CutoverAt); err != nil {
			return fmt.Errorf("board_communication.responders.%s.cutover_at: %w", responder.ActorID, err)
		}
	}
	return nil
}

func validateBoardCommunicationURL(raw string) error {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return errors.New("board_communication.url must be an absolute http(s) root URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return errors.New("board_communication.url must be an absolute http(s) root URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("board_communication.url must be an absolute http(s) root URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.RawFragment != "" || strings.Contains(raw, "#") {
		return errors.New("board_communication.url must not contain userinfo, query, or fragment")
	}
	if (parsed.Path != "" && parsed.Path != "/") || (parsed.RawPath != "" && parsed.RawPath != "/") {
		return errors.New("board_communication.url must target the BOARD root")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return errors.New("board_communication.url has an invalid port")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return errors.New("board_communication.url has an invalid port")
		}
	}
	if scheme == "http" && !isLoopbackStorageHostHost(parsed.Hostname()) {
		return errors.New("board_communication.url permits http only for loopback hosts")
	}
	return nil
}

func validBoardCommunicationPath(path string) bool {
	return path != "" && strings.TrimSpace(path) == path && !strings.ContainsRune(path, '\x00') &&
		filepath.IsAbs(path) && filepath.Clean(path) == path
}

func validateBoardCommunicationIDAllowlist(field string, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("board_communication.%s must contain at least one BOARD id", field)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !validBoardCommunicationID(id) {
			return fmt.Errorf("board_communication.%s contains a noncanonical BOARD id", field)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("board_communication.%s must not contain duplicate BOARD ids", field)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// validBoardCommunicationID mirrors the fixed BOARD canonical-ID grammar
// used by the wire adapter without making config validation depend on it.
func validBoardCommunicationID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	first := value[0]
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func validateBoardCommunicationCutover(value string) error {
	if value == "" || strings.TrimSpace(value) != value || !strings.HasSuffix(value, "Z") {
		return errors.New("board_communication.cutover_at must be a nonzero UTC RFC3339 timestamp ending in Z")
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.IsZero() {
		return errors.New("board_communication.cutover_at must be a nonzero UTC RFC3339 timestamp ending in Z")
	}
	return nil
}

// ReadBoardCommunicationBearerToken is the token-source API for the BOARD
// wire adapter. It reads only from the handle whose native confidentiality was
// checked and never returns filesystem paths or token bytes in diagnostics.
func ReadBoardCommunicationBearerToken(path string) ([]byte, error) {
	if !validBoardCommunicationPath(path) {
		return nil, errors.New("board_communication.token_file must be an absolute clean path")
	}
	handle, err := openConfidentialFile("board_communication.token_file", path, errBoardCommunicationTokenUnavailable)
	if err != nil {
		// openConfidentialFile may return a platform permission error containing
		// the path. Keep this API's diagnostics fixed and path-free.
		return nil, errBoardCommunicationTokenUnavailable
	}
	defer handle.Close()

	contents, err := io.ReadAll(io.LimitReader(handle, maxStorageHostBearerTokenBytes+1))
	if err != nil || len(contents) > maxStorageHostBearerTokenBytes {
		return nil, errBoardCommunicationTokenInvalid
	}
	if bytes.HasSuffix(contents, []byte("\r\n")) {
		contents = contents[:len(contents)-2]
	} else if bytes.HasSuffix(contents, []byte("\n")) {
		contents = contents[:len(contents)-1]
	}
	if len(contents) == 0 || !validStorageHostBearerToken(contents) {
		return nil, errBoardCommunicationTokenInvalid
	}
	return contents, nil
}
