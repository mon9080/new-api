package helper

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

const codexCapacityMessage = "Selected model is at capacity. Please try a different model."

func TestCodexResponseGuard(t *testing.T) {
	tests := []struct {
		name            string
		reasoningTokens int
		want            bool
	}{
		{name: "matches first boundary", reasoningTokens: 516, want: true},
		{name: "matches second boundary", reasoningTokens: 1034, want: true},
		{name: "matches third boundary", reasoningTokens: 1552, want: true},
		{name: "matches fourth boundary", reasoningTokens: 2070, want: true},
		{name: "does not match zero", reasoningTokens: 0, want: false},
		{name: "does not match lower neighbor", reasoningTokens: 515, want: false},
		{name: "does not match upper neighbor", reasoningTokens: 517, want: false},
		{name: "does not match middle value", reasoningTokens: 1533, want: false},
		{name: "does not match negative value", reasoningTokens: -2, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCodexResponseGuardReasoningTokens(tt.reasoningTokens); got != tt.want {
				t.Fatalf("IsCodexResponseGuardReasoningTokens(%d) = %t, want %t", tt.reasoningTokens, got, tt.want)
			}
		})
	}
}

func TestShouldBlockCodexResponse(t *testing.T) {
	if ShouldBlockCodexResponse(nil) {
		t.Fatal("nil response must not be blocked")
	}
	if ShouldBlockCodexResponse(&dto.OpenAIResponsesResponse{}) {
		t.Fatal("response without usage must not be blocked")
	}

	blocked := &dto.OpenAIResponsesResponse{
		Usage: &dto.Usage{
			CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 516},
		},
	}
	if !ShouldBlockCodexResponse(blocked) {
		t.Fatal("response with reasoning_tokens=516 must be blocked")
	}
}

func TestShouldBlockCodexResponseWithOpenAIResponsesUsage(t *testing.T) {
	var response dto.OpenAIResponsesResponse
	err := common.Unmarshal([]byte(`{"usage":{"output_tokens_details":{"reasoning_tokens":516}}}`), &response)
	require.NoError(t, err)
	require.True(t, ShouldBlockCodexResponse(&response))
}

func TestCodexResponseGuardReasoningTokensPrecedence(t *testing.T) {
	// OutputTokensDetails 优先于 CompletionTokenDetails
	tokens, ok := CodexResponseGuardReasoningTokens(&dto.OpenAIResponsesResponse{
		Usage: &dto.Usage{
			OutputTokensDetails:    &dto.OutputTokenDetails{ReasoningTokens: 516},
			CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 1034},
		},
	})
	require.True(t, ok)
	require.Equal(t, 516, tokens)

	// 回退 CompletionTokenDetails
	tokens, ok = CodexResponseGuardReasoningTokens(&dto.OpenAIResponsesResponse{
		Usage: &dto.Usage{
			CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 1034},
		},
	})
	require.True(t, ok)
	require.Equal(t, 1034, tokens)
}

func TestCodexResponseGuardStatusCodeRules(t *testing.T) {
	settings := dto.ChannelOtherSettings{CodexResponseGuardStatusCodes: "429,500-503"}

	matched, err := MatchCodexResponseGuardStatusCode(settings, 429)
	require.NoError(t, err)
	require.True(t, matched)

	matched, err = MatchCodexResponseGuardStatusCode(settings, 502)
	require.NoError(t, err)
	require.True(t, matched)

	matched, err = MatchCodexResponseGuardStatusCode(settings, 504)
	require.NoError(t, err)
	require.False(t, matched)
}

func TestCodexResponseGuardStatusCodeRulesRejectInvalidValues(t *testing.T) {
	for _, rule := range []string{"399", "600", "500-400", "invalid"} {
		t.Run(rule, func(t *testing.T) {
			_, err := MatchCodexResponseGuardStatusCode(
				dto.ChannelOtherSettings{CodexResponseGuardStatusCodes: rule},
				500,
			)
			require.Error(t, err)
		})
	}
}

func TestCodexResponseGuardErrorMessageRules(t *testing.T) {
	t.Run("missing configuration uses capacity default", func(t *testing.T) {
		settings := dto.ChannelOtherSettings{}
		require.True(t, MatchCodexResponseGuardErrorMessage(settings, "  SELECTED MODEL IS AT CAPACITY. PLEASE TRY A DIFFERENT MODEL.  "))
	})

	t.Run("explicit empty configuration stays empty", func(t *testing.T) {
		empty := ""
		settings := dto.ChannelOtherSettings{CodexResponseGuardErrorMessages: &empty}
		require.False(t, MatchCodexResponseGuardErrorMessage(settings, codexCapacityMessage))
	})

	t.Run("matches normalized configured fragments", func(t *testing.T) {
		messages := "temporary failure\nselected model is at capacity\n..."
		settings := dto.ChannelOtherSettings{CodexResponseGuardErrorMessages: &messages}
		require.True(t, MatchCodexResponseGuardErrorMessage(settings, "Upstream: TEMPORARY—FAILURE; retry later"))
		require.True(t, MatchCodexResponseGuardErrorMessage(settings, "Error: Selected MODEL is at capacity!!! Please retry."))
		require.False(t, MatchCodexResponseGuardErrorMessage(settings, "temporarily unavailable"))
		require.False(t, MatchCodexResponseGuardErrorMessage(settings, "ordinary upstream error"))
	})
}

func TestCodexResponseGuardRetryTimes(t *testing.T) {
	require.Equal(t, 5, CodexResponseGuardRetryTimes(dto.ChannelOtherSettings{}))
	require.Equal(t, 3, CodexResponseGuardRetryTimes(dto.ChannelOtherSettings{CodexResponseGuardRetryTimes: 3}))
	require.Equal(t, 20, CodexResponseGuardRetryTimes(dto.ChannelOtherSettings{CodexResponseGuardRetryTimes: 21}))
}

func TestCodexResponseGuardErrorMessageJSONCompatibility(t *testing.T) {
	var legacy dto.ChannelOtherSettings
	require.NoError(t, common.Unmarshal([]byte(`{"codex_response_guard_enabled":true}`), &legacy))
	require.Nil(t, legacy.CodexResponseGuardErrorMessages)
	require.True(t, MatchCodexResponseGuardErrorMessage(legacy, codexCapacityMessage))

	var explicitlyCleared dto.ChannelOtherSettings
	require.NoError(t, common.Unmarshal([]byte(`{"codex_response_guard_enabled":true,"codex_response_guard_error_messages":""}`), &explicitlyCleared))
	require.NotNil(t, explicitlyCleared.CodexResponseGuardErrorMessages)
	require.False(t, MatchCodexResponseGuardErrorMessage(explicitlyCleared, codexCapacityMessage))
}
