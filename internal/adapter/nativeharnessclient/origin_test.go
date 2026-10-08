package nativeharnessclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type fixedClock struct {
	now   time.Time
	mu    sync.Mutex
	calls int
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.now
}

type fixedNonce struct {
	value string
	err   error
	mu    sync.Mutex
	calls int
}

func (n *fixedNonce) NewNonce() (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	return n.value, n.err
}

type originVector struct {
	Scope          string      `json:"scope"`
	KeyHex         string      `json:"key_hex"`
	Text           string      `json:"text"`
	Proof          OriginProof `json:"proof"`
	MACInputSHA256 string      `json:"mac_input_sha256"`
}

func loadOriginVector(t *testing.T) originVector {
	t.Helper()
	var vector originVector
	readJSON(t, "origin_proof_vector.json", &vector)
	return vector
}

// fixtureKey parses the published synthetic key with the pure ParseKey. The
// deployment loader rejects this key on purpose, so it is not used here.
func fixtureKey(t *testing.T) Key {
	t.Helper()
	raw, err := os.ReadFile(testdataPath(t, "synthetic_origin_key.hex"))
	if err != nil {
		t.Fatalf("read fixture key: %v", err)
	}
	key, err := ParseKey(raw)
	if err != nil {
		t.Fatalf("parse fixture key: %v", err)
	}
	return key
}

func fixtureSettings() IssuerSettings {
	return IssuerSettings{Issuer: "core:fixture", KeyID: "fixture-key-1", Audience: "core:local", TTLSeconds: 300}
}

func newFixtureSigner(t *testing.T, clock Clock, nonces NonceSource) *OriginSigner {
	t.Helper()
	signer, err := NewOriginSigner(fixtureSettings(), fixtureKey(t), clock, nonces)
	if err != nil {
		t.Fatalf("NewOriginSigner: %v", err)
	}
	return signer
}

func vectorRequest(vector originVector) OriginProofRequest {
	return OriginProofRequest{
		Text: vector.Text,
		Accepted: AcceptedInput{
			MessageID: modulecore.MessageID(vector.Proof.SourceMessageID),
			ThreadID:  modulecore.ThreadID(vector.Proof.SourceThreadID),
			Sequence:  vector.Proof.Sequence,
			Raw:       []byte(vector.Text),
		},
		DestinationThreadID: vector.Proof.DestinationThreadID,
		MutationKey:         vector.Proof.MutationKey,
	}
}

func vectorClock(t *testing.T) *fixedClock {
	t.Helper()
	issued, err := time.Parse(time.RFC3339, "2026-10-07T00:00:00Z")
	if err != nil {
		t.Fatalf("parse issued_at: %v", err)
	}
	return &fixedClock{now: issued}
}

func TestSignOriginProofReproducesDesignVector(t *testing.T) {
	vector := loadOriginVector(t)
	if vector.KeyHex != fixtureKeyHex {
		t.Fatalf("vector key_hex differs from the fixture key constant")
	}
	signer := newFixtureSigner(t, vectorClock(t), &fixedNonce{value: vector.Proof.Nonce})

	proof, err := signer.Sign(vectorRequest(vector))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !reflect.DeepEqual(proof, vector.Proof) {
		t.Fatalf("proof differs from vector:\n got %+v\nwant %+v", proof, vector.Proof)
	}
	if got := sha256Hex(originMACInput(proof)); got != vector.MACInputSHA256 {
		t.Fatalf("MAC input sha256 = %s, want %s", got, vector.MACInputSHA256)
	}
	if got := sha256Hex([]byte(vector.Text)); got != vector.Proof.RawHash {
		t.Fatalf("raw_hash must be the SHA-256 of the text UTF-8 bytes: %s vs %s", got, vector.Proof.RawHash)
	}
}

func TestOriginProofJSONKeepsSpecFieldOrderAndNumericSequence(t *testing.T) {
	vector := loadOriginVector(t)
	raw, err := json.Marshal(vector.Proof)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	order := []string{
		"issuer", "key_id", "audience", "origin", "source_message_id", "source_thread_id",
		"destination_thread_id", "mutation_key", "raw_hash", "sequence",
		"issued_at", "expires_at", "nonce", "mac",
	}
	last := -1
	for _, name := range order {
		position := strings.Index(string(raw), `"`+name+`":`)
		if position < 0 || position <= last {
			t.Fatalf("field %q is missing or out of order in %s", name, raw)
		}
		last = position
	}
	if !strings.Contains(string(raw), `"sequence":1,`) {
		t.Fatalf("sequence must be a JSON number: %s", raw)
	}
	var back OriginProof
	if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back, vector.Proof) {
		t.Fatalf("proof is not a storable value type: %v", err)
	}
}

func TestSignOriginProofMatchesIndependentGoldenWithEdgeValues(t *testing.T) {
	const (
		wantRawHash = "2037894c6e6e9ee95d5bbae8e359998b6a6bbe101633dbf1c9cf0153cfb86858"
		wantMAC     = "4a9f74359f934acfc597b12a54d767110d633e9bbf55eebad72ed0ed425e74a4"
		wantMACSeq0 = "e18188f3b2f5ebf7ef83bb543fb4a0173f6dd557e33c8f22cc133e53f081a413"
	)
	jst := time.FixedZone("JST", 9*60*60)
	// 2027-01-01 08:59:59.987654321 JST is 2026-12-31T23:59:59Z once truncated to seconds.
	clock := &fixedClock{now: time.Date(2027, 1, 1, 8, 59, 59, 987654321, jst)}
	settings := IssuerSettings{Issuer: "core:prod-test", KeyID: "k2", Audience: "harness:local", TTLSeconds: 1}
	signer, err := NewOriginSigner(settings, fixtureKey(t), clock, &fixedNonce{value: "n-éx"})
	if err != nil {
		t.Fatalf("NewOriginSigner: %v", err)
	}
	text := "日本語\nsecond line "
	request := OriginProofRequest{
		Text: text,
		Accepted: AcceptedInput{
			MessageID: "msg_00000000-0000-7000-8000-000000000102",
			ThreadID:  "thr_00000000-0000-7000-8000-000000000102",
			Sequence:  1<<64 - 1,
			Raw:       []byte(text),
		},
		DestinationThreadID: "thr_00000000-0000-7000-8000-000000000002",
		MutationKey:         "変更.キー/1",
	}
	proof, err := signer.Sign(request)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if proof.RawHash != wantRawHash || proof.MAC != wantMAC {
		t.Fatalf("raw_hash/mac = %s/%s, want %s/%s", proof.RawHash, proof.MAC, wantRawHash, wantMAC)
	}
	if proof.IssuedAt != "2026-12-31T23:59:59Z" || proof.ExpiresAt != "2027-01-01T00:00:00Z" {
		t.Fatalf("times = %s .. %s, want UTC second precision with ttl 1", proof.IssuedAt, proof.ExpiresAt)
	}
	if proof.Sequence != 1<<64-1 {
		t.Fatalf("sequence = %d", proof.Sequence)
	}

	request.Accepted.Sequence = 0
	proof, err = signer.Sign(request)
	if err != nil {
		t.Fatalf("Sign(sequence 0): %v", err)
	}
	if proof.MAC != wantMACSeq0 {
		t.Fatalf("mac for sequence 0 = %s, want %s", proof.MAC, wantMACSeq0)
	}
}

// macInputByHand builds the MAC input without calling production helpers.
func macInputByHand(proof OriginProof) []byte {
	out := []byte("rencrow-origin-proof/v1\x00")
	for _, value := range []string{
		proof.Issuer, proof.KeyID, proof.Audience, proof.Origin, proof.SourceMessageID,
		proof.SourceThreadID, proof.DestinationThreadID, proof.MutationKey, proof.RawHash,
		strconv.FormatUint(proof.Sequence, 10), proof.IssuedAt, proof.ExpiresAt, proof.Nonce,
	} {
		length := uint64(len(value))
		for shift := 56; shift >= 0; shift -= 8 {
			out = append(out, byte(length>>uint(shift)))
		}
		out = append(out, value...)
	}
	return out
}

func TestMACInputBindsEveryFieldInOrderWithLengthPrefixes(t *testing.T) {
	vector := loadOriginVector(t)
	base := vector.Proof
	if got, want := originMACInput(base), macInputByHand(base); string(got) != string(want) {
		t.Fatalf("MAC input differs from the by-hand construction")
	}
	key := fixtureKey(t)
	mac := func(proof OriginProof) string {
		h := hmac.New(sha256.New, key.bytes[:])
		h.Write(originMACInput(proof))
		return hex.EncodeToString(h.Sum(nil))
	}
	if mac(base) != base.MAC {
		t.Fatalf("recomputed HMAC differs from the vector mac")
	}
	if strings.Contains(string(originMACInput(base)), base.MAC) {
		t.Fatalf("the mac must not be part of its own input")
	}

	mutations := map[string]func(*OriginProof){
		"issuer":            func(p *OriginProof) { p.Issuer += "x" },
		"key_id":            func(p *OriginProof) { p.KeyID += "x" },
		"audience":          func(p *OriginProof) { p.Audience += "x" },
		"origin":            func(p *OriginProof) { p.Origin = "automation" },
		"source_message_id": func(p *OriginProof) { p.SourceMessageID = p.SourceMessageID[:len(p.SourceMessageID)-1] + "2" },
		"source_thread_id":  func(p *OriginProof) { p.SourceThreadID = p.SourceThreadID[:len(p.SourceThreadID)-1] + "2" },
		"destination_thread_id": func(p *OriginProof) {
			p.DestinationThreadID = p.DestinationThreadID[:len(p.DestinationThreadID)-1] + "2"
		},
		"mutation_key": func(p *OriginProof) { p.MutationKey += "x" },
		"raw_hash":     func(p *OriginProof) { p.RawHash = strings.Repeat("0", 64) },
		"sequence":     func(p *OriginProof) { p.Sequence++ },
		"issued_at":    func(p *OriginProof) { p.IssuedAt = "2026-10-07T00:00:01Z" },
		"expires_at":   func(p *OriginProof) { p.ExpiresAt = "2026-10-07T00:05:01Z" },
		"nonce":        func(p *OriginProof) { p.Nonce += "x" },
	}
	if len(mutations) != 13 {
		t.Fatalf("expected the 13 MAC-bound fields, have %d", len(mutations))
	}
	for field, mutate := range mutations {
		mutated := base
		mutate(&mutated)
		if mac(mutated) == mac(base) {
			t.Fatalf("changing %s does not change the MAC", field)
		}
	}

	// Length prefixes keep field boundaries unambiguous.
	a, b := base, base
	a.Issuer, a.KeyID = "ab", "c"
	b.Issuer, b.KeyID = "a", "bc"
	if mac(a) == mac(b) {
		t.Fatalf("field boundary shift must change the MAC")
	}
}

func TestSignOriginProofRefusesWhenTextIsNotTheAcceptedRawInput(t *testing.T) {
	vector := loadOriginVector(t)
	cases := map[string]func(*OriginProofRequest){
		"summary of the input":          func(r *OriginProofRequest) { r.Text = "テスト失敗を修正する" },
		"trailing newline added":        func(r *OriginProofRequest) { r.Text += "\n" },
		"leading space added":           func(r *OriginProofRequest) { r.Text = " " + r.Text },
		"normalization changed":         func(r *OriginProofRequest) { r.Text = "Café"; r.Accepted.Raw = []byte("Café") },
		"raw record is a different one": func(r *OriginProofRequest) { r.Accepted.Raw = []byte("別の原文") },
		"raw record is empty":           func(r *OriginProofRequest) { r.Accepted.Raw = nil },
		"raw record is a prefix":        func(r *OriginProofRequest) { r.Accepted.Raw = r.Accepted.Raw[:len(r.Accepted.Raw)-3] },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			clock, nonce := vectorClock(t), &fixedNonce{value: "n"}
			signer := newFixtureSigner(t, clock, nonce)
			request := vectorRequest(vector)
			mutate(&request)
			proof, err := signer.Sign(request)
			if !errors.Is(err, ErrOriginRecordMismatch) {
				t.Fatalf("error = %v, want ErrOriginRecordMismatch", err)
			}
			if !reflect.DeepEqual(proof, OriginProof{}) {
				t.Fatalf("a refused request must return the zero proof, got %+v", proof)
			}
			if clock.calls != 0 || nonce.calls != 0 {
				t.Fatalf("nothing may be issued before the record check (clock=%d nonce=%d)", clock.calls, nonce.calls)
			}
		})
	}
}

func TestSignOriginProofRejectsInvalidRequests(t *testing.T) {
	vector := loadOriginVector(t)
	cases := map[string]func(*OriginProofRequest){
		"empty text":        func(r *OriginProofRequest) { r.Text = ""; r.Accepted.Raw = nil },
		"invalid utf8 text": func(r *OriginProofRequest) { r.Text = "bad\xff"; r.Accepted.Raw = []byte("bad\xff") },
		"source message id prefix": func(r *OriginProofRequest) {
			r.Accepted.MessageID = modulecore.MessageID("thr_00000000-0000-7000-8000-000000000101")
		},
		"source message id not uuid": func(r *OriginProofRequest) { r.Accepted.MessageID = "msg_not-a-uuid" },
		"source message id empty":    func(r *OriginProofRequest) { r.Accepted.MessageID = "" },
		"source message id uuid v4":  func(r *OriginProofRequest) { r.Accepted.MessageID = "msg_9b2f6a3e-5c1d-4e8a-8f0b-3a7d1c2e4b5f" },
		"source thread id prefix": func(r *OriginProofRequest) {
			r.Accepted.ThreadID = modulecore.ThreadID("msg_00000000-0000-7000-8000-000000000101")
		},
		"source thread id empty":       func(r *OriginProofRequest) { r.Accepted.ThreadID = "" },
		"destination thread id prefix": func(r *OriginProofRequest) { r.DestinationThreadID = "msg_00000000-0000-7000-8000-000000000001" },
		"destination thread id empty":  func(r *OriginProofRequest) { r.DestinationThreadID = "" },
		"destination thread id junk":   func(r *OriginProofRequest) { r.DestinationThreadID = "thr_00000000-0000-7000-8000-000000000001\n" },
		"mutation key empty":           func(r *OriginProofRequest) { r.MutationKey = "" },
		"mutation key control char":    func(r *OriginProofRequest) { r.MutationKey = "a\nb" },
		"mutation key invalid utf8":    func(r *OriginProofRequest) { r.MutationKey = "a\xffb" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			clock, nonce := vectorClock(t), &fixedNonce{value: "n"}
			signer := newFixtureSigner(t, clock, nonce)
			request := vectorRequest(vector)
			mutate(&request)
			proof, err := signer.Sign(request)
			if !errors.Is(err, ErrInvalidOriginRequest) {
				t.Fatalf("error = %v, want ErrInvalidOriginRequest", err)
			}
			if !reflect.DeepEqual(proof, OriginProof{}) {
				t.Fatalf("a rejected request must return the zero proof")
			}
			if clock.calls != 0 || nonce.calls != 0 {
				t.Fatalf("nothing may be issued for an invalid request (clock=%d nonce=%d)", clock.calls, nonce.calls)
			}
		})
	}
}

func TestSignOriginProofDoesNotDependOnRawAfterTheCheck(t *testing.T) {
	vector := loadOriginVector(t)
	signer := newFixtureSigner(t, vectorClock(t), &fixedNonce{value: vector.Proof.Nonce})
	request := vectorRequest(vector)
	proof, err := signer.Sign(request)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	for i := range request.Accepted.Raw {
		request.Accepted.Raw[i] = 'x'
	}
	if proof.RawHash != vector.Proof.RawHash || proof.MAC != vector.Proof.MAC {
		t.Fatalf("a returned proof must be immune to later mutation of the raw record")
	}
}

func TestNewOriginSignerValidatesSettings(t *testing.T) {
	good := fixtureSettings()
	key := fixtureKey(t)
	clock, nonce := vectorClock(t), &fixedNonce{value: "n"}
	for _, ttl := range []int{1, 60, 299, 300} {
		settings := good
		settings.TTLSeconds = ttl
		if _, err := NewOriginSigner(settings, key, clock, nonce); err != nil {
			t.Fatalf("ttl %d rejected: %v", ttl, err)
		}
	}
	mutations := map[string]func(*IssuerSettings){
		"ttl zero":            func(s *IssuerSettings) { s.TTLSeconds = 0 },
		"ttl negative":        func(s *IssuerSettings) { s.TTLSeconds = -1 },
		"ttl 301":             func(s *IssuerSettings) { s.TTLSeconds = 301 },
		"ttl large":           func(s *IssuerSettings) { s.TTLSeconds = 3600 },
		"issuer empty":        func(s *IssuerSettings) { s.Issuer = "" },
		"issuer control":      func(s *IssuerSettings) { s.Issuer = "core:a\nb" },
		"key id empty":        func(s *IssuerSettings) { s.KeyID = "" },
		"key id control":      func(s *IssuerSettings) { s.KeyID = "k\x00" },
		"key id invalid utf8": func(s *IssuerSettings) { s.KeyID = "k\xff" },
		"audience empty":      func(s *IssuerSettings) { s.Audience = "" },
		"audience no colon":   func(s *IssuerSettings) { s.Audience = "corelocal" },
		"audience uppercase":  func(s *IssuerSettings) { s.Audience = "Core:local" },
		"audience bad tail":   func(s *IssuerSettings) { s.Audience = "core:-local" },
		"audience too long":   func(s *IssuerSettings) { s.Audience = "core:" + strings.Repeat("a", 65) },
		"audience space":      func(s *IssuerSettings) { s.Audience = "core: local" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			settings := good
			mutate(&settings)
			signer, err := NewOriginSigner(settings, key, clock, nonce)
			if !errors.Is(err, ErrInvalidIssuerSettings) {
				t.Fatalf("error = %v, want ErrInvalidIssuerSettings", err)
			}
			if signer != nil {
				t.Fatalf("a rejected configuration must not produce a signer")
			}
		})
	}
	t.Run("zero key", func(t *testing.T) {
		if _, err := NewOriginSigner(good, Key{}, clock, nonce); !errors.Is(err, ErrInvalidIssuerSettings) {
			t.Fatalf("error = %v, want ErrInvalidIssuerSettings", err)
		}
	})
	t.Run("nil clock", func(t *testing.T) {
		if _, err := NewOriginSigner(good, key, nil, nonce); !errors.Is(err, ErrInvalidIssuerSettings) {
			t.Fatalf("error = %v, want ErrInvalidIssuerSettings", err)
		}
	})
	t.Run("nil nonce source", func(t *testing.T) {
		if _, err := NewOriginSigner(good, key, clock, nil); !errors.Is(err, ErrInvalidIssuerSettings) {
			t.Fatalf("error = %v, want ErrInvalidIssuerSettings", err)
		}
	})
}

func TestSignOriginProofNonceAndClockFailuresProduceNoProof(t *testing.T) {
	vector := loadOriginVector(t)
	boom := errors.New("entropy source failed")
	t.Run("nonce source error", func(t *testing.T) {
		signer := newFixtureSigner(t, vectorClock(t), &fixedNonce{err: boom})
		proof, err := signer.Sign(vectorRequest(vector))
		if !errors.Is(err, boom) {
			t.Fatalf("error = %v, want the nonce error", err)
		}
		if !reflect.DeepEqual(proof, OriginProof{}) {
			t.Fatalf("no proof may be produced without a nonce")
		}
	})
	for name, nonce := range map[string]string{
		"empty nonce":        "",
		"control in nonce":   "a\nb",
		"overlong nonce":     strings.Repeat("a", 129),
		"invalid utf8 nonce": "a\xff",
	} {
		t.Run(name, func(t *testing.T) {
			signer := newFixtureSigner(t, vectorClock(t), &fixedNonce{value: nonce})
			if _, err := signer.Sign(vectorRequest(vector)); !errors.Is(err, ErrInvalidOriginRequest) {
				t.Fatalf("error = %v, want ErrInvalidOriginRequest", err)
			}
		})
	}
	t.Run("zero time", func(t *testing.T) {
		signer := newFixtureSigner(t, &fixedClock{}, &fixedNonce{value: "n"})
		if _, err := signer.Sign(vectorRequest(vector)); !errors.Is(err, ErrInvalidOriginRequest) {
			t.Fatalf("error = %v, want ErrInvalidOriginRequest", err)
		}
	})
}

func TestSignOriginProofTimesAreUTCSecondPrecision(t *testing.T) {
	vector := loadOriginVector(t)
	pattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	for _, now := range []time.Time{
		time.Date(2026, 10, 7, 0, 0, 0, 999999999, time.UTC),
		time.Date(2026, 10, 7, 9, 0, 0, 1, time.FixedZone("JST", 9*3600)),
		time.Date(2026, 10, 6, 19, 0, 0, 0, time.FixedZone("EST", -5*3600)),
	} {
		signer := newFixtureSigner(t, &fixedClock{now: now}, &fixedNonce{value: "n"})
		proof, err := signer.Sign(vectorRequest(vector))
		if err != nil {
			t.Fatalf("Sign(%v): %v", now, err)
		}
		if !pattern.MatchString(proof.IssuedAt) || !pattern.MatchString(proof.ExpiresAt) {
			t.Fatalf("times not in YYYY-MM-DDTHH:MM:SSZ form: %s %s", proof.IssuedAt, proof.ExpiresAt)
		}
		issued, _ := time.Parse(time.RFC3339, proof.IssuedAt)
		expires, _ := time.Parse(time.RFC3339, proof.ExpiresAt)
		if expires.Sub(issued) != 300*time.Second {
			t.Fatalf("ttl = %v, want 300s", expires.Sub(issued))
		}
		if !issued.Equal(now.UTC().Truncate(time.Second)) {
			t.Fatalf("issued_at %s is not the injected time truncated to seconds in UTC", proof.IssuedAt)
		}
	}
}

func TestRandNonceSourceIsRandomLowercaseHex(t *testing.T) {
	source := RandNonceSource{}
	pattern := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		nonce, err := source.NewNonce()
		if err != nil {
			t.Fatalf("NewNonce: %v", err)
		}
		if !pattern.MatchString(nonce) {
			t.Fatalf("nonce %q is not 32 lowercase hex characters", nonce)
		}
		if seen[nonce] {
			t.Fatalf("nonce repeated: %s", nonce)
		}
		seen[nonce] = true
	}
}

func TestSystemClockReturnsCurrentTime(t *testing.T) {
	before := time.Now().Add(-time.Second)
	now := SystemClock{}.Now()
	if now.Before(before) || now.After(time.Now().Add(time.Second)) {
		t.Fatalf("SystemClock.Now() = %v is not the current time", now)
	}
}

func TestSignerNeverRevealsTheKeyWhenFormatted(t *testing.T) {
	signer := newFixtureSigner(t, vectorClock(t), &fixedNonce{value: "n"})
	for _, rendered := range []string{
		fmt.Sprintf("%v", signer),
		fmt.Sprintf("%+v", signer),
		fmt.Sprintf("%#v", signer),
		fmt.Sprintf("%v", *signer),
		fmt.Sprintf("%+v", *signer),
	} {
		lower := strings.ToLower(rendered)
		if strings.Contains(lower, fixtureKeyHex) || strings.Contains(rendered, "0 1 2 3 4 5 6 7") {
			t.Fatalf("formatted signer leaks key bytes: %q", rendered)
		}
	}
}

func TestSignIsSafeForConcurrentUse(t *testing.T) {
	vector := loadOriginVector(t)
	signer := newFixtureSigner(t, vectorClock(t), RandNonceSource{})
	var wg sync.WaitGroup
	results := make([]OriginProof, 16)
	errs := make([]error, len(results))
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = signer.Sign(vectorRequest(vector))
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, proof := range results {
		if errs[i] != nil {
			t.Fatalf("Sign #%d: %v", i, errs[i])
		}
		if seen[proof.Nonce] {
			t.Fatalf("two proofs share nonce %s", proof.Nonce)
		}
		seen[proof.Nonce] = true
	}
}

func TestOriginIsAlwaysHuman(t *testing.T) {
	vector := loadOriginVector(t)
	signer := newFixtureSigner(t, vectorClock(t), &fixedNonce{value: "n"})
	proof, err := signer.Sign(vectorRequest(vector))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if proof.Origin != "human" {
		t.Fatalf("origin = %q; only a relay of the human's own input is signed", proof.Origin)
	}
	if reflect.TypeOf(OriginProofRequest{}).NumField() != 4 {
		t.Fatalf("the request must not grow an origin or source field; sources come from the accepted record only")
	}
	if _, ok := reflect.TypeOf(OriginProofRequest{}).FieldByName("Origin"); ok {
		t.Fatalf("the request must not carry an origin")
	}
	if _, ok := reflect.TypeOf(AcceptedInput{}).FieldByName("Origin"); ok {
		t.Fatalf("the accepted record must not carry an origin")
	}
}

func TestIssuerSettingsValidateIsTheSigningRule(t *testing.T) {
	good := fixtureSettings()
	if err := good.Validate(); err != nil {
		t.Fatalf("fixture settings rejected: %v", err)
	}
	if err := (IssuerSettings{}).Validate(); !errors.Is(err, ErrInvalidIssuerSettings) {
		t.Fatalf("zero settings: error = %v, want ErrInvalidIssuerSettings", err)
	}
	for _, ttl := range []int{0, -1, 301} {
		settings := good
		settings.TTLSeconds = ttl
		if err := settings.Validate(); !errors.Is(err, ErrInvalidIssuerSettings) {
			t.Fatalf("ttl %d: error = %v, want ErrInvalidIssuerSettings", ttl, err)
		}
	}
}
