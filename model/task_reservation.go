package model

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrTaskReservationQuota = errors.New("insufficient quota for task reservation")

const (
	TaskReservationReserve     = "reserve"
	TaskReservationSettle      = "settle"
	TaskReservationRefund      = "refund"
	TaskReservationRecalculate = "recalculate"
)

// TaskReservation is the durable billing state of one deferred submission.
// The row and all balance changes commit together. No credentials are stored.
type TaskReservation struct {
	TaskID         string `gorm:"type:varchar(191);primaryKey"`
	UserID         int    `gorm:"index;not null"`
	TokenID        int
	ChannelID      int
	FundingSource  string `gorm:"type:varchar(20);not null"`
	SubscriptionID int
	Quota          int
	AccountedQuota int
	Accounted      bool
	State          string `gorm:"type:varchar(20);not null"`
	CreatedAt      int64
	UpdatedAt      int64
	// Live funding context is returned for existing quota notifications and is
	// not part of the durable idempotency record.
	SubscriptionAmountTotal int64 `gorm:"-"`
	SubscriptionAmountUsed  int64 `gorm:"-"`
	SubscriptionPlanID      int   `gorm:"-"`
}

type TaskReservationRequest struct {
	TaskID       string
	UserID       int
	TokenID      int
	ChannelID    int
	Model        string
	Preference   string
	Quota        int
	IsPlayground bool
}

func ReserveTaskQuota(ctx context.Context, request TaskReservationRequest) (*TaskReservation, error) {
	if !common.AssetLibraryEnabled {
		return nil, errors.New("durable task billing is disabled")
	}
	if request.TaskID == "" || len(request.TaskID) > 191 || request.UserID <= 0 || request.ChannelID <= 0 || request.Quota < 0 || request.Quota > common.MaxQuota {
		return nil, errors.New("invalid task reservation")
	}
	if request.IsPlayground {
		request.TokenID = 0
	} else if request.TokenID <= 0 {
		return nil, errors.New("task reservation requires an API token")
	}
	var reservation TaskReservation
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The user lock serializes the first insert too, before a reservation row
		// exists. Adjustment uses the same lock order.
		var user User
		if err := lockForUpdate(tx).First(&user, request.UserID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return errors.New("task reservation user is disabled")
		}
		result := tx.Where("task_id = ?", request.TaskID).Limit(1).Find(&reservation)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if reservation.UserID != request.UserID || reservation.TokenID != request.TokenID || reservation.ChannelID != request.ChannelID {
				return errors.New("task reservation identity does not match")
			}
			if reservation.State != "reserved" {
				return errors.New("task reservation was already submitted or refunded")
			}
			if err := adjustTaskTokenTx(tx, &reservation, 0, true); err != nil {
				return err
			}
			return taskReservationFundingContext(tx, &reservation)
		}
		reservation = TaskReservation{
			TaskID: request.TaskID, UserID: request.UserID, TokenID: request.TokenID,
			ChannelID: request.ChannelID, Quota: request.Quota, State: "reserved",
		}
		if err := reserveTaskFundingTx(tx, &reservation, request.Preference); err != nil {
			return err
		}
		if err := adjustTaskTokenTx(tx, &reservation, reservation.Quota, true); err != nil {
			return err
		}
		if err := tx.Create(&reservation).Error; err != nil {
			return err
		}
		return taskReservationFundingContext(tx, &reservation)
	})
	return &reservation, err
}

func reserveTaskFundingTx(tx *gorm.DB, reservation *TaskReservation, preference string) error {
	preference = common.NormalizeBillingPreference(preference)
	if reservation.Quota == 0 || preference == "wallet_only" {
		return reserveTaskWalletTx(tx, reservation)
	}
	if preference == "wallet_first" {
		if err := reserveTaskWalletTx(tx, reservation); !errors.Is(err, ErrTaskReservationQuota) {
			return err
		}
		return reserveTaskSubscriptionTx(tx, reservation)
	}
	if preference == "subscription_only" {
		return reserveTaskSubscriptionTx(tx, reservation)
	}
	var active, strict int64
	query := tx.Model(&UserSubscription{}).Where("user_id = ? AND status = ? AND end_time > ?", reservation.UserID, "active", common.GetTimestamp())
	if err := query.Count(&active).Error; err != nil {
		return err
	}
	if active == 0 {
		return reserveTaskWalletTx(tx, reservation)
	}
	if err := reserveTaskSubscriptionTx(tx, reservation); !errors.Is(err, ErrTaskReservationQuota) {
		return err
	}
	if err := query.Where("allow_wallet_overflow = ?", false).Count(&strict).Error; err != nil {
		return err
	}
	if strict > 0 {
		return ErrTaskReservationQuota
	}
	return reserveTaskWalletTx(tx, reservation)
}

func reserveTaskWalletTx(tx *gorm.DB, reservation *TaskReservation) error {
	if reservation.Quota > 0 {
		result := tx.Model(&User{}).Where("id = ? AND quota >= ?", reservation.UserID, reservation.Quota).
			Update("quota", gorm.Expr("quota - ?", reservation.Quota))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrTaskReservationQuota
		}
	}
	reservation.FundingSource = "wallet"
	return nil
}

func reserveTaskSubscriptionTx(tx *gorm.DB, reservation *TaskReservation) error {
	result := &SubscriptionPreConsumeResult{}
	err := preConsumeUserSubscriptionTx(tx, reservation.TaskID, reservation.UserID, int64(reservation.Quota), common.GetTimestamp(), result)
	if err != nil {
		// The existing subscription API has no typed insufficient-quota error.
		if strings.Contains(err.Error(), "no active subscription") || strings.Contains(err.Error(), "subscription quota insufficient") {
			return ErrTaskReservationQuota
		}
		return err
	}
	reservation.FundingSource = "subscription"
	reservation.SubscriptionID = result.UserSubscriptionId
	return nil
}

// AdjustTaskQuota returns the committed change in balance, so repeated callers
// do not emit another consumption/refund log for an already applied change.
func AdjustTaskQuota(ctx context.Context, taskID string, targetQuota int, operation string) (*TaskReservation, int, error) {
	if targetQuota < 0 || targetQuota > common.MaxQuota {
		return nil, 0, errors.New("invalid task quota")
	}
	switch operation {
	case TaskReservationReserve, TaskReservationSettle, TaskReservationRecalculate:
	case TaskReservationRefund:
		targetQuota = 0
	default:
		return nil, 0, errors.New("invalid task reservation operation")
	}
	var reservation TaskReservation
	if err := DB.WithContext(ctx).Where("task_id = ?", taskID).First(&reservation).Error; err != nil {
		return nil, 0, err
	}
	delta := 0
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		// Refunds must still reach the balances of soft-deleted users/tokens.
		if err := lockForUpdate(tx.Unscoped()).First(&user, reservation.UserID).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).Where("task_id = ?", taskID).First(&reservation).Error; err != nil {
			return err
		}
		if reservation.State == "refunded" || operation == TaskReservationSettle && reservation.State == "settled" {
			return nil
		}
		if operation == TaskReservationReserve && (reservation.State != "reserved" || targetQuota <= reservation.Quota) {
			return nil
		}
		if operation == TaskReservationRecalculate && reservation.State != "settled" {
			return errors.New("task reservation is not settled")
		}
		delta = targetQuota - reservation.Quota
		if delta != 0 {
			if reservation.FundingSource == "subscription" {
				if err := postConsumeUserSubscriptionDeltaTx(tx, reservation.SubscriptionID, int64(delta)); err != nil {
					if strings.Contains(err.Error(), "subscription used exceeds total") {
						return ErrTaskReservationQuota
					}
					return err
				}
			} else {
				query := tx.Unscoped().Model(&User{}).Where("id = ?", reservation.UserID)
				if delta > 0 {
					query = query.Where("quota >= ?", delta)
				} else {
					query = query.Where("quota <= ?", common.MaxWalletQuota+delta)
				}
				result := query.Update("quota", gorm.Expr("quota - ?", delta))
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrTaskReservationQuota
				}
			}
			if err := adjustTaskTokenTx(tx, &reservation, delta, operation == TaskReservationReserve); err != nil {
				return err
			}
		}
		reservation.Quota = targetQuota
		if operation == TaskReservationSettle || operation == TaskReservationRecalculate || operation == TaskReservationRefund {
			if reservation.Accounted || operation == TaskReservationSettle {
				usageDelta := targetQuota - reservation.AccountedQuota
				updates := map[string]any{"used_quota": gorm.Expr("used_quota + ?", usageDelta)}
				if !reservation.Accounted {
					updates["request_count"] = gorm.Expr("request_count + ?", 1)
				}
				if err := tx.Unscoped().Model(&User{}).Where("id = ?", reservation.UserID).Updates(updates).Error; err != nil {
					return err
				}
				if err := tx.Model(&Channel{}).Where("id = ?", reservation.ChannelID).Update("used_quota", gorm.Expr("used_quota + ?", usageDelta)).Error; err != nil {
					return err
				}
				reservation.Accounted = true
				reservation.AccountedQuota = targetQuota
			}
			reservation.State = "settled"
			if operation == TaskReservationRefund {
				reservation.State = "refunded"
				if reservation.FundingSource == "subscription" {
					if err := tx.Model(&SubscriptionPreConsumeRecord{}).Where("request_id = ?", taskID).Update("status", "refunded").Error; err != nil {
						return err
					}
				}
			}
		}
		if err := tx.Model(&Task{}).Where("task_id = ? AND user_id = ?", taskID, reservation.UserID).Update("quota", targetQuota).Error; err != nil {
			return err
		}
		if err := tx.Save(&reservation).Error; err != nil {
			return err
		}
		return taskReservationFundingContext(tx, &reservation)
	})
	if err != nil {
		return nil, 0, err
	}
	return &reservation, delta, nil
}

func adjustTaskTokenTx(tx *gorm.DB, reservation *TaskReservation, delta int, beforeSubmit bool) error {
	if reservation.TokenID == 0 {
		return nil
	}
	var token Token
	if err := lockForUpdate(tx.Unscoped()).Where("id = ? AND user_id = ?", reservation.TokenID, reservation.UserID).First(&token).Error; err != nil {
		return err
	}
	if beforeSubmit && (token.DeletedAt.Valid || token.Status != common.TokenStatusEnabled || token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp()) {
		return errors.New("task reservation token is invalid")
	}
	if delta > 0 && !token.UnlimitedQuota && token.RemainQuota < delta {
		return fmt.Errorf("%w: token balance", ErrTaskReservationQuota)
	}
	return tx.Unscoped().Model(&Token{}).Where("id = ?", token.Id).Updates(map[string]any{
		"remain_quota":  gorm.Expr("remain_quota - ?", delta),
		"used_quota":    gorm.Expr("used_quota + ?", delta),
		"accessed_time": common.GetTimestamp(),
	}).Error
}

func taskReservationFundingContext(tx *gorm.DB, reservation *TaskReservation) error {
	if reservation.FundingSource != "subscription" {
		return nil
	}
	var sub UserSubscription
	if err := tx.Select("amount_total", "amount_used", "plan_id").First(&sub, reservation.SubscriptionID).Error; err != nil {
		return err
	}
	reservation.SubscriptionAmountTotal = sub.AmountTotal
	reservation.SubscriptionAmountUsed = sub.AmountUsed
	reservation.SubscriptionPlanID = sub.PlanId
	return nil
}
