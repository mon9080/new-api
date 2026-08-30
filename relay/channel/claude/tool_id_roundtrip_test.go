package claude

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIChatToolIDRoundTrip(t *testing.T) {
	service.InitHttpClient()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var upstreamValidationErr error
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request dto.ClaudeRequest
				if err := common.DecodeJson(r.Body, &request); err != nil {
					upstreamValidationErr = fmt.Errorf("decode Claude request: %w", err)
					http.Error(w, upstreamValidationErr.Error(), http.StatusBadRequest)
					return
				}

				upstreamValidationErr = validateClaudeToolIDs(request)
				if upstreamValidationErr != nil {
					http.Error(w, upstreamValidationErr.Error(), http.StatusBadRequest)
					return
				}

				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range []string{
						`{"type":"message_start","message":{"id":"msg_tool_id_ok","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":12,"output_tokens":1}}}`,
						`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
						`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"TOOL_ID_OK"}}`,
						`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
						`{"type":"message_stop"}`,
					} {
						_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
					}
					return
				}

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_tool_id_ok","type":"message","role":"assistant","content":[{"type":"text","text":"TOOL_ID_OK"}],"model":"claude-test","stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":3}}`))
			}))
			defer upstream.Close()

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			ctx.Request.Header.Set("Content-Type", "application/json")
			ctx.Set(common.RequestIdKey, "tool-id-roundtrip")

			info := &relaycommon.RelayInfo{
				RelayFormat:        types.RelayFormatOpenAI,
				IsStream:           stream,
				ShouldIncludeUsage: true,
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelBaseUrl:    upstream.URL,
					ApiKey:            "test-key",
					UpstreamModelName: "claude-test",
				},
			}

			request := toolIDRoundTripRequest(stream)
			adaptor := &Adaptor{}
			converted, err := adaptor.ConvertOpenAIRequest(ctx, info, request)
			require.NoError(t, err)

			jsonData, err := common.Marshal(converted)
			require.NoError(t, err)
			requestBody, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
			require.NoError(t, err)
			defer closer.Close()

			responseAny, err := adaptor.DoRequest(ctx, info, requestBody)
			require.NoError(t, err)
			require.NoError(t, upstreamValidationErr)
			response, ok := responseAny.(*http.Response)
			require.True(t, ok)
			require.Equal(t, http.StatusOK, response.StatusCode)

			usage, relayErr := adaptor.DoResponse(ctx, response, info)
			require.Nil(t, relayErr)
			require.IsType(t, &dto.Usage{}, usage)
			assert.Positive(t, usage.(*dto.Usage).PromptTokens)
			assert.Positive(t, usage.(*dto.Usage).CompletionTokens)

			if stream {
				assertOpenAIStreamSuccess(t, recorder.Body.String())
			} else {
				assertOpenAINonStreamSuccess(t, recorder.Body.Bytes())
			}
		})
	}
}

func toolIDRoundTripRequest(stream bool) *dto.GeneralOpenAIRequest {
	maxTokens := uint(64)
	assistant := dto.Message{Role: "assistant", Content: nil}
	assistant.SetToolCalls([]dto.ToolCallRequest{
		{
			ID:   "call_test.bad:1",
			Type: "function",
			Function: dto.FunctionRequest{
				Name:      "echo",
				Arguments: `{"text":"hello"}`,
			},
		},
	})

	return &dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Stream:    &stream,
		Messages: []dto.Message{
			{Role: "user", Content: "call echo"},
			assistant,
			{Role: "tool", ToolCallId: "call_test.bad:1", Content: "hello"},
		},
	}
}

func validateClaudeToolIDs(request dto.ClaudeRequest) error {
	var toolUseID string
	var toolResultID string
	for _, message := range request.Messages {
		parts, err := message.ParseContent()
		if err != nil {
			continue
		}
		for _, part := range parts {
			switch part.Type {
			case "tool_use":
				toolUseID = part.Id
			case "tool_result":
				toolResultID = part.ToolUseId
			}
		}
	}

	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(toolUseID) {
		return fmt.Errorf("invalid tool_use.id %q", toolUseID)
	}
	if toolUseID != toolResultID {
		return fmt.Errorf("tool ID mismatch: tool_use.id=%q tool_result.tool_use_id=%q", toolUseID, toolResultID)
	}
	if toolUseID == "call_test.bad:1" {
		return fmt.Errorf("tool ID was not normalized")
	}
	return nil
}

func assertOpenAINonStreamSuccess(t *testing.T, body []byte) {
	t.Helper()
	var response dto.OpenAITextResponse
	require.NoError(t, common.Unmarshal(body, &response))
	require.Len(t, response.Choices, 1)
	assert.Contains(t, response.Choices[0].Message.StringContent(), "TOOL_ID_OK")
	assert.Equal(t, "stop", response.Choices[0].FinishReason)
	assert.Positive(t, response.Usage.PromptTokens)
	assert.Positive(t, response.Usage.CompletionTokens)
}

func assertOpenAIStreamSuccess(t *testing.T, body string) {
	t.Helper()
	var content strings.Builder
	var finishReason string
	var usage *dto.Usage
	foundDone := false

	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "[DONE]" {
			foundDone = true
			continue
		}

		var chunk dto.ChatCompletionsStreamResponse
		require.NoError(t, common.Unmarshal([]byte(data), &chunk))
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != nil {
				content.WriteString(*choice.Delta.Content)
			}
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
			}
		}
	}

	assert.Contains(t, content.String(), "TOOL_ID_OK")
	assert.Equal(t, "stop", finishReason)
	require.NotNil(t, usage)
	assert.Positive(t, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	assert.True(t, foundDone)
}
