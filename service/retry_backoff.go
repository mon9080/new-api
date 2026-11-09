package service

import (
	"context"
	"math/rand"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// 首包前的流式失败（上游 502，尚未向客户端写出内容）留给上游几秒自愈，比立刻
// 重试更有效。等待有总预算，超出后直接重试，避免 502 风暴把单请求驻留时间拉长。
const (
	preContentMaxTotalBackoff = 8 * time.Second
	keyRetryBackoffCount      = "key_retry_backoff_count"
	keyRetryBackoffTotal      = "key_retry_backoff_total"
)

var preContentRetryBackoff = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
}

// 可被测试替换的注入点。
var (
	retrySleeper = func(ctx context.Context, d time.Duration) bool {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
	// retryJitter 打散同时失败请求的重试时刻，避免形成同步脉冲。
	retryJitter = func(d time.Duration) time.Duration {
		delta := int64(d) / 4
		if delta <= 0 {
			return d
		}
		return d + time.Duration(rand.Int63n(2*delta+1)-delta)
	}
)

func backoffState(c *gin.Context) (count int, total time.Duration) {
	if c == nil {
		return 0, 0
	}
	n, _ := common.GetContextKeyType[int](c, keyRetryBackoffCount)
	t, _ := common.GetContextKeyType[int64](c, keyRetryBackoffTotal)
	return n, time.Duration(t)
}

func addBackoffState(c *gin.Context, d time.Duration) {
	if c == nil {
		return
	}
	n, total := backoffState(c)
	common.SetContextKey(c, keyRetryBackoffCount, n+1)
	common.SetContextKey(c, keyRetryBackoffTotal, int64(total+d))
}

// SleepBeforeRetry 在两次尝试之间等待。返回 false 表示不应继续重试
// （客户端已断开或剩余时间不足以再发起一次尝试）。
//
// 判据刻意收窄：只有「响应尚未提交 + 上游 502 + 可重试错误码」才等待，其它
// 失败立即重试，不额外引入延迟。
func SleepBeforeRetry(c *gin.Context, err *types.NewAPIError, retried, remainingBudget int) bool {
	if err == nil || retried < 1 || remainingBudget <= 0 {
		return true
	}
	if c == nil || c.Request == nil {
		return true
	}
	// 响应已提交（含只写了 header、或心跳注释帧）时不再等待：把等待时间留给
	// 上游没有意义，重试与否由重试决策单独决定。
	if c.Writer != nil && c.Writer.Written() {
		return true
	}
	if err.StatusCode != http.StatusBadGateway {
		return true
	}
	switch err.GetErrorCode() {
	case types.ErrorCodeBadResponse, types.ErrorCodeBadResponseBody:
	default:
		return true
	}

	// 阶梯按「已执行过几次退避」推进，不被非退避型失败（500/渠道错误）跳档。
	count, totalSlept := backoffState(c)
	idx := count
	if idx >= len(preContentRetryBackoff) {
		idx = len(preContentRetryBackoff) - 1
	}
	delay := retryJitter(preContentRetryBackoff[idx])
	if totalSlept+delay > preContentMaxTotalBackoff {
		return true
	}

	ctx := c.Request.Context()
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		// 剩余时间不足以睡完并留下一次尝试的余地时，直接放弃等待。
		if remaining <= delay {
			return false
		}
	}
	if err := ctx.Err(); err != nil {
		return false
	}

	addBackoffState(c, delay)
	if !retrySleeper(ctx, delay) {
		return false
	}
	return ctx.Err() == nil
}
