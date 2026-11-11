package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// TryReserveUserQuota atomically checks and deducts the committed wallet
// balance. Balance changes never enter the statistics batch queue.
func TryReserveUserQuota(id int, quota int) (bool, error) {
	if quota < 0 {
		return false, errors.New("quota cannot be negative")
	}
	if err := common.ValidateWalletQuota(quota); err != nil {
		return false, err
	}
	if quota == 0 {
		return true, nil
	}
	result := DB.Model(&User{}).
		Where("id = ? AND quota >= ?", id, quota).
		Update("quota", gorm.Expr("quota - ?", quota))
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		InvalidateUserQuotaCache(id)
		return true, nil
	}
	return false, nil
}

// TryReserveTokenQuota uses the persisted unlimited flag, because callers may
// hold a snapshot from before the token's quota settings changed. The legacy
// unlimited argument remains for callers; Redis never authorizes spending.
func TryReserveTokenQuota(id int, key string, quota int, _ bool) (bool, error) {
	if quota < 0 {
		return false, errors.New("quota cannot be negative")
	}
	if err := common.ValidateWalletQuota(quota); err != nil {
		return false, err
	}
	if quota == 0 {
		return true, nil
	}
	result := DB.Model(&Token{}).
		Where("id = ? AND (unlimited_quota = ? OR remain_quota >= ?)", id, true, quota).
		Where("remain_quota >= ? AND used_quota <= ?", -common.MaxWalletQuota+quota, common.MaxWalletQuota-quota).
		Updates(map[string]any{
			"remain_quota":  gorm.Expr("remain_quota - ?", quota),
			"used_quota":    gorm.Expr("used_quota + ?", quota),
			"accessed_time": common.GetTimestamp(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		InvalidateTokenQuotaCache(key)
		return true, nil
	}
	return false, nil
}
