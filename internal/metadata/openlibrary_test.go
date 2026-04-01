package metadata

import (
	"testing"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"this is a long string", 10, "this is a ..."},
		{"exact fit!", 10, "exact fit!"},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestOlWork_DescriptionText(t *testing.T) {
	tests := []struct {
		name string
		work olWork
		want string
	}{
		{
			name: "string description",
			work: olWork{Description: "A great book about things."},
			want: "A great book about things.",
		},
		{
			name: "object description",
			work: olWork{Description: map[string]interface{}{"value": "A great book.", "type": "/type/text"}},
			want: "A great book.",
		},
		{
			name: "nil description",
			work: olWork{Description: nil},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.work.descriptionText()
			if got != tt.want {
				t.Errorf("descriptionText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewClient(t *testing.T) {
	c := NewClient(nil)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	if c.cache == nil {
		t.Error("cache not initialized")
	}
	if c.httpClient == nil {
		t.Error("httpClient not initialized")
	}
}
