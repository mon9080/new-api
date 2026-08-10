package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOaiResponsesGuardTestContext(t *testing.T, body string, guardEnabled bool) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	common.SetContextKey(c, constant.ContextKeyChannelAutoBan, true)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         33,
			UpstreamModelName: "gpt-5.6-sol",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				CodexResponseGuardEnabled: guardEnabled,
			},
		},
	}
	return c, recorder, resp, info
}

func TestOaiResponsesHandlerCodexResponseGuard(t *testing.T) {
	const guardedBody = `{"object":"response","usage":{"input_tokens":2,"output_tokens":516,"total_tokens":518,"output_tokens_details":{"reasoning_tokens":516}}}`

	t.Run("returns retryable error before writing guarded response", func(t *testing.T) {
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, guardedBody, true)

		usage, err := OaiResponsesHandler(c, info, resp)

		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadGateway, err.StatusCode)
		require.Equal(t, types.ErrorCodeCodexResponseGuard, err.GetErrorCode())
		require.Empty(t, recorder.Body.String())
	})

	t.Run("passes guarded-shaped response when channel guard is disabled", func(t *testing.T) {
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, guardedBody, false)

		usage, err := OaiResponsesHandler(c, info, resp)

		require.Nil(t, err)
		require.NotNil(t, usage)
		require.Equal(t, 516, usage.CompletionTokens)
		require.Equal(t, guardedBody, recorder.Body.String())
	})

	t.Run("passes nonmatching response when channel guard is enabled", func(t *testing.T) {
		const ordinaryBody = `{"object":"response","usage":{"input_tokens":2,"output_tokens":515,"total_tokens":517,"completion_tokens_details":{"reasoning_tokens":515}}}`
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, ordinaryBody, true)

		usage, err := OaiResponsesHandler(c, info, resp)

		require.Nil(t, err)
		require.NotNil(t, usage)
		require.Equal(t, 515, usage.CompletionTokens)
		require.Equal(t, ordinaryBody, recorder.Body.String())
	})

	t.Run("converts configured protocol error before writing response", func(t *testing.T) {
		const errorBody = `{"object":"response","status":"failed","error":{"type":"server_overloaded","message":"Selected model is at capacity. Please try a different model."}}`
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, errorBody, true)

		usage, err := OaiResponsesHandler(c, info, resp)

		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadGateway, err.StatusCode)
		require.Equal(t, types.ErrorCodeCodexResponseGuard, err.GetErrorCode())
		require.Equal(t, 5, err.GetRetryLimit())
		require.True(t, types.IsSkipAutoDisableError(err))
		require.Empty(t, recorder.Body.String())
	})
}

func TestOaiResponsesStreamHandlerCodexResponseGuard(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	const guardedStream = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.6-sol\"}}\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"must not reach client\"}\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":516,\"total_tokens\":518,\"output_tokens_details\":{\"reasoning_tokens\":516}}}}\n" +
		"data: [DONE]\n"

	t.Run("returns retryable error without writing a partial stream", func(t *testing.T) {
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, guardedStream, true)
		resp.Header.Set("Content-Type", "text/event-stream")
		info.IsStream = true

		usage, err := OaiResponsesStreamHandler(c, info, resp)

		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadGateway, err.StatusCode)
		require.Equal(t, types.ErrorCodeCodexResponseGuard, err.GetErrorCode())
		require.Empty(t, recorder.Body.String())
	})

	t.Run("replays the complete stream when guard is disabled", func(t *testing.T) {
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, guardedStream, false)
		resp.Header.Set("Content-Type", "text/event-stream")
		info.IsStream = true

		usage, err := OaiResponsesStreamHandler(c, info, resp)

		require.Nil(t, err)
		require.NotNil(t, usage)
		require.Equal(t, 516, usage.CompletionTokens)
		require.Contains(t, recorder.Body.String(), "must not reach client")
	})

	t.Run("returns retryable error when usage arrives in a compatible event without a type", func(t *testing.T) {
		stream := "data: {\"response\":{\"usage\":{\"output_tokens_details\":{\"reasoning_tokens\":516}}}}\n" +
			"data: [DONE]\n"
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, stream, true)
		resp.Header.Set("Content-Type", "text/event-stream")
		info.IsStream = true

		usage, err := OaiResponsesStreamHandler(c, info, resp)

		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadGateway, err.StatusCode)
		require.Empty(t, recorder.Body.String())
	})

	t.Run("converts top-level capacity error without leaking buffered stream", func(t *testing.T) {
		stream := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n" +
			"data: {\"type\":\"error\",\"message\":\"Selected model is at capacity. Please try a different model.\"}\n" +
			"data: [DONE]\n"
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, stream, true)
		resp.Header.Set("Content-Type", "text/event-stream")
		info.IsStream = true

		usage, err := OaiResponsesStreamHandler(c, info, resp)

		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadGateway, err.StatusCode)
		require.Equal(t, types.ErrorCodeCodexResponseGuard, err.GetErrorCode())
		require.Empty(t, recorder.Body.String())
	})

	t.Run("preserves top-level error when automatic disable keyword matches", func(t *testing.T) {
		originalEnabled := common.AutomaticDisableChannelEnabled
		originalKeywords := operation_setting.AutomaticDisableKeywords
		originalRanges := operation_setting.AutomaticDisableStatusCodeRanges
		t.Cleanup(func() {
			common.AutomaticDisableChannelEnabled = originalEnabled
			operation_setting.AutomaticDisableKeywords = originalKeywords
			operation_setting.AutomaticDisableStatusCodeRanges = originalRanges
		})
		common.AutomaticDisableChannelEnabled = true
		operation_setting.AutomaticDisableKeywords = []string{"selected model is at capacity"}
		operation_setting.AutomaticDisableStatusCodeRanges = nil

		stream := "data: {\"type\":\"error\",\"message\":\"Selected model is at capacity. Please try a different model.\"}\n" +
			"data: [DONE]\n"
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, stream, true)
		resp.Header.Set("Content-Type", "text/event-stream")
		info.IsStream = true

		usage, err := OaiResponsesStreamHandler(c, info, resp)

		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusOK, err.StatusCode)
		require.False(t, types.IsSkipAutoDisableError(err))
		require.True(t, service.ShouldDisableChannel(err))
		require.Empty(t, recorder.Body.String())
	})

	t.Run("uses guard retry when automatic disable matches but channel auto-ban is disabled", func(t *testing.T) {
		originalEnabled := common.AutomaticDisableChannelEnabled
		originalKeywords := operation_setting.AutomaticDisableKeywords
		originalRanges := operation_setting.AutomaticDisableStatusCodeRanges
		t.Cleanup(func() {
			common.AutomaticDisableChannelEnabled = originalEnabled
			operation_setting.AutomaticDisableKeywords = originalKeywords
			operation_setting.AutomaticDisableStatusCodeRanges = originalRanges
		})
		common.AutomaticDisableChannelEnabled = true
		operation_setting.AutomaticDisableKeywords = []string{"selected model is at capacity"}
		operation_setting.AutomaticDisableStatusCodeRanges = nil

		stream := "data: {\"type\":\"error\",\"message\":\"Selected model is at capacity. Please try a different model.\"}\n" +
			"data: [DONE]\n"
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, stream, true)
		common.SetContextKey(c, constant.ContextKeyChannelAutoBan, false)
		resp.Header.Set("Content-Type", "text/event-stream")
		info.IsStream = true

		usage, err := OaiResponsesStreamHandler(c, info, resp)

		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadGateway, err.StatusCode)
		require.Equal(t, types.ErrorCodeCodexResponseGuard, err.GetErrorCode())
		require.True(t, types.IsSkipAutoDisableError(err))
		require.Empty(t, recorder.Body.String())
	})

	t.Run("converts response failed error without leaking buffered stream", func(t *testing.T) {
		stream := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n" +
			"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"type\":\"server_overloaded\",\"message\":\"Selected model is at capacity. Please try a different model.\"}}}\n" +
			"data: [DONE]\n"
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, stream, true)
		resp.Header.Set("Content-Type", "text/event-stream")
		info.IsStream = true

		usage, err := OaiResponsesStreamHandler(c, info, resp)

		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadGateway, err.StatusCode)
		require.Empty(t, recorder.Body.String())
	})

	t.Run("converts response error without leaking buffered stream", func(t *testing.T) {
		stream := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n" +
			"data: {\"type\":\"response.error\",\"response\":{\"status\":\"failed\",\"error\":{\"type\":\"server_overloaded\",\"message\":\"Selected model is at capacity. Please try a different model.\"}}}\n" +
			"data: [DONE]\n"
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, stream, true)
		resp.Header.Set("Content-Type", "text/event-stream")
		info.IsStream = true

		usage, err := OaiResponsesStreamHandler(c, info, resp)

		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadGateway, err.StatusCode)
		require.Equal(t, types.ErrorCodeCodexResponseGuard, err.GetErrorCode())
		require.Empty(t, recorder.Body.String())
	})

	t.Run("does not scan normal model output for configured error text", func(t *testing.T) {
		stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Selected model is at capacity. Please try a different model.\"}\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":8,\"total_tokens\":10}}}\n" +
			"data: [DONE]\n"
		c, recorder, resp, info := newOaiResponsesGuardTestContext(t, stream, true)
		resp.Header.Set("Content-Type", "text/event-stream")
		info.IsStream = true

		usage, err := OaiResponsesStreamHandler(c, info, resp)

		require.Nil(t, err)
		require.NotNil(t, usage)
		require.Contains(t, recorder.Body.String(), "Selected model is at capacity. Please try a different model.")
	})
}
