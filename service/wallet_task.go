package service

import (
	"context"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

func IsWalletBilling(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	session, ok := info.Billing.(*BillingSession)
	if !ok {
		return false
	}
	_, ok = session.funding.(*WalletFunding)
	return ok
}

func IsWalletFundedTask(task *model.Task) bool {
	return task != nil && task.PrivateData.WalletFundingRequestID != ""
}

// MarkWalletTaskSubmitted transfers financial ownership from the HTTP request
// to the persistent task ledger, even if saving the accepted task later fails.
func MarkWalletTaskSubmitted(info *relaycommon.RelayInfo) error {
	if !IsWalletBilling(info) {
		return nil
	}
	session := info.Billing.(*BillingSession)
	session.mu.Lock()
	defer session.mu.Unlock()
	session.upstreamPending, session.settled, session.fundingSettled = true, true, true
	funding := session.funding.(*WalletFunding)
	return model.MarkWalletFundingPending(funding.params.RequestID, true)
}

func AttachWalletTaskBilling(c *gin.Context, info *relaycommon.RelayInfo, task *model.Task) {
	if !IsWalletBilling(info) {
		return
	}
	session := info.Billing.(*BillingSession)
	session.mu.Lock()
	defer session.mu.Unlock()
	funding := session.funding.(*WalletFunding)
	task.PrivateData.WalletFundingRequestID = funding.params.RequestID
	task.PrivateData.TokenId = funding.params.TokenID
	task.PrivateData.BillingSource = BillingSourceWallet
	task.PrivateData.SubscriptionId = 0
	if task.PrivateData.Execution == nil {
		task.PrivateData.Execution = &model.TaskExecutionSnapshot{}
	}
	task.PrivateData.Execution.RequestID = funding.params.RequestID
	task.PrivateData.DeliveredInline = taskDeliveredInline(c, task)
}

func MarkWalletTaskUsageUnknown(info *relaycommon.RelayInfo, immediate *relaycommon.TaskInfo) {
	if IsWalletBilling(info) && immediate != nil && immediate.Status == model.TaskStatusSuccess {
		immediate.Status = model.TaskStatusAwaitingReview
		immediate.Reason = "Task billing could not be finalized; the reservation is pending review"
	}
}

func holdWalletTaskForReview(task *model.Task, fromStatus model.TaskStatus) error {
	return HoldCNYTaskForReview(task, fromStatus, "Task billing could not be finalized; the reservation is pending review")
}

func finalizeWalletTask(ctx context.Context, adaptor TaskPollingAdaptor, task *model.Task, fromStatus model.TaskStatus, result *relaycommon.TaskInfo) error {
	final, clamp, err := walletTaskSettlement(adaptor, task, result)
	if err != nil {
		if holdErr := holdWalletTaskForReview(task, fromStatus); holdErr != nil {
			return fmt.Errorf("task %s usage is unknown (%v); saving the pending review failed: %w", task.TaskID, err, holdErr)
		}
		return fmt.Errorf("task %s remains pending: %w", task.TaskID, err)
	}
	won, err := model.FinalizeWalletTask(task, fromStatus, final)
	if err == nil && won {
		perfmetrics.RecordTaskResult(task, result)
		LogWalletTaskConsumption(task, clamp)
	}
	return err
}

// walletTaskSettlement preserves the adaptor's positive-quota/zero-sentinel
// contract. Expression usage, including explicit zero, uses the frozen price.
func walletTaskSettlement(adaptor TaskPollingAdaptor, task *model.Task, result *relaycommon.TaskInfo) (model.WalletTaskSettlement, *common.QuotaClamp, error) {
	final := model.WalletTaskSettlement{Quota: task.Quota}
	bc := task.PrivateData.BillingContext
	if bc == nil || result == nil {
		return final, nil, fmt.Errorf("task billing snapshot or result is missing")
	}
	failed := task.Status == model.TaskStatusFailure
	if snap := bc.TieredSnapshot; snap != nil {
		if failed {
			return model.WalletTaskSettlement{Refund: true}, nil, nil
		}
		settlement, facts, err := EvaluateTaskCompletionUsage(snap, result.UsageFacts)
		if err != nil {
			return final, nil, err
		}
		snap.UsageFacts, snap.EstimatedTier = facts, settlement.MatchedTier
		final.Quota = settlement.ActualQuotaAfterGroup
		return final, settlement.Clamp, nil
	}
	if bc.PerCallBilling {
		final.Refund = failed
		return final, nil, nil
	}
	if adaptor != nil {
		if actual := adaptor.AdjustBillingOnComplete(task, result); actual > 0 {
			final.Quota = actual
			return final, nil, nil
		}
	}
	tokens := result.TotalTokens
	if tokens == 0 && result.CompletionTokens > 0 {
		tokens = result.CompletionTokens
	}
	if tokens > 0 {
		if bc.ModelRatio < 0 || bc.GroupRatio < 0 || math.IsNaN(bc.ModelRatio) || math.IsNaN(bc.GroupRatio) || math.IsInf(bc.ModelRatio, 0) || math.IsInf(bc.GroupRatio, 0) {
			return final, nil, fmt.Errorf("task token price snapshot is invalid")
		}
		otherMultiplier := 1.0
		if len(bc.OtherRatios) > 0 {
			price := taskBillingContextPriceData(bc)
			if price == nil {
				return final, nil, fmt.Errorf("task token multiplier snapshot is invalid")
			}
			otherMultiplier = price.OtherRatioMultiplier()
		}
		var clamp *common.QuotaClamp
		final.Quota, clamp = common.QuotaFromFloatChecked(float64(tokens) * bc.ModelRatio * bc.GroupRatio * otherMultiplier)
		return final, clamp, nil
	}
	final.Refund = failed
	return final, nil, nil
}

// Financial counters already committed with the task. The separately configured
// log database remains best effort, as with other consume logs.
func LogWalletTaskConsumption(task *model.Task, clamps ...*common.QuotaClamp) {
	other := taskBillingOther(task)
	model.AppendWalletFundingLog(other, task.PrivateData.WalletFundingRequestID, task.UserId)
	if task.PrivateData.DeliveredInline {
		other.SetPublic("task_sync", true)
	}
	if execution := task.PrivateData.Execution; execution != nil && execution.RequestPath != "" {
		other.SetPublic("request_path", execution.RequestPath)
	}
	for _, clamp := range clamps {
		attachQuotaSaturationToOther(other, clamp)
	}
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId: task.UserId, LogType: model.LogTypeConsume, ChannelId: task.ChannelId,
		ModelName: taskModelName(task), Quota: task.Quota, TokenId: task.PrivateData.TokenId,
		Group: task.Group, Other: other, NodeName: task.PrivateData.NodeName,
		Content: []*common.Message{common.NewMessage("Action {{action}}", map[string]any{"action": task.Action})},
	})
}
