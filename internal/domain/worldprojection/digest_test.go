package worldprojection

import "testing"

func TestPayloadDigestUsesCanonicalJSON(t *testing.T) {
	left, err := PayloadDigest([]byte(`{"b":2,"a":1,"nested":{"z":true,"y":null}}`))
	if err != nil {
		t.Fatalf("PayloadDigest(left) error = %v", err)
	}
	right, err := PayloadDigest([]byte(`{"nested":{"y":null,"z":true},"a":1,"b":2}`))
	if err != nil {
		t.Fatalf("PayloadDigest(right) error = %v", err)
	}
	if left != right {
		t.Fatalf("canonical digests differ: %s != %s", left, right)
	}
}

func TestPayloadDigestRejectsNonIntegerNumberAndTrailingData(t *testing.T) {
	for _, raw := range []string{`{"revision":1.5}`, `{"revision":1} trailing`} {
		if _, err := PayloadDigest([]byte(raw)); err == nil {
			t.Fatalf("PayloadDigest(%q) error = nil", raw)
		}
	}
}
