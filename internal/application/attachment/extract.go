package attachment

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxExtractedTextBytes = 32 << 10

func extractAttachmentText(filename, contentType string, data []byte) (string, string, bool) {
	if len(data) == 0 {
		return "", "", false
	}
	ct := normalizedContentType(contentType)
	ext := strings.ToLower(filepath.Ext(filename))
	switch {
	case ct == "application/pdf" || ext == ".pdf":
		return extractPDFText(data)
	case strings.HasPrefix(ct, "text/") || isTextDocumentExtension(ext) || isTextDocumentContentType(ct):
		return extractPlainDocumentText(data)
	default:
		return "", "", false
	}
}

func extractPlainDocumentText(data []byte) (string, string, bool) {
	if !utf8.Valid(data) {
		return "", "document is not valid UTF-8 text", false
	}
	text := strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n"))
	text = strings.ReplaceAll(text, "\r", "\n")
	return limitExtractedText(text)
}

func extractPDFText(data []byte) (string, string, bool) {
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("%PDF-")) {
		return "", "pdf header not found", false
	}
	var parts []string
	for i := 0; i < len(data); i++ {
		if data[i] != '(' {
			continue
		}
		text, next, ok := readPDFLiteralString(data, i+1)
		if !ok {
			continue
		}
		if text = strings.TrimSpace(text); text != "" {
			parts = append(parts, text)
		}
		i = next
	}
	if len(parts) == 0 {
		return "", "pdf text not found", false
	}
	return limitExtractedText(strings.Join(parts, "\n"))
}

func readPDFLiteralString(data []byte, start int) (string, int, bool) {
	var out strings.Builder
	depth := 1
	for i := start; i < len(data); i++ {
		ch := data[i]
		if ch == '\\' {
			if i+1 >= len(data) {
				return "", i, false
			}
			i++
			next := data[i]
			switch next {
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			case 'b':
				out.WriteByte('\b')
			case 'f':
				out.WriteByte('\f')
			case '(', ')', '\\':
				out.WriteByte(next)
			default:
				if next >= '0' && next <= '7' {
					val := int(next - '0')
					for n := 0; n < 2 && i+1 < len(data) && data[i+1] >= '0' && data[i+1] <= '7'; n++ {
						i++
						val = val*8 + int(data[i]-'0')
					}
					out.WriteByte(byte(val))
				} else {
					out.WriteByte(next)
				}
			}
			continue
		}
		switch ch {
		case '(':
			depth++
			out.WriteByte(ch)
		case ')':
			depth--
			if depth == 0 {
				return out.String(), i, true
			}
			out.WriteByte(ch)
		default:
			out.WriteByte(ch)
		}
	}
	return "", len(data), false
}

func limitExtractedText(text string) (string, string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "", false
	}
	if len(text) <= maxExtractedTextBytes {
		return text, "", false
	}
	for cut := maxExtractedTextBytes; cut > 0; cut-- {
		if utf8.RuneStart(text[cut]) {
			return strings.TrimSpace(text[:cut]), fmt.Sprintf("extracted text truncated at %d bytes", maxExtractedTextBytes), true
		}
	}
	return strings.TrimSpace(text[:maxExtractedTextBytes]), fmt.Sprintf("extracted text truncated at %d bytes", maxExtractedTextBytes), true
}

func normalizedContentType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

func isTextDocumentExtension(ext string) bool {
	switch ext {
	case ".txt", ".md", ".json", ".csv", ".yaml", ".yml", ".xml":
		return true
	default:
		return false
	}
}

func isTextDocumentContentType(ct string) bool {
	switch ct {
	case "application/json", "application/x-yaml", "application/yaml", "application/xml":
		return true
	default:
		return false
	}
}
