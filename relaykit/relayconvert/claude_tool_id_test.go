package relayconvert

import (
	"context"
	"regexp"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIChatToClaudeNormalizesToolIDs(t *testing.T) {
	maxTokens := uint(64)
	assistant := dto.Message{Role: "assistant", Content: nil}
	assistant.SetToolCalls([]dto.ToolCallRequest{
		{
			ID:   "call.test:1",
			Type: "function",
			Function: dto.FunctionRequest{
				Name:      "echo",
				Arguments: `{"text":"hello"}`,
			},
		},
	})

	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), &convmeta.Values{}, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "user", Content: "call echo"},
			assistant,
			{Role: "tool", ToolCallId: "call.test:1", Content: "hello"},
		},
	})
	require.NoError(t, err)
	require.Len(t, got.Messages, 3)

	toolUseParts, err := got.Messages[1].ParseContent()
	require.NoError(t, err)
	require.Len(t, toolUseParts, 2)
	assert.Equal(t, "tool_use", toolUseParts[1].Type)
	assert.Equal(t, map[string]any{"text": "hello"}, toolUseParts[1].Input)

	toolResultParts, err := got.Messages[2].ParseContent()
	require.NoError(t, err)
	require.Len(t, toolResultParts, 1)
	assert.Equal(t, "tool_result", toolResultParts[0].Type)

	assertClaudeToolIDsMatch(t, toolUseParts[1].Id, toolResultParts[0].ToolUseId)
}

func TestOpenAIResponsesToClaudeNormalizesToolIDs(t *testing.T) {
	maxOutputTokens := uint(64)
	got, err := OpenAIResponsesRequestToClaudeMessages(context.Background(), &convmeta.Values{}, &dto.OpenAIResponsesRequest{
		Model:           "claude-test",
		MaxOutputTokens: &maxOutputTokens,
		Input: mustRawMessage(t, []map[string]any{
			{
				"type":      "function_call",
				"call_id":   "call.test:1",
				"name":      "echo",
				"arguments": map[string]any{"text": "hello"},
			},
			{
				"type":    "function_call_output",
				"call_id": "call.test:1",
				"output":  "hello",
			},
		}),
	})
	require.NoError(t, err)
	require.Len(t, got.Messages, 3)

	toolUseParts, err := got.Messages[1].ParseContent()
	require.NoError(t, err)
	require.Len(t, toolUseParts, 1)
	assert.Equal(t, "tool_use", toolUseParts[0].Type)
	assert.Equal(t, map[string]any{"text": "hello"}, toolUseParts[0].Input)

	toolResultParts, err := got.Messages[2].ParseContent()
	require.NoError(t, err)
	require.Len(t, toolResultParts, 1)
	assert.Equal(t, "tool_result", toolResultParts[0].Type)
	assert.Equal(t, "hello", toolResultParts[0].Content)

	assertClaudeToolIDsMatch(t, toolUseParts[0].Id, toolResultParts[0].ToolUseId)
}

func assertClaudeToolIDsMatch(t *testing.T, toolUseID string, toolResultID string) {
	t.Helper()
	validToolID := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	assert.True(t, validToolID.MatchString(toolUseID))
	assert.Equal(t, toolUseID, toolResultID)
	assert.NotEqual(t, "call.test:1", toolUseID)
}
