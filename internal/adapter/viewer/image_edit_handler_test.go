package viewer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/imagegateway"
)

type editPartFixture struct {
	name        string
	filename    string
	contentType string
	body        []byte
}

type recordingImageEditGateway struct {
	request domainimage.EditRequest
	hits    int
	result  domainimage.GenerateResult
	err     error
}

func (g *recordingImageEditGateway) Edit(_ context.Context, request domainimage.EditRequest) (domainimage.GenerateResult, error) {
	g.hits++
	g.request = request
	if g.err != nil {
		return domainimage.GenerateResult{}, g.err
	}
	if g.result.ID == "" {
		g.result = domainimage.GenerateResult{
			OK: true, ID: "img_edit_1", Profile: "pose-profile",
			Image: domainimage.ImageResult{ID: "img_edit_1", ContentType: "image/png", Width: 640, Height: 480},
		}
	}
	return g.result, nil
}

func TestHandleImageEditValidatesAndRewritesResultURL(t *testing.T) {
	person := testPNG(t, 3, 2)
	reference := testJPEG(t, 2, 3)
	recorder := &recordingImageEditGateway{}
	request := imageEditRequest(t, validEditParts(person, reference, "pose", "-1", "match the pose"))
	response := httptest.NewRecorder()
	HandleImageEdit(recorder).ServeHTTP(response, request)

	if response.Code != http.StatusOK || recorder.hits != 1 {
		t.Fatalf("status=%d hits=%d body=%s", response.Code, recorder.hits, response.Body.String())
	}
	if recorder.request.Operation != "pose" || recorder.request.Seed != -1 || recorder.request.Prompt != "match the pose" ||
		!bytes.Equal(recorder.request.Person, person) || recorder.request.PersonContentType != "image/png" ||
		!bytes.Equal(recorder.request.Reference, reference) || recorder.request.ReferenceContentType != "image/jpeg" {
		t.Fatalf("gateway request did not preserve validated fields and bytes: %+v", recorder.request)
	}
	var body struct {
		OK    bool   `json:"ok"`
		ID    string `json:"id"`
		Image struct {
			ID          string `json:"id"`
			URL         string `json:"url"`
			ContentType string `json:"content_type"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
		} `json:"image"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.OK || body.ID != "img_edit_1" || body.Image.URL != "/viewer/image/result?id=img_edit_1" || body.Image.ContentType != "image/png" {
		t.Fatalf("unexpected edit result: %+v", body)
	}
}

func TestHandleImageEditRejectsInvalidInputWithoutGatewayCall(t *testing.T) {
	validPerson := testPNG(t, 2, 2)
	validReference := testJPEG(t, 2, 2)
	widerImage := pngHeaderOnly(12001, 1)
	largeImage := pngHeaderOnly(8000, 5001)
	overImageLimit := append(bytes.Clone(validPerson), bytes.Repeat([]byte{0}, maxImageEditImageBytes-len(validPerson)+1)...)
	cases := []struct {
		name      string
		parts     []editPartFixture
		bodyLimit int64
	}{
		{name: "missing reference", parts: []editPartFixture{fieldPart("operation", "pose"), filePart("person", "person.png", "image/png", validPerson), fieldPart("prompt", "edit"), fieldPart("seed", "-1")}},
		{name: "duplicate seed", parts: append(validEditParts(validPerson, validReference, "pose", "-1", "edit"), fieldPart("seed", "5"))},
		{name: "unexpected field", parts: append(validEditParts(validPerson, validReference, "pose", "-1", "edit"), fieldPart("workflow", "arbitrary"))},
		{name: "invalid operation", parts: validEditParts(validPerson, validReference, "../workflow", "-1", "edit")},
		{name: "malformed seed", parts: validEditParts(validPerson, validReference, "clothing", "1.5", "edit")},
		{name: "negative seed other than random", parts: validEditParts(validPerson, validReference, "clothing", "-2", "edit")},
		{name: "invalid prompt UTF-8", parts: append(validEditParts(validPerson, validReference, "pose", "2", ""), fieldBytesPart("prompt", []byte{0xff, 0xfe}))},
		{name: "prompt too large", parts: append(validEditParts(validPerson, validReference, "pose", "2", ""), fieldPart("prompt", strings.Repeat("x", maxImageEditPromptBytes+1)))},
		{name: "file with non-image MIME type", parts: replaceEditPart(validEditParts(validPerson, validReference, "pose", "2", "edit"), "person", filePart("person", "person.bin", "application/octet-stream", validPerson))},
		{name: "MIME does not match image bytes", parts: replaceEditPart(validEditParts(validPerson, validReference, "pose", "2", "edit"), "person", filePart("person", "person.jpg", "image/jpeg", validPerson))},
		{name: "image too wide", parts: replaceEditPart(validEditParts(validPerson, validReference, "pose", "2", "edit"), "person", filePart("person", "wide.png", "image/png", widerImage))},
		{name: "image exceeds megapixel limit", parts: replaceEditPart(validEditParts(validPerson, validReference, "pose", "2", "edit"), "person", filePart("person", "large.png", "image/png", largeImage))},
		{name: "image exceeds byte limit", parts: replaceEditPart(validEditParts(validPerson, validReference, "pose", "2", "edit"), "person", filePart("person", "large.png", "image/png", overImageLimit))},
		{name: "body exceeds total limit", parts: validEditParts(validPerson, validReference, "pose", "2", "edit"), bodyLimit: maxImageEditBodyBytes + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &recordingImageEditGateway{}
			request := imageEditRequest(t, tc.parts)
			if tc.bodyLimit > 0 {
				request.ContentLength = tc.bodyLimit
			}
			response := httptest.NewRecorder()
			HandleImageEdit(gateway).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || gateway.hits != 0 {
				t.Fatalf("status=%d gateway hits=%d body=%s", response.Code, gateway.hits, response.Body.String())
			}
		})
	}
}

func TestHandleImageEditRequiresNonBlankPrompt(t *testing.T) {
	person := testPNG(t, 2, 2)
	reference := testJPEG(t, 2, 2)
	baseParts := []editPartFixture{
		fieldPart("operation", "pose"),
		filePart("person", "person.png", "image/png", person),
		filePart("reference", "reference.jpg", "image/jpeg", reference),
		fieldPart("seed", "1"),
	}
	for _, tc := range []struct {
		name  string
		parts []editPartFixture
	}{
		{name: "missing", parts: baseParts},
		{name: "empty", parts: append(append([]editPartFixture(nil), baseParts...), fieldPart("prompt", ""))},
		{name: "whitespace only", parts: append(append([]editPartFixture(nil), baseParts...), fieldPart("prompt", " \t\n\r"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &recordingImageEditGateway{}
			response := httptest.NewRecorder()
			HandleImageEdit(gateway).ServeHTTP(response, imageEditRequest(t, tc.parts))
			if response.Code != http.StatusBadRequest || gateway.hits != 0 {
				t.Fatalf("status=%d gateway hits=%d body=%s", response.Code, gateway.hits, response.Body.String())
			}
		})
	}
}

func TestHandleImageEditMapsImageBadRequestAndUnavailable(t *testing.T) {
	person := testPNG(t, 2, 2)
	reference := testJPEG(t, 2, 2)
	for _, tc := range []struct {
		name    string
		gateway ImageEditGateway
		want    int
	}{
		{name: "Image 400", gateway: &recordingImageEditGateway{err: &imagegateway.ServiceError{StatusCode: http.StatusBadRequest, Code: "IMAGE_INVALID_REQUEST", Message: "edit validation failed"}}, want: http.StatusBadRequest},
		{name: "Image 503", gateway: &recordingImageEditGateway{err: &imagegateway.ServiceError{StatusCode: http.StatusServiceUnavailable}}, want: http.StatusServiceUnavailable},
		{name: "disabled gateway", gateway: nil, want: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := imageEditRequest(t, validEditParts(person, reference, "pose", "1", "edit"))
			HandleImageEdit(tc.gateway).ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.want, response.Body.String())
			}
		})
	}
}

func imageEditRequest(t *testing.T, parts []editPartFixture) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, fixture := range parts {
		header := make(textproto.MIMEHeader)
		params := map[string]string{"name": fixture.name}
		if fixture.filename != "" {
			params["filename"] = fixture.filename
		}
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", params))
		if fixture.contentType != "" {
			header.Set("Content-Type", fixture.contentType)
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create multipart fixture: %v", err)
		}
		if _, err := part.Write(fixture.body); err != nil {
			t.Fatalf("write multipart fixture: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart fixture: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/viewer/image/edit", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func validEditParts(person, reference []byte, operation, seed, prompt string) []editPartFixture {
	parts := []editPartFixture{
		fieldPart("operation", operation),
		filePart("person", "person.png", "image/png", person),
		filePart("reference", "reference.jpg", "image/jpeg", reference),
	}
	if prompt != "" {
		parts = append(parts, fieldPart("prompt", prompt))
	}
	return append(parts, fieldPart("seed", seed))
}

func fieldPart(name, value string) editPartFixture {
	return fieldBytesPart(name, []byte(value))
}

func fieldBytesPart(name string, value []byte) editPartFixture {
	return editPartFixture{name: name, body: value}
}

func filePart(name, filename, contentType string, value []byte) editPartFixture {
	return editPartFixture{name: name, filename: filename, contentType: contentType, body: value}
}

func replaceEditPart(parts []editPartFixture, name string, replacement editPartFixture) []editPartFixture {
	for i, part := range parts {
		if part.name == name {
			parts[i] = replacement
			return parts
		}
	}
	return append(parts, replacement)
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	return encoded.Bytes()
}

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatalf("encode JPEG fixture: %v", err)
	}
	return encoded.Bytes()
}

func pngHeaderOnly(width, height uint32) []byte {
	payload := make([]byte, 13)
	binary.BigEndian.PutUint32(payload[0:4], width)
	binary.BigEndian.PutUint32(payload[4:8], height)
	payload[8], payload[9] = 8, 2
	chunk := append([]byte("IHDR"), payload...)
	result := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	result = binary.BigEndian.AppendUint32(result, uint32(len(payload)))
	result = append(result, chunk...)
	result = binary.BigEndian.AppendUint32(result, crc32.ChecksumIEEE(chunk))
	return result
}
