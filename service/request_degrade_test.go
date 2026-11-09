package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func testContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func TestStripBoundStateFromResponsesRequest(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model:   "gpt-6.1-sol",
		Store:   json.RawMessage("true"),
		Include: json.RawMessage(`["reasoning.encrypted_content","message.output_text.logprobs"]`),
		Input: json.RawMessage(`[
			{"type":"message","role":"user","id":"msg_abc","content":[{"type":"input_text","text":"hi"}]},
			{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAA"},
			{"type":"function_call","id":"fc_1","call_id":"call_9","name":"shell","arguments":"{}"}
		]`),
	}

	modified, err := stripBoundStateFromResponsesRequest(testContext(t), req)
	if err != nil {
		t.Fatalf("degrade error: %v", err)
	}
	if !modified {
		t.Fatal("expected payload to be modified")
	}

	var items []map[string]any
	if err := json.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("input unmarshal: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items after dropping reasoning, got %d", len(items))
	}
	for _, item := range items {
		if item["type"] == "reasoning" {
			t.Fatal("reasoning item must be dropped")
		}
		if _, has := item["id"]; has {
			t.Fatalf("id must be stripped from %v", item["type"])
		}
	}
	if items[1]["call_id"] != "call_9" {
		t.Fatalf("call_id must be preserved, got %v", items[1]["call_id"])
	}

	var include []string
	if err := json.Unmarshal(req.Include, &include); err != nil {
		t.Fatalf("include unmarshal: %v", err)
	}
	if len(include) != 1 || include[0] != "message.output_text.logprobs" {
		t.Fatalf("encrypted include must be removed, got %v", include)
	}
	if string(req.Store) != "true" {
		t.Fatalf("store must be left to the client, got %s", req.Store)
	}
	if req.PreviousResponseID != "" {
		t.Fatalf("previous_response_id must stay untouched, got %q", req.PreviousResponseID)
	}
}

func TestStripBoundStateRefusesServerSideSession(t *testing.T) {
	// previous_response_id / conversation 指向的历史只在服务端，剥掉会让模型
	// 失忆却仍返回 200；宁可把原始错误交还客户端。
	for _, req := range []*dto.OpenAIResponsesRequest{
		{
			PreviousResponseID: "resp_123",
			Input:              json.RawMessage(`[{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAA"},{"role":"user","content":"continue"}]`),
		},
		{
			Conversation: json.RawMessage(`"conv_abc"`),
			Input:        json.RawMessage(`[{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAA"},{"role":"user","content":"continue"}]`),
		},
	} {
		modified, err := stripBoundStateFromResponsesRequest(testContext(t), req)
		if err != nil {
			t.Fatalf("degrade error: %v", err)
		}
		if modified {
			t.Fatalf("request depending on server-side session must not be degraded: %+v", req)
		}
	}
}

func TestStripBoundStateDropsItemReference(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Input: json.RawMessage(`[{"type":"item_reference","id":"msg_123"},{"role":"user","content":"hi"}]`),
	}
	modified, err := stripBoundStateFromResponsesRequest(testContext(t), req)
	if err != nil {
		t.Fatalf("degrade error: %v", err)
	}
	if !modified {
		t.Fatal("item_reference must trigger modification")
	}
	var items []map[string]any
	if err := json.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("input unmarshal: %v", err)
	}
	// item_reference 唯一的语义就是那个上游 id，删 id 会得到非法项，必须整条删掉。
	if len(items) != 1 || items[0]["type"] == "item_reference" {
		t.Fatalf("item_reference must be dropped entirely, got %#v", items)
	}
}

func TestStripBoundStateNoopOnPlainInput(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Input: json.RawMessage(`[{"role":"user","content":"hi"}]`),
	}
	modified, err := stripBoundStateFromResponsesRequest(testContext(t), req)
	if err != nil {
		t.Fatalf("degrade error: %v", err)
	}
	if modified {
		t.Fatal("nothing to strip, payload must stay untouched")
	}
}

func TestStripBoundStateFromInputNonArray(t *testing.T) {
	raw := json.RawMessage(`"a plain prompt"`)
	res := stripBoundStateFromInput(raw)
	if res.Changed {
		t.Fatal("non-array input must not be rewritten")
	}
	if string(res.Input) != string(raw) {
		t.Fatal("non-array input must be returned unchanged")
	}
}

func TestStripBoundStateSkipsWhenInputNotSelfContained(t *testing.T) {
	// function_call_output 的 function_call 在上一条 response 里，只能靠
	// previous_response_id 看见；此时丢掉 prev id 会制造 No tool call found。
	req := &dto.OpenAIResponsesRequest{
		PreviousResponseID: "resp_123",
		Input: json.RawMessage(`[
			{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAA"},
			{"type":"function_call_output","call_id":"call_9","output":"ok"}
		]`),
	}
	modified, err := stripBoundStateFromResponsesRequest(testContext(t), req)
	if err != nil {
		t.Fatalf("degrade error: %v", err)
	}
	if modified {
		t.Fatal("orphan function_call_output must block degradation")
	}
	if req.PreviousResponseID != "resp_123" {
		t.Fatal("previous_response_id must be preserved when degradation is skipped")
	}
}

func TestStripBoundStateLeavesStoreUntouched(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Store: json.RawMessage("true"),
		Input: json.RawMessage(`[{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAA"},{"role":"user","content":"hi"}]`),
	}
	modified, err := stripBoundStateFromResponsesRequest(testContext(t), req)
	if err != nil {
		t.Fatalf("degrade error: %v", err)
	}
	if !modified {
		t.Fatal("expected modification")
	}
	// 强制 store=false 会让客户端后续轮次的 previous_response_id 失效，
	// 必须保留客户端原值。
	if string(req.Store) != "true" {
		t.Fatalf("store must be left as the client sent it, got %s", req.Store)
	}
}

func TestDegradeBoundStateForRetryMarksAttempt(t *testing.T) {
	c := testContext(t)
	req := &dto.OpenAIResponsesRequest{
		Input: json.RawMessage(`[{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAA"},{"role":"user","content":"hi"}]`),
	}
	modified, err := DegradeBoundStateForRetry(c, req)
	if err != nil {
		t.Fatalf("degrade error: %v", err)
	}
	if !modified {
		t.Fatal("expected modification")
	}
	if !BoundStateDegradeAttempted(c) {
		t.Fatal("degrade must be marked as attempted")
	}
	// 第二次调用必须拒绝，避免把重试预算烧在重复降级上。
	if again, _ := DegradeBoundStateForRetry(c, req); again {
		t.Fatal("second degrade must be refused")
	}
}

func TestDegradeBoundStateForRetryNothingToDo(t *testing.T) {
	c := testContext(t)
	req := &dto.OpenAIResponsesRequest{Input: json.RawMessage(`[{"role":"user","content":"hi"}]`)}
	modified, err := DegradeBoundStateForRetry(c, req)
	if err != nil {
		t.Fatalf("degrade error: %v", err)
	}
	if modified {
		t.Fatal("nothing to degrade must report false")
	}
	if BoundStateDegradeAttempted(c) {
		t.Fatal("no degrade happened, attempt must not be marked")
	}
}

func TestIsUpstreamBoundStateMismatchError(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"The encrypted content for item rs_abc could not be verified.", true},
		{"Invalid 'input[176].id': 'item_4009cf28'. Expected an ID that begins with 'msg'.", true},
		{"created under a different Azure OpenAI resource", true},
		// 字段校验错误与上游绑定状态无关，绝不能触发降级。
		{"Invalid 'input[2].content': expected a string or array of content items.", false},
		{"Selected model is at capacity. Please try a different model.", false},
		{"rate limit exceeded", false},
	}
	for _, tc := range cases {
		err := types.NewOpenAIError(errors.New(tc.text), types.ErrorCodeBadResponseBody, 400)
		if got := IsUpstreamBoundStateMismatchError(err); got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.text, got, tc.want)
		}
	}
}

func withTestSleeper(t *testing.T) *[]time.Duration {
	t.Helper()
	origSleeper, origJitter := retrySleeper, retryJitter
	var calls []time.Duration
	retrySleeper = func(ctx context.Context, d time.Duration) bool {
		calls = append(calls, d)
		return ctx.Err() == nil
	}
	retryJitter = func(d time.Duration) time.Duration { return d }
	t.Cleanup(func() { retrySleeper, retryJitter = origSleeper, origJitter })
	return &calls
}

func badGatewayErr(code types.ErrorCode) *types.NewAPIError {
	return types.NewOpenAIError(errors.New("upstream failed"), code, http.StatusBadGateway)
}

func TestSleepBeforeRetrySkipsWhenNoBudget(t *testing.T) {
	calls := withTestSleeper(t)
	if !SleepBeforeRetry(testContext(t), badGatewayErr(types.ErrorCodeBadResponse), 1, 0) {
		t.Fatal("no budget must still report continue=true")
	}
	if len(*calls) != 0 {
		t.Fatalf("must not sleep without budget, slept %v", *calls)
	}
}

func TestSleepBeforeRetrySkipsAfterWriterCommitted(t *testing.T) {
	calls := withTestSleeper(t)
	c := testContext(t)
	c.Writer.WriteHeaderNow()
	if !SleepBeforeRetry(c, badGatewayErr(types.ErrorCodeBadResponse), 1, 2) {
		t.Fatal("committed response must report continue=true")
	}
	if len(*calls) != 0 {
		t.Fatalf("must not sleep after commit, slept %v", *calls)
	}
}

func TestSleepBeforeRetrySkipsOtherStatuses(t *testing.T) {
	calls := withTestSleeper(t)
	err := types.NewOpenAIError(errors.New("boom"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	SleepBeforeRetry(testContext(t), err, 1, 2)
	if len(*calls) != 0 {
		t.Fatalf("non-502 must not sleep, slept %v", *calls)
	}
}

func TestSleepBeforeRetryLadderAdvancesPerBackoff(t *testing.T) {
	calls := withTestSleeper(t)
	c := testContext(t)
	err := badGatewayErr(types.ErrorCodeBadResponse)
	// 真实调用点预算序列（RetryTimes=3）：3,2,1,0；预算为 0 的那次不等待。
	budgets := []int{3, 2, 1, 0}
	for i, budget := range budgets {
		if !SleepBeforeRetry(c, err, i+1, budget) {
			t.Fatalf("attempt %d must continue", i+1)
		}
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(*calls) != len(want) {
		t.Fatalf("slept %v, want %v", *calls, want)
	}
	for i := range want {
		if (*calls)[i] != want[i] {
			t.Fatalf("sleep %d = %v, want %v", i, (*calls)[i], want[i])
		}
	}
}

func TestSleepBeforeRetryTotalBudgetCapped(t *testing.T) {
	calls := withTestSleeper(t)
	c := testContext(t)
	err := badGatewayErr(types.ErrorCodeBadResponse)
	for i := 1; i <= 8; i++ {
		SleepBeforeRetry(c, err, i, 5)
	}
	var total time.Duration
	for _, d := range *calls {
		total += d
	}
	if total > preContentMaxTotalBackoff {
		t.Fatalf("total backoff %v exceeds cap %v (calls %v)", total, preContentMaxTotalBackoff, *calls)
	}
	if len(*calls) == 0 {
		t.Fatal("expected at least one backoff")
	}
}

func TestSleepBeforeRetryNonBackoffFailureDoesNotAdvanceLadder(t *testing.T) {
	calls := withTestSleeper(t)
	c := testContext(t)
	// 先来一次不退避的 500，阶梯不应被推进。
	SleepBeforeRetry(c, types.NewOpenAIError(errors.New("boom"), types.ErrorCodeBadResponse, http.StatusInternalServerError), 1, 3)
	if len(*calls) != 0 {
		t.Fatalf("500 must not sleep, slept %v", *calls)
	}
	SleepBeforeRetry(c, badGatewayErr(types.ErrorCodeBadResponse), 2, 3)
	if len(*calls) != 1 || (*calls)[0] != time.Second {
		t.Fatalf("first real backoff must be 1s, got %v", *calls)
	}
}

func TestSleepBeforeRetryReturnsFalseWhenContextCancelled(t *testing.T) {
	calls := withTestSleeper(t)
	c := testContext(t)
	ctx, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	cancel()
	if SleepBeforeRetry(c, badGatewayErr(types.ErrorCodeBadResponse), 1, 3) {
		t.Fatalf("cancelled context must stop retrying, slept %v", *calls)
	}
}

func TestSleepBeforeRetryGivesUpWhenDeadlineTooClose(t *testing.T) {
	calls := withTestSleeper(t)
	c := testContext(t)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 300*time.Millisecond)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	if SleepBeforeRetry(c, badGatewayErr(types.ErrorCodeBadResponse), 1, 3) {
		t.Fatal("insufficient remaining time must stop retrying instead of sleeping it away")
	}
	if len(*calls) != 0 {
		t.Fatalf("must not sleep the whole remaining budget, slept %v", *calls)
	}
}

func TestIsUpstreamBoundStateMismatchRejectsInfraPhrases(t *testing.T) {
	// 这些是 TLS / 鉴权 / 账单类措辞，与会话状态无关；误判会触发破坏性改写。
	for _, text := range []string{
		"The provided API key could not be verified.",
		"Your organization's billing profile could not be verified.",
		"The TLS certificate could not be verified.",
		"Invalid 'input[3].call_id': expected an ID that begins with 'call_'.",
	} {
		err := types.NewOpenAIError(errors.New(text), types.ErrorCodeBadResponseBody, 400)
		if IsUpstreamBoundStateMismatchError(err) {
			t.Fatalf("%q must not be treated as a bound-state mismatch", text)
		}
	}
}

func TestRequestCarriesUpstreamBoundStateFalseAfterDegrade(t *testing.T) {
	body := `{"input":[{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAA"},{"role":"user","content":"hi"}]}`
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	if !RequestCarriesUpstreamBoundState(c) {
		t.Fatal("fixture must carry bound state")
	}
	req := &dto.OpenAIResponsesRequest{
		Input: json.RawMessage(`[{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAA"},{"role":"user","content":"hi"}]`),
	}
	if modified, err := DegradeBoundStateForRetry(c, req); err != nil || !modified {
		t.Fatalf("degrade must succeed, modified=%v err=%v", modified, err)
	}
	// 降级后请求已无绑定状态，否则会把后续重试钉死在刚失败的渠道上。
	if RequestCarriesUpstreamBoundState(c) {
		t.Fatal("bound state must read false after a successful degrade")
	}
}
