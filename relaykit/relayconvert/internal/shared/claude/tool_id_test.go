package claude

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeToolID(t *testing.T) {
	validToolID := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	tests := []struct {
		name   string
		toolID string
		want   string
	}{
		{name: "valid", toolID: "call_abc-123", want: "call_abc-123"},
		{name: "invalid characters", toolID: "call.test:1"},
		{name: "empty", toolID: ""},
		{name: "too long", toolID: strings.Repeat("a", 65)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeToolID(tt.toolID)
			assert.True(t, validToolID.MatchString(got))
			if tt.want != "" {
				assert.Equal(t, tt.want, got)
			} else {
				assert.Equal(t, got, NormalizeToolID(tt.toolID))
				assert.NotEqual(t, tt.toolID, got)
			}
		})
	}

	assert.NotEqual(t, NormalizeToolID("call.a"), NormalizeToolID("call:a"))
}
