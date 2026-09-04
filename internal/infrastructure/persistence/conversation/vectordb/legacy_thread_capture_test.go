package vectordb

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/protobuf/proto"
)

const captureCollection = "legacy-threads"

type capturePage struct {
	points []*qdrant.RetrievedPoint
	next   *qdrant.PointId
	err    error
}

type captureSource struct {
	pages []*capturePage
	calls []*qdrant.ScrollPoints
}

func (source *captureSource) ScrollAndOffset(_ context.Context, request *qdrant.ScrollPoints) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error) {
	requestCopy, ok := proto.Clone(request).(*qdrant.ScrollPoints)
	if !ok {
		return nil, nil, errors.New("failed to clone fake request")
	}
	source.calls = append(source.calls, requestCopy)
	call := len(source.calls) - 1
	if call >= len(source.pages) {
		return nil, nil, errors.New("unexpected fake source call")
	}
	page := source.pages[call]
	return page.points, page.next, page.err
}

func captureUUID(value string) *qdrant.PointId {
	return &qdrant.PointId{PointIdOptions: &qdrant.PointId_Uuid{Uuid: value}}
}

func capturePointUUID(number int) string {
	return []string{
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003",
		"00000000-0000-4000-8000-000000000004",
	}[number-1]
}

func capturePayload(values map[string]*qdrant.Value) map[string]*qdrant.Value {
	if values == nil {
		return map[string]*qdrant.Value{
			"session_id": qdrant.NewValueString("capture-session"),
		}
	}
	return values
}

func capturePoint(id string, vector []float32, payload map[string]*qdrant.Value) *qdrant.RetrievedPoint {
	return &qdrant.RetrievedPoint{
		Id:      captureUUID(id),
		Payload: capturePayload(payload),
		Vectors: &qdrant.VectorsOutput{VectorsOptions: &qdrant.VectorsOutput_Vector{Vector: &qdrant.VectorOutput{
			Vector: &qdrant.VectorOutput_Dense{Dense: &qdrant.DenseVector{Data: vector}},
		}}},
	}
}

func captureDeprecatedPoint(id string, vector []float32, payload map[string]*qdrant.Value) *qdrant.RetrievedPoint {
	return &qdrant.RetrievedPoint{
		Id:      captureUUID(id),
		Payload: capturePayload(payload),
		Vectors: &qdrant.VectorsOutput{VectorsOptions: &qdrant.VectorsOutput_Vector{Vector: &qdrant.VectorOutput{Data: vector}}},
	}
}

func captureRequestOffset(request *qdrant.ScrollPoints) *qdrant.PointId {
	if request == nil {
		return nil
	}
	return request.Offset
}

func TestCaptureLegacyThreadPointsPaginationSelectorsSortingAndDeepCopy(t *testing.T) {
	vectorA := []float32{1, 2}
	vectorB := []float32{3, 4}
	vectorC := []float32{5, 6}
	nested := &qdrant.Value{Kind: &qdrant.Value_StructValue{StructValue: &qdrant.Struct{Fields: map[string]*qdrant.Value{
		"z": qdrant.NewValueInt(7),
		"a": qdrant.NewValueString("nested"),
	}}}}
	payload := map[string]*qdrant.Value{
		"null":   qdrant.NewValueNull(),
		"double": qdrant.NewValueDouble(1.5),
		"int":    qdrant.NewValueInt(math.MaxInt64),
		"string": qdrant.NewValueString("日本語"),
		"bool":   qdrant.NewValueBool(true),
		"struct": nested,
		"list":   qdrant.NewValueFromList(qdrant.NewValueInt(-2), qdrant.NewValueNull()),
	}
	source := &captureSource{pages: []*capturePage{
		{points: []*qdrant.RetrievedPoint{
			capturePoint(capturePointUUID(2), vectorB, payload),
			capturePoint(capturePointUUID(1), vectorA, map[string]*qdrant.Value{"value": qdrant.NewValueString("a")}),
		}, next: captureUUID(capturePointUUID(2))},
		{points: []*qdrant.RetrievedPoint{
			capturePoint(capturePointUUID(3), vectorC, map[string]*qdrant.Value{"value": qdrant.NewValueString("c")}),
		}},
	}}

	got, err := captureLegacyThreadPoints(context.Background(), source, captureCollection)
	if err != nil {
		t.Fatalf("captureLegacyThreadPoints() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("captured points = %d, want 3", len(got))
	}
	for index, point := range got {
		wantID := capturePointUUID(index + 1)
		if point.PointID != wantID {
			t.Fatalf("point %d ID = %q, want %q", index, point.PointID, wantID)
		}
	}
	first := got[1]
	if string(first.Payload["int"]) != "9223372036854775807" {
		t.Fatalf("MaxInt64 payload = %s, want exact integer", first.Payload["int"])
	}
	if string(first.Payload["struct"]) != `{"a":"nested","z":7}` {
		t.Fatalf("canonical struct = %s", first.Payload["struct"])
	}
	if string(first.Payload["list"]) != `[-2,null]` {
		t.Fatalf("canonical list = %s", first.Payload["list"])
	}
	if !reflect.DeepEqual(first.Vector, vectorB) {
		t.Fatalf("vector = %#v, want %#v", first.Vector, vectorB)
	}
	if len(source.calls) != 2 {
		t.Fatalf("source calls = %d, want 2", len(source.calls))
	}
	if source.calls[0].CollectionName != captureCollection || source.calls[1].CollectionName != captureCollection {
		t.Fatalf("collection selectors = %#v/%#v", source.calls[0].CollectionName, source.calls[1].CollectionName)
	}
	for index, request := range source.calls {
		if request.GetLimit() != 256 {
			t.Fatalf("request %d limit = %d, want 256", index, request.GetLimit())
		}
		if request.GetWithPayload().GetEnable() != true {
			t.Fatalf("request %d payload selector = %#v, want enabled", index, request.GetWithPayload())
		}
		if request.GetWithVectors().GetEnable() != true {
			t.Fatalf("request %d vector selector = %#v, want enabled", index, request.GetWithVectors())
		}
	}
	if captureRequestOffset(source.calls[0]) != nil {
		t.Fatal("first request unexpectedly had an offset")
	}
	if gotOffset := captureRequestOffset(source.calls[1]); gotOffset == nil || gotOffset.GetUuid() != capturePointUUID(2) {
		t.Fatalf("second request offset = %#v, want point 2 UUID", gotOffset)
	}

	vectorB[0] = 99
	payload["int"].Kind = &qdrant.Value_IntegerValue{IntegerValue: 1}
	nested.GetStructValue().Fields["z"].Kind = &qdrant.Value_IntegerValue{IntegerValue: 8}
	if got[1].Vector[0] != 3 || string(got[1].Payload["int"]) != "9223372036854775807" || string(got[1].Payload["struct"]) != `{"a":"nested","z":7}` {
		t.Fatal("capture result aliases source vector or payload")
	}
	got[1].Vector[0] = 77
	if vectorB[0] != 99 {
		t.Fatal("capture vector does not own its result slice")
	}
}

func TestCaptureLegacyThreadPointsAcceptsModernAndDeprecatedDenseVectors(t *testing.T) {
	for _, test := range []struct {
		name  string
		point func(string, []float32, map[string]*qdrant.Value) *qdrant.RetrievedPoint
	}{
		{name: "modern", point: capturePoint},
		{name: "deprecated", point: captureDeprecatedPoint},
	} {
		t.Run(test.name, func(t *testing.T) {
			vector := []float32{0.25, -0.5}
			source := &captureSource{pages: []*capturePage{{points: []*qdrant.RetrievedPoint{
				test.point(capturePointUUID(1), vector, nil),
			}}}}
			got, err := captureLegacyThreadPoints(context.Background(), source, captureCollection)
			if err != nil {
				t.Fatalf("captureLegacyThreadPoints() error = %v", err)
			}
			if len(got) != 1 || !reflect.DeepEqual(got[0].Vector, vector) {
				t.Fatalf("captured vector = %#v, want %#v", got, vector)
			}
		})
	}
}

func TestCaptureLegacyThreadPointsRejectsInvalidInputs(t *testing.T) {
	valid := capturePoint(capturePointUUID(1), []float32{1, 2}, nil)
	deep := qdrant.NewValueNull()
	for index := 0; index < 129; index++ {
		deep = &qdrant.Value{Kind: &qdrant.Value_ListValue{ListValue: &qdrant.ListValue{Values: []*qdrant.Value{deep}}}}
	}
	base := func() *qdrant.RetrievedPoint { return capturePoint(capturePointUUID(1), []float32{1, 2}, nil) }
	malformedPayload := []struct {
		name    string
		payload map[string]*qdrant.Value
	}{
		{name: "nil payload", payload: nil},
		{name: "nil value", payload: map[string]*qdrant.Value{"bad": nil}},
		{name: "nil kind", payload: map[string]*qdrant.Value{"bad": &qdrant.Value{}}},
		{name: "nil struct", payload: map[string]*qdrant.Value{"bad": qdrant.NewValueStruct(nil)}},
		{name: "nil list", payload: map[string]*qdrant.Value{"bad": qdrant.NewValueList(nil)}},
		{name: "nil list child", payload: map[string]*qdrant.Value{"bad": qdrant.NewValueFromList(nil)}},
		{name: "invalid key", payload: map[string]*qdrant.Value{string([]byte{0xff}): qdrant.NewValueString("ok")}},
		{name: "invalid string", payload: map[string]*qdrant.Value{"bad": qdrant.NewValueString(string([]byte{0xff}))}},
		{name: "nonfinite NaN", payload: map[string]*qdrant.Value{"bad": qdrant.NewValueDouble(math.NaN())}},
		{name: "nonfinite Inf", payload: map[string]*qdrant.Value{"bad": qdrant.NewValueDouble(math.Inf(1))}},
		{name: "oversize payload", payload: map[string]*qdrant.Value{"bad": qdrant.NewValueString(strings.Repeat("x", threadmigration.QdrantPreparationMaxPayloadBytes))}},
		{name: "too deep", payload: map[string]*qdrant.Value{"bad": deep}},
	}
	for _, test := range malformedPayload {
		t.Run("payload/"+test.name, func(t *testing.T) {
			point := base()
			point.Payload = test.payload
			captureMustReject(t, &captureSource{pages: []*capturePage{{points: []*qdrant.RetrievedPoint{point}}}})
		})
	}

	vectorCases := []struct {
		name   string
		mutate func(*qdrant.RetrievedPoint)
	}{
		{name: "nil vectors", mutate: func(point *qdrant.RetrievedPoint) { point.Vectors = nil }},
		{name: "named vectors", mutate: func(point *qdrant.RetrievedPoint) {
			point.Vectors = &qdrant.VectorsOutput{VectorsOptions: &qdrant.VectorsOutput_Vectors{Vectors: &qdrant.NamedVectorsOutput{Vectors: map[string]*qdrant.VectorOutput{"default": {Data: []float32{1, 2}}}}}}
		}},
		{name: "nil unnamed vector", mutate: func(point *qdrant.RetrievedPoint) {
			point.Vectors = &qdrant.VectorsOutput{VectorsOptions: &qdrant.VectorsOutput_Vector{Vector: nil}}
		}},
		{name: "sparse vector", mutate: func(point *qdrant.RetrievedPoint) {
			point.Vectors = &qdrant.VectorsOutput{VectorsOptions: &qdrant.VectorsOutput_Vector{Vector: &qdrant.VectorOutput{Vector: &qdrant.VectorOutput_Sparse{Sparse: &qdrant.SparseVector{Values: []float32{1}, Indices: []uint32{0}}}}}}
		}},
		{name: "multi vector", mutate: func(point *qdrant.RetrievedPoint) {
			point.Vectors = &qdrant.VectorsOutput{VectorsOptions: &qdrant.VectorsOutput_Vector{Vector: &qdrant.VectorOutput{Vector: &qdrant.VectorOutput_MultiDense{MultiDense: &qdrant.MultiDenseVector{Vectors: []*qdrant.DenseVector{{Data: []float32{1, 2}}}}}}}}
		}},
		{name: "empty modern dense", mutate: func(point *qdrant.RetrievedPoint) {
			point.Vectors = &qdrant.VectorsOutput{VectorsOptions: &qdrant.VectorsOutput_Vector{Vector: &qdrant.VectorOutput{Vector: &qdrant.VectorOutput_Dense{Dense: &qdrant.DenseVector{}}}}}
		}},
		{name: "empty deprecated dense", mutate: func(point *qdrant.RetrievedPoint) {
			point.Vectors = &qdrant.VectorsOutput{VectorsOptions: &qdrant.VectorsOutput_Vector{Vector: &qdrant.VectorOutput{}}}
		}},
		{name: "deprecated sparse metadata", mutate: func(point *qdrant.RetrievedPoint) {
			count := uint32(1)
			point.Vectors = &qdrant.VectorsOutput{VectorsOptions: &qdrant.VectorsOutput_Vector{Vector: &qdrant.VectorOutput{Data: []float32{1, 2}, VectorsCount: &count}}}
		}},
		{name: "NaN", mutate: func(point *qdrant.RetrievedPoint) {
			point.Vectors.GetVector().Vector = &qdrant.VectorOutput_Dense{Dense: &qdrant.DenseVector{Data: []float32{float32(math.NaN())}}}
		}},
		{name: "Inf", mutate: func(point *qdrant.RetrievedPoint) {
			point.Vectors.GetVector().Vector = &qdrant.VectorOutput_Dense{Dense: &qdrant.DenseVector{Data: []float32{float32(math.Inf(1))}}}
		}},
	}
	for _, test := range vectorCases {
		t.Run("vector/"+test.name, func(t *testing.T) {
			point := base()
			test.mutate(point)
			captureMustReject(t, &captureSource{pages: []*capturePage{{points: []*qdrant.RetrievedPoint{point}}}})
		})
	}

	idCases := []struct {
		name string
		id   *qdrant.PointId
	}{
		{name: "nil ID", id: nil},
		{name: "numeric ID", id: &qdrant.PointId{PointIdOptions: &qdrant.PointId_Num{Num: 1}}},
		{name: "uppercase UUID", id: &qdrant.PointId{PointIdOptions: &qdrant.PointId_Uuid{Uuid: strings.ToUpper("00000000-0000-4000-8000-abcdefabcdef")}}},
		{name: "non UUID", id: &qdrant.PointId{PointIdOptions: &qdrant.PointId_Uuid{Uuid: "legacy-id"}}},
	}
	for _, test := range idCases {
		t.Run("id/"+test.name, func(t *testing.T) {
			point := base()
			point.Id = test.id
			captureMustReject(t, &captureSource{pages: []*capturePage{{points: []*qdrant.RetrievedPoint{point}}}})
		})
	}

	collectionCases := []struct {
		name       string
		collection string
	}{
		{name: "blank", collection: " "},
		{name: "invalid utf8", collection: string([]byte{0xff})},
		{name: "too long", collection: strings.Repeat("x", 256)},
		{name: "control", collection: "legacy\nthreads"},
	}
	for _, test := range collectionCases {
		t.Run("collection/"+test.name, func(t *testing.T) {
			source := &captureSource{pages: []*capturePage{{points: []*qdrant.RetrievedPoint{valid}}}}
			if _, err := captureLegacyThreadPoints(context.Background(), source, test.collection); err == nil {
				t.Fatal("captureLegacyThreadPoints() accepted invalid collection")
			}
			if len(source.calls) != 0 {
				t.Fatal("invalid collection reached source")
			}
		})
	}
}

func captureMustReject(t *testing.T, source legacyThreadPointSource) {
	t.Helper()
	if got, err := captureLegacyThreadPoints(context.Background(), source, captureCollection); err == nil || got != nil {
		t.Fatalf("captureLegacyThreadPoints() = %#v, %v; want bounded rejection", got, err)
	}
}

func TestCaptureLegacyThreadPointsRejectsPaginationDriftAndBounds(t *testing.T) {
	valid := capturePoint(capturePointUUID(1), []float32{1}, nil)
	tests := []struct {
		name  string
		pages []*capturePage
	}{
		{name: "duplicate point ID", pages: []*capturePage{{points: []*qdrant.RetrievedPoint{valid}, next: captureUUID(capturePointUUID(1))}, {points: []*qdrant.RetrievedPoint{valid}}}},
		{name: "empty page with cursor", pages: []*capturePage{{points: nil, next: captureUUID(capturePointUUID(1))}}},
		{name: "repeated cursor", pages: []*capturePage{{points: []*qdrant.RetrievedPoint{valid}, next: captureUUID(capturePointUUID(2))}, {points: []*qdrant.RetrievedPoint{capturePoint(capturePointUUID(3), []float32{1}, nil)}, next: captureUUID(capturePointUUID(2))}}},
		{name: "non-progress cursor", pages: []*capturePage{{points: []*qdrant.RetrievedPoint{valid}, next: captureUUID(capturePointUUID(3))}, {points: []*qdrant.RetrievedPoint{capturePoint(capturePointUUID(4), []float32{1}, nil)}, next: captureUUID(capturePointUUID(2))}}},
		{name: "numeric cursor", pages: []*capturePage{{points: []*qdrant.RetrievedPoint{valid}, next: &qdrant.PointId{PointIdOptions: &qdrant.PointId_Num{Num: 2}}}}},
		{name: "source error", pages: []*capturePage{{err: errors.New("backend contains secret and collection")}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			captureMustReject(t, &captureSource{pages: test.pages})
		})
	}

	points := make([]*qdrant.RetrievedPoint, threadmigration.QdrantPreparationMaxPoints+1)
	for index := range points {
		// The UUID suffix is kept deterministic without depending on source order.
		points[index] = capturePoint("00000000-0000-4000-8000-"+formatCaptureSuffix(index), []float32{1}, nil)
	}
	captureMustReject(t, &captureSource{pages: []*capturePage{{points: points}}})
}

func TestCaptureLegacyThreadPointsRejectsInconsistentVectorDimensions(t *testing.T) {
	source := &captureSource{pages: []*capturePage{{points: []*qdrant.RetrievedPoint{
		capturePoint(capturePointUUID(1), []float32{1, 2}, nil),
		capturePoint(capturePointUUID(2), []float32{1, 2, 3}, nil),
	}}}}
	captureMustReject(t, source)
}

func formatCaptureSuffix(number int) string {
	const digits = "0123456789abcdef"
	result := make([]byte, 12)
	for index := len(result) - 1; index >= 0; index-- {
		result[index] = digits[number%16]
		number /= 16
	}
	return string(result)
}

func TestCaptureLegacyThreadPointsRejectsNilAndCanceledContext(t *testing.T) {
	source := &captureSource{pages: []*capturePage{{points: []*qdrant.RetrievedPoint{capturePoint(capturePointUUID(1), []float32{1}, nil)}}}}
	if _, err := captureLegacyThreadPoints(nil, source, captureCollection); err == nil {
		t.Fatal("captureLegacyThreadPoints() accepted nil context")
	}
	if len(source.calls) != 0 {
		t.Fatal("nil context reached source")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := captureLegacyThreadPoints(ctx, source, captureCollection); err == nil {
		t.Fatal("captureLegacyThreadPoints() accepted canceled context")
	}
	if len(source.calls) != 0 {
		t.Fatal("canceled context reached source")
	}
}
