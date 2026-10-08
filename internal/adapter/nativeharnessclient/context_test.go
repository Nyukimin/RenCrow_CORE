package nativeharnessclient

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type revisionVector struct {
	Block          ContextBlock `json:"block"`
	ComputedDigest string       `json:"computed_text_digest"`
}

func readJSON(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile(testdataPath(t, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

func typed(kind llm.PromptContextType, content string) llm.Message {
	return llm.Message{Role: "system", Content: content, Type: kind}
}

func userMessage(content string) llm.Message {
	return llm.Message{Role: "user", Content: content, Type: llm.PromptContextUser}
}

// independentRevision recomputes the revision formula in a deliberately
// different shape from production code (bytes.Buffer and binary.Write).
func independentRevision(t *testing.T, kind, text string, source *SourceRef) string {
	t.Helper()
	var buf bytes.Buffer
	lp := func(value string) {
		if err := binary.Write(&buf, binary.BigEndian, uint64(len([]byte(value)))); err != nil {
			t.Fatalf("write length: %v", err)
		}
		buf.WriteString(value)
	}
	buf.WriteString("rencrow-context-block/v1\x00")
	lp(kind)
	lp(text)
	if source == nil {
		buf.WriteByte(0x00)
	} else {
		buf.WriteByte(0x01)
		lp(source.Owner)
		lp(source.SourceID)
		lp(source.RawHash)
		lp(source.ProjectionVersion)
		lp(strconv.FormatUint(source.Range.Start, 10))
		lp(strconv.FormatUint(source.Range.End, 10))
		lp(source.Origin)
		lp(strconv.FormatUint(source.Sequence, 10))
	}
	return "ctx-v1:" + sha256Hex(buf.Bytes())
}

func TestMaterializeReproducesDesignRevisionVectors(t *testing.T) {
	var vectors []revisionVector
	readJSON(t, "context_revision_vectors.json", &vectors)
	if len(vectors) != 5 {
		t.Fatalf("vector count = %d, want 5", len(vectors))
	}
	for index, vector := range vectors {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			messages := []llm.Message{
				typed(vector.Block.Kind, vector.Block.Text),
				userMessage("go"),
			}
			blocks, user, err := MaterializeContextRevision(messages, nil)
			if err != nil {
				t.Fatalf("MaterializeContextRevision: %v", err)
			}
			if user != "go" {
				t.Fatalf("user text = %q, want go", user)
			}
			if len(blocks) != 1 {
				t.Fatalf("blocks = %d, want 1", len(blocks))
			}
			if !reflect.DeepEqual(blocks[0], vector.Block) {
				t.Fatalf("block = %+v, want %+v", blocks[0], vector.Block)
			}
			if got := sha256Hex([]byte(vector.Block.Text)); got != vector.ComputedDigest {
				t.Fatalf("text digest = %s, want %s", got, vector.ComputedDigest)
			}
		})
	}
}

func TestMaterializeReproducesCoreStartFourBlocks(t *testing.T) {
	var start struct {
		Input struct {
			Text string `json:"text"`
		} `json:"input"`
		ContextBlocks []ContextBlock `json:"context_blocks"`
	}
	readJSON(t, "core_start.json", &start)
	if len(start.ContextBlocks) != 4 {
		t.Fatalf("fixture blocks = %d, want 4", len(start.ContextBlocks))
	}
	messages := make([]llm.Message, 0, 5)
	for _, block := range start.ContextBlocks {
		messages = append(messages, typed(block.Kind, block.Text))
	}
	messages = append(messages, userMessage(start.Input.Text))

	blocks, user, err := MaterializeContextRevision(messages, nil)
	if err != nil {
		t.Fatalf("MaterializeContextRevision: %v", err)
	}
	if user != start.Input.Text {
		t.Fatalf("user text = %q, want %q", user, start.Input.Text)
	}
	if !reflect.DeepEqual(blocks, start.ContextBlocks) {
		t.Fatalf("blocks = %+v, want %+v", blocks, start.ContextBlocks)
	}
	gotJSON, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	wantJSON, err := json.Marshal(start.ContextBlocks)
	if err != nil {
		t.Fatalf("marshal fixture blocks: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("serialized blocks differ:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestContextBlockJSONHasExactlyFourPublicFieldsAndNullSource(t *testing.T) {
	blocks, _, err := MaterializeContextRevision([]llm.Message{typed(llm.PromptContextCharacter, "c"), userMessage("u")}, nil)
	if err != nil {
		t.Fatalf("MaterializeContextRevision: %v", err)
	}
	raw, err := json.Marshal(blocks[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(fields) != 4 {
		t.Fatalf("public fields = %d (%s), want exactly 4", len(fields), raw)
	}
	for _, name := range []string{"kind", "text", "revision", "source"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("missing public field %q in %s", name, raw)
		}
	}
	if string(fields["source"]) != "null" {
		t.Fatalf("source = %s, want null", fields["source"])
	}
}

func TestMaterializeKeepsTextExactlyAsGiven(t *testing.T) {
	texts := []string{
		"  leading and trailing  ",
		"line\n",
		"\n\nblank lines first",
		"tab\tinside\r\ncrlf",
		"Café",
		"Café",
		"　全角空白　",
	}
	for _, text := range texts {
		blocks, _, err := MaterializeContextRevision([]llm.Message{typed(llm.PromptContextVariable, text), userMessage("u")}, nil)
		if err != nil {
			t.Fatalf("text %q: %v", text, err)
		}
		if blocks[0].Text != text {
			t.Fatalf("text changed: got %q want %q", blocks[0].Text, text)
		}
		if want := independentRevision(t, "variable_runtime_context", text, nil); blocks[0].Revision != want {
			t.Fatalf("revision for %q = %s, want %s", text, blocks[0].Revision, want)
		}
	}
}

func TestMaterializeKeepsUserTextRaw(t *testing.T) {
	for _, text := range []string{" padded \n", "Café", "line1\r\nline2"} {
		_, user, err := MaterializeContextRevision([]llm.Message{typed(llm.PromptContextCharacter, "c"), userMessage(text)}, nil)
		if err != nil {
			t.Fatalf("user %q: %v", text, err)
		}
		if user != text {
			t.Fatalf("user text changed: got %q want %q", user, text)
		}
	}
}

func TestMaterializeMapsOneMessageToOneBlockInOrder(t *testing.T) {
	messages := []llm.Message{
		typed(llm.PromptContextCharacter, "c1"),
		typed(llm.PromptContextCharacter, "c2"),
		typed(llm.PromptContextStable, "s1"),
		typed(llm.PromptContextStable, "s2"),
		typed(llm.PromptContextStable, "s3"),
		{Role: "user", Content: "r1", Type: llm.PromptContextRecall},
		{Role: "assistant", Content: "r2", Type: llm.PromptContextRecall},
		typed(llm.PromptContextVariable, "v1"),
		userMessage("now"),
	}
	blocks, user, err := MaterializeContextRevision(messages, nil)
	if err != nil {
		t.Fatalf("MaterializeContextRevision: %v", err)
	}
	wantKinds := []llm.PromptContextType{
		llm.PromptContextCharacter, llm.PromptContextCharacter,
		llm.PromptContextStable, llm.PromptContextStable, llm.PromptContextStable,
		llm.PromptContextRecall, llm.PromptContextRecall, llm.PromptContextVariable,
	}
	wantTexts := []string{"c1", "c2", "s1", "s2", "s3", "r1", "r2", "v1"}
	if len(blocks) != len(wantKinds) {
		t.Fatalf("blocks = %d, want %d", len(blocks), len(wantKinds))
	}
	for i, block := range blocks {
		if block.Kind != wantKinds[i] || block.Text != wantTexts[i] {
			t.Fatalf("block %d = %s/%q, want %s/%q", i, block.Kind, block.Text, wantKinds[i], wantTexts[i])
		}
	}
	if user != "now" {
		t.Fatalf("user = %q, want now", user)
	}
	if blocks[0].Revision == blocks[1].Revision {
		t.Fatalf("different texts must have different revisions")
	}
}

func TestRevisionDependsOnlyOnKindTextAndSource(t *testing.T) {
	plain := typed(llm.PromptContextStable, "same")
	decorated := llm.Message{
		Role: "developer", Content: "same", Type: llm.PromptContextStable,
		Metadata: map[string]string{"runtime_context_kind": "agent_contract", "request_id": "req-1"},
	}
	a, _, err := MaterializeContextRevision([]llm.Message{plain, userMessage("u1")}, nil)
	if err != nil {
		t.Fatalf("plain: %v", err)
	}
	b, _, err := MaterializeContextRevision([]llm.Message{decorated, userMessage("u2")}, nil)
	if err != nil {
		t.Fatalf("decorated: %v", err)
	}
	if a[0].Revision != b[0].Revision {
		t.Fatalf("role, metadata and user text must not change the revision: %s vs %s", a[0].Revision, b[0].Revision)
	}
	c, _, err := MaterializeContextRevision([]llm.Message{typed(llm.PromptContextVariable, "same"), userMessage("u")}, nil)
	if err != nil {
		t.Fatalf("variable: %v", err)
	}
	if a[0].Revision == c[0].Revision {
		t.Fatalf("kind must change the revision")
	}
}

func validSource() SourceRef {
	return SourceRef{
		Owner:             "RenCrow_CORE",
		SourceID:          "msg_00000000-0000-7000-8000-000000000101",
		RawHash:           "5eba7f706ae54dc35ff984acac2be22f04c257a958a1af6722ac29ca29aabf8d",
		ProjectionVersion: "recall-v1",
		Range:             ByteRange{Start: 0, End: 42},
		Origin:            "human",
		Sequence:          7,
	}
}

func TestMaterializeAttachesSourceAndMatchesIndependentGolden(t *testing.T) {
	const wantRecall = "ctx-v1:c43aa8538f05872c76ca189a29a68a02f5e13607e6c6ca6b502f3338c7ecc2f6"
	source := validSource()
	messages := []llm.Message{
		typed(llm.PromptContextStable, "stable"),
		{Role: "user", Content: "利用者が渡した過去情報の参照data。", Type: llm.PromptContextRecall},
		userMessage("u"),
	}
	var seen []int
	resolve := func(index int, message llm.Message) (*SourceRef, error) {
		seen = append(seen, index)
		if message.Type == llm.PromptContextRecall {
			copied := source
			return &copied, nil
		}
		return nil, nil
	}
	blocks, _, err := MaterializeContextRevision(messages, resolve)
	if err != nil {
		t.Fatalf("MaterializeContextRevision: %v", err)
	}
	if !reflect.DeepEqual(seen, []int{0, 1}) {
		t.Fatalf("resolver saw indexes %v, want [0 1] (the user message is not a block)", seen)
	}
	if blocks[0].Source != nil {
		t.Fatalf("stable block must keep null source")
	}
	if blocks[1].Source == nil || *blocks[1].Source != source {
		t.Fatalf("recall block source = %+v, want %+v", blocks[1].Source, source)
	}
	if blocks[1].Revision != wantRecall {
		t.Fatalf("recall revision = %s, want %s", blocks[1].Revision, wantRecall)
	}
	if want := independentRevision(t, "recall_pack", "利用者が渡した過去情報の参照data。", &source); blocks[1].Revision != want {
		t.Fatalf("recall revision = %s, independent = %s", blocks[1].Revision, want)
	}
	if blocks[0].Revision == independentRevision(t, "stable_runtime_context", "stable", &source) {
		t.Fatalf("null source and a source must not share a revision")
	}
}

func TestSourceRefWithLargeRangeAndZeroSequenceMatchesGolden(t *testing.T) {
	const want = "ctx-v1:fb703bbb0b213112b5684edbe846a62d50f15bb92fde6248269abb1f5d17da67"
	source := validSource()
	source.Range = ByteRange{Start: 3, End: 1234567890123}
	source.Sequence = 0
	source.Origin = "tool"
	blocks, _, err := MaterializeContextRevision(
		[]llm.Message{{Role: "user", Content: "a\n b ", Type: llm.PromptContextRecall}, userMessage("u")},
		func(int, llm.Message) (*SourceRef, error) { return &source, nil },
	)
	if err != nil {
		t.Fatalf("MaterializeContextRevision: %v", err)
	}
	if blocks[0].Revision != want {
		t.Fatalf("revision = %s, want %s", blocks[0].Revision, want)
	}
}

func TestSourceNullDiffersFromEmptyTextAndEmptyStrings(t *testing.T) {
	// SOURCE(null) is the single 0x00 tag. A reference whose fields are all
	// empty strings would start with 0x01 and must never collide with it.
	nullRevision := independentRevision(t, "recall_pack", "x", nil)
	emptyish := independentRevision(t, "recall_pack", "x", &SourceRef{})
	if nullRevision == emptyish {
		t.Fatalf("null source collides with an empty reference")
	}
}

func TestMaterializeDoesNotMutateInput(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "  keep  ", Type: llm.PromptContextCharacter, Metadata: map[string]string{"k": "v"}},
		userMessage(" u "),
	}
	snapshot := []llm.Message{
		{Role: "system", Content: "  keep  ", Type: llm.PromptContextCharacter, Metadata: map[string]string{"k": "v"}},
		userMessage(" u "),
	}
	if _, _, err := MaterializeContextRevision(messages, nil); err != nil {
		t.Fatalf("MaterializeContextRevision: %v", err)
	}
	if !reflect.DeepEqual(messages, snapshot) {
		t.Fatalf("input messages were modified: %+v", messages)
	}
}

func TestMaterializeRejectsInvalidMessageSets(t *testing.T) {
	multimodal := typed(llm.PromptContextRecall, "with parts")
	multimodal.Parts = []llm.MessagePart{{Type: llm.MessagePartImage, Data: []byte{1}}}
	userWithParts := userMessage("u")
	userWithParts.Parts = []llm.MessagePart{{Type: llm.MessagePartAudio, Data: []byte{1}}}

	cases := map[string][]llm.Message{
		"nil input":               {},
		"no user message":         {typed(llm.PromptContextCharacter, "c")},
		"untyped message":         {{Role: "system", Content: "c"}, userMessage("u")},
		"unknown type":            {typed(llm.PromptContextType("history"), "c"), userMessage("u")},
		"case variant type":       {typed(llm.PromptContextType("Recall_Pack"), "c"), userMessage("u")},
		"user message first":      {userMessage("u"), typed(llm.PromptContextCharacter, "c")},
		"user message in middle":  {typed(llm.PromptContextCharacter, "c"), userMessage("u"), typed(llm.PromptContextVariable, "v")},
		"two user messages":       {typed(llm.PromptContextCharacter, "c"), userMessage("u1"), userMessage("u2")},
		"stable before character": {typed(llm.PromptContextStable, "s"), typed(llm.PromptContextCharacter, "c"), userMessage("u")},
		"recall before stable":    {typed(llm.PromptContextRecall, "r"), typed(llm.PromptContextStable, "s"), userMessage("u")},
		"variable before recall":  {typed(llm.PromptContextVariable, "v"), typed(llm.PromptContextRecall, "r"), userMessage("u")},
		"empty block text":        {typed(llm.PromptContextRecall, ""), userMessage("u")},
		"empty user text":         {typed(llm.PromptContextRecall, "r"), userMessage("")},
		"multimodal block":        {multimodal, userMessage("u")},
		"multimodal user":         {typed(llm.PromptContextRecall, "r"), userWithParts},
		"invalid utf8 block":      {typed(llm.PromptContextRecall, "bad\xff"), userMessage("u")},
		"invalid utf8 user":       {typed(llm.PromptContextRecall, "r"), userMessage("bad\xc3")},
	}
	for name, messages := range cases {
		t.Run(name, func(t *testing.T) {
			blocks, user, err := MaterializeContextRevision(messages, nil)
			if !errors.Is(err, ErrInvalidContextInput) {
				t.Fatalf("error = %v, want ErrInvalidContextInput", err)
			}
			if blocks != nil || user != "" {
				t.Fatalf("a failed call must return no blocks and no user text, got %v / %q", blocks, user)
			}
		})
	}
}

func TestMaterializeAllowsOnlyAUserMessage(t *testing.T) {
	blocks, user, err := MaterializeContextRevision([]llm.Message{userMessage("only")}, nil)
	if err != nil {
		t.Fatalf("MaterializeContextRevision: %v", err)
	}
	if len(blocks) != 0 || user != "only" {
		t.Fatalf("blocks=%d user=%q, want 0/only", len(blocks), user)
	}
}

func TestMaterializeRejectsInvalidSources(t *testing.T) {
	mutate := func(edit func(*SourceRef)) SourceRef {
		source := validSource()
		edit(&source)
		return source
	}
	cases := map[string]SourceRef{
		"empty owner":              mutate(func(s *SourceRef) { s.Owner = "" }),
		"empty source id":          mutate(func(s *SourceRef) { s.SourceID = "" }),
		"empty projection version": mutate(func(s *SourceRef) { s.ProjectionVersion = "" }),
		"empty raw hash":           mutate(func(s *SourceRef) { s.RawHash = "" }),
		"short raw hash":           mutate(func(s *SourceRef) { s.RawHash = s.RawHash[:63] }),
		"uppercase raw hash":       mutate(func(s *SourceRef) { s.RawHash = strings.ToUpper(s.RawHash) }),
		"non hex raw hash":         mutate(func(s *SourceRef) { s.RawHash = "g" + s.RawHash[1:] }),
		"range end before start":   mutate(func(s *SourceRef) { s.Range = ByteRange{Start: 5, End: 4} }),
		"unknown origin":           mutate(func(s *SourceRef) { s.Origin = "operator" }),
		"empty origin":             mutate(func(s *SourceRef) { s.Origin = "" }),
		"upper case origin":        mutate(func(s *SourceRef) { s.Origin = "Human" }),
		"invalid utf8 owner":       mutate(func(s *SourceRef) { s.Owner = "bad\xff" }),
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			source := source
			blocks, user, err := MaterializeContextRevision(
				[]llm.Message{typed(llm.PromptContextRecall, "r"), userMessage("u")},
				func(int, llm.Message) (*SourceRef, error) { return &source, nil },
			)
			if !errors.Is(err, ErrInvalidContextInput) {
				t.Fatalf("error = %v, want ErrInvalidContextInput", err)
			}
			if blocks != nil || user != "" {
				t.Fatalf("a failed call must return no blocks and no user text")
			}
		})
	}
}

func TestMaterializeAcceptsEveryDesignOrigin(t *testing.T) {
	for _, origin := range []string{"human", "automation", "host", "agent", "tool", "unknown"} {
		source := validSource()
		source.Origin = origin
		_, _, err := MaterializeContextRevision(
			[]llm.Message{typed(llm.PromptContextRecall, "r"), userMessage("u")},
			func(int, llm.Message) (*SourceRef, error) { return &source, nil },
		)
		if err != nil {
			t.Fatalf("origin %q: %v", origin, err)
		}
	}
}

func TestMaterializePropagatesResolverError(t *testing.T) {
	boom := errors.New("resolver failed")
	blocks, user, err := MaterializeContextRevision(
		[]llm.Message{typed(llm.PromptContextRecall, "r"), userMessage("u")},
		func(int, llm.Message) (*SourceRef, error) { return nil, boom },
	)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want wrapped resolver error", err)
	}
	if blocks != nil || user != "" {
		t.Fatalf("a failed call must return no blocks and no user text")
	}
}

func TestMaterializeCopiesResolvedSource(t *testing.T) {
	source := validSource()
	blocks, _, err := MaterializeContextRevision(
		[]llm.Message{typed(llm.PromptContextRecall, "r"), userMessage("u")},
		func(int, llm.Message) (*SourceRef, error) { return &source, nil },
	)
	if err != nil {
		t.Fatalf("MaterializeContextRevision: %v", err)
	}
	before := blocks[0].Revision
	source.Owner = "changed-after-materialize"
	if blocks[0].Source.Owner != "RenCrow_CORE" {
		t.Fatalf("block source aliases the resolver's pointer")
	}
	if blocks[0].Revision != before {
		t.Fatalf("revision changed after the fact")
	}
}

// The classification must come from the typed message only. These guards make
// a flatten-and-reparse implementation impossible to add without failing here.
func TestMaterializeSignatureAcceptsNoFlattenedString(t *testing.T) {
	signature := reflect.TypeOf(MaterializeContextRevision)
	if signature.NumIn() != 2 {
		t.Fatalf("parameters = %d, want 2", signature.NumIn())
	}
	if got := signature.In(0); got != reflect.TypeOf([]llm.Message(nil)) {
		t.Fatalf("first parameter = %v, want []llm.Message", got)
	}
	for i := 0; i < signature.NumIn(); i++ {
		if signature.In(i).Kind() == reflect.String {
			t.Fatalf("parameter %d is a string; a flattened prompt must not be accepted", i)
		}
	}
}

func TestPackageSourceNeverTouchesFlattenOrHarnessCode(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range file.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			if strings.Contains(path, "RenCrow_Harness") {
				t.Fatalf("%s imports Harness code (%s); the revision must be reproduced independently", name, path)
			}
			if !strings.HasSuffix(name, "_test.go") && strings.HasSuffix(path, "internal/domain/agent") {
				t.Fatalf("%s imports the agent package; typed messages must arrive from the caller", name)
			}
		}
		if name != "context.go" {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, banned := range []string{"renderSystemMessages", "strings.Split", "strings.Fields", "regexp"} {
			if strings.Contains(string(raw), banned) {
				t.Fatalf("%s mentions %q; classification must never be restored from a flattened string", name, banned)
			}
		}
	}
}

func recallMessages(count int) []llm.Message {
	messages := make([]llm.Message, 0, count+1)
	for i := 0; i < count; i++ {
		messages = append(messages, typed(llm.PromptContextRecall, "r"+strconv.Itoa(i)))
	}
	return append(messages, userMessage("u"))
}

func TestMaxContextBlocksIsTheDesignLimit(t *testing.T) {
	if MaxContextBlocks != 256 {
		t.Fatalf("MaxContextBlocks = %d, want 256", MaxContextBlocks)
	}
}

func TestMaterializeAcceptsExactlyMaxContextBlocks(t *testing.T) {
	// Arrange
	messages := recallMessages(MaxContextBlocks)

	// Act
	blocks, user, err := MaterializeContextRevision(messages, nil)

	// Assert
	if err != nil {
		t.Fatalf("MaterializeContextRevision(%d blocks): %v", MaxContextBlocks, err)
	}
	if len(blocks) != MaxContextBlocks || user != "u" {
		t.Fatalf("blocks = %d, user = %q", len(blocks), user)
	}
}

func TestMaterializeRejectsMoreThanMaxContextBlocksBeforeAnyWork(t *testing.T) {
	for _, count := range []int{MaxContextBlocks + 1, 4 * MaxContextBlocks} {
		// Arrange
		messages := recallMessages(count)
		resolverCalls := 0
		resolve := func(int, llm.Message) (*SourceRef, error) {
			resolverCalls++
			return nil, nil
		}

		// Act
		blocks, user, err := MaterializeContextRevision(messages, resolve)

		// Assert
		if !errors.Is(err, ErrTooManyContextBlocks) || !errors.Is(err, ErrInvalidContextInput) {
			t.Fatalf("%d blocks: error = %v, want ErrTooManyContextBlocks (an ErrInvalidContextInput)", count, err)
		}
		if blocks != nil || user != "" {
			t.Fatalf("%d blocks: result must be empty on error, got %d blocks and user %q", count, len(blocks), user)
		}
		if resolverCalls != 0 {
			t.Fatalf("%d blocks: the resolver ran %d times; the limit must be checked first", count, resolverCalls)
		}
	}
}
