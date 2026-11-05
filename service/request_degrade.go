package service

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
)

// 会话状态归属不匹配：上游报出的 item id / 加密推理项由别的上游资源签发。
// 换渠道重放必然失败；把历史 reasoning 项与历史 id 剥掉后请求重新变成
// “无上游绑定状态”，换渠道才是安全的。
//
// 注意：只改真正发往上游的载荷（dto.OpenAIResponsesRequest）。默认转发路径
// 用 DeepCopy(relayInfo.Request) 重新序列化，body storage 只在 passthrough
// 模式下被读取；passthrough 模式由调用方跳过降级。
const (
	keyBoundStateDegradeAttempted = "key_bound_state_degrade_attempted"
	keyBoundStateStripped         = "key_bound_state_stripped"
)

// idMismatchPattern 只匹配“某个 input 项的 id 不被接受”。字段校验错误
// （Invalid 'input[2].content'）和 call_id 校验错误（Invalid 'input[3].call_id'）
// 都匹配不上：前者与绑定状态无关，后者我们不修改 call_id，降级救不了。
var idMismatchPattern = regexp.MustCompile(`invalid ['"]input\[\d+\]\.id['"]`)

// 归属错误措辞。前两条必须与 “encrypted” 同现，避免把 TLS 证书、API key、
// 账单验证失败（"could not be verified"）误判成会话状态问题。
var (
	encryptedStatePhrases = []string{"could not be verified", "could not be decrypted"}
	resourcePhrases       = []string{"different azure openai resource", "under a different openai resource"}
)

func isEncryptedStateMismatchText(text string) bool {
	if !strings.Contains(text, "encrypt") {
		return false
	}
	for _, p := range encryptedStatePhrases {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

// IsUpstreamBoundStateMismatchError 判断上游是否明确表示“这段会话状态不是我签发的”。
func IsUpstreamBoundStateMismatchError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	// 与 isEncryptedContentMismatchError 保持同一口径：上游的结构化码常常只出现在
	// message 里，漏掉它会让这类错误只被终止、永远进不了降级。
	if strings.Contains(text, "invalid_encrypted_content") {
		return true
	}
	if isEncryptedStateMismatchText(text) {
		return true
	}
	for _, p := range resourcePhrases {
		if strings.Contains(text, p) {
			return true
		}
	}
	return idMismatchPattern.MatchString(text)
}

// BoundStateDegradeAttempted 本次请求是否已经降级过一次（防止无限重试）。
func BoundStateDegradeAttempted(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := common.GetContextKeyType[bool](c, keyBoundStateDegradeAttempted)
	return ok && v
}

// BoundStateAlreadyStripped 本次请求的绑定状态是否已被剥离。
// 其它基于请求体的判定（例如是否钉死同一渠道）据此刻意放行。
func BoundStateAlreadyStripped(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := common.GetContextKeyType[bool](c, keyBoundStateStripped)
	return ok && v
}

func markBoundStateDegradeAttempted(c *gin.Context) {
	if c == nil {
		return
	}
	common.SetContextKey(c, keyBoundStateDegradeAttempted, true)
	common.SetContextKey(c, keyBoundStateStripped, true)
}

// stripResult 描述一次剥离的结果。
type stripResult struct {
	Input        json.RawMessage
	RemovedItems int
	RemovedIDs   int
	Changed      bool
	Kept         []any
}

// stripBoundStateFromInput 从 input 数组里剥掉由上游资源绑定的历史状态：
//   - 整条删除 type=reasoning 的项
//   - 整条删除携带 encrypted_content 的项
//   - 整条删除 type=item_reference 的项（它唯一的语义就是那个上游 id）
//   - 删除其余项的 id（工具配对的 call_id 保留）
//
// input 不是数组（例如纯字符串 prompt）时原样返回。
func stripBoundStateFromInput(raw json.RawMessage) stripResult {
	res := stripResult{Input: raw, Kept: nil}
	if len(raw) == 0 {
		return res
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var input []any
	if err := dec.Decode(&input); err != nil {
		return res
	}
	kept := make([]any, 0, len(input))
	for _, entry := range input {
		item, isMap := entry.(map[string]any)
		if !isMap {
			kept = append(kept, entry)
			continue
		}
		switch t, _ := item["type"].(string); t {
		case "reasoning", "item_reference":
			res.RemovedItems++
			continue
		}
		if ec, _ := item["encrypted_content"].(string); ec != "" {
			res.RemovedItems++
			continue
		}
		if _, has := item["id"]; has {
			delete(item, "id")
			res.RemovedIDs++
		}
		kept = append(kept, item)
	}
	if res.RemovedItems == 0 && res.RemovedIDs == 0 {
		return res
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return res
	}
	res.Input = out
	res.Changed = true
	res.Kept = kept
	return res
}

// selfContained 判断剥离后的 input 能否独立表达完整会话：
// 空 input 不可降级（上游会报缺参数）；孤儿 tool output 也不可降级（丢了
// previous_response_id 之后上游会报 No tool call found for call_id）。
//
// Responses 的调用类项有多种后缀（function_call / custom_tool_call /
// computer_call / local_shell_call…），统一按 *_call ↔ *_output 与 call_id 配对。
func selfContained(items []any) bool {
	if len(items) == 0 {
		return false
	}
	calls := map[string]bool{}
	outputs := map[string]bool{}
	for _, entry := range items {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		t, _ := item["type"].(string)
		callID, _ := item["call_id"].(string)
		if callID == "" {
			continue
		}
		switch {
		case strings.HasSuffix(t, "_output"):
			outputs[callID] = true
		case strings.HasSuffix(t, "_call"):
			calls[callID] = true
		}
	}
	for id := range outputs {
		if !calls[id] {
			return false
		}
	}
	return true
}


// stripBoundStateFromResponsesRequest 就地改写真正要发往上游的 Responses 请求。
//
// previous_response_id 指向的服务端状态同样属于签发它的上游，保留它等于保留
// 绑定状态；因此只有在剥离后的 input 能独立表达完整会话时才清掉它，否则整体
// 放弃降级，把原始错误交还给客户端。
//
// store 保持客户端原值：强制 store=false 会让客户端后续轮次的
// previous_response_id 失效，产生一串与原始问题无关的新失败。
func stripBoundStateFromResponsesRequest(c *gin.Context, req *dto.OpenAIResponsesRequest) (bool, error) {
	if req == nil {
		return false, nil
	}
	res := stripBoundStateFromInput(req.Input)
	if !res.Changed {
		return false, nil
	}
	// 剥离后必须是自洽的完整会话，否则重试只会换来另一个错误。
	if !selfContained(res.Kept) {
		if c != nil {
			logger.LogInfo(c, common.LogText(
				"bound state degrade skipped: input not self-contained after strip (kept=%d)", len(res.Kept)))
		}
		return false, nil
	}
	// previous_response_id / conversation 指向的历史只存在于服务端，剥离后无法
	// 证明 input 能独立表达完整会话；清掉它们会让模型“失忆”却仍返回 200。
	// 宁可把原始错误交还客户端，也不静默改变语义。
	if req.PreviousResponseID != "" || conversationInUse(req.Conversation) {
		if c != nil {
			logger.LogInfo(c, common.LogText(
				"bound state degrade skipped: request depends on server-side session state"))
		}
		return false, nil
	}

	req.Input = res.Input
	removedInclude := stripEncryptedInclude(req)
	if c != nil {
		logger.LogInfo(c, common.LogText(
			"bound state degraded on payload: removed_items=%d removed_ids=%d removed_include=%d",
			res.RemovedItems, res.RemovedIDs, removedInclude))
	}
	return true, nil
}

func stripEncryptedInclude(req *dto.OpenAIResponsesRequest) int {
	if req == nil || len(req.Include) == 0 {
		return 0
	}
	var include []any
	if err := json.Unmarshal(req.Include, &include); err != nil {
		return 0
	}
	rest := make([]any, 0, len(include))
	removed := 0
	for _, v := range include {
		if s, isStr := v.(string); isStr && strings.Contains(s, "encrypted_content") {
			removed++
			continue
		}
		rest = append(rest, v)
	}
	if removed == 0 {
		return 0
	}
	out, err := json.Marshal(rest)
	if err != nil {
		return 0
	}
	req.Include = out
	return removed
}

// DegradeRelayRequestPayload 对本次请求真正要发送的载荷做降级。
// 只处理 Responses 请求；其它载荷原样返回 false。
func DegradeRelayRequestPayload(c *gin.Context, payload any) (bool, error) {
	switch req := payload.(type) {
	case *dto.OpenAIResponsesRequest:
		return stripBoundStateFromResponsesRequest(c, req)
	default:
		return false, nil
	}
}

// DegradeBoundStateForRetry 在重试前把“上游绑定状态”从真正要发送的载荷中去掉，
// 并标记本次请求已降级，避免重复降级耗尽重试预算。
//
// 返回 modified=false 表示没有可降级的内容（或降级后会引入新错误），
// 调用方应保留原决策。
func DegradeBoundStateForRetry(c *gin.Context, payload any) (bool, error) {
	if BoundStateDegradeAttempted(c) {
		return false, nil
	}
	modified, err := DegradeRelayRequestPayload(c, payload)
	if err != nil || !modified {
		return false, err
	}
	markBoundStateDegradeAttempted(c)
	return true, nil
}

// RequestPassthrough 报告本次请求是否以原始字节透传给上游。
// 透传模式下上游收到的是客户端原始 body，改 DTO 不会生效，因此不承诺降级。
func RequestPassthrough(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	if info.ChannelMeta == nil {
		// 渠道元信息尚未初始化，无法判断；不确定时按“不承诺降级”处理更安全。
		return true
	}
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled {
		return true
	}
	return info.ChannelSetting.PassThroughBodyEnabled
}

// conversationInUse 判断 conversation 是否真的引用了服务端会话；
// null / 空串 / 空对象都视为未使用。
func conversationInUse(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch string(trimmed) {
	case "null", `""`, "{}", "[]":
		return false
	}
	return true
}
