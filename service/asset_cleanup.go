package service

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/assetprovider"
	"gorm.io/gorm"
)

// CleanupDeletedAssets retains originals while accepted tasks can still use
// them. Unknown imports stay visible for reconciliation rather than losing the
// only copy from which their provider-side state can be investigated.
func CleanupDeletedAssets(ctx context.Context) error {
	now := time.Now().Unix()
	if err := model.DB.WithContext(ctx).Model(&model.Asset{}).Where("status = ? AND updated_at < ?", model.AssetStatusUploading, now-60*60).
		Updates(map[string]any{"status": model.AssetStatusDeleted, "error": "Asset upload did not complete", "deleted_at": now}).Error; err != nil {
		return err
	}
	var assets []model.Asset
	if err := model.DB.WithContext(ctx).Where("deleted_at > 0 AND updated_at <= ?", now-15).Order("updated_at, id").Limit(10).Find(&assets).Error; err != nil {
		return err
	}
	if len(assets) == 0 {
		return nil
	}
	for _, asset := range assets {
		// Rotate unsuccessful cleanup attempts so one unknown import cannot
		// indefinitely prevent unrelated deleted files from being reclaimed.
		if err := model.DB.WithContext(ctx).Model(&asset).Updates(map[string]any{"updated_at": now, "error": "Asset cleanup is pending"}).Error; err != nil {
			return err
		}
		used, err := model.AssetHasConsumers(ctx, asset.ID)
		if err != nil {
			return err
		}
		if used {
			continue
		}
		var replicas []model.AssetReplica
		if err := model.DB.WithContext(ctx).Where("asset_id = ?", asset.ID).Find(&replicas).Error; err != nil {
			return err
		}
		clean := true
		for _, replica := range replicas {
			if replica.LeaseUntil > now || replica.Status == model.AssetReplicaUnknown && replica.RemoteID == "" {
				clean = false
				continue
			}
			if replica.RemoteID != "" {
				var space model.AssetSpace
				if err := model.DB.WithContext(ctx).Where("id = ?", replica.SpaceID).First(&space).Error; err != nil {
					clean = false
					continue
				}
				client, err := AssetProviderClient(ctx, &space)
				if err == nil {
					err = client.Delete(ctx, replica.RemoteID)
				}
				if err != nil {
					clean = false
					if err := model.DB.WithContext(ctx).Model(&replica).Update("error", "Asset cleanup failed").Error; err != nil {
						return err
					}
					continue
				}
			}
			if err := model.DB.WithContext(ctx).Delete(&replica).Error; err != nil {
				return err
			}
		}
		if !clean {
			continue
		}
		store, err := GetAssetStoreForBackend(asset.StorageBackend)
		if err == nil {
			err = store.Delete(ctx, asset.ObjectKey)
		}
		if err != nil {
			if err := model.DB.WithContext(ctx).Model(&asset).Update("error", "Asset cleanup failed").Error; err != nil {
				return err
			}
			continue
		}
		if err := model.DB.WithContext(ctx).Where("asset_id = ?", asset.ID).Delete(&model.TaskAssetUse{}).Error; err != nil {
			return err
		}
		if err := model.DB.WithContext(ctx).Delete(&asset).Error; err != nil {
			return err
		}
	}
	return nil
}

// CleanupChannelAssets removes a disabled channel's dedicated group only after
// all its consumers and accounting recovery have finished. DeleteAssetGroup is
// idempotent and also removes imports whose result was lost in transit.
func CleanupChannelAssets(ctx context.Context, owner string) error {
	now := time.Now().Unix()
	db := model.DB.WithContext(ctx)
	var spaces []model.AssetSpace
	if err := db.Where("group_state = ? AND lease_until <= ? AND next_attempt <= ?", "cleaning", now, now).
		Order("updated_at, id").Limit(10).Find(&spaces).Error; err != nil {
		return err
	}
	for _, space := range spaces {
		claim := db.Model(&model.AssetSpace{}).Where("id = ? AND group_state = ? AND lease_until <= ?", space.ID, "cleaning", now).
			Updates(map[string]any{"lease_owner": owner, "lease_until": now + 90, "updated_at": now})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			continue
		}
		err := cleanupChannelAssetGroup(ctx, &space, owner)
		if err != nil {
			if updateErr := db.Model(&model.AssetSpace{}).Where("id = ? AND lease_owner = ?", space.ID, owner).
				Updates(map[string]any{"error": "Asset channel cleanup is pending", "lease_owner": "", "lease_until": 0, "next_attempt": now + 30}).Error; updateErr != nil {
				return updateErr
			}
		}
	}
	return nil
}

func cleanupChannelAssetGroup(ctx context.Context, space *model.AssetSpace, owner string) error {
	db := model.DB.WithContext(ctx)
	var pending int64
	if err := db.Model(&model.TaskSubmission{}).Where("space_id = ? AND phase <> ?", space.ID, model.SubmissionComplete).Count(&pending).Error; err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("asset tasks are still active")
	}
	if err := db.Model(&model.Task{}).Where("task_id IN (?) AND status NOT IN ?",
		db.Model(&model.TaskSubmission{}).Select("task_id").Where("space_id = ?", space.ID),
		[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).Count(&pending).Error; err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("asset tasks are still active")
	}
	if err := db.Model(&model.AssetReplica{}).Where("space_id = ? AND lease_until > ?", space.ID, time.Now().Unix()).Count(&pending).Error; err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("asset imports are still active")
	}
	if space.GroupID != "" {
		client, err := AssetProviderClient(ctx, space)
		if err != nil {
			return err
		}
		group, err := client.GetGroup(ctx, space.GroupID)
		if err != nil && !assetprovider.IsNotFound(err) {
			return err
		}
		if err == nil {
			// Only this gateway's automatically created group may be deleted.
			if group.Name != space.ID {
				return errors.New("asset group identity changed")
			}
			if err := client.DeleteGroup(ctx, space.GroupID); err != nil {
				return err
			}
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND group_state = ? AND lease_owner = ?", space.ID, "cleaning", owner).Delete(&model.AssetSpace{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("asset group lease lost")
		}
		return tx.Where("space_id = ?", space.ID).Delete(&model.AssetReplica{}).Error
	})
}
