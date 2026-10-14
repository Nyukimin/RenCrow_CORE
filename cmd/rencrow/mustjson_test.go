package main

import "encoding/json"

// mustJSON marshals v for tests that build request bodies; it returns "" when v
// cannot be marshalled so that the test's own assertion fails instead.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
