package poll

import (
	"strings"
	"testing"
)

func TestValidateCreate(t *testing.T) {
	tests := []struct {
		name     string
		question string
		options  []string
		wantErr  error
	}{
		{"valid", "Tabs or spaces?", []string{"Tabs", "Spaces"}, nil},
		{"valid max options", "Q?", []string{"a", "b", "c", "d", "e"}, nil},
		{"blank question", "   ", []string{"a", "b"}, ErrBlankQuestion},
		{"oversized question", strings.Repeat("x", MaxQuestionLen+1), []string{"a", "b"}, ErrQuestionTooLong},
		{"too few options", "Q?", []string{"only one"}, ErrOptionCount},
		{"too many options", "Q?", []string{"a", "b", "c", "d", "e", "f"}, ErrOptionCount},
		{"blank option", "Q?", []string{"a", "   "}, ErrBlankOption},
		{"oversized option", "Q?", []string{"a", strings.Repeat("x", MaxLabelLen+1)}, ErrOptionTooLong},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreate(tt.question, tt.options)
			if err != tt.wantErr {
				t.Fatalf("ValidateCreate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewID(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if len(id) != idLen {
		t.Fatalf("len(id) = %d, want %d", len(id), idLen)
	}

	id2, err := NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if id == id2 {
		t.Fatalf("two calls to NewID() returned the same value: %q", id)
	}
}
