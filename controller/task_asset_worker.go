package controller

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type assetTaskHandler struct{}

func (assetTaskHandler) Type() string            { return "asset_tasks" }
func (assetTaskHandler) Enabled() bool           { return common.AssetLibraryEnabled }
func (assetTaskHandler) Interval() time.Duration { return 15 * time.Second }
func (assetTaskHandler) NewPayload() any         { return nil }
func init()                                      { service.RegisterSystemTaskHandler(assetTaskHandler{}) }

func (assetTaskHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	err := runAssetTasks(ctx, runnerID)
	status, message := model.SystemTaskStatusSucceeded, ""
	if err != nil {
		status, message = model.SystemTaskStatusFailed, "Asset task processing failed"
		logger.LogWarn(ctx, common.LogText("asset task processing failed: %s", err.Error()))
	}
	if finishErr := model.FinishSystemTask(task.TaskID, runnerID, status, nil, message); finishErr != nil {
		logger.LogWarn(ctx, common.LogText("asset system task completion failed: %s", finishErr.Error()))
	}
}

func runAssetTasks(ctx context.Context, owner string) error {
	now := time.Now().Unix()
	// Expired sending leases are ambiguous. They cannot enter the normal task
	// poller (which would mistake the public ID for a provider task ID).
	var expired []model.TaskSubmission
	if err := model.DB.WithContext(ctx).Select("task_id").Where("phase = ? AND lease_until < ?", model.SubmissionSending, now).Limit(20).Find(&expired).Error; err != nil {
		return err
	}
	for _, candidate := range expired {
		if err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// Match the acceptance lock order: fence the companion first. A
			// snapshot subquery on tasks could overwrite a concurrently accepted job.
			result := tx.Model(&model.TaskSubmission{}).Where("task_id = ? AND phase = ? AND lease_until < ?", candidate.TaskID, model.SubmissionSending, now).
				Updates(map[string]any{"phase": model.SubmissionUnknown, "error": "Submission outcome needs administrator review", "lease_owner": "", "lease_until": 0})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return nil
			}
			return tx.Model(&model.Task{}).Where("task_id = ? AND status = ?", candidate.TaskID, model.TaskStatusSubmitting).Updates(map[string]any{"status": model.TaskStatusSubmitUnknown, "fail_reason": "Submission outcome needs administrator review"}).Error
		}); err != nil {
			return err
		}
	}
	var pending []model.TaskSubmission
	if err := model.DB.WithContext(ctx).Where("phase IN ? AND next_attempt <= ? AND lease_until < ?", []string{model.SubmissionWaiting, model.SubmissionAccepted, model.SubmissionFailed}, now, now).Order("next_attempt, created_at, task_id").Limit(5).Find(&pending).Error; err != nil {
		return err
	}
	for _, candidate := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		submission, claimed, err := model.ClaimTaskSubmission(ctx, candidate.TaskID, common.GetRandomString(32), time.Now().Unix())
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		workCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		err = processAssetTask(workCtx, submission)
		cancel()
		if err != nil {
			logger.LogWarn(ctx, common.LogText("asset task %s processing failed: %s", submission.TaskID, err.Error()))
		}
	}
	billingErr := service.RecoverDurableTaskCompletions(ctx)
	if ctx.Err() != nil {
		return errors.Join(billingErr, ctx.Err())
	}
	return errors.Join(billingErr, service.CleanupDeletedAssets(ctx), service.CleanupChannelAssets(ctx, owner))
}

func processAssetTask(ctx context.Context, submission *model.TaskSubmission) error {
	if submission.Phase == model.SubmissionAccepted {
		return settleAssetSubmission(ctx, submission)
	}
	if submission.Phase == model.SubmissionFailed {
		return failAssetSubmission(ctx, submission, submission.Error)
	}
	if time.Now().Unix()-submission.CreatedAt > 24*60*60 {
		return failAssetSubmission(ctx, submission, "Asset preparation timed out")
	}
	resolved, ready, err := service.ResolveTaskAssets(ctx, submission, submission.LeaseOwner)
	if err != nil {
		return failAssetSubmission(ctx, submission, err.Error())
	}
	if !ready {
		return model.DB.WithContext(ctx).Model(submission).Where("lease_owner = ?", submission.LeaseOwner).Updates(map[string]any{"lease_owner": "", "lease_until": 0, "next_attempt": time.Now().Unix() + 15}).Error
	}
	c, info, err := resumeAssetTaskContext(ctx, submission, resolved)
	if err != nil {
		return failAssetSubmission(ctx, submission, err.Error())
	}
	defer common.CleanupBodyStorage(c)
	result, taskErr := relay.RelayTaskSubmit(c, info)
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer finishCancel()
	if taskErr != nil {
		if c.GetBool("task_submission_sent") && !c.GetBool("task_submission_rejected") {
			// Even a provider 5xx can follow an accepted POST. Without provider
			// idempotency, conservative reconciliation is safer than a duplicate.
			return markAssetSubmissionUnknown(finishCtx, submission)
		}
		return failAssetSubmission(finishCtx, submission, taskErr.Message)
	}
	task, exists, err := model.GetByTaskId(submission.UserID, submission.TaskID)
	if err != nil || !exists {
		return errors.New("pending asset task disappeared")
	}
	task.PrivateData.UpstreamTaskID = result.UpstreamTaskID
	task.Data, task.PrivateData.PluginState = result.TaskData, result.PluginState
	task.Quota, task.Action = result.Quota, info.Action
	task.PrivateData.BillingContext = service.TaskBillingContextFromRelayInfo(info)
	submission.ResultStatus = model.TaskStatusSubmitted
	submission.ResultQuota = result.Quota
	if result.Immediate != nil {
		submission.ResultStatus = model.TaskStatus(result.Immediate.Status)
		task.Progress, task.FailReason = result.Immediate.Progress, result.Immediate.Reason
		task.PrivateData.ResultURL = result.Immediate.Url
	}
	// Persist acceptance before any operation which can fail during accounting.
	err = model.DB.WithContext(finishCtx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(submission).Where("phase = ? AND lease_owner = ? AND lease_until > ?", model.SubmissionSending, submission.LeaseOwner, time.Now().Unix()).
			Updates(map[string]any{"phase": model.SubmissionAccepted, "result_status": submission.ResultStatus, "result_quota": result.Quota, "updated_at": time.Now().Unix()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("submission lease lost")
		}
		if err := tx.Model(task).Select("private_data", "data", "quota", "action", "progress", "fail_reason").Updates(task).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	submission.Phase = model.SubmissionAccepted
	return settleAssetSubmission(finishCtx, submission)
}

func settleAssetSubmission(ctx context.Context, submission *model.TaskSubmission) error {
	var owned int64
	if err := model.DB.WithContext(ctx).Model(&model.TaskSubmission{}).Where("task_id = ? AND phase = ? AND lease_owner = ? AND lease_until > ?", submission.TaskID, model.SubmissionAccepted, submission.LeaseOwner, time.Now().Unix()).Count(&owned).Error; err != nil {
		return err
	}
	if owned != 1 {
		return errors.New("submission lease lost")
	}
	if _, _, err := model.AdjustTaskQuota(ctx, submission.TaskID, submission.ResultQuota, model.TaskReservationSettle); err != nil {
		// A known accepted job must never be sent again. Expose its recoverable
		// accounting state; topping up permits the next scheduled settlement.
		_ = model.DB.WithContext(ctx).Model(&model.Task{}).Where("task_id = ?", submission.TaskID).Update("fail_reason", "Accepted task is waiting for billing settlement").Error
		_ = model.DB.WithContext(ctx).Model(submission).Where("phase = ? AND lease_owner = ?", model.SubmissionAccepted, submission.LeaseOwner).Updates(map[string]any{"error": "Accepted task is waiting for billing settlement", "lease_until": 0, "lease_owner": "", "next_attempt": time.Now().Unix() + 30}).Error
		return err
	}
	var task model.Task
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(submission).Where("phase = ? AND lease_owner = ?", model.SubmissionAccepted, submission.LeaseOwner).Updates(map[string]any{"phase": model.SubmissionComplete, "request_body": "", "lease_until": 0, "lease_owner": ""})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("submission lease lost")
		}
		updates := map[string]any{"status": submission.ResultStatus, "updated_at": time.Now().Unix(), "submit_time": time.Now().Unix()}
		if submission.ResultStatus != model.TaskStatusFailure {
			updates["fail_reason"] = ""
		}
		if submission.ResultStatus == model.TaskStatusSuccess || submission.ResultStatus == model.TaskStatusFailure {
			updates["finish_time"] = time.Now().Unix()
		}
		result = tx.Model(&model.Task{}).Where("task_id = ? AND status = ?", submission.TaskID, model.TaskStatusSubmitting).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("submission status changed")
		}
		return tx.Where("task_id = ? AND user_id = ?", submission.TaskID, submission.UserID).First(&task).Error
	})
	if err != nil {
		return err
	}
	// Both first acceptance and recovery use the frozen billing context. This
	// keeps the existing best-effort log semantics without resubmitting a job.
	service.LogDurableTaskConsumption(ctx, &task)
	return nil
}

func failAssetSubmission(ctx context.Context, submission *model.TaskSubmission, reason string) error {
	if submission.Phase == model.SubmissionFailed {
		var owned int64
		if err := model.DB.WithContext(ctx).Model(&model.TaskSubmission{}).Where("task_id = ? AND phase = ? AND lease_owner = ? AND lease_until > ?", submission.TaskID, model.SubmissionFailed, submission.LeaseOwner, time.Now().Unix()).Count(&owned).Error; err != nil {
			return err
		}
		if owned != 1 {
			return errors.New("submission lease lost")
		}
	} else {
		result := model.DB.WithContext(ctx).Model(submission).Where("phase IN ? AND lease_owner = ? AND lease_until > ?", []string{model.SubmissionWaiting, model.SubmissionSending}, submission.LeaseOwner, time.Now().Unix()).Updates(map[string]any{"phase": model.SubmissionFailed, "error": reason})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("submission lease lost")
		}
	}
	if err := service.RefundDurableTaskBilling(ctx, submission.TaskID); err != nil {
		return err
	}
	return model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(submission).Where("phase = ? AND lease_owner = ?", model.SubmissionFailed, submission.LeaseOwner).Updates(map[string]any{"phase": model.SubmissionComplete, "error": reason, "request_body": "", "lease_until": 0, "lease_owner": ""})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("submission lease lost")
		}
		if err := tx.Model(&model.Task{}).Where("task_id = ?", submission.TaskID).Updates(map[string]any{"status": model.TaskStatusFailure, "fail_reason": reason, "finish_time": time.Now().Unix(), "progress": "100%"}).Error; err != nil {
			return err
		}
		return nil
	})
}

func markAssetSubmissionUnknown(ctx context.Context, submission *model.TaskSubmission) error {
	return model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(submission).Where("phase = ? AND lease_owner = ?", model.SubmissionSending, submission.LeaseOwner).Updates(map[string]any{"phase": model.SubmissionUnknown, "lease_until": 0, "lease_owner": ""})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("submission lease lost")
		}
		if err := tx.Model(&model.Task{}).Where("task_id = ?", submission.TaskID).Updates(map[string]any{"status": model.TaskStatusSubmitUnknown, "fail_reason": "Submission outcome needs administrator review"}).Error; err != nil {
			return err
		}
		return nil
	})
}

func resumeAssetTaskContext(ctx context.Context, submission *model.TaskSubmission, resolved map[string]any) (*gin.Context, *relaycommon.RelayInfo, error) {
	generation := jsplugin.DefaultRegistry.Generation()
	plugin, exists := generation.Get(submission.PluginKey)
	if !exists || plugin.SourceHash != submission.PluginHash {
		return nil, nil, errors.New("Task plugin changed while preparing assets")
	}
	channel, err := model.GetChannelById(submission.ChannelID, true)
	if err != nil || channel.Status != common.ChannelStatusEnabled || channel.ChannelInfo.IsMultiKey || !channel.SupportsAssets() {
		return nil, nil, errors.New("Asset channel changed or is disabled")
	}
	space, err := model.GetAssetSpace(ctx, submission.SpaceID)
	if err != nil || space.ChannelID != channel.Id || space.GroupState != "ready" {
		return nil, nil, errors.New("Asset channel changed or is disabled")
	}
	if _, err := service.ChannelAssetProviderClient(channel, space); err != nil {
		return nil, nil, errors.New("Asset channel changed or is disabled")
	}
	token, err := model.GetTokenById(submission.TokenID)
	if err != nil || token.UserId != submission.UserID || token.Status != common.TokenStatusEnabled || token.ExpiredTime > 0 && token.ExpiredTime < time.Now().Unix() {
		return nil, nil, errors.New("Task token expired or was revoked")
	}
	if token.ModelLimitsEnabled && !token.GetModelLimitsMap()[submission.Model] {
		return nil, nil, errors.New("Task model is no longer allowed by token")
	}
	user, err := model.GetUserCache(submission.UserID)
	if err != nil || user.Status != common.UserStatusEnabled {
		return nil, nil, errors.New("Task user is unavailable")
	}
	if !service.GroupInUserUsableGroups(user.Group, submission.Group) {
		return nil, nil, errors.New("Task group is no longer available")
	}
	if token.Group != "" && token.Group != "auto" && token.Group != submission.Group {
		return nil, nil, errors.New("Task token group changed")
	}
	if token.Group == "" && user.Group != submission.Group {
		return nil, nil, errors.New("Task user group changed")
	}
	var enabled int64
	if err := model.DB.WithContext(ctx).Model(&model.Ability{}).Where(&model.Ability{ChannelId: submission.ChannelID, Model: submission.Model, Group: submission.Group, Enabled: true}).Count(&enabled).Error; err != nil || enabled == 0 {
		return nil, nil, errors.New("Task channel no longer serves the selected model and group")
	}
	// Submission runs without an HTTP response; relay preparation uses only
	// the request and context values, and the worker persists its outcome.
	c := &gin.Context{}
	c.Request, err = http.NewRequestWithContext(ctx, http.MethodPost, submission.RequestPath, strings.NewReader(string(submission.RequestBody)))
	if err != nil {
		return nil, nil, err
	}
	c.Request.Header.Set("Content-Type", "application/json")
	user.WriteContext(c)
	if err := middleware.SetupContextForToken(c, token); err != nil {
		return nil, nil, err
	}
	if token.Group == "auto" && !slices.Contains(service.GetRequestAutoGroups(c, user.Group), submission.Group) {
		return nil, nil, errors.New("Task token auto group is no longer available")
	}
	common.SetContextKey(c, constant.ContextKeyUsingGroup, submission.Group)
	c.Set(common.RequestIdKey, submission.TaskID)
	c.Set("expected_task_plugin_key", submission.PluginKey)
	c.Set("task_plugin_key", submission.PluginKey)
	c.Set("platform", submission.PluginKey)
	c.Set("resolved_task_model", submission.Model)
	c.Set("task_action", submission.Action)
	c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{Generation: generation, Plugin: plugin})
	if err := middleware.SetupContextForSelectedChannel(c, channel, submission.Model); err != nil {
		return nil, nil, errors.New("Task channel binding changed")
	}
	var body any
	if err := common.Unmarshal([]byte(submission.RequestBody), &body); err != nil {
		return nil, nil, err
	}
	c.Set("task_request", body)
	c.Set(service.ContextTaskSubmission, submission)
	c.Set(service.ContextResolvedAssets, resolved)
	info, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	info.PublicTaskID, info.Action, info.OriginModelName = submission.TaskID, submission.Action, submission.Model
	for _, id := range submission.OriginTaskIDs {
		origin, exists, err := model.GetByTaskId(submission.UserID, id)
		if err != nil || !exists || origin.ChannelId != submission.ChannelID || string(origin.Platform) != submission.PluginKey || !origin.ResultRetrievable() {
			return nil, nil, errors.New("Origin task unavailable")
		}
		info.OriginTasks = append(info.OriginTasks, relaycommon.OriginTaskRef{TaskID: id, UpstreamTaskID: origin.GetUpstreamTaskID(), Action: origin.Action, Status: string(origin.Status), Data: origin.Data})
	}
	return c, info, nil
}
