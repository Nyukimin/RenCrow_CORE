package tool

import (
	"strings"
	"testing"
)

func TestValidatePath(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{"/home/user/file.txt", false},
		{"./relative/path", false},
		{"../traversal", true},
		{"/some/../path", true},
		{"safe/path/here", false},
		{"", false},
	}
	for _, tt := range tests {
		err := ValidatePath(tt.path)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidatePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
		}
		if err != nil && err.Code != ErrValidationFailed {
			t.Errorf("ValidatePath(%q) code = %s, want VALIDATION_FAILED", tt.path, err.Code)
		}
	}
}

func TestValidateNoControlChars(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"normal text", false},
		{"with\nnewline", false},
		{"with\ttab", false},
		{"with\rcarriage", false},
		{"with\x00null", true},
		{"with\x01soh", true},
		{"with\x7fDEL", true},
		{"日本語テキスト", false},
		{"", false},
	}
	for _, tt := range tests {
		err := ValidateNoControlChars(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateNoControlChars(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
		}
	}
}

func TestValidateID(t *testing.T) {
	tests := []struct {
		id      string
		wantErr bool
	}{
		{"valid-id-123", false},
		{"also_valid", false},
		{"has?query", true},
		{"has#hash", true},
		{"has/slash", true},
		{"has\\backslash", true},
		{"", false},
	}
	for _, tt := range tests {
		err := ValidateID(tt.id)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
		}
	}
}

func TestValidateLength(t *testing.T) {
	tests := []struct {
		input   string
		maxLen  int
		wantErr bool
	}{
		{"short", 10, false},
		{"exact", 5, false},
		{"toolong", 5, true},
		{"", 0, false},
		{strings.Repeat("x", 1001), 1000, true},
	}
	for _, tt := range tests {
		err := ValidateLength(tt.input, tt.maxLen)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateLength(len=%d, max=%d) error = %v, wantErr %v", len(tt.input), tt.maxLen, err, tt.wantErr)
		}
	}
}

func TestValidateNoDoubleEncoding(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"normal%20space", false},
		{"double%2520encoded", true},
		{"has%25percent", true},
		{"clean", false},
		{"", false},
	}
	for _, tt := range tests {
		err := ValidateNoDoubleEncoding(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateNoDoubleEncoding(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
		}
	}
}

func TestValidateNotEmpty(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"value", false},
		{"", true},
		{"   ", true},
		{"\t\n", true},
	}
	for _, tt := range tests {
		err := ValidateNotEmpty(tt.input, "field")
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateNotEmpty(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
		}
	}
}
