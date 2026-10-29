package model

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Callers hold the storage lock so deletion cannot race a new reference,
// upload, or replica. A real-person group only uses its authenticated replica.
func checkAssetGroupChannel(tx *gorm.DB, groupID int64, channelID int) (*AssetGroup, error) {
	var group AssetGroup
	if err := lockForUpdate(tx).First(&group, groupID).Error; err != nil {
		return nil, err
	}
	if !group.Usable() {
		return nil, ErrAssetGroupUnavailable
	}
	if group.GroupType == AssetGroupLivenessFace {
		if group.InitialChannelID <= 0 || (channelID != 0 && group.InitialChannelID != channelID) {
			return nil, ErrAssetChannelConflict
		}
		if channelID != 0 {
			var replica AssetGroupReplica
			if err := tx.Where("group_id = ? AND channel_id = ? AND state = ? AND upstream_id <> ''", group.ID, channelID, AssetReady).First(&replica).Error; err != nil {
				return nil, err
			}
		}
	}
	return &group, nil
}

// Human deletion leaves the group and originals intact until the upstream has
// accepted deletion. Unknown outcomes stay frozen even after an explicit retry.
func requestHumanAssetGroupDeletion(tx *gorm.DB, group *AssetGroup) error {
	if group.Lifecycle == AssetGroupDeleted {
		return nil
	}
	if group.DeletedAt > 0 {
		return gorm.ErrRecordNotFound
	}
	now := time.Now().Unix()
	if group.LeaseExpiresAt > now {
		return nil
	}
	status := AssetGroupDeletePending
	if group.DeleteUncertain || group.Lifecycle == AssetGroupDeleteUnknown {
		status = AssetGroupDeleteUnknown
	}
	return tx.Model(group).Updates(map[string]any{
		"lifecycle": status, "next_attempt_at": now, "updated_at": now,
		"lease_token": "", "lease_expires_at": 0,
	}).Error
}

// Cancellation is safe only after a definite rejection, with no earlier
// uncertain request. A timeout followed by a rejection is still uncertain.
func CancelAssetGroupDeletion(ctx context.Context, userID int, id, project string) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockAssetStorage(tx); err != nil {
			return err
		}
		var group AssetGroup
		if err := lockForUpdate(tx).Where("user_id = ? AND public_id = ? AND project_name = ? AND deleted_at = 0", userID, id, project).First(&group).Error; err != nil {
			return err
		}
		if group.GroupType != AssetGroupLivenessFace || group.Lifecycle != AssetGroupDeleteBlocked || group.DeleteUncertain || group.LeaseExpiresAt > time.Now().Unix() {
			return ErrAssetConflict
		}
		return tx.Model(&group).Updates(map[string]any{
			"lifecycle": AssetGroupActive, "error_code": "", "error_message": "", "next_attempt_at": 0,
			"lease_token": "", "lease_expires_at": 0, "updated_at": time.Now().Unix(),
		}).Error
	})
}

// Claims wait for both inference holds and network-operation leases. No child
// cleanup is allowed while deletion of the upstream group is unresolved.
func ClaimHumanAssetGroupDeletions(ctx context.Context, now int64, limit int) ([]AssetGroup, error) {
	db := protectedAssetDB(ctx)
	uses := db.Model(&AssetUse{}).Select("asset_id")
	heldGroups := db.Model(&Asset{}).Select("group_id").Where("id IN (?)", uses)
	busySources := db.Model(&Asset{}).Select("group_id").Where("lease_expires_at > ?", now)
	busyReplicas := db.Model(&AssetReplica{}).Select("asset_id").Where("lease_expires_at > ?", now)
	busyReplicaGroups := db.Model(&Asset{}).Select("group_id").Where("id IN (?)", busyReplicas)
	busyGroups := db.Model(&AssetGroupReplica{}).Select("group_id").Where("lease_expires_at > ?", now)
	var candidates []AssetGroup
	if err := db.Where("group_type = ? AND deleted_at = 0 AND lifecycle IN ?", AssetGroupLivenessFace, []string{AssetGroupDeletePending, AssetGroupDeleteUnknown}).
		Where("next_attempt_at > 0 AND next_attempt_at <= ? AND lease_expires_at <= ?", now, now).
		Where("id NOT IN (?) AND id NOT IN (?) AND id NOT IN (?) AND id NOT IN (?)", heldGroups, busySources, busyReplicaGroups, busyGroups).
		Order("next_attempt_at, id").Limit(limit).Find(&candidates).Error; err != nil {
		return nil, err
	}
	claimed := []AssetGroup{}
	for _, candidate := range candidates {
		err := protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
			if _, err := lockAssetStorage(tx); err != nil {
				return err
			}
			var group AssetGroup
			if err := lockForUpdate(tx).First(&group, candidate.ID).Error; err != nil {
				return err
			}
			if group != candidate {
				return nil
			}
			assets := tx.Model(&Asset{}).Select("id").Where("group_id = ?", group.ID)
			var count int64
			if err := tx.Model(&AssetUse{}).Where("asset_id IN (?)", assets).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
			if err := tx.Model(&Asset{}).Where("group_id = ? AND lease_expires_at > ?", group.ID, now).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
			if err := tx.Model(&AssetReplica{}).Where("asset_id IN (?) AND lease_expires_at > ?", assets, now).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
			if err := tx.Model(&AssetGroupReplica{}).Where("group_id = ? AND lease_expires_at > ?", group.ID, now).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
			group.LeaseToken = common.GetUUID()
			group.LeaseExpiresAt = now + 90
			group.Lifecycle = AssetGroupDeleteUnknown
			// Persist uncertainty before I/O. The returned snapshot retains the
			// previous flag so a definite first-attempt rejection can be cancelled.
			if err := tx.Model(&AssetGroup{}).Where("id = ?", group.ID).Updates(map[string]any{
				"lifecycle": group.Lifecycle, "delete_uncertain": true, "lease_token": group.LeaseToken,
				"lease_expires_at": group.LeaseExpiresAt, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			claimed = append(claimed, group)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return claimed, nil
}

// Only a fenced confirmation of upstream deletion releases originals for S3
// cleanup. Errors never erase the bound upstream group or its assets.
func FinishHumanAssetGroupDeletion(ctx context.Context, group *AssetGroup, status, code, message string, retryAt int64) error {
	if group.LeaseToken == "" || (status != AssetGroupDeleted && status != AssetGroupDeleteUnknown && status != AssetGroupDeleteBlocked && status != AssetGroupDeletePending) {
		return ErrAssetConflict
	}
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockAssetStorage(tx); err != nil {
			return err
		}
		var current AssetGroup
		if err := lockForUpdate(tx).First(&current, group.ID).Error; err != nil {
			return err
		}
		if current.GroupType != AssetGroupLivenessFace || current.Lifecycle != AssetGroupDeleteUnknown || current.LeaseToken != group.LeaseToken || current.DeletedAt != 0 {
			return ErrAssetConflict
		}
		uncertain := group.DeleteUncertain || status == AssetGroupDeleteUnknown
		if uncertain && (status == AssetGroupDeleteBlocked || status == AssetGroupDeletePending) {
			status = AssetGroupDeleteUnknown
		}
		now := time.Now().Unix()
		values := map[string]any{
			"lifecycle": status, "error_code": code, "error_message": message,
			"delete_uncertain": uncertain, "next_attempt_at": retryAt,
			"lease_token": "", "lease_expires_at": 0, "updated_at": now,
		}
		if status == AssetGroupDeleted {
			values["deleted_at"] = now
			values["error_code"], values["error_message"] = "", ""
			values["delete_uncertain"], values["next_attempt_at"] = false, 0
			assets := tx.Model(&Asset{}).Select("id").Where("group_id = ?", group.ID)
			if err := tx.Model(&AssetReplica{}).Where("asset_id IN (?)", assets).Updates(map[string]any{
				"state": AssetDeleted, "lease_token": "", "lease_expires_at": 0, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			if err := tx.Model(&AssetGroupReplica{}).Where("group_id = ?", group.ID).Updates(map[string]any{
				"state": AssetDeleted, "lease_token": "", "lease_expires_at": 0, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			if err := tx.Model(&Asset{}).Where("group_id = ? AND deleted_at = 0", group.ID).Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
			if err := tx.Model(&Asset{}).Where("group_id = ?", group.ID).Updates(map[string]any{"next_attempt_at": now, "lease_token": "", "lease_expires_at": 0}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&current).Updates(values).Error
	})
}
