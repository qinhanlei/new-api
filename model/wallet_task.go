package model

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type WalletTaskSettlement struct {
	Quota  int
	Refund bool
}

func walletFundingMatchesTask(record *WalletFundingRecord, task *Task) bool {
	if billing := task.PrivateData.BillingContext; billing == nil || billing.TieredSnapshot != nil && billing.TieredSnapshot.BillingCurrency() != "USD" {
		return false
	}
	return record.Currency == "USD" && task.UserId == record.UserID && task.PrivateData.TokenId == record.TokenID &&
		task.PrivateData.WalletFundingRequestID == record.RequestID && task.TaskID != "" &&
		task.PrivateData.Execution != nil && task.PrivateData.Execution.RequestID == record.RequestID &&
		task.PrivateData.BillingSource == "wallet" && task.PrivateData.SubscriptionId == 0
}

// InsertWalletTask transfers an accepted USD request to durable task ownership.
// Only the creator increments request_count; actual model usage is recorded
// when a terminal result and its funding commit together.
func InsertWalletTask(ctx context.Context, task *Task, final *WalletTaskSettlement, omitColumns ...string) (bool, error) {
	if task == nil || task.PrivateData.WalletFundingRequestID == "" || task.Quota < 0 || task.Quota > common.MaxQuota {
		return false, ErrCNYInvalid
	}
	terminal := task.Status == TaskStatusSuccess || task.Status == TaskStatusFailure
	if terminal != (final != nil) {
		return false, ErrCNYInvalid
	}
	record, err := GetWalletFundingRecord(task.PrivateData.WalletFundingRequestID)
	if err != nil {
		return false, err
	}
	candidate := *task
	created := false
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		wallet, err := lockCNYWallet(tx, record.UserID)
		if err != nil {
			return err
		}
		var user User
		if err = lockForUpdate(tx.Unscoped()).First(&user, record.UserID).Error; err != nil {
			return err
		}
		if err = lockForUpdate(tx).First(record, record.ID).Error; err != nil {
			return err
		}
		if !walletFundingMatchesTask(record, task) {
			return ErrCNYConflict
		}
		if record.TaskID != "" {
			if record.TaskID != task.TaskID {
				return ErrCNYConflict
			}
			var existing Task
			if err = tx.Where("task_id = ? AND user_id = ?", record.TaskID, record.UserID).First(&existing).Error; err != nil {
				return err
			}
			if !walletFundingMatchesTask(record, &existing) || final != nil &&
				((final.Refund && record.Status != CNYBillingRefunded) || (!final.Refund && (record.Status != CNYBillingSettled || record.Quota != final.Quota))) {
				return ErrCNYConflict
			}
			candidate = existing
			return nil
		}
		if record.Status != CNYBillingReserved {
			return ErrCNYState
		}
		record.TaskID, record.Status, record.UpdatedAt = task.TaskID, CNYBillingSubmitted, common.GetTimestamp()
		if err = tx.Save(record).Error; err != nil {
			return err
		}
		if final != nil {
			if err = finalizeWalletTaskFunding(tx, wallet, &user, record, &candidate, *final); err != nil {
				return err
			}
		}
		if err = tx.Omit(omitColumns...).Create(&candidate).Error; err != nil {
			return err
		}
		if err = tx.Unscoped().Model(&User{}).Where("id = ?", record.UserID).UpdateColumn("request_count", gorm.Expr("request_count + ?", 1)).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	if err == nil {
		*task = candidate
		invalidateWalletTaskQuotaCache(record)
	}
	return created && err == nil, err
}

// FinalizeWalletTask makes the task status CAS, both wallets, the USD key budget,
// and native USD usage counters one commit. A stale poll never moves money.
func FinalizeWalletTask(task *Task, fromStatus TaskStatus, final WalletTaskSettlement) (bool, error) {
	if task == nil || task.ID <= 0 || task.PrivateData.WalletFundingRequestID == "" ||
		(task.Status != TaskStatusSuccess && task.Status != TaskStatusFailure) || fromStatus == TaskStatusSuccess || fromStatus == TaskStatusFailure {
		return false, ErrCNYInvalid
	}
	record, err := GetWalletFundingRecord(task.PrivateData.WalletFundingRequestID)
	if err != nil {
		return false, err
	}
	candidate := *task
	won := false
	err = DB.Transaction(func(tx *gorm.DB) error {
		wallet, err := lockCNYWallet(tx, record.UserID)
		if err != nil {
			return err
		}
		var user User
		if err = lockForUpdate(tx.Unscoped()).First(&user, record.UserID).Error; err != nil {
			return err
		}
		if err = lockForUpdate(tx).First(record, record.ID).Error; err != nil {
			return err
		}
		if !walletFundingMatchesTask(record, task) || record.TaskID != task.TaskID {
			return ErrCNYConflict
		}
		candidate.Quota = final.Quota
		if final.Refund {
			candidate.Quota = 0
		}
		result := tx.Model(&candidate).Where("status = ? AND user_id = ? AND task_id = ?", fromStatus, record.UserID, record.TaskID).Select("*").Updates(&candidate)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		if record.Status != CNYBillingSubmitted {
			return ErrCNYState
		}
		if err = finalizeWalletTaskFunding(tx, wallet, &user, record, &candidate, final); err != nil {
			return err
		}
		won = true
		return nil
	})
	if won && err == nil {
		*task = candidate
		invalidateWalletTaskQuotaCache(record)
	}
	return won && err == nil, err
}

func finalizeWalletTaskFunding(tx *gorm.DB, wallet *CNYWallet, user *User, record *WalletFundingRecord, task *Task, final WalletTaskSettlement) error {
	if err := finalizeWalletFunding(tx, wallet, user, record, final.Quota, final.Refund); err != nil {
		return err
	}
	task.Quota = final.Quota
	if final.Refund {
		task.Quota = 0
	}
	if task.Quota == 0 {
		return nil
	}
	if err := tx.Unscoped().Model(&User{}).Where("id = ?", task.UserId).UpdateColumn("used_quota", gorm.Expr("used_quota + ?", task.Quota)).Error; err != nil {
		return err
	}
	return tx.Model(&Channel{}).Where("id = ?", task.ChannelId).UpdateColumn("used_quota", gorm.Expr("used_quota + ?", task.Quota)).Error
}

func invalidateWalletTaskQuotaCache(record *WalletFundingRecord) {
	InvalidateUserQuotaCache(record.UserID)
	if record.TokenID > 0 {
		var token Token
		if DB.Unscoped().Select("key").First(&token, record.TokenID).Error == nil {
			InvalidateTokenQuotaCache(token.Key)
		}
	}
}
