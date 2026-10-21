package worldread

import "testing"

func TestTransportIDMatchesWorldFixtures(t *testing.T) {
	// Copied from WORLD internal/idmap/testdata/cases.json. CORE must run the
	// same fixture before its ASCII transport is compatible with WORLD R07.
	cases := []struct {
		canonical string
		transport string
	}{
		{canonical: "mio", transport: "mio"},
		{canonical: "rencrow-home", transport: "rencrow-home"},
		{canonical: "1f-living", transport: "1f-living"},
		{canonical: "a~b", transport: "a~b"},
		{canonical: "ミオ", transport: "~44Of44Kq"},
		{canonical: "世界", transport: "~5LiW55WM"},
		{canonical: "ルミナ", transport: "~44Or44Of44OK"},
		{canonical: "~mio", transport: "~fm1pbw"},
		{canonical: "~", transport: "~fg"},
		{canonical: "a b", transport: "~YSBi"},
		{canonical: "a/b", transport: "~YS9i"},
	}
	for _, test := range cases {
		t.Run(test.canonical, func(t *testing.T) {
			got, err := encodeTransportID(test.canonical)
			if err != nil {
				t.Fatalf("encodeTransportID(%q) error = %v", test.canonical, err)
			}
			if got != test.transport {
				t.Fatalf("encodeTransportID(%q) = %q; want %q", test.canonical, got, test.transport)
			}
			decoded, err := decodeTransportID(test.transport)
			if err != nil {
				t.Fatalf("decodeTransportID(%q) error = %v", test.transport, err)
			}
			if decoded != test.canonical {
				t.Fatalf("decodeTransportID(%q) = %q; want %q", test.transport, decoded, test.canonical)
			}
		})
	}
}

func TestTransportIDRejectsInvalidValues(t *testing.T) {
	for _, id := range []string{"", " mio", "mio ", "\x00", "bad\x7f"} {
		if _, err := encodeTransportID(id); err == nil {
			t.Fatalf("encodeTransportID(%q) error = nil", id)
		}
	}
	for _, transport := range []string{"", "~", "~!", "~44Of44Kq=", "ミオ"} {
		if _, err := decodeTransportID(transport); err == nil {
			t.Fatalf("decodeTransportID(%q) error = nil", transport)
		}
	}
}
