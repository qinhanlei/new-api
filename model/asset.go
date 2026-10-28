package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

const (
	AssetIDPrefix           = "asset-na-"
	AssetGroupIDPrefix      = "group-na-"
	AssetProcessing         = "Processing"
	AssetActive             = "Active"
	AssetFailed             = "Failed"
	AssetPending            = "pending"
	AssetReady              = "ready"
	AssetUnknown            = "unknown"
	AssetDeleted            = "deleted"
	AssetGroupAIGC          = "AIGC"
	AssetGroupLivenessFace  = "LivenessFace"
	AssetGroupActive        = "active"
	AssetGroupDeletePending = "delete_pending"
	AssetGroupDeleteUnknown = "delete_unknown"
	AssetGroupDeleteBlocked = "delete_blocked"
	AssetGroupDeleted       = "deleted"
)

var ErrAssetConflict = errors.New("asset configuration or state changed; retry the operation")
var ErrAssetInUse = errors.New("asset resources must be removed before changing their storage or upstream account")
var ErrAssetStorageDisabled = errors.New("asset storage is disabled")
var ErrAssetGroupUnavailable = errors.New("asset group is unavailable while deletion is pending")
var ErrAssetChannelConflict = errors.New("real-person assets must use their authorized channel")

type AssetGroup struct {
	ID               int64  `gorm:"primaryKey"`
	PublicID         string `gorm:"size:64;uniqueIndex"`
	UserID           int    `gorm:"index:,composite:owner"`
	ProjectName      string `gorm:"size:64;index:,composite:owner"`
	Name             string `gorm:"size:64"`
	Description      string `gorm:"size:300"`
	GroupType        string `gorm:"size:32"`
	InitialChannelID int
	Lifecycle        string `gorm:"size:24;index"`
	ErrorCode        string `gorm:"size:128"`
	ErrorMessage     string `gorm:"type:text"`
	DeleteUncertain  bool
	NextAttemptAt    int64
	LeaseToken       string `gorm:"size:64"`
	LeaseExpiresAt   int64
	CreatedAt        int64
	UpdatedAt        int64
	DeletedAt        int64 `gorm:"index"`
}

func (AssetGroup) TableName(namer schema.Namer) string { return namer.TableName("ArkAssetGroup") }

// Empty lifecycle values belong to groups created before deletion tracking.
func (group AssetGroup) Usable() bool {
	return group.DeletedAt == 0 && (group.Lifecycle == "" || group.Lifecycle == AssetGroupActive)
}

type Asset struct {
	ID                 int64  `gorm:"primaryKey"`
	PublicID           string `gorm:"size:64;uniqueIndex"`
	UserID             int    `gorm:"index:,composite:owner"`
	TokenID            int
	ProjectName        string `gorm:"size:64;index:,composite:owner"`
	GroupID            int64  `gorm:"index"`
	GroupPublicID      string `gorm:"size:64"`
	Name               string `gorm:"size:64"`
	AssetType          string `gorm:"size:16"`
	Status             string `gorm:"size:16;index"`
	SourceURL          string `gorm:"type:text"`
	ObjectKey          string `gorm:"size:191"`
	ObjectState        string `gorm:"size:16;index"`
	ContentType        string `gorm:"size:128"`
	Size               int64
	SHA256             string `gorm:"size:64"`
	ModerationStrategy string `gorm:"size:64"`
	ErrorCode          string `gorm:"size:128"`
	ErrorMessage       string `gorm:"type:text"`
	Attempts           int
	NextAttemptAt      int64  `gorm:"index:,composite:work"`
	LeaseToken         string `gorm:"size:64"`
	LeaseExpiresAt     int64  `gorm:"index:,composite:work"`
	CreatedAt          int64
	UpdatedAt          int64
	LastInferenceAt    int64
	DeletedAt          int64 `gorm:"index"`
}

func (Asset) TableName(namer schema.Namer) string { return namer.TableName("ArkAsset") }

type AssetGroupReplica struct {
	ID              int64  `gorm:"primaryKey"`
	GroupID         int64  `gorm:"uniqueIndex:,composite:group_channel"`
	ChannelID       int    `gorm:"uniqueIndex:,composite:group_channel;index"`
	UpstreamID      string `gorm:"size:191"`
	State           string `gorm:"size:16"`
	ErrorCode       string `gorm:"size:128"`
	ErrorMessage    string `gorm:"type:text"`
	LeaseToken      string `gorm:"size:64"`
	LeaseExpiresAt  int64
	CreateStartedAt int64
	UpdatedAt       int64
}

func (AssetGroupReplica) TableName(namer schema.Namer) string {
	return namer.TableName("ArkAssetGroupReplica")
}

type AssetReplica struct {
	ID                 int64 `gorm:"primaryKey"`
	AssetID            int64 `gorm:"uniqueIndex:,composite:asset_channel"`
	ChannelID          int   `gorm:"uniqueIndex:,composite:asset_channel;index"`
	TokenID            int
	UpstreamID         string `gorm:"size:191"`
	Status             string `gorm:"size:16"`
	State              string `gorm:"size:16;index"`
	ErrorCode          string `gorm:"size:128"`
	ErrorMessage       string `gorm:"type:text"`
	ModerationStrategy string `gorm:"size:64"`
	Attempts           int
	NextAttemptAt      int64  `gorm:"index:,composite:work"`
	LeaseToken         string `gorm:"size:64"`
	LeaseExpiresAt     int64  `gorm:"index:,composite:work"`
	CreateStartedAt    int64
	UpdatedAt          int64
}

func (AssetReplica) TableName(namer schema.Namer) string { return namer.TableName("ArkAssetReplica") }

type AssetUse struct {
	ID        int64  `gorm:"primaryKey"`
	AssetID   int64  `gorm:"uniqueIndex:,composite:asset_task;index"`
	TaskID    string `gorm:"size:191;uniqueIndex:,composite:asset_task;index"`
	ChannelID int
	CreatedAt int64
	Committed bool
}

func (AssetUse) TableName(namer schema.Namer) string { return namer.TableName("ArkAssetUse") }

func AssetModels() []any {
	return []any{&AssetGroup{}, &Asset{}, &AssetGroupReplica{}, &AssetReplica{}, &AssetUse{}, &AssetValidationSession{}, &AssetValidationRateWindow{}}
}

func CreateAssetGroup(ctx context.Context, group *AssetGroup) error {
	return protectedAssetDB(ctx).Create(group).Error
}

func GetAssetGroup(ctx context.Context, userID int, publicID, project string) (*AssetGroup, error) {
	var group AssetGroup
	err := protectedAssetDB(ctx).Where("user_id = ? AND public_id = ? AND project_name = ?", userID, publicID, project).
		Where("deleted_at = 0 OR (group_type = ? AND lifecycle = ?)", AssetGroupLivenessFace, AssetGroupDeleted).First(&group).Error
	return &group, err
}

func GetAsset(ctx context.Context, userID int, publicID, project string) (*Asset, error) {
	var asset Asset
	query := protectedAssetDB(ctx).Where("user_id = ? AND public_id = ? AND deleted_at = 0", userID, publicID)
	if project != "" {
		query = query.Where("project_name = ?", project)
	}
	err := query.First(&asset).Error
	return &asset, err
}

func GetAssetByID(ctx context.Context, id int64) (*Asset, error) {
	var asset Asset
	err := protectedAssetDB(ctx).First(&asset, id).Error
	return &asset, err
}

func GetAssetGroupByID(ctx context.Context, id int64) (*AssetGroup, error) {
	var group AssetGroup
	err := protectedAssetDB(ctx).First(&group, id).Error
	return &group, err
}

// ReserveAsset records the intended object key before any S3 write. The storage
// lock is shared with configuration changes, including uploads not yet complete.
func ReserveAsset(ctx context.Context, asset *Asset, channelID int) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		config, err := lockAssetStorage(tx)
		if err != nil {
			return err
		}
		if !config.Enabled {
			return ErrAssetStorageDisabled
		}
		var group AssetGroup
		if err = lockForUpdate(tx).Where("id = ? AND user_id = ? AND deleted_at = 0", asset.GroupID, asset.UserID).First(&group).Error; err != nil {
			return err
		}
		if !group.Usable() {
			return ErrAssetGroupUnavailable
		}
		if group.GroupType == AssetGroupLivenessFace && (channelID <= 0 || group.InitialChannelID != channelID) {
			return ErrAssetChannelConflict
		}
		if group.InitialChannelID == 0 {
			if err = tx.Model(&group).Update("initial_channel_id", channelID).Error; err != nil {
				return err
			}
		}
		if err = tx.Create(asset).Error; err != nil {
			return err
		}
		return ensureAssetReplica(tx, asset, channelID, asset.TokenID)
	})
}

func UpdateAssetGroup(ctx context.Context, userID int, id, project string, name, description *string) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockAssetStorage(tx); err != nil {
			return err
		}
		var group AssetGroup
		if err := lockForUpdate(tx).Where("user_id = ? AND public_id = ? AND project_name = ? AND deleted_at = 0", userID, id, project).First(&group).Error; err != nil {
			return err
		}
		if !group.Usable() {
			return ErrAssetGroupUnavailable
		}
		values := map[string]any{"updated_at": time.Now().Unix()}
		if name != nil {
			values["name"] = *name
		}
		if description != nil {
			values["description"] = *description
		}
		return tx.Model(&group).Updates(values).Error
	})
}

func UpdateAssetName(ctx context.Context, userID int, id, project, name string) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockAssetStorage(tx); err != nil {
			return err
		}
		var asset Asset
		if err := tx.Where("user_id = ? AND public_id = ? AND project_name = ? AND deleted_at = 0", userID, id, project).First(&asset).Error; err != nil {
			return err
		}
		if _, err := checkAssetGroupChannel(tx, asset.GroupID, 0); err != nil {
			return err
		}
		return tx.Model(&asset).Updates(map[string]any{"name": name, "updated_at": time.Now().Unix()}).Error
	})
}

func DeleteAsset(ctx context.Context, userID int, id, project string) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockAssetStorage(tx); err != nil {
			return err
		}
		var asset Asset
		if err := tx.Where("user_id = ? AND public_id = ? AND project_name = ? AND deleted_at = 0", userID, id, project).First(&asset).Error; err != nil {
			return err
		}
		if _, err := checkAssetGroupChannel(tx, asset.GroupID, 0); err != nil {
			return err
		}
		now := time.Now().Unix()
		return tx.Model(&asset).Updates(map[string]any{"deleted_at": now, "updated_at": now, "next_attempt_at": now}).Error
	})
}

func DeleteAssetGroup(ctx context.Context, userID int, id, project string) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockAssetStorage(tx); err != nil {
			return err
		}
		var group AssetGroup
		if err := lockForUpdate(tx).Where("user_id = ? AND public_id = ? AND project_name = ?", userID, id, project).First(&group).Error; err != nil {
			return err
		}
		if group.GroupType == AssetGroupLivenessFace {
			return requestHumanAssetGroupDeletion(tx, &group)
		}
		if group.DeletedAt > 0 {
			return gorm.ErrRecordNotFound
		}
		now := time.Now().Unix()
		if err := tx.Model(&group).Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&Asset{}).Where("group_id = ? AND deleted_at = 0", group.ID).Updates(map[string]any{"deleted_at": now, "updated_at": now, "next_attempt_at": now}).Error
	})
}

type AssetListQuery struct {
	UserID      int
	AllUsers    bool
	ProjectName string
	GroupType   string
	GroupIDs    []string
	Name        string
	Statuses    []string
	SortBy      string
	Descending  bool
	Limit       int
	Offset      int
	AfterID     int64
	AfterValue  any
}

func assetListQuery(ctx context.Context, q AssetListQuery, groups bool) *gorm.DB {
	query := protectedAssetDB(ctx)
	if groups {
		query = query.Model(&AssetGroup{})
	} else {
		query = query.Model(&Asset{})
	}
	query = query.Where("deleted_at = 0")
	if q.GroupType != "" {
		if groups {
			query = query.Where("group_type = ?", q.GroupType)
		} else {
			groupIDs := protectedAssetDB(ctx).Model(&AssetGroup{}).Select("id").Where("group_type = ?", q.GroupType)
			query = query.Where("group_id IN (?)", groupIDs)
		}
	}
	if !q.AllUsers {
		query = query.Where("user_id = ?", q.UserID)
	}
	if q.ProjectName != "" {
		query = query.Where("project_name = ?", q.ProjectName)
	}
	if len(q.GroupIDs) > 0 {
		column := "group_public_id"
		if groups {
			column = "public_id"
		}
		query = query.Where(column+" IN ?", q.GroupIDs)
	}
	if q.Name != "" {
		pattern := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(q.Name)
		query = query.Where("name LIKE ? ESCAPE '!'", "%"+pattern+"%")
	}
	if !groups && len(q.Statuses) > 0 {
		query = query.Where("status IN ?", q.Statuses)
	}
	return query
}

func assetListPage(query *gorm.DB, q AssetListQuery) *gorm.DB {
	column := "created_at"
	if q.SortBy == "UpdateTime" {
		column = "updated_at"
	}
	if q.SortBy == "GroupId" {
		column = "group_public_id"
	}
	if q.AfterID > 0 {
		op := ">"
		if q.Descending {
			op = "<"
		}
		query = query.Where(fmt.Sprintf("(%s %s ? OR (%s = ? AND id %s ?))", column, op, column, op), q.AfterValue, q.AfterValue, q.AfterID)
	}
	return query.Order(clause.OrderByColumn{Column: clause.Column{Name: column}, Desc: q.Descending}).Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: q.Descending}).Offset(q.Offset).Limit(q.Limit)
}

func ListAssetGroups(ctx context.Context, q AssetListQuery) ([]AssetGroup, int64, error) {
	var total int64
	query := assetListQuery(ctx, q, true)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := []AssetGroup{}
	err := assetListPage(query, q).Find(&items).Error
	return items, total, err
}

func ListAssets(ctx context.Context, q AssetListQuery) ([]Asset, int64, error) {
	var total int64
	query := assetListQuery(ctx, q, false)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := []Asset{}
	err := assetListPage(query, q).Find(&items).Error
	return items, total, err
}

func ensureAssetReplica(tx *gorm.DB, asset *Asset, channelID, tokenID int) error {
	if channelID <= 0 {
		return ErrAssetChannelConflict
	}
	sourceGroup, err := checkAssetGroupChannel(tx, asset.GroupID, channelID)
	if err != nil {
		return err
	}
	if sourceGroup.GroupType != AssetGroupLivenessFace {
		group := AssetGroupReplica{GroupID: asset.GroupID, ChannelID: channelID, State: AssetPending}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&group).Error; err != nil {
			return err
		}
	}
	replica := AssetReplica{AssetID: asset.ID, ChannelID: channelID, TokenID: tokenID, Status: AssetProcessing, State: AssetPending, NextAttemptAt: time.Now().Unix()}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&replica).Error
}

func EnsureAssetReplicas(ctx context.Context, assets []Asset, channelID, tokenID int) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockAssetStorage(tx); err != nil {
			return err
		}
		for i := range assets {
			var current Asset
			if err := tx.Where("id = ? AND deleted_at = 0", assets[i].ID).First(&current).Error; err != nil {
				return err
			}
			if err := ensureAssetReplica(tx, &current, channelID, tokenID); err != nil {
				return err
			}
		}
		return nil
	})
}

func AssetReplicas(ctx context.Context, assetIDs []int64) ([]AssetReplica, error) {
	items := []AssetReplica{}
	err := protectedAssetDB(ctx).Where("asset_id IN ?", assetIDs).Find(&items).Error
	return items, err
}

func HoldAssetUses(ctx context.Context, userID, channelID int, ids []string, taskID string) ([]dto.ResolvedAssetReference, error) {
	if channelID <= 0 {
		return nil, ErrAssetChannelConflict
	}
	var result []dto.ResolvedAssetReference
	err := protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		config, err := lockAssetStorage(tx)
		if err != nil {
			return err
		}
		if !config.Enabled {
			return ErrAssetStorageDisabled
		}
		for _, id := range ids {
			var asset Asset
			if err = tx.Where("public_id = ? AND user_id = ? AND deleted_at = 0", id, userID).First(&asset).Error; err != nil {
				return err
			}
			if _, err = checkAssetGroupChannel(tx, asset.GroupID, channelID); err != nil {
				return err
			}
			var replica AssetReplica
			if err = tx.Where("asset_id = ? AND channel_id = ? AND status = ? AND state = ? AND upstream_id <> ''", asset.ID, channelID, AssetActive, AssetReady).First(&replica).Error; err != nil {
				return err
			}
			use := AssetUse{AssetID: asset.ID, TaskID: taskID, ChannelID: channelID, CreatedAt: time.Now().Unix()}
			if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "asset_id"}, {Name: "task_id"}}, DoUpdates: clause.AssignmentColumns([]string{"channel_id"})}).Create(&use).Error; err != nil {
				return err
			}
			result = append(result, dto.ResolvedAssetReference{ID: asset.PublicID, UpstreamID: replica.UpstreamID, AssetType: asset.AssetType})
		}
		return nil
	})
	return result, err
}

func ReleaseAssetUses(ctx context.Context, taskID string) error {
	return protectedAssetDB(ctx).Where("task_id = ?", taskID).Delete(&AssetUse{}).Error
}

func CommitAssetUses(ctx context.Context, taskID string) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		var uses []AssetUse
		if err := tx.Where("task_id = ?", taskID).Find(&uses).Error; err != nil {
			return err
		}
		ids := make([]int64, 0, len(uses))
		for _, use := range uses {
			ids = append(ids, use.AssetID)
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Model(&AssetUse{}).Where("task_id = ?", taskID).Update("committed", true).Error; err != nil {
			return err
		}
		return tx.Model(&Asset{}).Where("id IN ?", ids).Update("last_inference_at", time.Now().Unix()).Error
	})
}

// ExpiredAssetUses only releases rows whose task is terminal, or whose task was
// never committed after the full upstream retention window. Active tasks win
// over age, so a long-running task cannot lose its source during cleanup.
func ExpiredAssetUses(ctx context.Context, orphanBefore int64) error {
	var uses []AssetUse
	terminalTasks := DB.Model(&Task{}).Select("task_id").Where("status IN ?", []TaskStatus{TaskStatusSuccess, TaskStatusFailure})
	knownTasks := DB.Model(&Task{}).Select("task_id").Where("task_id IS NOT NULL")
	if err := protectedAssetDB(ctx).Where("task_id IN (?) OR (created_at < ? AND task_id NOT IN (?))", terminalTasks, orphanBefore, knownTasks).Order("id").Limit(200).Find(&uses).Error; err != nil {
		return err
	}
	for _, use := range uses {
		var task Task
		err := protectedAssetDB(ctx).Where("task_id = ?", use.TaskID).First(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if use.CreatedAt >= orphanBefore {
				continue
			}
		} else if err != nil {
			return err
		} else if task.Status != TaskStatusSuccess && task.Status != TaskStatusFailure {
			continue
		}
		if err := ReleaseAssetUses(ctx, use.TaskID); err != nil {
			return err
		}
	}
	return nil
}

func NewAssetID() string      { return AssetIDPrefix + common.GetUUID() }
func NewAssetGroupID() string { return AssetGroupIDPrefix + common.GetUUID() }
