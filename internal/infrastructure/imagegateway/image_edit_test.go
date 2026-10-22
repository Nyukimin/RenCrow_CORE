package imagegateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
)

func TestClientEditUsesCanonicalOrderedMultipartAndPreservesImageBytes(t *testing.T) {
	person := []byte("person-png-fixture")
	reference := []byte("reference-jpeg-fixture")
	wantNames := []string{"operation", "person", "reference", "prompt", "seed"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/edits" {
			http.Error(w, "wrong image edit route", http.StatusNotFound)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
			http.Error(w, "expected multipart", http.StatusBadRequest)
			return
		}
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		var names []string
		values := map[string][]byte{}
		contentTypes := map[string]string{}
		for {
			part, err := reader.NextRawPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "bad part", http.StatusBadRequest)
				return
			}
			name := part.FormName()
			names = append(names, name)
			values[name], err = io.ReadAll(part)
			if err != nil {
				http.Error(w, "bad part body", http.StatusBadRequest)
				return
			}
			contentTypes[name] = part.Header.Get("Content-Type")
		}
		if !reflect.DeepEqual(names, wantNames) || string(values["operation"]) != "pose" ||
			!bytes.Equal(values["person"], person) || !bytes.Equal(values["reference"], reference) ||
			string(values["prompt"]) != "match the reference" || string(values["seed"]) != "-1" ||
			contentTypes["person"] != "image/png" || contentTypes["reference"] != "image/jpeg" {
			http.Error(w, fmt.Sprintf("wrong edit request: names=%v content-types=%v", names, contentTypes), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, validEditResponseJSON("pose"))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := client.Edit(context.Background(), domainimage.EditRequest{
		Operation: "pose", Person: person, PersonContentType: "image/png",
		Reference: reference, ReferenceContentType: "image/jpeg", Prompt: "match the reference", Seed: -1,
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if !result.OK || result.ID != "img_edit_23" || result.Created != 1761091200 ||
		result.Profile != "qwen-image-edit-2511-int8" || result.Prompt != "match the reference" ||
		result.Image.ID != result.ID || result.Image.Path != "/v1/images/img_edit_23.png" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClientEditRejectsMalformedUpstreamResponses(t *testing.T) {
	validResponse := validEditResponseJSON("clothing")
	validSHA256 := validEditResponseSHA256()
	cases := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "invalid image id", contentType: "application/json", body: strings.Replace(validResponse, `"id":"img_edit_23"`, `"id":"../secret"`, 2)},
		{name: "mismatched image id", contentType: "application/json", body: strings.Replace(validResponse, `"image":{"id":"img_edit_23"`, `"image":{"id":"img_other"`, 1)},
		{name: "wrong content type", contentType: "application/json", body: strings.Replace(validResponse, `"content_type":"image/png"`, `"content_type":"image/jpeg"`, 1)},
		{name: "invalid dimensions", contentType: "application/json", body: strings.Replace(validResponse, `"width":512`, `"width":0`, 1)},
		{name: "unknown JSON field", contentType: "application/json", body: strings.TrimSuffix(validResponse, `}`) + `,"unexpected":true}`},
		{name: "missing operation", contentType: "application/json", body: strings.Replace(validResponse, `"operation":"clothing",`, ``, 1)},
		{name: "wrong operation", contentType: "application/json", body: strings.Replace(validResponse, `"operation":"clothing"`, `"operation":"pose"`, 1)},
		{name: "missing image checksum", contentType: "application/json", body: strings.Replace(validResponse, `,"sha256":"`+validSHA256+`"`, ``, 1)},
		{name: "missing image size", contentType: "application/json", body: strings.Replace(validResponse, `,"size_bytes":20`, ``, 1)},
		{name: "invalid image checksum", contentType: "application/json", body: strings.Replace(validResponse, `"sha256":"`+validSHA256+`"`, `"sha256":"bad"`, 1)},
		{name: "trailing JSON value", contentType: "application/json", body: validResponse + ` {}`},
		{name: "wrong response media type", contentType: "text/plain", body: validResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, 2*time.Second)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if _, err := client.Edit(context.Background(), validGatewayEditRequest()); err == nil {
				t.Fatal("Edit accepted malformed upstream response")
			}
		})
	}
}

func validEditResponseJSON(operation string) string {
	imageBytes := []byte("rendered-png-fixture")
	digest := sha256.Sum256(imageBytes)
	return fmt.Sprintf(`{"ok":true,"id":"img_edit_23","created":1761091200,"profile":"qwen-image-edit-2511-int8","prompt":"match the reference","operation":"%s","image":{"id":"img_edit_23","path":"/v1/images/img_edit_23.png","content_type":"image/png","width":512,"height":512,"sha256":"%s","size_bytes":%d}}`, operation, hex.EncodeToString(digest[:]), len(imageBytes))
}

func validEditResponseSHA256() string {
	digest := sha256.Sum256([]byte("rendered-png-fixture"))
	return hex.EncodeToString(digest[:])
}

func TestClientEditMapsImageBadRequestAndBoundsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || len(raw) == 0 {
			http.Error(w, "missing request body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error_code":"IMAGE_INVALID_REQUEST","message":"unsupported edit operation"}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Edit(context.Background(), validGatewayEditRequest())
	serviceError, ok := err.(*ServiceError)
	if !ok || serviceError.StatusCode != http.StatusBadRequest {
		t.Fatalf("error=%v, want Image 400 ServiceError", err)
	}

	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("x", maxJSONResponseBytes+1))
	}))
	defer oversized.Close()
	client, err = NewClient(oversized.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient for oversized response: %v", err)
	}
	if _, err := client.Edit(context.Background(), validGatewayEditRequest()); err == nil {
		t.Fatal("Edit accepted oversized upstream response")
	}
}

func TestClientEditOptionsValidatesOperationsContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/images/edits/options" {
			http.Error(w, "wrong options route", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"operations":["pose","clothing"]}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	options, err := client.EditOptions(context.Background())
	if err != nil {
		t.Fatalf("EditOptions: %v", err)
	}
	if !options.OK || !reflect.DeepEqual(options.Operations, []string{"pose", "clothing"}) {
		t.Fatalf("unexpected edit options: %+v", options)
	}

	for _, body := range []string{
		`{"ok":true,"operations":["pose","workflow"]}`,
		`{"ok":true,"operations":["pose","pose"]}`,
		`{"ok":true,"operations":["pose"],"path":"/private"}`,
		`{"ok":true,"operations":["pose"]} {}`,
	} {
		t.Run(body, func(t *testing.T) {
			badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			}))
			defer badServer.Close()
			badClient, err := NewClient(badServer.URL, 2*time.Second)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if _, err := badClient.EditOptions(context.Background()); err == nil {
				t.Fatal("EditOptions accepted invalid response")
			}
		})
	}
}

func validGatewayEditRequest() domainimage.EditRequest {
	return domainimage.EditRequest{
		Operation: "clothing", Person: []byte("person"), PersonContentType: "image/png",
		Reference: []byte("reference"), ReferenceContentType: "image/jpeg", Seed: 1,
	}
}
