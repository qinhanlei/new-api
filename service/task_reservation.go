package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type durableTaskBilling struct {
	mu          sync.Mutex
	ctx         context.Context
	info        *relaycommon.RelayInfo
	reservation *model.TaskReservation
}

// PreConsumeDurableTaskBilling is called only after the task's asset replicas
// are ready. Unknown upstream outcomes retain this reservation for reconciliation.
func PreConsumeDurableTaskBilling(c *gin.Context, info *relaycommon.RelayInfo, quota int) *types.NewAPIError {
	if info == nil || info.TaskRelayInfo == nil || info.ChannelMeta == nil || info.PublicTaskID == "" {
		return types.NewError(fmt.Errorf("durable task billing requires a public task ID"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if info.QuotaClamp != nil || quota < 0 || quota > common.MaxQuota {
		return types.NewErrorWithStatusCode(errors.New("invalid task reservation quota"), types.ErrorCodeModelPriceError, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	reservation, err := model.ReserveTaskQuota(c.Request.Context(), model.TaskReservationRequest{
		TaskID: info.PublicTaskID, UserID: info.UserId, TokenID: info.TokenId, ChannelID: info.ChannelId,
		Model: info.GetBillingModelName(), Preference: info.UserSetting.BillingPreference,
		Quota: quota, IsPlayground: info.IsPlayground,
	})
	if err != nil {
		if errors.Is(err, model.ErrTaskReservationQuota) {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	session := &durableTaskBilling{ctx: c.Request.Context(), info: info, reservation: reservation}
	if quota > reservation.Quota {
		if err := session.Reserve(quota); err != nil {
			if errors.Is(err, model.ErrTaskReservationQuota) {
				return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry())
			}
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
	}
	session.syncRelayInfo()
	info.Billing = session
	return nil
}

func (s *durableTaskBilling) syncRelayInfo() {
	s.info.FinalPreConsumedQuota = s.reservation.Quota
	s.info.BillingSource = s.reservation.FundingSource
	s.info.SubscriptionId = s.reservation.SubscriptionID
	if s.reservation.FundingSource == BillingSourceSubscription {
		s.info.SubscriptionPreConsumed = int64(s.reservation.Quota)
		s.info.SubscriptionPostDelta = 0
		s.info.SubscriptionAmountTotal = s.reservation.SubscriptionAmountTotal
		s.info.SubscriptionAmountUsedAfterPreConsume = s.reservation.SubscriptionAmountUsed
		s.info.SubscriptionPlanId = s.reservation.SubscriptionPlanID
	}
}

func (s *durableTaskBilling) Reserve(quota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reservation, _, err := model.AdjustTaskQuota(s.ctx, s.reservation.TaskID, quota, model.TaskReservationReserve)
	if err == nil {
		s.reservation = reservation
		s.syncRelayInfo()
	}
	return err
}

func (s *durableTaskBilling) ReserveBeforeSubmit(quota int) error {
	// Durable reservations always require available wallet and token balances.
	return s.Reserve(quota)
}

func (s *durableTaskBilling) Settle(quota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reservation, _, err := model.AdjustTaskQuota(s.ctx, s.reservation.TaskID, quota, model.TaskReservationSettle)
	if err == nil {
		s.reservation = reservation
		s.syncRelayInfo()
	}
	return err
}

func (s *durableTaskBilling) Refund(c *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reservation.State != "reserved" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
	defer cancel()
	reservation, _, err := model.AdjustTaskQuota(ctx, s.reservation.TaskID, 0, model.TaskReservationRefund)
	if err != nil {
		logger.LogError(c, common.LogText("failed to refund durable task %s: %s", s.reservation.TaskID, err.Error()))
		return
	}
	s.reservation = reservation
	s.syncRelayInfo()
}

func (s *durableTaskBilling) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reservation.State == "reserved" && s.reservation.Quota > 0
}

func (s *durableTaskBilling) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reservation.Quota
}

// RefundDurableTaskBilling lets the worker retry a known failed submission's
// refund after a restart. It must never be used for an unknown send outcome.
func RefundDurableTaskBilling(ctx context.Context, taskID string) error {
	_, _, err := model.AdjustTaskQuota(ctx, taskID, 0, model.TaskReservationRefund)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil // Validation can fail before any reservation was made.
	}
	return err
}

// taskCompletionBilling freezes the final decision before terminal visibility.
// Recovery never reevaluates mutable pricing or invokes a plugin/provider.
type taskCompletionBilling struct {
	Operation    string               `json:"operation"`
	Quota        int                  `json:"quota"`
	Reason       *common.Message      `json:"reason,omitempty"`
	Clamps       []*common.QuotaClamp `json:"-"`
	Saturation   map[string]any       `json:"saturation,omitempty"`
	RefundReason string               `json:"refund_reason,omitempty"`
}

func encodeTaskCompletionBilling(billing taskCompletionBilling) (model.LongText, error) {
	if billing.Quota < 0 || billing.Quota > common.MaxQuota {
		return "", errors.New("invalid task completion quota")
	}
	for _, clamp := range billing.Clamps {
		if clamp != nil {
			billing.Saturation = clamp.AuditMap()
			if math.IsNaN(clamp.Original) || math.IsInf(clamp.Original, 0) {
				billing.Saturation["original"] = fmt.Sprint(clamp.Original)
			}
		}
	}
	data, err := common.Marshal(billing)
	return model.LongText(data), err
}

func updateTaskWithCompletion(ctx context.Context, adaptor TaskPollingAdaptor, task *model.Task, fromStatus model.TaskStatus, result *relaycommon.TaskInfo) (bool, error) {
	if !task.PrivateData.DurableBilling {
		return task.UpdateWithStatus(fromStatus)
	}
	billing, handled := taskCompletionBillingPlan(ctx, adaptor, task, result)
	if task.Status == model.TaskStatusFailure && !handled {
		billing = taskCompletionBilling{Operation: model.TaskReservationRefund, RefundReason: task.FailReason}
	}
	data, err := encodeTaskCompletionBilling(billing)
	if err != nil {
		return false, err
	}
	return model.CompleteTaskWithBilling(ctx, task, fromStatus, data)
}

func refundDurableTaskQuota(ctx context.Context, task *model.Task, reason string) bool {
	billing := taskCompletionBilling{Operation: model.TaskReservationRefund, RefundReason: reason}
	if err := queueDurableTaskCompletion(ctx, task, billing); err != nil {
		logger.LogWarn(ctx, common.LogText("failed to refund durable task %s: %s", task.TaskID, err.Error()))
		return false
	}
	return true
}

func recalculateDurableTaskQuota(ctx context.Context, task *model.Task, quota int, reason *common.Message, clamps ...*common.QuotaClamp) {
	billing := taskCompletionBilling{Operation: model.TaskReservationRecalculate, Quota: quota, Reason: reason, Clamps: clamps}
	if err := queueDurableTaskCompletion(ctx, task, billing); err != nil {
		logger.LogWarn(ctx, common.LogText("failed to settle durable task %s: %s", task.TaskID, err.Error()))
	}
}

func queueDurableTaskCompletion(ctx context.Context, task *model.Task, billing taskCompletionBilling) error {
	data, err := encodeTaskCompletionBilling(billing)
	if err != nil {
		return err
	}
	if err := model.QueueTaskBillingCompletion(ctx, task.TaskID, data); err != nil {
		return err
	}
	return finishDurableTaskCompletion(ctx, task)
}

func finishDurableTaskCompletion(ctx context.Context, task *model.Task) error {
	var submission model.TaskSubmission
	if err := model.DB.WithContext(ctx).Where("task_id = ?", task.TaskID).First(&submission).Error; err != nil {
		return err
	}
	if submission.Phase != model.SubmissionCompleting {
		return nil
	}
	return applyDurableTaskCompletion(ctx, task, &submission)
}

// RecoverDurableTaskCompletions is called by the existing asset task scheduler.
// A crash after charging but before clearing the intent is safe to replay.
func RecoverDurableTaskCompletions(ctx context.Context) error {
	var submissions []model.TaskSubmission
	if err := model.DB.WithContext(ctx).Where("phase = ? AND next_attempt <= ?", model.SubmissionCompleting, time.Now().Unix()).
		Order("next_attempt, task_id").Limit(100).Find(&submissions).Error; err != nil {
		return err
	}
	var firstErr error
	for _, submission := range submissions {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var task model.Task
		err := model.DB.WithContext(ctx).Where("task_id = ? AND user_id = ?", submission.TaskID, submission.UserID).First(&task).Error
		if err == nil {
			err = applyDurableTaskCompletion(ctx, &task, &submission)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func applyDurableTaskCompletion(ctx context.Context, task *model.Task, submission *model.TaskSubmission) error {
	var billing taskCompletionBilling
	if err := common.Unmarshal([]byte(submission.CompletionBilling), &billing); err != nil {
		return err
	}
	if !task.PrivateData.DurableBilling || (billing.Operation != "" && billing.Operation != model.TaskReservationRecalculate && billing.Operation != model.TaskReservationRefund) {
		return errors.New("invalid durable task completion")
	}
	if billing.Operation != "" {
		reservation, delta, err := model.AdjustTaskQuota(ctx, task.TaskID, billing.Quota, billing.Operation)
		if err != nil {
			// Keep the provider result/failure reason intact; reconciliation state
			// belongs to the private companion and is retried by the scheduler.
			_ = model.DB.WithContext(ctx).Model(&model.TaskSubmission{}).
				Where("task_id = ? AND phase = ? AND completion_billing = ?", task.TaskID, model.SubmissionCompleting, submission.CompletionBilling).
				Updates(map[string]any{"error": "Task billing settlement is pending", "next_attempt": time.Now().Unix() + 30, "updated_at": time.Now().Unix()}).Error
			return err
		}
		task.Quota = reservation.Quota
		if delta != 0 {
			logType, logQuota := model.LogTypeConsume, delta
			if delta < 0 {
				logType, logQuota = model.LogTypeRefund, -delta
			}
			other := taskBillingOther(task)
			other.SetPublic("task_id", task.TaskID)
			other.SetPublic("pre_consumed_quota", reservation.Quota-delta)
			other.SetPublic("actual_quota", reservation.Quota)
			if billing.RefundReason != "" {
				other.SetPublic("reason", billing.RefundReason)
			}
			if billing.Saturation != nil {
				other.SetAdmin("quota_saturation", billing.Saturation)
			}
			var content []*common.Message
			if billing.Reason != nil {
				content = []*common.Message{billing.Reason}
			}
			model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
				UserId: task.UserId, LogType: logType, Content: content, ChannelId: task.ChannelId,
				ModelName: taskModelName(task), Quota: logQuota, TokenId: task.PrivateData.TokenId,
				Group: task.Group, Other: other, NodeName: task.PrivateData.NodeName,
			})
		}
	}
	return model.DB.WithContext(ctx).Model(&model.TaskSubmission{}).
		Where("task_id = ? AND phase = ? AND completion_billing = ?", task.TaskID, model.SubmissionCompleting, submission.CompletionBilling).
		Updates(map[string]any{"phase": model.SubmissionComplete, "completion_billing": "", "next_attempt": 0, "updated_at": time.Now().Unix(), "error": ""}).Error
}
