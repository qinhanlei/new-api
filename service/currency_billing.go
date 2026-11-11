package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
)

// BillingCurrency keeps the admitted funding currency stable across retries.
func BillingCurrency(info *relaycommon.RelayInfo) string {
	if info == nil {
		return billingexpr.CurrencyUSD
	}
	if info.FundingCurrency != "" {
		return info.FundingCurrency
	}
	if info.TieredBillingSnapshot != nil {
		return info.TieredBillingSnapshot.BillingCurrency()
	}
	if info.PriceData.Currency != "" {
		return info.PriceData.Currency
	}
	return billingexpr.CurrencyUSD
}

func IsCNYBilling(info *relaycommon.RelayInfo) bool {
	return BillingCurrency(info) == billingexpr.CurrencyCNY
}

func IsCNYTask(task *model.Task) bool {
	return task != nil && task.PrivateData.BillingContext != nil &&
		task.PrivateData.BillingContext.TieredSnapshot != nil &&
		task.PrivateData.BillingContext.TieredSnapshot.BillingCurrency() == billingexpr.CurrencyCNY
}

// ValidateBillingCurrency runs even for free models: a free request must not
// bypass the selected currency's authorization or enter the USD fallback.
func ValidateBillingCurrency(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	currency := billingexpr.CurrencyUSD
	if info.TieredBillingSnapshot != nil {
		currency = info.TieredBillingSnapshot.BillingCurrency()
	} else if info.PriceData.Currency != "" {
		currency = info.PriceData.Currency
	}
	fail := func(message string) *types.NewAPIError {
		return types.NewErrorWithStatusCode(fmt.Errorf("%s", i18n.T(c, message)), types.ErrorCodeModelPriceError,
			http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if info.FundingCurrency != "" && info.FundingCurrency != currency {
		return fail(i18n.MsgBillingCurrencyRetryMismatch)
	}
	if currency == billingexpr.CurrencyUSD {
		if c.GetBool("token_usd_exhausted") && !info.IsPlayground {
			return types.NewErrorWithStatusCode(fmt.Errorf("%s", i18n.T(c, i18n.MsgBillingUSDTokenExhausted)),
				types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry())
		}
		info.FundingCurrency = currency
		return nil
	}
	if currency != billingexpr.CurrencyCNY || info.TieredBillingSnapshot == nil {
		return fail(i18n.MsgBillingCurrencyUnsupported)
	}
	if common.NormalizeBillingPreference(info.UserSetting.BillingPreference) == "subscription_only" {
		return fail(i18n.MsgBillingCNYWalletOnly)
	}
	if c.Request != nil && strings.EqualFold(c.Request.Header.Get("Upgrade"), "websocket") {
		return fail(i18n.MsgBillingCNYRequestUnsupported)
	}
	if !info.TieredBillingSnapshot.TaskUsageBilling {
		if info.ChannelMeta != nil && (info.ApiType == constant.APITypeXunfei || (info.ApiType == constant.APITypeAws && info.ChannelOtherSettings.AwsApiType != dto.AwsApiTypeMantle)) {
			return fail(i18n.MsgBillingCNYRequestUnsupported)
		}
		switch info.RelayMode {
		case relayconstant.RelayModeChatCompletions, relayconstant.RelayModeCompletions,
			relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
		default:
			return fail(i18n.MsgBillingCNYRequestUnsupported)
		}
		if cnyRequestHasPaidTools(info) {
			return fail(i18n.MsgBillingCNYPaidToolsUnsupported)
		}
	}
	if settings := model_setting.GetGrokSettings(); settings != nil && settings.ViolationDeductionEnabled &&
		(strings.HasPrefix(strings.ToLower(info.OriginModelName), "grok") || (info.ChannelMeta != nil && strings.HasPrefix(strings.ToLower(info.UpstreamModelName), "grok"))) {
		return fail(i18n.MsgBillingCNYViolationFeeUnsupported)
	}
	info.FundingCurrency = currency
	return nil
}

func cnyRequestHasPaidTools(info *relaycommon.RelayInfo) bool {
	if strings.HasSuffix(strings.ToLower(info.OriginModelName), "search-preview") || (info.ChannelMeta != nil && strings.HasSuffix(strings.ToLower(info.UpstreamModelName), "search-preview")) {
		return true
	}
	if info.Request == nil {
		return false
	}
	encoded, err := common.Marshal(info.Request)
	if err != nil {
		return true
	}
	return CNYBodyHasPaidTools(encoded)
}

// CNYBodyHasPaidTools also checks the final body after channel overrides.
func CNYBodyHasPaidTools(encoded []byte) bool {
	var body map[string]any
	if common.Unmarshal(encoded, &body) != nil {
		return true
	}
	for _, key := range []string{"web_search_options", "search_parameters", "web_search", "enable_search", "search_options", "google_search", "googleSearch", "google_search_retrieval", "codeExecution", "code_execution"} {
		if value, exists := body[key]; exists && value != nil && value != false {
			return true
		}
	}
	tools, _ := body["tools"].([]any)
	for _, value := range tools {
		tool, ok := value.(map[string]any)
		if !ok {
			return true
		}
		for key := range tool {
			if key != "type" && key != "function" && key != "name" && key != "description" && key != "parameters" && key != "input_schema" && key != "strict" && key != "cache_control" && key != "functionDeclarations" && key != "function_declarations" {
				return true
			}
		}
		kind, _ := tool["type"].(string)
		if kind != "" && kind != "function" && kind != "custom" {
			return true
		}
	}
	return false
}

// CNYBillingSession persists funding independently of the optional log database.
// USD and CNY wallets use the same transactional funding ledger.
type CNYBillingSession struct {
	info            *relaycommon.RelayInfo
	params          model.CNYBillingParams
	usage           model.CNYBillingUsage
	reserved        int
	closed          bool
	upstreamPending bool
	mu              sync.Mutex
}

func newCNYBillingSession(c *gin.Context, info *relaycommon.RelayInfo, quota int) (*CNYBillingSession, error) {
	if info.RequestId == "" {
		info.RequestId = common.NewRequestId()
	}
	session := &CNYBillingSession{info: info, params: model.CNYBillingParams{
		RequestID: info.RequestId, UserID: info.UserId, TokenID: info.TokenId,
		ChannelID: c.GetInt("channel_id"), IsPlayground: info.IsPlayground, Username: c.GetString("username"), TokenName: c.GetString("token_name"),
		ModelName: info.OriginModelName,
	}}
	if err := session.ReserveBeforeSubmit(quota); err != nil {
		return nil, err
	}
	info.BillingSource = BillingSourceWallet
	return session, nil
}

func (s *CNYBillingSession) Reserve(targetQuota int) error {
	return s.ReserveBeforeSubmit(targetQuota)
}

func (s *CNYBillingSession) ReserveBeforeSubmit(targetQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.upstreamPending {
		return fmt.Errorf("CNY request cannot be submitted again after acceptance or an unknown upstream result")
	}
	snapshot, err := common.Marshal(s.info.TieredBillingSnapshot)
	if err != nil {
		return err
	}
	s.params.Free = s.info.TieredBillingSnapshot.GroupRatio == 0
	if !s.params.Free && len(billingexpr.UsedVars(s.info.TieredBillingSnapshot.ExprString)) == 0 {
		cost, _, err := billingexpr.RunExpr(s.info.TieredBillingSnapshot.ExprString, billingexpr.TokenParams{})
		s.params.Free = err == nil && cost == 0
	}
	s.params.Group = s.info.UsingGroup
	s.params.Snapshot = string(snapshot)
	record, err := model.ReserveCNYBilling(s.params, targetQuota)
	if err != nil {
		return err
	}
	s.reserved = record.ReservedQuota
	s.info.FinalPreConsumedQuota = s.reserved
	return nil
}

func (s *CNYBillingSession) Settle(actualQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := model.SettleCNYBilling(s.params.RequestID, actualQuota, s.usage)
	if err == nil {
		s.closed = true
	}
	return err
}

func (s *CNYBillingSession) Refund(c *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.upstreamPending {
		return
	}
	if _, err := model.RefundCNYBilling(s.params.RequestID, "Request failed"); err != nil {
		logger.LogError(c, fmt.Sprintf("failed to refund CNY request %s: %v", s.params.RequestID, err))
		return
	}
	s.closed = true
}

func (s *CNYBillingSession) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserved
}

func (s *CNYBillingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && !s.upstreamPending
}

// MarkCNYUpstreamPending prevents a second paid submit or a speculative refund
// while the first submit may have been accepted. A definite rejection clears it.
func MarkCNYUpstreamPending(info *relaycommon.RelayInfo, pending bool) error {
	if session, ok := info.Billing.(*CNYBillingSession); ok {
		session.mu.Lock()
		defer session.mu.Unlock()
		if err := model.MarkWalletFundingPending(info.RequestId, pending); err != nil {
			return err
		}
		session.upstreamPending = pending
	}
	if session, ok := info.Billing.(*BillingSession); ok {
		session.mu.Lock()
		defer session.mu.Unlock()
		if _, wallet := session.funding.(*WalletFunding); wallet {
			if err := model.MarkWalletFundingPending(info.RequestId, pending); err != nil {
				return err
			}
			session.upstreamPending = pending
		}
	}
	return nil
}

func CNYUpstreamPending(info *relaycommon.RelayInfo) bool {
	if session, ok := info.Billing.(*CNYBillingSession); ok {
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.upstreamPending
	}
	if session, ok := info.Billing.(*BillingSession); ok {
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.upstreamPending
	}
	return false
}

// RecordUnknownWalletUsage exposes the durable hold without inventing a final
// charge or changing the provider's response frames.
func RecordUnknownWalletUsage(ctx *gin.Context, info *relaycommon.RelayInfo) {
	other := model.NewLogOther()
	appendStreamStatus(info, other)
	model.RecordErrorLog(ctx, info.UserId, info.ChannelId, info.OriginModelName, ctx.GetString("token_name"),
		"Upstream usage is unknown; the wallet reservation is retained", info.TokenId,
		int(max(int64(0), time.Now().Unix()-info.StartTime.Unix())), info.IsStream, info.UsingGroup, other)
}

// PrepareCNYHTTPRequest is the final safety gate after provider conversion and
// channel overrides. The body remains byte-for-byte unchanged.
func PrepareCNYHTTPRequest(c *gin.Context, info *relaycommon.RelayInfo, req *http.Request) error {
	if !IsCNYBilling(info) {
		return nil
	}
	if apiErr := ValidateBillingCurrency(c, info); apiErr != nil {
		MarkCNYUpstreamPending(info, false)
		return apiErr
	}
	if info.TieredBillingSnapshot.TaskUsageBilling {
		return nil
	}
	if req.Body == nil {
		return fmt.Errorf("CNY text request body is missing")
	}
	var reader io.ReadCloser
	var err error
	if req.GetBody != nil {
		reader, err = req.GetBody()
	} else {
		reader = req.Body
	}
	if err != nil {
		return err
	}
	data, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return err
	}
	if req.GetBody == nil {
		req.Body = io.NopCloser(bytes.NewReader(data))
	}
	if CNYBodyHasPaidTools(data) {
		MarkCNYUpstreamPending(info, false)
		return types.NewErrorWithStatusCode(fmt.Errorf("%s", i18n.T(c, i18n.MsgBillingCNYPaidToolsUnsupported)), types.ErrorCodeModelPriceError, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	return nil
}

func ObserveCNYHTTPResponse(info *relaycommon.RelayInfo, status int) {
	// A timeout or server error can arrive after the provider accepted work.
	if status >= 300 && status < 500 && status != http.StatusRequestTimeout {
		MarkCNYUpstreamPending(info, false)
	}
}

func postCNYTextUsage(ctx *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage) {
	session, ok := info.Billing.(*CNYBillingSession)
	if !ok {
		logger.LogError(ctx, "CNY text settlement has no native billing session")
		return
	}
	billingUsage := effectiveBillingUsage(usage)
	if billingUsage == nil {
		logger.LogWarn(ctx, "CNY request returned no usage; keeping its reservation pending")
		return
	}
	if info.ResponsesUsageInfo != nil {
		for _, tool := range info.ResponsesUsageInfo.BuiltInTools {
			if tool != nil && tool.CallCount > 0 {
				logger.LogError(ctx, "CNY request returned unsupported paid tool usage; keeping its reservation pending")
				return
			}
		}
	}
	snap := info.TieredBillingSnapshot
	params := BuildTieredTokenParams(billingUsage, usageSemanticFromUsage(info, billingUsage) == "anthropic", billingexpr.UsedVarsByHash(snap.ExprString, snap.ExprHash))
	_, quota, result := TryTieredSettle(info, params)
	if result == nil {
		logger.LogWarn(ctx, "CNY expression settlement failed; keeping its reservation pending")
		return
	}
	ObserveChannelAffinityUsageCacheByRelayFormat(ctx, billingUsage, info.GetFinalRequestRelayFormat())
	other := GenerateTextOtherInfo(ctx, info, 0, snap.GroupRatio, 0, billingUsage.PromptTokensDetails.CachedTokens, 0, 0, info.PriceData.GroupRatioInfo.GroupSpecialRatio)
	other.SetPublic("usage_semantic", usageSemanticFromUsage(info, billingUsage))
	appendUsageBillingPathForLog(other, common.GetContextKeyBool(ctx, constant.ContextKeyLocalCountTokens), usage)
	InjectTieredBillingInfo(other, info, result)
	attachQuotaSaturation(ctx, info, other)
	snapshot, err := common.Marshal(snap)
	if err != nil {
		logger.LogError(ctx, err.Error())
		return
	}
	session.mu.Lock()
	session.usage = model.CNYBillingUsage{PromptTokens: billingUsage.PromptTokens, CompletionTokens: billingUsage.CompletionTokens,
		UseTime: int(max(int64(0), time.Now().Unix()-info.StartTime.Unix())), IsStream: info.IsStream, Other: other.JSONString(), Snapshot: string(snapshot)}
	session.mu.Unlock()
	if err := session.Settle(quota); err != nil {
		logger.LogError(ctx, fmt.Sprintf("CNY settlement failed for %s: %v", info.RequestId, err))
	}
	info.PerformanceOutputTokens = int64(billingUsage.CompletionTokens)
}

func CNYTaskUsage(task *model.Task, clamps ...*common.QuotaClamp) model.CNYBillingUsage {
	snapshot, _ := common.Marshal(task.PrivateData.BillingContext.TieredSnapshot)
	other := taskBillingOther(task)
	for _, clamp := range clamps {
		attachQuotaSaturationToOther(other, clamp)
	}
	return model.CNYBillingUsage{UseTime: int(max(int64(0), task.FinishTime-task.SubmitTime)), Other: other.JSONString(), Snapshot: string(snapshot)}
}

func finalizeCNYTask(ctx context.Context, task *model.Task, fromStatus model.TaskStatus, result *relaycommon.TaskInfo) error {
	final := model.CNYTaskSettlement{Quota: task.Quota, Refund: task.Status == model.TaskStatusFailure}
	var clamp *common.QuotaClamp
	if !final.Refund {
		snap := task.PrivateData.BillingContext.TieredSnapshot
		settlement, facts, err := EvaluateTaskCompletionUsage(snap, result.UsageFacts)
		if err != nil {
			return fmt.Errorf("CNY task %s remains pending: %w", task.TaskID, err)
		}
		final.Quota = settlement.ActualQuotaAfterGroup
		clamp = settlement.Clamp
		snap.UsageFacts, snap.EstimatedTier = facts, settlement.MatchedTier
	}
	final.Usage = CNYTaskUsage(task, clamp)
	won, err := model.FinalizeCNYTask(task, fromStatus, final)
	if err == nil && won {
		perfmetrics.RecordTaskResult(task, result)
	}
	return err
}

// HoldCNYTaskForReview stops automatic polling without claiming a provider
// failure or releasing money whose final cost has not been established.
func HoldCNYTaskForReview(task *model.Task, fromStatus model.TaskStatus, reason string) error {
	task.Status = model.TaskStatusAwaitingReview
	task.FailReason = reason
	_, err := task.UpdateWithStatus(fromStatus)
	return err
}

func cnyReservationError(c *gin.Context, err error) *types.NewAPIError {
	key, code, status := i18n.MsgBillingCNYUnavailable, types.ErrorCodeQueryDataError, http.StatusInternalServerError
	switch {
	case errors.Is(err, model.ErrCNYQuota):
		key, code, status = i18n.MsgBillingWalletInsufficient, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden
	case errors.Is(err, model.ErrExchangeRateUnavailable):
		key, code, status = i18n.MsgBillingExchangeUnavailable, types.ErrorCodeInsufficientUserQuota, http.StatusServiceUnavailable
	case errors.Is(err, model.ErrCNYTokenQuota):
		key, code, status = i18n.MsgBillingCNYInsufficientToken, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden
	case errors.Is(err, model.ErrTokenInvalid), errors.Is(err, model.ErrUserQuotaPermission):
		key, code, status = i18n.MsgTokenInvalid, types.ErrorCodeAccessDenied, http.StatusForbidden
	}
	logger.LogWarn(c, fmt.Sprintf("CNY reservation rejected: %v", err))
	return types.NewErrorWithStatusCode(errors.New(i18n.T(c, key)), code, status, types.ErrOptionWithSkipRetry())
}

func MarkCNYTaskUsageUnknown(info *relaycommon.RelayInfo, immediate *relaycommon.TaskInfo) {
	if IsCNYBilling(info) && immediate != nil && immediate.Status == model.TaskStatusSuccess {
		immediate.Status = model.TaskStatusAwaitingReview
		immediate.Reason = "CNY billing could not be finalized; the reservation is pending review"
	}
}
