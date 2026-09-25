package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ContentHashPrefix is the algorithm tag of the canonical Artifact content
// digest. It is the sha256-prefixed lowercase SHA-256 form that CORE content
// hashes already use, and content_hash stays a content field: IDENTITY_CANONICAL
// 2.3 forbids renaming it into an ID or an EventID.
const ContentHashPrefix = "sha256:"

// ContentHashOf returns the canonical content digest of the exact bytes that are
// persisted as an artifact content. Nothing is trimmed, normalized or re-encoded,
// so a non-ASCII content and a content with trailing whitespace get distinct
// digests and any later byte change is detectable.
func ContentHashOf(content []byte) string {
	sum := sha256.Sum256(content)
	return ContentHashPrefix + hex.EncodeToString(sum[:])
}

// ValidateContentHash checks the canonical form only: the prefix plus one
// lowercase 64-character SHA-256 hex digest. A caller that also holds the content
// must compare it with ContentHashOf, because a well-formed digest of other bytes
// still cannot be trusted as the content identity.
func ValidateContentHash(value string, field string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	digest, ok := strings.CutPrefix(value, ContentHashPrefix)
	if err := validateContentDigest(digest); err != nil || !ok {
		return fmt.Errorf("%s must be a %sprefixed lowercase SHA-256 digest, got %q", field, ContentHashPrefix, value)
	}
	return nil
}

func validateContentDigest(digest string) error {
	if len(digest) != hex.EncodedLen(sha256.Size) {
		return fmt.Errorf("content digest must be %d hex characters, got %d", hex.EncodedLen(sha256.Size), len(digest))
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil {
		return fmt.Errorf("content digest must be lowercase hex: %w", err)
	}
	if hex.EncodeToString(decoded) != digest {
		return fmt.Errorf("content digest must be lowercase hex")
	}
	return nil
}

// ValidateArtifactSupersession checks the shape and direction of one supersession
// edge: the superseded artifact points at the artifact that replaces it. The field
// is optional, an empty value means the row is not superseded. A replacement must
// be a canonical ArtifactID (the shared art_ prefix plus UUIDv5 or UUIDv7) and it
// is never the artifact itself. Whether that artifact exists, shares the owner and
// forms no cycle spans several rows, so it is closed by the supersede operation
// and its route evidence, not by this single-row check.
func ValidateArtifactSupersession(artifactID ArtifactID, supersededBy ArtifactID) error {
	if err := artifactID.Validate(); err != nil {
		return fmt.Errorf("artifact_id is invalid: %w", err)
	}
	if supersededBy == "" {
		return nil
	}
	if err := supersededBy.Validate(); err != nil {
		return fmt.Errorf("superseded_by is invalid: %w", err)
	}
	if supersededBy == artifactID {
		return fmt.Errorf("superseded_by %q must not reference the artifact itself", string(supersededBy))
	}
	return nil
}
