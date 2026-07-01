package persona

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCharactersReadsLorePersonaAndModes(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(path string, body string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("characters/mio/lore/profile.md", "Mio profile")
	mustWrite("characters/mio/persona/self.md", "Mio self")
	mustWrite("characters/mio/persona/triggers/tired.md", "Tired trigger")
	mustWrite("characters/mio/modes/as_character.md", "As character")
	mustWrite("characters/mio/persona/ignore.txt", "ignored")

	characters, err := LoadCharacters(root)
	if err != nil {
		t.Fatalf("LoadCharacters() error = %v", err)
	}
	mio, ok := characters["mio"]
	if !ok {
		t.Fatalf("mio missing: %#v", characters)
	}
	if mio.Lore["profile"] != "Mio profile" {
		t.Fatalf("lore=%#v", mio.Lore)
	}
	if mio.Persona["self"] != "Mio self" || mio.Persona["triggers/tired"] != "Tired trigger" {
		t.Fatalf("persona=%#v", mio.Persona)
	}
	if mio.Modes["as_character"] != "As character" {
		t.Fatalf("modes=%#v", mio.Modes)
	}
	if _, ok := mio.Persona["ignore"]; ok {
		t.Fatalf("non-md file should be ignored: %#v", mio.Persona)
	}
}

func TestLoadCharactersReturnsEmptyWhenMissing(t *testing.T) {
	characters, err := LoadCharacters(t.TempDir())
	if err != nil {
		t.Fatalf("LoadCharacters() error = %v", err)
	}
	if len(characters) != 0 {
		t.Fatalf("characters=%#v", characters)
	}
}
