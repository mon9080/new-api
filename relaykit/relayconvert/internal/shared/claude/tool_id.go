package claude

import (
	"crypto/sha256"
	"encoding/base64"
)

const claudeToolIDMaxLength = 64

func NormalizeToolID(toolID string) string {
	if isValidToolID(toolID) {
		return toolID
	}

	digest := sha256.Sum256([]byte(toolID))
	return "toolu_" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func isValidToolID(toolID string) bool {
	if len(toolID) == 0 || len(toolID) > claudeToolIDMaxLength {
		return false
	}
	for _, char := range toolID {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}
