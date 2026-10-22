package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	AssetStorageLocal      = "local"
	AssetStorageS3         = "s3"
	AssetStatusUploading   = "uploading"
	AssetStatusReady       = "ready"
	AssetStatusDeleted     = "deleted"
	AssetStatusFailed      = "failed"
	AssetReplicaPending    = "pending"
	AssetReplicaProcessing = "processing"
	AssetReplicaReady      = "ready"
	AssetReplicaFailed     = "failed"
	AssetReplicaUnknown    = "unknown"
	AssetGroupPending      = "pending"
	AssetGroupReady        = "ready"
	AssetGroupUnknown      = "unknown"
	AssetGroupCleaning     = "cleaning"
	AssetGroupFailed       = "failed"
)

var ErrAssetLimit = errors.New("Asset storage limit reached")
var ErrAssetUploading = errors.New("Asset upload is still in progress")

// Asset contains an immutable source. Provider identities and storage keys are
// never returned by the public API.
type Asset struct {
	ID              string  `json:"id" gorm:"type:varchar(64);primaryKey"`
	UserID          int     `json:"-" gorm:"index:idx_asset_owner"`
	Name            string  `json:"name" gorm:"type:varchar(255)"`
	Kind            string  `json:"kind" gorm:"type:varchar(16)"`
	MIMEType        string  `json:"mime_type" gorm:"type:varchar(128)"`
	Size            int64   `json:"size"`
	SHA256          string  `json:"sha256" gorm:"type:varchar(64)"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	ObjectKey       string  `json:"-" gorm:"type:varchar(255)"`
	StorageBackend  string  `json:"-" gorm:"type:varchar(16)"`
	Status          string  `json:"status" gorm:"type:varchar(32)"`
	Error           string  `json:"error,omitempty" gorm:"type:text"`
	CreatedAt       int64   `json:"created_at"`
	UpdatedAt       int64   `json:"updated_at"`
	DeletedAt       int64   `json:"-" gorm:"index:idx_asset_owner"`
}

// AssetSpace is the private, automatically created asset namespace of a channel.
// Credentials remain on the channel; endpoint and project are pinned until cleanup.
type AssetSpace struct {
	ID            string `json:"id" gorm:"type:varchar(64);primaryKey"`
	ChannelID     int    `json:"channel_id" gorm:"uniqueIndex"`
	BaseURL       string `json:"-" gorm:"type:varchar(512)"`
	ProjectName   string `json:"-" gorm:"type:varchar(255)"`
	GroupID       string `json:"-" gorm:"type:varchar(255)"`
	GroupState    string `json:"status" gorm:"type:varchar(32)"`
	Error         string `json:"error,omitempty" gorm:"type:text"`
	LeaseOwner    string `json:"-" gorm:"type:varchar(64)"`
	LeaseUntil    int64  `json:"-"`
	NextAttempt   int64  `json:"-"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	ReconciledBy  int    `json:"-"`
	ReconcileNote string `json:"-" gorm:"type:text"`
	ReconciledAt  int64  `json:"-"`
}

type AssetReplica struct {
	AssetID       string `json:"-" gorm:"type:varchar(64);primaryKey"`
	SpaceID       string `json:"-" gorm:"type:varchar(64);primaryKey"`
	RemoteID      string `json:"-" gorm:"type:varchar(255)"`
	GenerationRef string `json:"-" gorm:"type:varchar(512)"`
	Status        string `json:"status" gorm:"type:varchar(32)"`
	Error         string `json:"error,omitempty" gorm:"type:text"`
	LeaseOwner    string `json:"-" gorm:"type:varchar(64)"`
	LeaseUntil    int64  `json:"-"`
	NextAttempt   int64  `json:"-"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	ReconciledBy  int    `json:"-"`
	ReconcileNote string `json:"-" gorm:"type:text"`
	ReconciledAt  int64  `json:"-"`
}

func GetAsset(ctx context.Context, id string) (*Asset, error) {
	var asset Asset
	err := DB.WithContext(ctx).Where("id = ?", id).First(&asset).Error
	return &asset, err
}

func GetUserAsset(ctx context.Context, userID int, id string) (*Asset, error) {
	var asset Asset
	err := DB.WithContext(ctx).Where("id = ? AND user_id = ? AND deleted_at = 0", id, userID).First(&asset).Error
	return &asset, err
}

func ListUserAssets(ctx context.Context, userID, offset, limit int) ([]Asset, int64, error) {
	assets := make([]Asset, 0)
	query := DB.WithContext(ctx).Model(&Asset{}).Where("user_id = ?", userID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&assets).Error
	return assets, total, err
}

// ReserveAsset includes unfinished uploads in limits. The user lock serializes
// reservations on databases with row locks; SQLite serializes database writes.
func ReserveAsset(ctx context.Context, asset *Asset, maxCount, maxBytes int64) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).Select("id").First(&user, asset.UserID).Error; err != nil {
			return err
		}
		var usage struct {
			Count int64
			Bytes int64
		}
		if err := tx.Model(&Asset{}).Select("COUNT(*) AS count, COALESCE(SUM(size), 0) AS bytes").Where("user_id = ?", asset.UserID).Scan(&usage).Error; err != nil {
			return err
		}
		if usage.Count >= maxCount || asset.Size > maxBytes-usage.Bytes {
			return ErrAssetLimit
		}
		return tx.Create(asset).Error
	})
}

func RenameUserAsset(ctx context.Context, userID int, id, name string) error {
	result := DB.WithContext(ctx).Model(&Asset{}).Where("id = ? AND user_id = ? AND deleted_at = 0", id, userID).Updates(map[string]any{"name": name, "updated_at": time.Now().Unix()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		_, err := GetUserAsset(ctx, userID, id)
		return err
	}
	return nil
}

// Deletion is a tombstone: accepted video tasks may still need the source and
// replicas. A background worker removes them after their task holds finish.
func DeleteUserAsset(ctx context.Context, userID int, id string) error {
	now := time.Now().Unix()
	result := DB.WithContext(ctx).Model(&Asset{}).Where("id = ? AND user_id = ? AND deleted_at = 0 AND status <> ?", id, userID, AssetStatusUploading).Updates(map[string]any{"status": AssetStatusDeleted, "deleted_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		asset, err := GetUserAsset(ctx, userID, id)
		if err != nil {
			return err
		}
		if asset.Status == AssetStatusUploading {
			return ErrAssetUploading
		}
		return gorm.ErrRecordNotFound
	}
	return nil
}

func GetAssetSpace(ctx context.Context, id string) (*AssetSpace, error) {
	var space AssetSpace
	err := DB.WithContext(ctx).Where("id = ?", id).First(&space).Error
	return &space, err
}

func GetChannelAssetSpace(ctx context.Context, channelID int) (*AssetSpace, error) {
	var space AssetSpace
	err := DB.WithContext(ctx).Where("channel_id = ?", channelID).First(&space).Error
	return &space, err
}

func EnsureChannelAssetSpace(ctx context.Context, channelID int) (*AssetSpace, error) {
	var space AssetSpace
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, channelID).Error; err != nil {
			return err
		}
		config, err := channel.AssetConfiguration()
		if err != nil || channel.Status != common.ChannelStatusEnabled {
			return ErrChannelAssetsUnavailable
		}
		err = tx.Where("channel_id = ?", channelID).First(&space).Error
		if err == nil {
			if space.BaseURL != config.BaseURL || space.ProjectName != config.ProjectName || space.GroupState == AssetGroupCleaning {
				return ErrChannelAssetScopeInUse
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().Unix()
		space = AssetSpace{ID: fmt.Sprintf("na_space_%d_%s", channelID, common.GetUUID()), ChannelID: channelID, BaseURL: config.BaseURL, ProjectName: config.ProjectName, GroupState: AssetGroupPending, CreatedAt: now, UpdatedAt: now}
		return tx.Create(&space).Error
	})
	return &space, err
}

// LockChannelAssetScope serializes task acceptance against configuration changes
// and cleanup. All these flows acquire the channel row before its scope row.
func LockChannelAssetScope(tx *gorm.DB, channelID int, spaceID string) error {
	var channel Channel
	if err := lockForUpdate(tx).First(&channel, channelID).Error; err != nil {
		return err
	}
	config, err := channel.AssetConfiguration()
	if err != nil || channel.Status != common.ChannelStatusEnabled {
		return ErrChannelAssetsUnavailable
	}
	var space AssetSpace
	if err := lockForUpdate(tx).Where("id = ? AND channel_id = ?", spaceID, channelID).First(&space).Error; err != nil {
		return err
	}
	if space.BaseURL != config.BaseURL || space.ProjectName != config.ProjectName || space.GroupState == AssetGroupCleaning {
		return ErrChannelAssetScopeInUse
	}
	return nil
}

func RequestChannelAssetCleanup(ctx context.Context, channelID int) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, channelID).Error; err != nil {
			return err
		}
		if channel.Status == common.ChannelStatusEnabled {
			return common.NewMessage("Disable the channel before cleaning its assets")
		}
		var space AssetSpace
		if err := lockForUpdate(tx).Where("channel_id = ?", channelID).First(&space).Error; err != nil {
			return err
		}
		if space.GroupState == AssetGroupCleaning {
			return nil
		}
		if space.GroupState == AssetGroupUnknown && space.GroupID == "" {
			return common.NewMessage("Reconcile the unknown asset group before cleanup")
		}
		if space.LeaseUntil > time.Now().Unix() {
			return common.NewMessage("Asset group is busy; try again after the current operation finishes")
		}
		return tx.Model(&space).Updates(map[string]any{"group_state": AssetGroupCleaning, "error": "", "lease_owner": "", "lease_until": 0, "next_attempt": 0, "updated_at": time.Now().Unix()}).Error
	})
}

// ClaimAssetReplica creates a single mapping and grants only one worker a
// bounded lease. An expired import must be reconciled, never blindly replayed.
func ClaimAssetReplica(ctx context.Context, assetID, spaceID, owner string, now, leaseUntil int64) (*AssetReplica, bool, error) {
	replica := AssetReplica{AssetID: assetID, SpaceID: spaceID, Status: AssetReplicaPending, CreatedAt: now, UpdatedAt: now}
	db := DB.WithContext(ctx)
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&replica).Error; err != nil {
		return nil, false, err
	}
	result := db.Model(&AssetReplica{}).Where("asset_id = ? AND space_id = ? AND lease_until <= ? AND next_attempt <= ? AND status NOT IN ?", assetID, spaceID, now, now, []string{AssetReplicaReady, AssetReplicaFailed, AssetReplicaUnknown}).Updates(map[string]any{"lease_owner": owner, "lease_until": leaseUntil, "updated_at": now})
	if result.Error != nil {
		return nil, false, result.Error
	}
	if err := db.Where("asset_id = ? AND space_id = ?", assetID, spaceID).First(&replica).Error; err != nil {
		return nil, false, err
	}
	return &replica, result.RowsAffected == 1, nil
}

func SaveAssetReplica(ctx context.Context, replica *AssetReplica, owner string) error {
	result := DB.WithContext(ctx).Model(&AssetReplica{}).Where("asset_id = ? AND space_id = ? AND lease_owner = ?", replica.AssetID, replica.SpaceID, owner).Updates(map[string]any{
		"remote_id": replica.RemoteID, "generation_ref": replica.GenerationRef, "status": replica.Status, "error": replica.Error,
		"next_attempt": replica.NextAttempt, "lease_owner": "", "lease_until": 0, "updated_at": time.Now().Unix(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("asset replica lease lost")
	}
	return nil
}

// MarkAssetReplicaCreating is persisted before the import request leaves the
// process. A crash after this point requires reconciliation, not another import.
func MarkAssetReplicaCreating(ctx context.Context, assetID, spaceID, owner string) error {
	result := DB.WithContext(ctx).Model(&AssetReplica{}).Where("asset_id = ? AND space_id = ? AND lease_owner = ? AND lease_until > ?", assetID, spaceID, owner, time.Now().Unix()).Updates(map[string]any{"status": AssetReplicaUnknown, "updated_at": time.Now().Unix()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("asset replica lease lost")
	}
	return nil
}
