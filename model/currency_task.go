package model

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type CNYTaskSettlement struct {
	Quota  int
	Refund bool
	Usage  CNYBillingUsage
}

func attachCNYTask(tx *gorm.DB, record *CNYBillingRecord, task *Task) error {
	if task == nil || task.UserId != record.UserID || task.PrivateData.TokenId != record.TokenID || task.TaskID == "" ||
		task.PrivateData.Execution == nil || task.PrivateData.Execution.RequestID != record.RequestID {
		return ErrCNYConflict
	}
	if record.TaskID != "" {
		if record.TaskID != task.TaskID {
			return ErrCNYConflict
		}
		return nil
	}
	if record.Status != CNYBillingReserved {
		return ErrCNYState
	}
	record.TaskID, record.Status, record.UpdatedAt = task.TaskID, CNYBillingSubmitted, common.GetTimestamp()
	return tx.Save(record).Error
}

// AttachCNYTask associates an already persisted task without settling it.
func AttachCNYTask(requestID string, task *Task) error {
	record, err := GetCNYBillingRecord(requestID)
	if err != nil {
		return err
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err = lockCNYWallet(tx, record.UserID); err != nil {
			return err
		}
		if err = tx.First(record, record.ID).Error; err != nil {
			return err
		}
		if task == nil || task.ID <= 0 {
			return ErrCNYInvalid
		}
		var persisted Task
		if err = tx.First(&persisted, task.ID).Error; err != nil {
			return err
		}
		if persisted.TaskID != task.TaskID {
			return ErrCNYConflict
		}
		return attachCNYTask(tx, record, &persisted)
	})
}

// InsertCNYTask keeps acceptance pending. A terminal submit result is inserted
// and settled in the same transaction, including zero-cost final records.
func InsertCNYTask(ctx context.Context, task *Task, final *CNYTaskSettlement, omitColumns ...string) error {
	if task == nil || task.PrivateData.Execution == nil {
		return ErrCNYInvalid
	}
	terminal := task.Status == TaskStatusSuccess || task.Status == TaskStatusFailure
	if terminal != (final != nil) {
		return ErrCNYInvalid
	}
	record, err := GetCNYBillingRecord(task.PrivateData.Execution.RequestID)
	if err != nil {
		return err
	}
	candidate := *task
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		wallet, err := lockCNYWallet(tx, record.UserID)
		if err != nil {
			return err
		}
		if err = tx.First(record, record.ID).Error; err != nil {
			return err
		}
		if record.TaskID != "" {
			if record.TaskID != task.TaskID || record.UserID != task.UserId {
				return ErrCNYConflict
			}
			var existing Task
			if err = tx.Where("task_id = ? AND user_id = ?", record.TaskID, record.UserID).First(&existing).Error; err != nil {
				return err
			}
			if final != nil && ((final.Refund && record.Status != CNYBillingRefunded) || (!final.Refund && (record.Status != CNYBillingSettled || record.Quota != final.Quota))) {
				return ErrCNYConflict
			}
			candidate.ID, candidate.Quota, candidate.Status, candidate.Currency = existing.ID, existing.Quota, existing.Status, existing.Currency
			return nil
		}
		if err = attachCNYTask(tx, record, task); err != nil {
			return err
		}
		if final != nil {
			if err = finalizeCNYBilling(tx, wallet, record, final.Quota, final.Refund, final.Usage, task.FailReason); err != nil {
				return err
			}
			if final.Refund {
				candidate.Quota = 0
			} else {
				candidate.Quota = final.Quota
			}
		} else {
			candidate.Quota = record.ReservedQuota
		}
		candidate.Currency = "CNY"
		return tx.Omit(omitColumns...).Create(&candidate).Error
	})
	if err == nil {
		*task = candidate
	}
	return err
}

// FinalizeCNYTask makes task status CAS and funds one commit. Failed funds writes
// leave the task nonterminal so the existing polling loop can retry safely.
func FinalizeCNYTask(task *Task, fromStatus TaskStatus, final CNYTaskSettlement) (bool, error) {
	if task == nil || task.ID <= 0 || task.PrivateData.Execution == nil ||
		(task.Status != TaskStatusSuccess && task.Status != TaskStatusFailure) || fromStatus == TaskStatusSuccess || fromStatus == TaskStatusFailure {
		return false, ErrCNYInvalid
	}
	record, err := GetCNYBillingRecord(task.PrivateData.Execution.RequestID)
	if err != nil {
		return false, err
	}
	updated := false
	candidate := *task
	err = DB.Transaction(func(tx *gorm.DB) error {
		wallet, err := lockCNYWallet(tx, record.UserID)
		if err != nil {
			return err
		}
		if err = tx.First(record, record.ID).Error; err != nil {
			return err
		}
		if record.UserID != task.UserId || record.TokenID != task.PrivateData.TokenId || record.TaskID != task.TaskID {
			return ErrCNYConflict
		}
		if final.Refund {
			candidate.Quota = 0
		} else {
			candidate.Quota = final.Quota
		}
		result := tx.Model(&candidate).Where("status = ? AND user_id = ? AND task_id = ?", fromStatus, record.UserID, record.TaskID).Select("*").Updates(&candidate)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if err = finalizeCNYBilling(tx, wallet, record, final.Quota, final.Refund, final.Usage, task.FailReason); err != nil {
			return err
		}
		updated = true
		return nil
	})
	if updated && err == nil {
		candidate.Currency = "CNY"
		*task = candidate
	}
	return updated && err == nil, err
}
