package viewer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"unicode"
)

const (
	defaultViewerOperationSource = "RenCrow_CORE_VIEWER"
	defaultViewerUserID          = "viewer-user"
	unknownViewerMetadata        = "unknown"
)

// RequestProvenance contains observation-only metadata for correlating Viewer requests.
// These client-provided values must not be used for authentication or authorization.
type RequestProvenance struct {
	OperationSource string
	InputSource     string
	UserID          string
	DeviceName      string
	SourceIPMasked  string
	SourceIPHash    string
	UserAgent       string
}

func buildViewerRequestProvenance(r *http.Request, req viewerSendRequest) (RequestProvenance, error) {
	inputSource := strings.ToLower(strings.TrimSpace(req.InputSource))
	if inputSource == "" {
		inputSource = unknownViewerMetadata
	}
	if inputSource != "text" && inputSource != "stt" && inputSource != unknownViewerMetadata {
		return RequestProvenance{}, fmt.Errorf("invalid input_source: %q", req.InputSource)
	}

	operationSource := sanitizeViewerMetadata(r.Header.Get("X-RenCrow-Client"), 80)
	if operationSource == "" {
		operationSource = defaultViewerOperationSource
	}
	userID := sanitizeViewerMetadata(req.UserID, 120)
	if userID == "" {
		userID = defaultViewerUserID
	}
	deviceName := sanitizeViewerMetadata(req.DeviceName, 120)
	if deviceName == "" {
		deviceName = unknownViewerMetadata
	}
	userAgent := sanitizeViewerMetadata(r.UserAgent(), 512)
	if userAgent == "" {
		userAgent = unknownViewerMetadata
	}

	sourceIP := viewerSourceIP(r, operationSource)
	return RequestProvenance{
		OperationSource: operationSource,
		InputSource:     inputSource,
		UserID:          userID,
		DeviceName:      deviceName,
		SourceIPMasked:  maskViewerSourceIP(sourceIP),
		SourceIPHash:    hashViewerSourceIP(sourceIP),
		UserAgent:       userAgent,
	}, nil
}

// LogFields formats escaped provenance fields for the existing text logger.
func (p RequestProvenance) LogFields() string {
	return fmt.Sprintf(
		`operation_source=%q input_source=%s user_id=%q device_name=%q source_ip_masked=%q source_ip_hash=%s user_agent=%q`,
		p.OperationSource,
		p.InputSource,
		p.UserID,
		p.DeviceName,
		p.SourceIPMasked,
		p.SourceIPHash,
		p.UserAgent,
	)
}

func sanitizeViewerMetadata(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}

func viewerSourceIP(r *http.Request, operationSource string) string {
	if operationSource == "RenCrow_PORTAL" {
		forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(forwarded) - 1; i >= 0; i-- {
			if candidate := normalizeViewerIP(forwarded[i]); candidate != "" {
				return candidate
			}
		}
	}
	return normalizeViewerIP(r.RemoteAddr)
}

func normalizeViewerIP(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return ""
}

func maskViewerSourceIP(value string) string {
	ip := net.ParseIP(value)
	if ip == nil {
		return unknownViewerMetadata
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return fmt.Sprintf("%d.%d.%d.x", ipv4[0], ipv4[1], ipv4[2])
	}
	masked := ip.Mask(net.CIDRMask(64, 128))
	return masked.String() + "/64"
}

func hashViewerSourceIP(value string) string {
	if value == "" {
		return unknownViewerMetadata
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
