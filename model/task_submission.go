package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

const (
	SubmissionWaiting    = "waiting_assets"
	SubmissionSending    = "submitting"
	SubmissionAccepted   = "accepted"
	SubmissionComplete   = "complete"
	SubmissionCompleting = "completing"
	SubmissionUnknown    = "unknown"
	SubmissionFailed     = "failed"
)

// TaskSubmission owns the private, resumable input; audit snapshots deliberately
// contain no request payload. A sending lease may never be replayed after expiry.
type TaskSubmission struct {
	TaskID            string `gorm:"type:varchar(64);primaryKey"`
	UserID            int
	TokenID           int
	ChannelID         int
	SpaceID           string `gorm:"type:varchar(64);index"`
	PluginKey         string `gorm:"type:varchar(30)"`
	PluginHash        string `gorm:"type:varchar(64)"`
	Model             string `gorm:"type:varchar(191)"`
	UpstreamModel     string `gorm:"type:varchar(191)"`
	Action            string `gorm:"type:varchar(40)"`
	Group             string `gorm:"type:varchar(50)"`
	RequestPath       string `gorm:"type:varchar(191)"`
	RequestBody       LongText
	OriginTaskIDs     []string `gorm:"serializer:json;type:text"`
	AssetIDs          []string `gorm:"serializer:json;type:text"`
	Phase             string   `gorm:"type:varchar(24);index:idx_submission_work"`
	LeaseOwner        string   `gorm:"type:varchar(64)"`
	LeaseUntil        int64
	NextAttempt       int64 `gorm:"index:idx_submission_work"`
	CreatedAt         int64
	UpdatedAt         int64
	Error             string `gorm:"type:text"`
	ReconciledBy      int
	ReconcileNote     string `gorm:"type:text"`
	ReconciledAt      int64
	ResultStatus      TaskStatus `gorm:"type:varchar(20)"`
	ResultQuota       int
	CompletionBilling LongText
}

// CompleteTaskWithBilling makes the terminal result and its frozen accounting
// intent visible together. Quota remains owned by the reservation ledger.
func CompleteTaskWithBilling(ctx context.Context, task *Task, fromStatus TaskStatus, billing LongText) (bool, error) {
	won := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(task).Where("status = ?", fromStatus).Select("*").Omit("quota").Updates(task)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		result = tx.Model(&TaskSubmission{}).Where("task_id = ? AND phase = ?", task.TaskID, SubmissionComplete).
			Updates(map[string]any{"phase": SubmissionCompleting, "completion_billing": billing, "next_attempt": 0, "updated_at": time.Now().Unix(), "error": ""})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("task completion has no settled submission")
		}
		won = true
		return nil
	})
	return won && err == nil, err
}

// QueueTaskBillingCompletion also persists explicitly requested adjustments.
// A refund may supersede a pending completion; a refunded ledger is final.
func QueueTaskBillingCompletion(ctx context.Context, taskID string, billing LongText) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var submission TaskSubmission
		if err := lockForUpdate(tx).Where("task_id = ?", taskID).First(&submission).Error; err != nil {
			return err
		}
		if submission.Phase != SubmissionComplete && submission.Phase != SubmissionCompleting {
			return errors.New("task submission is not ready for completion")
		}
		return tx.Model(&submission).Updates(map[string]any{"phase": SubmissionCompleting, "completion_billing": billing, "next_attempt": 0, "updated_at": time.Now().Unix(), "error": ""}).Error
	})
}

// TaskAssetUse holds deleted sources until every accepted consumer is terminal.
type TaskAssetUse struct {
	TaskID  string `gorm:"type:varchar(64);primaryKey"`
	AssetID string `gorm:"type:varchar(64);primaryKey;index"`
}

func CreateTaskSubmission(ctx context.Context, task *Task, submission *TaskSubmission) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := LockChannelAssetScope(tx, submission.ChannelID, submission.SpaceID); err != nil {
			return err
		}
		for _, id := range submission.AssetIDs {
			var asset Asset
			if err := lockForUpdate(tx).Where("id = ? AND user_id = ? AND deleted_at = 0 AND status = ?", id, submission.UserID, AssetStatusReady).First(&asset).Error; err != nil {
				return errors.New("asset not found or unavailable")
			}
		}
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		if err := tx.Create(submission).Error; err != nil {
			return err
		}
		for _, id := range submission.AssetIDs {
			if err := tx.Create(&TaskAssetUse{TaskID: task.TaskID, AssetID: id}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func ClaimTaskSubmission(ctx context.Context, id, owner string, now int64) (*TaskSubmission, bool, error) {
	result := DB.WithContext(ctx).Model(&TaskSubmission{}).
		Where("task_id = ? AND phase IN ? AND lease_until < ? AND next_attempt <= ?", id, []string{SubmissionWaiting, SubmissionAccepted, SubmissionFailed}, now, now).
		Updates(map[string]any{"lease_owner": owner, "lease_until": now + 120, "updated_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		return nil, false, result.Error
	}
	var submission TaskSubmission
	err := DB.WithContext(ctx).Where("task_id = ? AND lease_owner = ?", id, owner).First(&submission).Error
	return &submission, err == nil, err
}

func MarkTaskSubmissionSending(ctx context.Context, id, owner string, private TaskPrivateData, quota int) error {
	now := time.Now().Unix()
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&TaskSubmission{}).Where("task_id = ? AND phase = ? AND lease_owner = ? AND lease_until > ?", id, SubmissionWaiting, owner, now).
			Updates(map[string]any{"phase": SubmissionSending, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("submission lease lost")
		}
		return tx.Model(&Task{}).Where("task_id = ?", id).Updates(map[string]any{"status": TaskStatusSubmitting, "private_data": private, "quota": quota}).Error
	})
}

func AssetHasConsumers(ctx context.Context, id string) (bool, error) {
	var count int64
	err := DB.WithContext(ctx).Model(&TaskAssetUse{}).
		Where("asset_id = ? AND task_id IN (?)", id, DB.Model(&Task{}).Select("task_id").Where("status NOT IN ?", []string{TaskStatusSuccess, TaskStatusFailure})).Count(&count).Error
	return count > 0, err
}
