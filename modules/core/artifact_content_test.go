package core

import (
	"strings"
	"testing"
)

const (
	testSHA256OfEmpty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	testSHA256OfABC   = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
)

// TestContentHashOfKnownVectors pins the content digest to the published SHA-256
// test vectors, so the CORE content hash is the plain digest of the persisted
// bytes and not a trimmed, normalized or JSON-wrapped variant of them.
func TestContentHashOfKnownVectors(t *testing.T) {
	if got := ContentHashOf([]byte("abc")); got != ContentHashPrefix+testSHA256OfABC {
		t.Errorf("ContentHashOf(\"abc\") = %q, want %q", got, ContentHashPrefix+testSHA256OfABC)
	}
	if got := ContentHashOf(nil); got != ContentHashPrefix+testSHA256OfEmpty {
		t.Errorf("ContentHashOf(nil) = %q, want %q", got, ContentHashPrefix+testSHA256OfEmpty)
	}
	if got := ContentHashOf([]byte("abc")); got != ContentHashOf([]byte("abc")) {
		t.Errorf("ContentHashOf is not deterministic, got %q and %q", got, ContentHashOf([]byte("abc")))
	}
	// Byte-exact coverage: non-ASCII and trailing whitespace change the digest.
	ascii := ContentHashOf([]byte("report"))
	for _, other := range []string{"report\n", " report", "レポート"} {
		if got := ContentHashOf([]byte(other)); got == ascii {
			t.Errorf("ContentHashOf(%q) = %q, want a digest distinct from the %q digest", other, got, "report")
		}
	}
}

// TestValidateContentHash pins the canonical content-hash form: the sha256 prefix
// plus one lowercase 64-character hex digest. A bare hex digest, an uppercase
// digest and another algorithm tag are not interchangeable content identities.
func TestValidateContentHash(t *testing.T) {
	valid := []string{
		ContentHashPrefix + testSHA256OfABC,
		ContentHashPrefix + strings.Repeat("0", 64),
		ContentHashPrefix + strings.Repeat("abcdef0123456789", 4),
	}
	for _, value := range valid {
		if err := ValidateContentHash(value, "content_hash"); err != nil {
			t.Errorf("ValidateContentHash(%q) = %v, want acceptance", value, err)
		}
	}
	invalid := []struct {
		value string
		want  string
	}{
		{"", "content_hash is required"},
		{testSHA256OfABC, "content_hash must be"},
		{"sha256:" + strings.ToUpper(testSHA256OfABC), "content_hash must be"},
		{"sha256:" + strings.Repeat("a", 63), "content_hash must be"},
		{"sha256:" + strings.Repeat("a", 65), "content_hash must be"},
		{"sha256:" + strings.Repeat("g", 64), "content_hash must be"},
		{"sha512:" + testSHA256OfABC, "content_hash must be"},
		{"sha256:" + testSHA256OfABC + " ", "content_hash must be"},
		{" sha256:" + testSHA256OfABC, "content_hash must be"},
	}
	for _, tt := range invalid {
		err := ValidateContentHash(tt.value, "content_hash")
		if err == nil {
			t.Errorf("ValidateContentHash(%q) = nil, want rejection with %q", tt.value, tt.want)
			continue
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ValidateContentHash(%q) error = %v, want reason %q", tt.value, err, tt.want)
		}
	}
}

// TestValidateArtifactSupersession pins the shared supersession contract: the
// field is optional, it points forward to a canonical ArtifactID, and it never
// references the artifact itself.
func TestValidateArtifactSupersession(t *testing.T) {
	artifactID := ArtifactID("art_00000000-0000-5000-8000-00000000000a")
	if err := ValidateArtifactSupersession(artifactID, ""); err != nil {
		t.Errorf("ValidateArtifactSupersession(unsuperseded) = %v, want acceptance of an empty superseded_by", err)
	}
	for _, replacement := range []string{
		"art_00000000-0000-5000-8000-00000000000b", // UUIDv5: NewMigrationID shape
		"art_018db8d4-8a2a-7a3e-9a1c-2b5f0d1e4c20", // UUIDv7: NewArtifactID shape
	} {
		if err := ValidateArtifactSupersession(artifactID, ArtifactID(replacement)); err != nil {
			t.Errorf("ValidateArtifactSupersession(%q) = %v, want acceptance", replacement, err)
		}
	}
	invalid := []struct {
		replacement string
		want        string
	}{
		{"art_00000000-0000-5000-8000-00000000000a", "must not reference the artifact itself"},
		{"art_1", "superseded_by is invalid"},
		{"art_00000000-0000-1000-8000-00000000000b", "superseded_by is invalid"}, // UUIDv1
		{"art_00000000-0000-4000-8000-00000000000b", "superseded_by is invalid"}, // UUIDv4
		{"00000000-0000-5000-8000-00000000000b", "superseded_by is invalid"},     // missing prefix
		{"artifact_00000000-0000-5000-8000-00000000000b", "superseded_by is invalid"},
	}
	for _, tt := range invalid {
		err := ValidateArtifactSupersession(artifactID, ArtifactID(tt.replacement))
		if err == nil {
			t.Errorf("ValidateArtifactSupersession(%q) = nil, want rejection with %q", tt.replacement, tt.want)
			continue
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ValidateArtifactSupersession(%q) error = %v, want reason %q", tt.replacement, err, tt.want)
		}
	}
	if err := ValidateArtifactSupersession(ArtifactID("art_1"), ArtifactID("art_00000000-0000-5000-8000-00000000000b")); err == nil {
		t.Error("ValidateArtifactSupersession() accepted a non-canonical artifact_id, want rejection")
	}
}
