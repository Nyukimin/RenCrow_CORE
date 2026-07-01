package core

import (
	"encoding/json"
	"net/http"
)

const (
	JSONContentType = "application/json"
)

func RequireHTTPMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func WriteJSON(w http.ResponseWriter, value any) error {
	w.Header().Set("Content-Type", JSONContentType)
	return json.NewEncoder(w).Encode(value)
}
