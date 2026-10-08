package nativeharnessclient

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureKeyHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

// deployKeyHex is a synthetic key that is not a published fixture, so the
// loader accepts it. It is a test value, never a real key.
var deployKeyHex = strings.Repeat("5a", 32)

func TestParseKeyAcceptsLowercaseHex64WithOptionalSingleLF(t *testing.T) {
	want, err := hex.DecodeString(fixtureKeyHex)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	for name, raw := range map[string]string{
		"no trailing newline": fixtureKeyHex,
		"single LF":           fixtureKeyHex + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			key, err := ParseKey([]byte(raw))
			if err != nil {
				t.Fatalf("ParseKey: %v", err)
			}
			if !bytes.Equal(key.bytes[:], want) {
				t.Fatalf("decoded key bytes differ from fixture")
			}
		})
	}
}

func TestParseKeyRejectsEveryOtherShape(t *testing.T) {
	valid := fixtureKeyHex
	raw32 := make([]byte, 32)
	for i := range raw32 {
		raw32[i] = byte(i)
	}
	cases := map[string][]byte{
		"empty":                  nil,
		"only LF":                []byte("\n"),
		"BOM prefix":             []byte("\xef\xbb\xbf" + valid),
		"BOM prefix with LF":     []byte("\xef\xbb\xbf" + valid + "\n"),
		"CRLF":                   []byte(valid + "\r\n"),
		"CR only":                []byte(valid + "\r"),
		"two LF":                 []byte(valid + "\n\n"),
		"trailing space":         []byte(valid + " "),
		"trailing space and LF":  []byte(valid + " \n"),
		"leading space":          []byte(" " + valid),
		"leading LF":             []byte("\n" + valid),
		"inner space":            []byte(valid[:32] + " " + valid[32:]),
		"tab":                    []byte(valid + "\t"),
		"uppercase":              []byte(strings.ToUpper(valid)),
		"mixed case":             []byte("A" + valid[1:]),
		"63 chars":               []byte(valid[:63]),
		"65 chars":               []byte(valid + "0"),
		"128 chars":              []byte(valid + valid),
		"non hex letter":         []byte("g" + valid[1:]),
		"0x prefix":              []byte("0x" + valid[2:]),
		"NUL byte":               []byte("\x00" + valid[1:]),
		"raw binary 32 bytes":    raw32,
		"base64 of 32 bytes":     []byte("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="),
		"url safe base64":        []byte("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"),
		"non ASCII digit":        []byte("\xef\xbc\x90" + valid[3:]),
		"truncated multibyte":    []byte(valid[:62] + "\xe3\x81"),
		"hex with comment":       []byte(valid + " # key"),
		"double quoted":          []byte("\"" + valid + "\""),
		"json object":            []byte("{\"key\":\"" + valid + "\"}"),
		"valid then second line": []byte(valid + "\n" + valid),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			key, err := ParseKey(raw)
			if !errors.Is(err, ErrKeyFormat) {
				t.Fatalf("ParseKey error = %v, want ErrKeyFormat", err)
			}
			if key != (Key{}) {
				t.Fatalf("ParseKey must return the zero Key on failure")
			}
		})
	}
}

func TestParseKeyErrorNeverContainsKeyMaterial(t *testing.T) {
	secret := "1f1e1d1c1b1a19181716151413121110"
	inputs := [][]byte{
		[]byte(secret + secret + "z"),
		[]byte(strings.ToUpper(secret + secret)),
		[]byte(secret + secret + "\r\n"),
		[]byte(secret),
	}
	for _, raw := range inputs {
		_, err := ParseKey(raw)
		if err == nil {
			t.Fatalf("ParseKey(%d bytes) succeeded, want error", len(raw))
		}
		if strings.Contains(strings.ToLower(err.Error()), secret) {
			t.Fatalf("error message leaks key material: %q", err.Error())
		}
	}
}

func TestKeyNeverFormatsItsBytes(t *testing.T) {
	key, err := ParseKey([]byte(fixtureKeyHex))
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	for _, rendered := range []string{
		fmt.Sprintf("%v", key),
		fmt.Sprintf("%+v", key),
		fmt.Sprintf("%#v", key),
		fmt.Sprintf("%s", key),
		fmt.Sprintf("%x", key),
		fmt.Sprintf("%d", key),
		fmt.Sprintf("%v", &key),
		fmt.Sprintf("%v", []Key{key}),
		fmt.Sprintf("%+v", struct{ K Key }{key}),
		key.String(),
	} {
		lower := strings.ToLower(rendered)
		if strings.Contains(lower, fixtureKeyHex) || strings.Contains(lower, "0102030405") || strings.Contains(rendered, "[0 1 2 3") {
			t.Fatalf("formatted Key leaks bytes: %q", rendered)
		}
	}
	if got := key.String(); got != "[REDACTED]" {
		t.Fatalf("Key.String() = %q, want [REDACTED]", got)
	}
}

func TestLoadKeyFile(t *testing.T) {
	dir := t.TempDir()
	want, _ := hex.DecodeString(deployKeyHex)
	write := func(name string, content []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	t.Run("valid file with LF", func(t *testing.T) {
		key, err := LoadKeyFile(write("ok-lf.hex", []byte(deployKeyHex+"\n")))
		if err != nil {
			t.Fatalf("LoadKeyFile: %v", err)
		}
		if !bytes.Equal(key.bytes[:], want) {
			t.Fatalf("loaded key differs from the file")
		}
	})
	t.Run("valid file without LF", func(t *testing.T) {
		if _, err := LoadKeyFile(write("ok.hex", []byte(deployKeyHex))); err != nil {
			t.Fatalf("LoadKeyFile: %v", err)
		}
	})
	t.Run("CRLF file is rejected", func(t *testing.T) {
		_, err := LoadKeyFile(write("crlf.hex", []byte(deployKeyHex+"\r\n")))
		if !errors.Is(err, ErrKeyFormat) {
			t.Fatalf("error = %v, want ErrKeyFormat", err)
		}
	})
	t.Run("oversized file is rejected without reading it all", func(t *testing.T) {
		path := write("big.hex", bytes.Repeat([]byte("a"), 1<<20))
		_, err := LoadKeyFile(path)
		if !errors.Is(err, ErrKeyFormat) {
			t.Fatalf("error = %v, want ErrKeyFormat", err)
		}
	})
	t.Run("relative path is rejected", func(t *testing.T) {
		_, err := LoadKeyFile("key.hex")
		if !errors.Is(err, ErrKeyPath) {
			t.Fatalf("error = %v, want ErrKeyPath", err)
		}
	})
	t.Run("empty path is rejected", func(t *testing.T) {
		if _, err := LoadKeyFile(""); !errors.Is(err, ErrKeyPath) {
			t.Fatalf("error = %v, want ErrKeyPath", err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		_, err := LoadKeyFile(filepath.Join(dir, "missing.hex"))
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("error = %v, want os.ErrNotExist", err)
		}
	})
	t.Run("directory is rejected", func(t *testing.T) {
		if _, err := LoadKeyFile(dir); err == nil {
			t.Fatalf("LoadKeyFile(directory) succeeded")
		}
	})
	t.Run("read error never carries the file content", func(t *testing.T) {
		secret := strings.Repeat("ab", 32)
		_, err := LoadKeyFile(write("secret-upper.hex", []byte(strings.ToUpper(secret))))
		if err == nil {
			t.Fatalf("uppercase key file accepted")
		}
		if strings.Contains(strings.ToLower(err.Error()), secret) {
			t.Fatalf("error message leaks key material")
		}
	})
}

func TestSyntheticFixtureKeyFileMatchesVectorKey(t *testing.T) {
	raw, err := os.ReadFile(testdataPath(t, "synthetic_origin_key.hex"))
	if err != nil {
		t.Fatalf("read fixture key file: %v", err)
	}
	key, err := ParseKey(raw)
	if err != nil {
		t.Fatalf("ParseKey(fixture): %v", err)
	}
	want, _ := hex.DecodeString(fixtureKeyHex)
	if !bytes.Equal(key.bytes[:], want) {
		t.Fatalf("fixture key file differs from the vector key_hex")
	}
}

func TestLoadKeyFileRejectsThePublishedFixtureKey(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"fixture-lf.hex":    fixtureKeyHex + "\n",
		"fixture-no-lf.hex": fixtureKeyHex,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
			key, err := LoadKeyFile(path)
			if !errors.Is(err, ErrKeyFixture) {
				t.Fatalf("error = %v, want ErrKeyFixture", err)
			}
			if !key.isZero() {
				t.Fatalf("a rejected fixture key must be the zero Key")
			}
			if strings.Contains(err.Error(), fixtureKeyHex) || strings.Contains(err.Error(), "0001020304") {
				t.Fatalf("error leaks key material: %q", err.Error())
			}
		})
	}
	t.Run("the file that ships with the repository", func(t *testing.T) {
		if _, err := LoadKeyFile(testdataPath(t, "synthetic_origin_key.hex")); !errors.Is(err, ErrKeyFixture) {
			t.Fatalf("error = %v, want ErrKeyFixture", err)
		}
	})
	t.Run("ParseKey stays pure so the vectors can be reproduced", func(t *testing.T) {
		if _, err := ParseKey([]byte(fixtureKeyHex)); err != nil {
			t.Fatalf("ParseKey(fixture): %v", err)
		}
	})
}

// The production code keeps only the digest of the published key. This guard
// fails if the digest and the fixture key file ever drift apart.
func TestFixtureKeyDigestMatchesTheFixtureKeyFile(t *testing.T) {
	raw, err := os.ReadFile(testdataPath(t, "synthetic_origin_key.hex"))
	if err != nil {
		t.Fatalf("read fixture key file: %v", err)
	}
	key, err := ParseKey(raw)
	if err != nil {
		t.Fatalf("ParseKey(fixture): %v", err)
	}
	sum := sha256.Sum256(key.bytes[:])
	if got := hex.EncodeToString(sum[:]); got != fixtureKeyDigest {
		t.Fatalf("fixtureKeyDigest = %s, but the fixture key hashes to %s", fixtureKeyDigest, got)
	}
}

func testdataPath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("resolve testdata path: %v", err)
	}
	return path
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadKeyAppliesTheFileRules(t *testing.T) {
	want, _ := hex.DecodeString(deployKeyHex)
	for _, content := range []string{deployKeyHex, deployKeyHex + "\n"} {
		key, err := ReadKey(strings.NewReader(content))
		if err != nil {
			t.Fatalf("ReadKey: %v", err)
		}
		if !bytes.Equal(key.bytes[:], want) {
			t.Fatalf("ReadKey returned a different key")
		}
	}
	if _, err := ReadKey(strings.NewReader(fixtureKeyHex + "\n")); !errors.Is(err, ErrKeyFixture) {
		t.Fatalf("fixture key: error = %v, want ErrKeyFixture", err)
	}
	for name, content := range map[string]string{
		"uppercase": strings.ToUpper(deployKeyHex),
		"CRLF":      deployKeyHex + "\r\n",
		"oversized": strings.Repeat("a", 1<<20),
		"empty":     "",
	} {
		if _, err := ReadKey(strings.NewReader(content)); !errors.Is(err, ErrKeyFormat) {
			t.Fatalf("%s: error = %v, want ErrKeyFormat", name, err)
		}
	}
	readFailure := errors.New("disk failure")
	if _, err := ReadKey(failingReader{err: readFailure}); !errors.Is(err, readFailure) {
		t.Fatalf("read failure: error = %v, want the reader error wrapped", err)
	}
}
