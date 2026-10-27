package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"gorm.io/gorm"
)

const AssetCreating = "creating"

func AssetCursorSecret(ctx context.Context) (string, error) {
	config, err := LoadAssetStorageConfig(ctx)
	if err != nil || config.CursorSecret != "" {
		return config.CursorSecret, err
	}
	err = protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error { var loadErr error; config, loadErr = lockAssetStorage(tx); return loadErr })
	return config.CursorSecret, err
}

func GetAssetReplicaViews(ctx context.Context, userID int, publicID string) ([]dto.AssetReplicaView, error) {
	asset, err := GetAsset(ctx, userID, publicID, "")
	if err != nil {
		return nil, err
	}
	replicas, err := AssetReplicas(ctx, []int64{asset.ID})
	if err != nil {
		return nil, err
	}
	result := make([]dto.AssetReplicaView, 0, len(replicas))
	for _, replica := range replicas {
		var channel Channel
		name := ""
		if err = protectedAssetDB(ctx).Select("id", "name").First(&channel, replica.ChannelID).Error; err == nil {
			name = channel.Name
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		status := replica.Status
		if replica.State == AssetUnknown {
			status = "Unknown"
		}
		view := dto.AssetReplicaView{ChannelID: replica.ChannelID, ChannelName: name, Status: status, ErrorCode: replica.ErrorCode, ErrorMessage: replica.ErrorMessage, LastAttemptAt: replica.UpdatedAt, CanRetry: replica.Status == AssetFailed && replica.State != AssetDeleted && replica.LeaseExpiresAt <= time.Now().Unix()}
		if replica.ErrorCode == "FileSizeTooLarge" && replica.ErrorMessage == "Material exceeds this channel's size limit" {
			config, err := LoadAssetChannelConfig(ctx, replica.ChannelID)
			if err != nil {
				return nil, err
			}
			limit := config.SizeLimitsMB.ByteLimit(asset.AssetType)
			view.FileSizeBytes = &asset.Size
			view.CurrentLimitBytes = &limit
		}
		result = append(result, view)
	}
	return result, nil
}

func AssetChannelGroupReplicas(ctx context.Context, channelID int) ([]AssetGroupReplica, error) {
	items := []AssetGroupReplica{}
	err := protectedAssetDB(ctx).Where("channel_id = ? AND state <> ?", channelID, AssetDeleted).Find(&items).Error
	return items, err
}

func GetAssetReplica(ctx context.Context, assetID int64, channelID int) (AssetReplica, error) {
	var replica AssetReplica
	err := protectedAssetDB(ctx).Where("asset_id = ? AND channel_id = ?", assetID, channelID).First(&replica).Error
	return replica, err
}

func GetAssetGroupReplica(ctx context.Context, groupID int64, channelID int) (AssetGroupReplica, error) {
	var replica AssetGroupReplica
	err := protectedAssetDB(ctx).Where("group_id = ? AND channel_id = ?", groupID, channelID).First(&replica).Error
	return replica, err
}

// RequeueAssetReplica publishes a read-only reconciliation only if its source
// state is still current. Row locks also fence group workers, which do not take
// the storage lock. No network operation runs inside this transaction.
func RequeueAssetReplica(ctx context.Context, asset *Asset, replica AssetReplica, group AssetGroupReplica, upstreamID, groupUpstreamID string, revision int64) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		storage, err := lockAssetStorage(tx)
		if err != nil {
			return err
		}
		if !storage.Enabled {
			return ErrAssetStorageDisabled
		}
		config, err := loadAssetChannelConfig(tx, replica.ChannelID)
		if err != nil {
			return err
		}
		var channel Channel
		if err = lockForUpdate(tx).First(&channel, replica.ChannelID).Error; err != nil {
			return err
		}
		if config.Revision != revision || !config.Enabled || channel.Status != common.ChannelStatusEnabled || config.ChannelScope != AssetChannelScope(&channel) {
			return ErrAssetConflict
		}
		var current Asset
		if err = lockForUpdate(tx).First(&current, asset.ID).Error; err != nil {
			return err
		}
		if current.DeletedAt > 0 || current.ObjectState != AssetReady || current.UserID != asset.UserID || current.GroupID != asset.GroupID || current.ObjectKey != asset.ObjectKey {
			return ErrAssetConflict
		}
		var owner User
		if err = lockForUpdate(tx).First(&owner, asset.UserID).Error; err != nil {
			return err
		}
		if owner.Status != common.UserStatusEnabled {
			return ErrAssetConflict
		}
		var sourceGroup AssetGroup
		if err = lockForUpdate(tx).First(&sourceGroup, asset.GroupID).Error; err != nil {
			return err
		}
		if sourceGroup.DeletedAt > 0 {
			return ErrAssetConflict
		}
		var currentGroup AssetGroupReplica
		if err = lockForUpdate(tx).First(&currentGroup, group.ID).Error; err != nil {
			return err
		}
		var currentReplica AssetReplica
		if err = lockForUpdate(tx).First(&currentReplica, replica.ID).Error; err != nil {
			return err
		}
		now := time.Now().Unix()
		if currentGroup != group || currentReplica != replica || replica.Status != AssetFailed || replica.State == AssetDeleted ||
			group.State == AssetDeleted || group.LeaseExpiresAt > now || replica.LeaseExpiresAt > now ||
			(replica.UpstreamID != "" && replica.UpstreamID != upstreamID) || (group.UpstreamID != "" && group.UpstreamID != groupUpstreamID) {
			return ErrAssetConflict
		}
		groupState := AssetPending
		if groupUpstreamID != "" {
			groupState = AssetReady
		}
		if group.State != groupState || group.UpstreamID != groupUpstreamID || group.ErrorCode != "" || group.ErrorMessage != "" || group.LeaseToken != "" || group.LeaseExpiresAt != 0 {
			result := tx.Model(&AssetGroupReplica{}).Where("id = ?", group.ID).Updates(map[string]any{"state": groupState, "upstream_id": groupUpstreamID, "error_code": "", "error_message": "", "lease_token": "", "lease_expires_at": 0, "create_started_at": 0, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrAssetConflict
			}
		}
		state := AssetPending
		if upstreamID != "" {
			state = AssetReady
		}
		result := tx.Model(&AssetReplica{}).Where("id = ?", replica.ID).Updates(map[string]any{"status": AssetProcessing, "state": state, "upstream_id": upstreamID, "error_code": "", "error_message": "", "attempts": 0, "next_attempt_at": now, "lease_token": "", "lease_expires_at": 0, "create_started_at": 0, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAssetConflict
		}
		return refreshAssetStatus(tx, asset.ID, "", "")
	})
}

// Leases use conditional updates rather than process-local locks. Their token
// fences completions from a worker whose lease was already reclaimed.
func ClaimAssetWork(ctx context.Context, now int64, limit int) ([]Asset, error) {
	var candidates []Asset
	inUse := protectedAssetDB(ctx).Model(&AssetUse{}).Select("asset_id")
	busyReplicas := protectedAssetDB(ctx).Model(&AssetReplica{}).Select("asset_id").Where("lease_expires_at > ?", now)
	err := protectedAssetDB(ctx).Where("next_attempt_at <= ? AND lease_expires_at <= ?", now, now).
		Where("(deleted_at > 0 AND object_state <> ? AND id NOT IN (?) AND id NOT IN (?)) OR (deleted_at = 0 AND status = ? AND object_state IN ?)", AssetDeleted, inUse, busyReplicas, AssetProcessing, []string{AssetPending, "uploading"}).Order("next_attempt_at, id").Limit(limit).Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	claimed := []Asset{}
	for _, asset := range candidates {
		err = protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
			if _, err := lockAssetStorage(tx); err != nil {
				return err
			}
			if asset.DeletedAt > 0 {
				var count int64
				if err := tx.Model(&AssetUse{}).Where("asset_id = ?", asset.ID).Count(&count).Error; err != nil {
					return err
				}
				if count > 0 {
					return nil
				}
				if err := tx.Model(&AssetReplica{}).Where("asset_id = ? AND lease_expires_at > ?", asset.ID, now).Count(&count).Error; err != nil {
					return err
				}
				if count > 0 {
					return nil
				}
			}
			token := common.GetUUID()
			result := tx.Model(&Asset{}).Where("id = ? AND lease_expires_at <= ? AND deleted_at = ?", asset.ID, now, asset.DeletedAt).Updates(map[string]any{"lease_token": token, "lease_expires_at": now + 180})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				asset.LeaseToken = token
				asset.LeaseExpiresAt = now + 180
				claimed = append(claimed, asset)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return claimed, nil
}

func FinishAssetSource(ctx context.Context, asset *Asset, code, message string, retryAt int64) error {
	values := map[string]any{"lease_token": "", "lease_expires_at": 0, "updated_at": time.Now().Unix(), "error_code": code, "error_message": message, "next_attempt_at": retryAt, "attempts": asset.Attempts + 1}
	if code == "" {
		values["object_state"] = asset.ObjectState
		values["content_type"] = asset.ContentType
		values["size"] = asset.Size
		values["sha256"] = asset.SHA256
		values["source_url"] = ""
	} else if retryAt == 0 {
		values["status"] = AssetFailed
	}
	result := protectedAssetDB(ctx).Model(&Asset{}).Where("id = ? AND lease_token = ?", asset.ID, asset.LeaseToken).Updates(values)
	if result.Error == nil && result.RowsAffected == 0 {
		return ErrAssetConflict
	}
	return result.Error
}

func FinishAssetCleanup(ctx context.Context, asset *Asset, done bool) error {
	values := map[string]any{"lease_token": "", "lease_expires_at": 0, "next_attempt_at": time.Now().Add(time.Minute).Unix()}
	if done {
		values["object_state"] = AssetDeleted
		values["source_url"] = ""
	}
	return protectedAssetDB(ctx).Model(&Asset{}).Where("id = ? AND lease_token = ?", asset.ID, asset.LeaseToken).Updates(values).Error
}

func ClaimAssetReplicas(ctx context.Context, now int64, limit int) ([]AssetReplica, error) {
	var candidates []AssetReplica
	readyAssets := protectedAssetDB(ctx).Model(&Asset{}).Select("id").Where("deleted_at = 0 AND object_state = ?", AssetReady)
	err := protectedAssetDB(ctx).Where("status = ? AND state <> ? AND next_attempt_at <= ? AND lease_expires_at <= ? AND asset_id IN (?)", AssetProcessing, AssetDeleted, now, now, readyAssets).Order("next_attempt_at, id").Limit(limit).Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	claimed := []AssetReplica{}
	for _, replica := range candidates {
		err = protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
			if _, err := lockAssetStorage(tx); err != nil {
				return err
			}
			var asset Asset
			if err := tx.First(&asset, replica.AssetID).Error; err != nil {
				return err
			}
			if asset.DeletedAt > 0 || asset.ObjectState != AssetReady {
				return nil
			}
			token := common.GetUUID()
			result := tx.Model(&AssetReplica{}).Where("id = ? AND status = ? AND lease_expires_at <= ?", replica.ID, AssetProcessing, now).Updates(map[string]any{"lease_token": token, "lease_expires_at": now + 120, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				replica.LeaseToken = token
				replica.LeaseExpiresAt = now + 120
				claimed = append(claimed, replica)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return claimed, nil
}

func MarkAssetReplicaCreating(ctx context.Context, replica *AssetReplica) error {
	now := time.Now().Unix()
	result := protectedAssetDB(ctx).Model(&AssetReplica{}).Where("id = ? AND lease_token = ? AND state = ?", replica.ID, replica.LeaseToken, AssetPending).Updates(map[string]any{"state": AssetCreating, "create_started_at": now})
	if result.Error == nil && result.RowsAffected == 0 {
		return ErrAssetConflict
	}
	if result.Error == nil {
		replica.State = AssetCreating
		replica.CreateStartedAt = now
	}
	return result.Error
}

func FinishAssetReplica(ctx context.Context, replica *AssetReplica, retryAt int64) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize aggregate publication across replicas and deletion/holds.
		if _, err := lockAssetStorage(tx); err != nil {
			return err
		}
		result := tx.Model(&AssetReplica{}).Where("id = ? AND lease_token = ?", replica.ID, replica.LeaseToken).Updates(map[string]any{"state": replica.State, "status": replica.Status, "upstream_id": replica.UpstreamID, "error_code": replica.ErrorCode, "error_message": replica.ErrorMessage, "moderation_strategy": replica.ModerationStrategy, "attempts": replica.Attempts + 1, "next_attempt_at": retryAt, "lease_token": "", "lease_expires_at": 0, "updated_at": time.Now().Unix()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrAssetConflict
		}
		return refreshAssetStatus(tx, replica.AssetID, replica.ErrorCode, replica.ErrorMessage)
	})
}

func refreshAssetStatus(tx *gorm.DB, assetID int64, code, message string) error {
	var replicas []AssetReplica
	if err := tx.Where("asset_id = ? AND state <> ?", assetID, AssetDeleted).Find(&replicas).Error; err != nil {
		return err
	}
	status := AssetFailed
	moderation := ""
	for _, item := range replicas {
		if item.Status == AssetActive && item.State == AssetReady && item.UpstreamID != "" {
			status = AssetActive
			moderation = item.ModerationStrategy
			break
		}
		if item.Status == AssetProcessing {
			status = AssetProcessing
		}
	}
	if status != AssetFailed {
		code, message = "", ""
	}
	return tx.Model(&Asset{}).Where("id = ?", assetID).Updates(map[string]any{"status": status, "error_code": code, "error_message": message, "moderation_strategy": moderation, "updated_at": time.Now().Unix()}).Error
}

func ClaimAssetGroupReplica(ctx context.Context, groupID int64, channelID int, now int64) (*AssetGroupReplica, bool, error) {
	var replica AssetGroupReplica
	err := protectedAssetDB(ctx).Where("group_id = ? AND channel_id = ?", groupID, channelID).First(&replica).Error
	if err != nil {
		return nil, false, err
	}
	if replica.State == AssetReady || replica.State == AssetFailed {
		return &replica, false, nil
	}
	token := common.GetUUID()
	result := protectedAssetDB(ctx).Model(&AssetGroupReplica{}).
		Where("id = ? AND state = ? AND upstream_id = ? AND create_started_at = ? AND lease_token = ? AND lease_expires_at = ? AND lease_expires_at <= ?",
			replica.ID, replica.State, replica.UpstreamID, replica.CreateStartedAt, replica.LeaseToken, replica.LeaseExpiresAt, now).
		Updates(map[string]any{"lease_token": token, "lease_expires_at": now + 90})
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		return &replica, false, nil
	}
	replica.LeaseToken = token
	replica.LeaseExpiresAt = now + 90
	return &replica, true, nil
}

func MarkAssetGroupCreating(ctx context.Context, replica *AssetGroupReplica) error {
	now := time.Now().Unix()
	result := protectedAssetDB(ctx).Model(&AssetGroupReplica{}).Where("id = ? AND lease_token = ? AND state = ?", replica.ID, replica.LeaseToken, AssetPending).Updates(map[string]any{"state": AssetCreating, "create_started_at": now})
	if result.Error == nil && result.RowsAffected == 0 {
		return ErrAssetConflict
	}
	if result.Error == nil {
		replica.State = AssetCreating
		replica.CreateStartedAt = now
	}
	return result.Error
}

func FinishAssetGroupReplica(ctx context.Context, replica *AssetGroupReplica) error {
	result := protectedAssetDB(ctx).Model(&AssetGroupReplica{}).Where("id = ? AND lease_token = ?", replica.ID, replica.LeaseToken).Updates(map[string]any{"state": replica.State, "upstream_id": replica.UpstreamID, "error_code": replica.ErrorCode, "error_message": replica.ErrorMessage, "lease_token": "", "lease_expires_at": 0, "updated_at": time.Now().Unix()})
	if result.Error == nil && result.RowsAffected == 0 {
		return ErrAssetConflict
	}
	return result.Error
}

func MarkAssetReplicaDeleted(ctx context.Context, id int64) error {
	return protectedAssetDB(ctx).Model(&AssetReplica{}).Where("id = ?", id).Updates(map[string]any{"state": AssetDeleted, "lease_token": "", "lease_expires_at": 0}).Error
}

func ClaimDeletedAssetGroups(ctx context.Context, now int64, limit int) ([]AssetGroupReplica, error) {
	deletedGroups := protectedAssetDB(ctx).Model(&AssetGroup{}).Select("id").Where("deleted_at > 0")
	remainingAssets := protectedAssetDB(ctx).Model(&Asset{}).Select("group_id").Where("object_state <> ?", AssetDeleted)
	var candidates []AssetGroupReplica
	if err := protectedAssetDB(ctx).Where("group_id IN (?) AND group_id NOT IN (?) AND state <> ? AND lease_expires_at <= ? AND updated_at <= ?", deletedGroups, remainingAssets, AssetDeleted, now, now-30).Order("id").Limit(limit).Find(&candidates).Error; err != nil {
		return nil, err
	}
	claimed := []AssetGroupReplica{}
	for _, replica := range candidates {
		token := common.GetUUID()
		result := protectedAssetDB(ctx).Model(&AssetGroupReplica{}).Where("id = ? AND lease_expires_at <= ?", replica.ID, now).Updates(map[string]any{"lease_token": token, "lease_expires_at": now + 90})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected > 0 {
			replica.LeaseToken = token
			claimed = append(claimed, replica)
		}
	}
	return claimed, nil
}
