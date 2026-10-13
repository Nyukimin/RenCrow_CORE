package stt

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

// CORE -> RenCrow_STT contract: POST multipart/form-data with the WAV in
// `file` (audio.wav) and response_format=json. Nothing else is sent; model and
// language belong to RenCrow_STT.
func TestGatewayProviderRequestContract(t *testing.T) {
	type seen struct {
		method, path, requestID string
		fields                  map[string]string
		fileName                string
		file                    []byte
		err                     error
	}
	got := make(chan seen, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := seen{method: r.Method, path: r.URL.Path, requestID: r.Header.Get("X-RenCrow-Request-ID"), fields: map[string]string{}}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			s.err = err
			got <- s
			return
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				s.err = err
				break
			}
			body, _ := io.ReadAll(part)
			if part.FormName() == "file" {
				s.fileName, s.file = part.FileName(), body
			} else {
				s.fields[part.FormName()] = string(body)
			}
		}
		got <- s
		_, _ = w.Write([]byte(`{"text":"ok","language":"ja"}`))
	}))
	defer server.Close()

	wav := contractTestWAV(3200)
	if _, err := (GatewayProvider{URL: server.URL + "/v1/audio/transcriptions", Timeout: time.Second}).Transcribe(context.Background(), wav); err != nil {
		t.Fatal(err)
	}
	s := <-got
	if s.err != nil {
		t.Fatalf("request was not valid multipart/form-data: %v", s.err)
	}
	if s.method != http.MethodPost || s.path != "/v1/audio/transcriptions" {
		t.Fatalf("request = %s %s", s.method, s.path)
	}
	if s.fileName != "audio.wav" || string(s.file) != string(wav) {
		t.Fatalf("file part name=%q equal=%t", s.fileName, string(s.file) == string(wav))
	}
	var names []string
	for name := range s.fields {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != "response_format" || s.fields["response_format"] != "json" {
		t.Fatalf("form fields = %v, want only response_format=json", s.fields)
	}
	if s.requestID == "" {
		t.Fatal("X-RenCrow-Request-ID header is part of the contract")
	}
}

// contractTestWAV builds a minimal canonical 16 kHz mono PCM16 WAV of n data bytes.
func contractTestWAV(n int) []byte {
	out := make([]byte, 44+n)
	copy(out[0:4], "RIFF")
	out[4] = byte(36 + n)
	out[5] = byte((36 + n) >> 8)
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	out[16] = 16
	out[20] = 1
	out[22] = 1
	out[24] = 0x80
	out[25] = 0x3e
	out[28] = 0x00
	out[29] = 0x7d
	out[32] = 2
	out[34] = 16
	copy(out[36:40], "data")
	out[40] = byte(n)
	out[41] = byte(n >> 8)
	return out
}
