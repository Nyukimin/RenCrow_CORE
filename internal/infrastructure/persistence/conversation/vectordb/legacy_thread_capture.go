package vectordb

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

const (
	legacyThreadCapturePageLimit    uint32 = 256
	legacyThreadCaptureMaxNameRunes        = 255
	legacyThreadCaptureMaxJSONDepth        = 128
)

var (
	errLegacyThreadCaptureInvalid = errors.New("legacy Qdrant capture request is invalid")
	errLegacyThreadCaptureFailed  = errors.New("legacy Qdrant capture failed")
	errLegacyThreadCaptureLimit   = errors.New("legacy Qdrant capture limit exceeded")
)

// legacyThreadPointSource is the read-only Qdrant seam. Production supplies a
// qdrant.Client; tests use a deterministic fake without opening a network
// connection.
type legacyThreadPointSource interface {
	ScrollAndOffset(context.Context, *qdrant.ScrollPoints) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error)
}

// captureLegacyThreadPoints scrolls one collection without changing it. The
// caller/service boundary must prove writers are stopped; a successful read
// alone is not a consistent production snapshot.
func captureLegacyThreadPoints(ctx context.Context, source legacyThreadPointSource, collection string) ([]threadmigration.QdrantPointSnapshot, error) {
	if legacyThreadCaptureNilInterface(ctx) || legacyThreadCaptureNilInterface(source) {
		return nil, errLegacyThreadCaptureInvalid
	}
	if err := legacyThreadCaptureContextErr(ctx); err != nil {
		return nil, err
	}
	if !validLegacyThreadCaptureCollection(collection) {
		return nil, errLegacyThreadCaptureInvalid
	}

	limit := legacyThreadCapturePageLimit
	points := make([]threadmigration.QdrantPointSnapshot, 0)
	seenPointIDs := make(map[string]struct{})
	var offset *qdrant.PointId
	var previousCursor string
	vectorDimension := 0
	var serializedQdrantBytes int64

	for {
		if err := legacyThreadCaptureContextErr(ctx); err != nil {
			return nil, err
		}
		request := &qdrant.ScrollPoints{
			CollectionName: collection,
			Offset:         legacyThreadCaptureClonePointID(offset),
			Limit:          &limit,
			WithPayload:    qdrant.NewWithPayload(true),
			WithVectors:    qdrant.NewWithVectors(true),
		}
		page, next, err := source.ScrollAndOffset(ctx, request)
		if err != nil {
			if ctxErr := legacyThreadCaptureContextErr(ctx); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, errLegacyThreadCaptureFailed
		}
		if err := legacyThreadCaptureContextErr(ctx); err != nil {
			return nil, err
		}
		if len(page) == 0 && next != nil {
			return nil, errLegacyThreadCaptureInvalid
		}
		if len(page) > threadmigration.QdrantPreparationMaxPoints-len(points) {
			return nil, errLegacyThreadCaptureLimit
		}

		pagePointIDs := make([]string, 0, len(page))
		for _, point := range page {
			snapshot, err := legacyThreadCapturePoint(point)
			if err != nil {
				return nil, errLegacyThreadCaptureInvalid
			}
			if vectorDimension == 0 {
				vectorDimension = len(snapshot.Vector)
			} else if len(snapshot.Vector) != vectorDimension {
				return nil, errLegacyThreadCaptureInvalid
			}
			if _, exists := seenPointIDs[snapshot.PointID]; exists {
				return nil, errLegacyThreadCaptureInvalid
			}
			serialized, err := json.Marshal(snapshot)
			if err != nil {
				return nil, errLegacyThreadCaptureInvalid
			}
			separator := int64(0)
			if serializedQdrantBytes > 0 {
				separator = 1
			}
			if serializedQdrantBytes > threadmigration.ExternalSnapshotMaxFileBytes-int64(len(serialized))-separator {
				return nil, errLegacyThreadCaptureLimit
			}
			serializedQdrantBytes += int64(len(serialized)) + separator
			seenPointIDs[snapshot.PointID] = struct{}{}
			pagePointIDs = append(pagePointIDs, snapshot.PointID)
			points = append(points, snapshot)
		}

		if next == nil {
			sort.Slice(points, func(left, right int) bool { return points[left].PointID < points[right].PointID })
			if err := legacyThreadCaptureContextErr(ctx); err != nil {
				return nil, err
			}
			snapshot, err := threadmigration.NewExternalSnapshot(nil, points)
			if err != nil {
				return nil, errLegacyThreadCaptureInvalid
			}
			canonical, err := snapshot.CanonicalJSON()
			if err != nil {
				return nil, errLegacyThreadCaptureInvalid
			}
			if int64(len(canonical)) > threadmigration.ExternalSnapshotMaxFileBytes {
				return nil, errLegacyThreadCaptureLimit
			}
			if err := legacyThreadCaptureContextErr(ctx); err != nil {
				return nil, err
			}
			return snapshot.Qdrant, nil
		}
		nextID, err := legacyThreadCapturePointID(next)
		if err != nil {
			return nil, errLegacyThreadCaptureInvalid
		}
		if previousCursor != "" && nextID <= previousCursor {
			return nil, errLegacyThreadCaptureInvalid
		}
		if len(pagePointIDs) > 0 {
			pageMaximum := pagePointIDs[0]
			for _, pointID := range pagePointIDs[1:] {
				if pointID > pageMaximum {
					pageMaximum = pointID
				}
			}
			if nextID < pageMaximum {
				return nil, errLegacyThreadCaptureInvalid
			}
		}
		previousCursor = nextID
		offset = legacyThreadCaptureClonePointID(next)
	}
}

// CaptureLegacyThreadPoints opens only the configured Qdrant client and reads
// the requested collection. It never initializes a collection or performs an
// upsert/delete. Collection writers and snapshot consistency are owned by the
// caller/service boundary.
func CaptureLegacyThreadPoints(ctx context.Context, qdrantURL, collection string) (points []threadmigration.QdrantPointSnapshot, err error) {
	if legacyThreadCaptureNilInterface(ctx) {
		return nil, errLegacyThreadCaptureInvalid
	}
	if ctxErr := legacyThreadCaptureContextErr(ctx); ctxErr != nil {
		return nil, ctxErr
	}
	if !validLegacyThreadCaptureCollection(collection) {
		return nil, errLegacyThreadCaptureInvalid
	}
	host, port, ok := parseLegacyThreadCaptureEndpoint(qdrantURL)
	if !ok {
		return nil, errLegacyThreadCaptureInvalid
	}
	client, clientErr := qdrant.NewClient(&qdrant.Config{
		Host:                   host,
		Port:                   port,
		SkipCompatibilityCheck: true,
	})
	if clientErr != nil {
		return nil, errLegacyThreadCaptureFailed
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil && err == nil {
			points = nil
			err = errLegacyThreadCaptureFailed
		}
	}()
	return captureLegacyThreadPoints(ctx, client, collection)
}

func legacyThreadCapturePoint(point *qdrant.RetrievedPoint) (threadmigration.QdrantPointSnapshot, error) {
	if point == nil {
		return threadmigration.QdrantPointSnapshot{}, errLegacyThreadCaptureInvalid
	}
	pointID, err := legacyThreadCapturePointID(point.GetId())
	if err != nil {
		return threadmigration.QdrantPointSnapshot{}, err
	}
	vector, err := legacyThreadCaptureDenseVector(point.GetVectors())
	if err != nil {
		return threadmigration.QdrantPointSnapshot{}, err
	}
	payload, err := legacyThreadCapturePayload(point.GetPayload())
	if err != nil {
		return threadmigration.QdrantPointSnapshot{}, err
	}
	return threadmigration.QdrantPointSnapshot{
		PointID: pointID,
		Vector:  vector,
		Payload: payload,
	}, nil
}

func legacyThreadCapturePointID(pointID *qdrant.PointId) (string, error) {
	if pointID == nil {
		return "", errLegacyThreadCaptureInvalid
	}
	uuidOption, ok := pointID.GetPointIdOptions().(*qdrant.PointId_Uuid)
	if !ok || uuidOption == nil {
		return "", errLegacyThreadCaptureInvalid
	}
	parsed, err := uuid.Parse(uuidOption.Uuid)
	if err != nil || parsed.String() != uuidOption.Uuid {
		return "", errLegacyThreadCaptureInvalid
	}
	return uuidOption.Uuid, nil
}

func legacyThreadCaptureClonePointID(pointID *qdrant.PointId) *qdrant.PointId {
	if pointID == nil {
		return nil
	}
	return &qdrant.PointId{PointIdOptions: &qdrant.PointId_Uuid{Uuid: pointID.GetUuid()}}
}

func legacyThreadCaptureDenseVector(vectors *qdrant.VectorsOutput) ([]float32, error) {
	if vectors == nil || vectors.GetVectors() != nil {
		return nil, errLegacyThreadCaptureInvalid
	}
	vectorOutput := vectors.GetVector()
	if vectorOutput == nil {
		return nil, errLegacyThreadCaptureInvalid
	}
	switch variant := vectorOutput.GetVector().(type) {
	case nil:
		if len(vectorOutput.GetData()) == 0 || vectorOutput.GetIndices() != nil || vectorOutput.VectorsCount != nil {
			return nil, errLegacyThreadCaptureInvalid
		}
	case *qdrant.VectorOutput_Dense:
		if variant == nil || variant.Dense == nil || len(vectorOutput.GetData()) != 0 || vectorOutput.GetIndices() != nil || vectorOutput.VectorsCount != nil {
			return nil, errLegacyThreadCaptureInvalid
		}
	default:
		return nil, errLegacyThreadCaptureInvalid
	}
	dense := vectorOutput.GetDenseVector()
	if dense == nil || len(dense.GetData()) == 0 {
		return nil, errLegacyThreadCaptureInvalid
	}
	vector := append([]float32(nil), dense.GetData()...)
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, errLegacyThreadCaptureInvalid
		}
	}
	return vector, nil
}

func legacyThreadCapturePayload(payload map[string]*qdrant.Value) (map[string]json.RawMessage, error) {
	if payload == nil {
		return nil, errLegacyThreadCaptureInvalid
	}
	result := make(map[string]json.RawMessage, len(payload))
	for key, value := range payload {
		if !utf8.ValidString(key) {
			return nil, errLegacyThreadCaptureInvalid
		}
		raw, err := legacyThreadCaptureJSONValue(value, 0)
		if err != nil {
			return nil, err
		}
		result[key] = append(json.RawMessage(nil), raw...)
	}
	return result, nil
}

func legacyThreadCaptureJSONValue(value *qdrant.Value, depth int) (json.RawMessage, error) {
	if value == nil || depth > legacyThreadCaptureMaxJSONDepth {
		return nil, errLegacyThreadCaptureInvalid
	}
	switch kind := value.GetKind().(type) {
	case *qdrant.Value_NullValue:
		if kind == nil || kind.NullValue != qdrant.NullValue_NULL_VALUE {
			return nil, errLegacyThreadCaptureInvalid
		}
		return json.RawMessage("null"), nil
	case *qdrant.Value_DoubleValue:
		if kind == nil || math.IsNaN(kind.DoubleValue) || math.IsInf(kind.DoubleValue, 0) {
			return nil, errLegacyThreadCaptureInvalid
		}
		encoded, err := json.Marshal(kind.DoubleValue)
		if err != nil {
			return nil, errLegacyThreadCaptureInvalid
		}
		return encoded, nil
	case *qdrant.Value_IntegerValue:
		if kind == nil {
			return nil, errLegacyThreadCaptureInvalid
		}
		return json.RawMessage(strconv.FormatInt(kind.IntegerValue, 10)), nil
	case *qdrant.Value_StringValue:
		if kind == nil || !utf8.ValidString(kind.StringValue) {
			return nil, errLegacyThreadCaptureInvalid
		}
		encoded, err := json.Marshal(kind.StringValue)
		if err != nil {
			return nil, errLegacyThreadCaptureInvalid
		}
		return encoded, nil
	case *qdrant.Value_BoolValue:
		if kind == nil {
			return nil, errLegacyThreadCaptureInvalid
		}
		if kind.BoolValue {
			return json.RawMessage("true"), nil
		}
		return json.RawMessage("false"), nil
	case *qdrant.Value_StructValue:
		if kind == nil || kind.StructValue == nil {
			return nil, errLegacyThreadCaptureInvalid
		}
		return legacyThreadCaptureJSONObject(kind.StructValue.Fields, depth)
	case *qdrant.Value_ListValue:
		if kind == nil || kind.ListValue == nil {
			return nil, errLegacyThreadCaptureInvalid
		}
		values := kind.ListValue.Values
		encoded := []byte{'['}
		for index, child := range values {
			raw, err := legacyThreadCaptureJSONValue(child, depth+1)
			if err != nil {
				return nil, err
			}
			if index > 0 {
				encoded = append(encoded, ',')
			}
			encoded = append(encoded, raw...)
		}
		return append(encoded, ']'), nil
	default:
		return nil, errLegacyThreadCaptureInvalid
	}
}

func legacyThreadCaptureJSONObject(fields map[string]*qdrant.Value, depth int) (json.RawMessage, error) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if !utf8.ValidString(key) {
			return nil, errLegacyThreadCaptureInvalid
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	encoded := []byte{'{'}
	for index, key := range keys {
		value, err := legacyThreadCaptureJSONValue(fields[key], depth+1)
		if err != nil {
			return nil, err
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, errLegacyThreadCaptureInvalid
		}
		if index > 0 {
			encoded = append(encoded, ',')
		}
		encoded = append(encoded, encodedKey...)
		encoded = append(encoded, ':')
		encoded = append(encoded, value...)
	}
	return append(encoded, '}'), nil
}

func validLegacyThreadCaptureCollection(collection string) bool {
	if !utf8.ValidString(collection) || strings.TrimSpace(collection) == "" || utf8.RuneCountInString(collection) > legacyThreadCaptureMaxNameRunes {
		return false
	}
	for _, character := range collection {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func parseLegacyThreadCaptureEndpoint(endpoint string) (string, int, bool) {
	if !utf8.ValidString(endpoint) {
		return "", 0, false
	}
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil || !utf8.ValidString(host) || strings.TrimSpace(host) == "" {
		return "", 0, false
	}
	for _, character := range host {
		if unicode.IsControl(character) {
			return "", 0, false
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, false
	}
	return host, port, true
}

func legacyThreadCaptureNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func legacyThreadCaptureContextErr(ctx context.Context) error {
	if ctx == nil {
		return errLegacyThreadCaptureInvalid
	}
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errLegacyThreadCaptureInvalid
}
