package viewer

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	videoSessionCookieName = "RenCrowVideoSession"
	videoSessionCookiePath = "/viewer/image/video"
	videoSessionTTL        = 12 * time.Hour
	maxVideoCredentialBody = 4 << 10
	maxVideoCredentialSize = 512
)

var videoBrowserActorIDPattern = regexp.MustCompile(`^user:[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func HandleVideoSession(auth VideoBrowserAuthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.videoRequestAllowed(r) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			if _, ok := auth.videoActorID(r); !ok {
				writeVideoSessionJSON(w, http.StatusUnauthorized, false)
				return
			}
			writeVideoSessionJSON(w, http.StatusOK, true)
		case http.MethodPost:
			credential, ok := readVideoCredential(w, r)
			if !ok {
				return
			}
			if !auth.verifyVideoCredential(credential) {
				writeVideoSessionJSON(w, http.StatusUnauthorized, false)
				return
			}
			cookie, err := auth.newSessionCookie(time.Now())
			if err != nil {
				http.Error(w, "Video session is unavailable.", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			http.SetCookie(w, cookie)
			writeVideoSessionJSON(w, http.StatusOK, true)
		case http.MethodDelete:
			w.Header().Set("Cache-Control", "no-store")
			http.SetCookie(w, &http.Cookie{
				Name: videoSessionCookieName, Value: "", Path: videoSessionCookiePath,
				Expires: time.Unix(1, 0).UTC(), MaxAge: -1,
				Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func (c VideoBrowserAuthConfig) videoActorID(r *http.Request) (string, bool) {
	if !c.videoRequestAllowed(r) || !c.hasValidVideoSession(r, time.Now()) {
		return "", false
	}
	return c.ActorID, true
}

func (c VideoBrowserAuthConfig) videoRequestAllowed(r *http.Request) bool {
	if r == nil || !c.Enabled || !videoBrowserActorIDPattern.MatchString(c.ActorID) ||
		!videoSHA256Pattern.MatchString(c.CredentialSHA256) || !videoSHA256Pattern.MatchString(c.CookieHMACKeyHex) ||
		c.Host == "" || c.Origin == "" || !videoLoopbackRemoteAddr(r.RemoteAddr) || r.Host != c.Host ||
		hasHeaderName(r.Header, "Tailscale-Funnel-Request") {
		return false
	}
	originValues := headerValuesCaseInsensitive(r.Header, "Origin")
	requiresOrigin := r.Method == http.MethodPost || r.Method == http.MethodDelete
	if requiresOrigin && len(originValues) != 1 {
		return false
	}
	if len(originValues) > 0 && (len(originValues) != 1 || originValues[0] == "null" || originValues[0] != c.Origin) {
		return false
	}
	return true
}

func videoLoopbackRemoteAddr(raw string) bool {
	host, port, err := net.SplitHostPort(raw)
	if err != nil || host == "" {
		return false
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c VideoBrowserAuthConfig) verifyVideoCredential(credential string) bool {
	if len(credential) < 32 || len(credential) > maxVideoCredentialSize {
		return false
	}
	expected, err := hex.DecodeString(c.CredentialSHA256)
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	actual := sha256.Sum256([]byte(credential))
	return subtle.ConstantTimeCompare(expected, actual[:]) == 1
}

func readVideoCredential(w http.ResponseWriter, r *http.Request) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "application/json is required", http.StatusUnsupportedMediaType)
		return "", false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxVideoCredentialBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var payload struct {
		Credential string `json:"credential"`
	}
	if err := decoder.Decode(&payload); err != nil || payload.Credential == "" || len(payload.Credential) > maxVideoCredentialSize {
		http.Error(w, "Invalid video credential request.", http.StatusBadRequest)
		return "", false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(w, "Invalid video credential request.", http.StatusBadRequest)
		return "", false
	}
	return payload.Credential, true
}

func (c VideoBrowserAuthConfig) newSessionCookie(now time.Time) (*http.Cookie, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	expires := now.Add(videoSessionTTL).UTC().Truncate(time.Second)
	value, err := c.signVideoSession(expires.Unix(), nonce)
	if err != nil {
		return nil, err
	}
	return &http.Cookie{
		Name: videoSessionCookieName, Value: value, Path: videoSessionCookiePath,
		Expires: expires, MaxAge: int(videoSessionTTL.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	}, nil
}

func (c VideoBrowserAuthConfig) signVideoSession(expiresUnix int64, nonce []byte) (string, error) {
	key, ok := c.videoCookieHMACKey()
	if !ok || !videoBrowserActorIDPattern.MatchString(c.ActorID) || len(nonce) != 32 || expiresUnix <= 0 {
		return "", errors.New("video session signing configuration is invalid")
	}
	expiry := strconv.FormatInt(expiresUnix, 10)
	nonceValue := base64.RawURLEncoding.EncodeToString(nonce)
	message := strings.Join([]string{"video-session-v1", c.ActorID, c.Host, c.Origin, expiry, nonceValue}, "\n")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return strings.Join([]string{"v1", expiry, nonceValue, signature}, "."), nil
}

func (c VideoBrowserAuthConfig) hasValidVideoSession(r *http.Request, now time.Time) bool {
	var sessionCookie *http.Cookie
	for _, cookie := range r.Cookies() {
		if cookie.Name != videoSessionCookieName {
			continue
		}
		if sessionCookie != nil {
			return false
		}
		sessionCookie = cookie
	}
	if sessionCookie == nil {
		return false
	}
	return c.verifyVideoSession(sessionCookie.Value, now)
}

func (c VideoBrowserAuthConfig) verifyVideoSession(value string, now time.Time) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return false
	}
	expiresUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expiresUnix <= now.Unix() || expiresUnix > now.Add(videoSessionTTL+time.Minute).Unix() {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(nonce) != 32 || base64.RawURLEncoding.EncodeToString(nonce) != parts[2] {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(signature) != sha256.Size || base64.RawURLEncoding.EncodeToString(signature) != parts[3] {
		return false
	}
	expected, err := c.signVideoSession(expiresUnix, nonce)
	if err != nil {
		return false
	}
	expectedParts := strings.Split(expected, ".")
	if len(expectedParts) != 4 {
		return false
	}
	expectedSignature, err := base64.RawURLEncoding.DecodeString(expectedParts[3])
	return err == nil && hmac.Equal(signature, expectedSignature)
}

func (c VideoBrowserAuthConfig) videoCookieHMACKey() ([]byte, bool) {
	if !videoSHA256Pattern.MatchString(c.CookieHMACKeyHex) {
		return nil, false
	}
	key, err := hex.DecodeString(c.CookieHMACKeyHex)
	return key, err == nil && len(key) == sha256.Size
}

func writeVideoSessionJSON(w http.ResponseWriter, status int, authenticated bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Authenticated bool `json:"authenticated"`
	}{Authenticated: authenticated})
}
