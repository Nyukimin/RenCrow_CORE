package nativeharnessclient

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// ErrInvalidContextInput reports typed messages that cannot be projected to
// ContextBlocks (unknown or misplaced type, empty or non-text content, or an
// invalid source reference).
var ErrInvalidContextInput = errors.New("invalid context input")

// MaxContextBlocks is the most ContextBlocks one start request may carry
// (the context_blocks array limit of the Harness start input). More blocks are
// refused here, before any block is built, so no request is sent that the
// receiver would reject.
const MaxContextBlocks = 256

// ErrTooManyContextBlocks reports typed messages that would produce more than
// MaxContextBlocks blocks. It is an ErrInvalidContextInput.
var ErrTooManyContextBlocks = fmt.Errorf("%w: more than %d context blocks", ErrInvalidContextInput, MaxContextBlocks)

const (
	contextRevisionDomain = "rencrow-context-block/v1\x00"
	contextRevisionPrefix = "ctx-v1:"
)

// ByteRange and SourceRef are aliases of CORE's canonical prompt provenance
// types. Harness serialization uses the same field names and JSON shape.
type ByteRange = llm.ByteRange
type SourceRef = llm.PromptSourceRef

// ContextBlock is the public block handed to RenCrow_Harness. It has exactly
// the four public fields kind, text, revision and source. Kind reuses
// llm.PromptContextType; Materialize never produces user_message.
type ContextBlock struct {
	Kind     llm.PromptContextType `json:"kind"`
	Text     string                `json:"text"`
	Revision string                `json:"revision"`
	Source   *SourceRef            `json:"source"`
}

// SourceResolver returns the evidence reference of the block built from
// messages[index], or nil for a block without a source. It receives a copy of
// the message and is never called for the user message.
type SourceResolver func(index int, message llm.Message) (*SourceRef, error)

// PromptSourceFromMessage resolves only the typed CORE source carried beside a
// message. Metadata and text never create a source reference.
func PromptSourceFromMessage(_ int, message llm.Message) (*SourceRef, error) {
	return llm.ClonePromptSourceRef(message.PromptSource), nil
}

// sourceOrigins are the origin values a SourceRef may carry.
var sourceOrigins = map[string]bool{
	"human": true, "automation": true, "host": true, "agent": true, "tool": true, "unknown": true,
}

// blockRank is the standard input order of the four block kinds.
func blockRank(kind llm.PromptContextType) (int, bool) {
	switch kind {
	case llm.PromptContextCharacter:
		return 0, true
	case llm.PromptContextStable:
		return 1, true
	case llm.PromptContextRecall:
		return 2, true
	case llm.PromptContextVariable:
		return 3, true
	}
	return 0, false
}

// MaterializeContextRevision projects the typed messages of the CORE prompt
// assembly (Character, Stable, Recall, Variable, then exactly one user
// message last) to ContextBlocks and returns the current user text
// separately. The user message never becomes a block.
//
// Each message becomes one block in order. The block kind is the message
// Type; nothing is derived from the text, Role or Metadata, so a flattened
// prompt cannot be passed in, and Role and Metadata do not affect the
// revision. Text is used exactly as given (no trim, no normalization). The
// result is all or nothing: on error the blocks and user text are empty. More
// than MaxContextBlocks blocks are refused (ErrTooManyContextBlocks) before
// any block is built or the resolver is called.
func MaterializeContextRevision(messages []llm.Message, resolve SourceResolver) ([]ContextBlock, string, error) {
	if len(messages) == 0 {
		return nil, "", fmt.Errorf("%w: no messages", ErrInvalidContextInput)
	}
	last := len(messages) - 1
	if messages[last].Type != llm.PromptContextUser {
		return nil, "", fmt.Errorf("%w: the last message must be the user message", ErrInvalidContextInput)
	}
	if err := validateMessageText(last, messages[last]); err != nil {
		return nil, "", err
	}
	if last > MaxContextBlocks {
		return nil, "", fmt.Errorf("%w (got %d)", ErrTooManyContextBlocks, last)
	}
	blocks := make([]ContextBlock, 0, last)
	previousRank := -1
	for index, message := range messages[:last] {
		rank, known := blockRank(message.Type)
		if !known {
			return nil, "", fmt.Errorf("%w: message %d has no block type (user_message must be exactly one and last)", ErrInvalidContextInput, index)
		}
		if rank < previousRank {
			return nil, "", fmt.Errorf("%w: message %d (%s) is out of the standard order", ErrInvalidContextInput, index, message.Type)
		}
		previousRank = rank
		if err := validateMessageText(index, message); err != nil {
			return nil, "", err
		}
		source, err := resolveSource(resolve, index, message)
		if err != nil {
			return nil, "", err
		}
		blocks = append(blocks, ContextBlock{
			Kind:     message.Type,
			Text:     message.Content,
			Revision: contextRevision(message.Type, message.Content, source),
			Source:   source,
		})
	}
	return blocks, messages[last].Content, nil
}

// validateMessageText requires plain, non-empty, valid UTF-8 text content.
func validateMessageText(index int, message llm.Message) error {
	switch {
	case len(message.Parts) > 0:
		return fmt.Errorf("%w: message %d (%s) has non-text parts", ErrInvalidContextInput, index, message.Type)
	case message.Content == "":
		return fmt.Errorf("%w: message %d (%s) has empty content", ErrInvalidContextInput, index, message.Type)
	case !utf8.ValidString(message.Content):
		return fmt.Errorf("%w: message %d (%s) is not valid UTF-8", ErrInvalidContextInput, index, message.Type)
	}
	return nil
}

// resolveSource returns a validated private copy of the resolved reference.
func resolveSource(resolve SourceResolver, index int, message llm.Message) (*SourceRef, error) {
	if resolve == nil {
		return nil, nil
	}
	resolved, err := resolve(index, message)
	if err != nil {
		return nil, fmt.Errorf("resolve source of message %d: %w", index, err)
	}
	if resolved == nil {
		return nil, nil
	}
	source := *llm.ClonePromptSourceRef(resolved)
	if err := validateSource(source); err != nil {
		return nil, fmt.Errorf("%w: source of message %d: %v", ErrInvalidContextInput, index, err)
	}
	return &source, nil
}

func validateSource(source SourceRef) error {
	for _, field := range []struct{ name, value string }{
		{"owner", source.Owner}, {"source_id", source.SourceID}, {"projection_version", source.ProjectionVersion},
	} {
		if field.value == "" || !utf8.ValidString(field.value) {
			return fmt.Errorf("%s must be non-empty UTF-8", field.name)
		}
	}
	if len(source.RawHash) != sha256.Size*2 || !isLowerHex(source.RawHash) {
		return errors.New("raw_hash must be 64 lowercase hex characters")
	}
	if source.Range.End < source.Range.Start {
		return errors.New("range end is before its start")
	}
	if !sourceOrigins[source.Origin] {
		return errors.New("origin is not a known value")
	}
	return nil
}

// contextRevision implements the content-address of the 04 ContextBlock
// section: "ctx-v1:" + hex(SHA256(domain + LP(kind) + LP(text) + SOURCE)).
// It is a content address only; it implies neither order nor authentication.
func contextRevision(kind llm.PromptContextType, text string, source *SourceRef) string {
	input := make([]byte, 0, len(contextRevisionDomain)+len(kind)+len(text)+256)
	input = append(input, contextRevisionDomain...)
	input = appendLP(input, string(kind))
	input = appendLP(input, text)
	if source == nil {
		input = append(input, 0x00)
	} else {
		input = append(input, 0x01)
		input = appendLP(input, source.Owner)
		input = appendLP(input, source.SourceID)
		input = appendLP(input, source.RawHash)
		input = appendLP(input, source.ProjectionVersion)
		input = appendLP(input, strconv.FormatUint(source.Range.Start, 10))
		input = appendLP(input, strconv.FormatUint(source.Range.End, 10))
		input = appendLP(input, source.Origin)
		input = appendLP(input, strconv.FormatUint(source.Sequence, 10))
	}
	sum := sha256.Sum256(input)
	return contextRevisionPrefix + hex.EncodeToString(sum[:])
}
