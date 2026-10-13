package stt

import (
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// zeroReader yields zero bytes forever; wrap it in io.LimitReader.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// uploadSize is well above the cap but finite, so a handler without the cap
// fails the test instead of filling the disk.
const uploadSize = 80 << 20

// An upload is read through ParseMultipartForm, which spills everything above
// its memory budget to temporary files. Without a cap on the request body, a
// client could fill the disk. The handler must stop reading at the cap.
func TestHandlerRefusesAnUploadOverTheLimitWithoutReadingItAll(t *testing.T) {
	for _, path := range []string{"/stt/file", "/stt/chat-input"} {
		t.Run(path, func(t *testing.T) {
			pr, pw := io.Pipe()
			mw := multipart.NewWriter(pw)
			go func() {
				part, err := mw.CreateFormFile("file", "audio.wav")
				if err == nil {
					// "RIFF....WAVE" so that only the size, not the format, is what gets refused.
					_, err = part.Write([]byte("RIFF\x00\x00\x00\x00WAVE"))
				}
				if err == nil {
					_, err = io.Copy(part, io.LimitReader(zeroReader{}, uploadSize))
				}
				pw.CloseWithError(err)
			}()
			counted := &countingReader{r: pr}
			req := httptest.NewRequest(http.MethodPost, path, counted)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			rec := httptest.NewRecorder()
			h := NewHandler(MockProvider{Text: "unreachable"})

			if path == "/stt/file" {
				h.File(rec, req)
			} else {
				h.ChatInput(rec, req)
			}
			_ = pr.Close() // unblock the writer
			if req.MultipartForm != nil {
				_ = req.MultipartForm.RemoveAll()
			}

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
			}
			if read := counted.n.Load(); read > maxUploadBytes+(1<<20) {
				t.Fatalf("handler consumed %d bytes of an %d byte upload; the cap is %d", read, int64(uploadSize), int64(maxUploadBytes))
			}
		})
	}
}
