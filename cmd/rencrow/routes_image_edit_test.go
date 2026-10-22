package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestRegisterImageRoutesProxiesImageEditAndReturnsCoreResultURL(t *testing.T) {
	person := routeImagePNG(t, 2, 2)
	reference := routeImagePNG(t, 3, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/images/edits/options" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true,"operations":["pose","clothing"]}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/edits" {
			http.Error(w, "wrong Image route", http.StatusNotFound)
			return
		}
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "invalid multipart", http.StatusBadRequest)
			return
		}
		var fields []string
		values := make(map[string][]byte)
		for {
			part, err := reader.NextRawPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "invalid part", http.StatusBadRequest)
				return
			}
			fields = append(fields, part.FormName())
			values[part.FormName()], err = io.ReadAll(part)
			if err != nil {
				http.Error(w, "invalid part data", http.StatusBadRequest)
				return
			}
		}
		if strings.Join(fields, ",") != "operation,person,reference,seed" || string(values["operation"]) != "clothing" ||
			!bytes.Equal(values["person"], person) || !bytes.Equal(values["reference"], reference) || string(values["seed"]) != "12" {
			http.Error(w, "wrong image edit contract", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"id":"img_route_edit","profile":"clothing","image":{"id":"img_route_edit","content_type":"image/png","width":32,"height":32}}`)
	}))
	defer server.Close()

	mux := http.NewServeMux()
	registerImageRoutes(mux, &config.Config{Image: config.ImageConfig{
		Enabled: true, BaseURL: server.URL, TimeoutMS: 1000,
	}})
	body, contentType := routeImageEditMultipart(t, person, reference)
	optionsResponse := httptest.NewRecorder()
	mux.ServeHTTP(optionsResponse, httptest.NewRequest(http.MethodGet, "/viewer/image/edit/options", nil))
	if optionsResponse.Code != http.StatusOK || !strings.Contains(optionsResponse.Body.String(), `"operations":["pose","clothing"]`) {
		t.Fatalf("GET /viewer/image/edit/options status=%d body=%s", optionsResponse.Code, optionsResponse.Body.String())
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/image/edit", body)
	request.Header.Set("Content-Type", contentType)
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("POST /viewer/image/edit status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"url":"/viewer/image/result?id=img_route_edit"`) {
		t.Fatalf("response did not rewrite the result URL through CORE: %s", response.Body.String())
	}
}

func routeImageEditMultipart(t *testing.T, person, reference []byte) (io.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writeText := func(name, value string) {
		t.Helper()
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeFile := func(name, filename string, content []byte) {
		t.Helper()
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": name, "filename": filename}))
		header.Set("Content-Type", "image/png")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeText("operation", "clothing")
	writeFile("person", "person.png", person)
	writeFile("reference", "reference.png", reference)
	writeText("seed", "12")
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return bytes.NewReader(body.Bytes()), writer.FormDataContentType()
}

func routeImagePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var result bytes.Buffer
	if err := png.Encode(&result, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode image fixture: %v", err)
	}
	return result.Bytes()
}
