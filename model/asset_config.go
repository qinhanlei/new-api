package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Asset rows can contain signed source URLs and storage credentials. Never
// interpolate these values into SQL logs, including when DEBUG is enabled.
func protectedAssetDB(ctx context.Context) *gorm.DB {
	return DB.WithContext(ctx).Session(&gorm.Session{Logger: logger.Discard})
}

func IsAssetOption(key string) bool {
	// Database collations can ignore case and trailing spaces. Reserve the
	// entire namespace before a generic option request can reach persistence.
	key = cases.Fold().String(norm.NFKD.String(strings.TrimSpace(key)))
	key = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Cf, r) || unicode.IsControl(r) {
			return -1
		}
		return r
	}, key)
	return key == strings.ToLower(system_setting.AssetStorageOption) || strings.HasPrefix(key, strings.ToLower(system_setting.AssetChannelOptionPrefix))
}

func guardAssetOptionWrite(tx *gorm.DB, key string) error {
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		// An empty aggregate retains the key column's actual collation without
		// requiring a canonical row to exist. Connection-collation comparisons
		// would miss aliases on upgraded installations with a different column
		// collation. Compare the namespace as a whole: LIKE expands characters
		// differently from equality in MySQL.
		candidate := norm.NFKC.String(strings.TrimSpace(key))
		prefix, _, _ := strings.Cut(candidate, ".")
		var reserved bool
		err := tx.Model(&Option{}).Select("COALESCE(MAX(?), ?) = ? OR COALESCE(MAX(?), ?) = ?", clause.Column{Name: "key"}, candidate, system_setting.AssetStorageOption, clause.Column{Name: "key"}, prefix, strings.TrimSuffix(system_setting.AssetChannelOptionPrefix, ".")).Where("1 = 0").Scan(&reserved).Error
		if err != nil {
			return err
		}
		if reserved {
			return common.NewMessage("Use the asset settings API to update protected material settings")
		}
	}
	var stored Option
	err := tx.Select("key").Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).First(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if IsAssetOption(stored.Key) {
		return common.NewMessage("Use the asset settings API to update protected material settings")
	}
	return nil
}

func AssetChannelOptionKey(id int) string {
	return system_setting.AssetChannelOptionPrefix + strconv.Itoa(id) + ".Secret"
}

func LoadAssetStorageConfig(ctx context.Context) (system_setting.AssetStorageConfig, error) {
	var config system_setting.AssetStorageConfig
	var option Option
	err := protectedAssetDB(ctx).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: system_setting.AssetStorageOption}).First(&option).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return config, nil
	}
	if err == nil && option.Value != "" {
		err = common.UnmarshalJsonStr(option.Value, &config)
	}
	return config, err
}

// A no-op write acquires SQLite's writer lock before the subsequent reads. On
// MySQL/PostgreSQL the same row serializes storage changes with upload/replica
// registration; the transaction never contains S3 or upstream network I/O.
func lockAssetStorage(tx *gorm.DB) (system_setting.AssetStorageConfig, error) {
	tx = tx.Session(&gorm.Session{Logger: logger.Discard})
	var config system_setting.AssetStorageConfig
	option := Option{Key: system_setting.AssetStorageOption}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&option).Error; err != nil {
		return config, err
	}
	key := clause.Eq{Column: clause.Column{Name: "key"}, Value: option.Key}
	if err := tx.Model(&Option{}).Where(key).UpdateColumn("value", gorm.Expr("value")).Error; err != nil {
		return config, err
	}
	if err := lockForUpdate(tx).Where(key).First(&option).Error; err != nil {
		return config, err
	}
	if option.Value != "" {
		if err := common.UnmarshalJsonStr(option.Value, &config); err != nil {
			return config, err
		}
	}
	if config.CursorSecret == "" {
		config.CursorSecret = common.GetUUID() + common.GetUUID()
		encoded, err := common.Marshal(config)
		if err != nil {
			return config, err
		}
		if err = tx.Model(&Option{}).Where(key).Update("value", string(encoded)).Error; err != nil {
			return config, err
		}
	}
	return config, nil
}

func SaveAssetStorageConfig(ctx context.Context, next system_setting.AssetStorageConfig) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockAssetStorage(tx)
		if err != nil {
			return err
		}
		if current.Revision != next.Revision {
			return ErrAssetConflict
		}
		locationChanged := current.Endpoint != next.Endpoint || current.Region != next.Region || current.Bucket != next.Bucket || current.ForcePathStyle != next.ForcePathStyle
		if locationChanged {
			var count int64
			if err = tx.Model(&Asset{}).Where("object_state <> ?", AssetDeleted).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return ErrAssetInUse
			}
		}
		next.CursorSecret = current.CursorSecret
		next.Revision++
		encoded, err := common.Marshal(next)
		if err != nil {
			return err
		}
		return tx.Model(&Option{}).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: system_setting.AssetStorageOption}).Update("value", string(encoded)).Error
	})
}

func AssetStoredObject(ctx context.Context) (*Asset, error) {
	var asset Asset
	err := protectedAssetDB(ctx).Where("object_state = ?", AssetReady).First(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &asset, err
}

func LoadAssetChannelConfig(ctx context.Context, channelID int) (system_setting.AssetChannelConfig, error) {
	return loadAssetChannelConfig(protectedAssetDB(ctx), channelID)
}

func loadAssetChannelConfig(tx *gorm.DB, channelID int) (system_setting.AssetChannelConfig, error) {
	config := system_setting.AssetChannelConfig{}
	config.ProjectName = "default"
	config.AuthType = "bearer"
	var option Option
	err := tx.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: AssetChannelOptionKey(channelID)}).First(&option).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return config, nil
	}
	if err == nil && option.Value != "" {
		err = common.UnmarshalJsonStr(option.Value, &config)
	}
	return config, err
}

func AssetChannelScope(channel *Channel) string {
	// A channel can rotate between accounts when its key or endpoint changes.
	// Persist only a fingerprint, never a second copy of the generation key.
	data := strconv.Itoa(channel.Type) + "\x00" + channel.GetBaseURL() + "\x00" + channel.Key
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func SaveAssetChannelConfig(ctx context.Context, channelID int, next system_setting.AssetChannelConfig) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockAssetStorage(tx); err != nil {
			return err
		}
		current, err := loadAssetChannelConfig(tx, channelID)
		if err != nil {
			return err
		}
		if current.Revision != next.Revision {
			return ErrAssetConflict
		}
		var channel Channel
		if err = lockForUpdate(tx).First(&channel, channelID).Error; err != nil {
			return err
		}
		if next.Enabled && (channel.ChannelInfo.IsMultiKey || len(channel.GetKeys()) != 1) {
			return errors.New("asset channels must use one API key")
		}
		if next.ChannelScope != AssetChannelScope(&channel) {
			return ErrAssetConflict
		}
		if current.Endpoint != next.Endpoint || current.ProjectName != next.ProjectName || current.AuthType != next.AuthType || current.AccessKey != next.AccessKey || current.ChannelScope != next.ChannelScope {
			if err = checkAssetChannelResources(tx, []int{channelID}); err != nil {
				if !errors.Is(err, ErrAssetInUse) {
					return err
				}
				if err = checkAssetEndpointRepair(tx, channelID, current, next); err != nil {
					return err
				}
			}
		}
		next.Revision++
		encoded, err := common.Marshal(next)
		if err != nil {
			return err
		}
		option := Option{Key: AssetChannelOptionKey(channelID), Value: string(encoded)}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&option).Error
	})
}

// Only a path correction on the same account is recoverable without removing
// resources. Uncertain creations stay recorded for explicit reconciliation.
func checkAssetEndpointRepair(tx *gorm.DB, channelID int, current, next system_setting.AssetChannelConfig) error {
	before, beforeErr := url.Parse(current.Endpoint)
	after, afterErr := url.Parse(next.Endpoint)
	if !next.Enabled || beforeErr != nil || afterErr != nil || before.Scheme != after.Scheme || !strings.EqualFold(before.Host, after.Host) ||
		current.ProjectName != next.ProjectName || current.AuthType != next.AuthType || current.AccessKey != next.AccessKey || current.ChannelScope != next.ChannelScope {
		return ErrAssetInUse
	}
	var groups []AssetGroupReplica
	if err := lockForUpdate(tx).Where("channel_id = ? AND state <> ?", channelID, AssetDeleted).Find(&groups).Error; err != nil {
		return err
	}
	for _, group := range groups {
		if group.UpstreamID != "" || group.LeaseToken != "" || group.LeaseExpiresAt > time.Now().Unix() || group.State == AssetCreating {
			return ErrAssetInUse
		}
		var source AssetGroup
		if err := tx.First(&source, group.GroupID).Error; err != nil {
			return err
		}
		if source.DeletedAt > 0 {
			return ErrAssetInUse
		}
	}
	var replicas []AssetReplica
	if err := lockForUpdate(tx).Where("channel_id = ? AND state <> ?", channelID, AssetDeleted).Find(&replicas).Error; err != nil {
		return err
	}
	for _, replica := range replicas {
		if replica.UpstreamID != "" || replica.Status != AssetFailed || replica.LeaseToken != "" || replica.LeaseExpiresAt > time.Now().Unix() || replica.State == AssetCreating {
			return ErrAssetInUse
		}
		var asset Asset
		if err := tx.First(&asset, replica.AssetID).Error; err != nil {
			return err
		}
		if asset.DeletedAt > 0 {
			return ErrAssetInUse
		}
	}
	return nil
}

func checkAssetChannelResources(tx *gorm.DB, ids []int) error {
	var count int64
	if err := tx.Model(&AssetGroupReplica{}).Where("channel_id IN ? AND state <> ?", ids, AssetDeleted).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrAssetInUse
	}
	return nil
}

// GuardAssetChannelMutation is used inside the transaction which updates or
// deletes a channel. Callers preserve normal status, price and model changes.
func GuardAssetChannelMutation(tx *gorm.DB, next *Channel, deletingIDs []int) error {
	// Channel-only fixtures and upgrades predating assets have no resources to
	// protect. No schema creation is performed from a request path.
	if !tx.Migrator().HasTable(&AssetGroupReplica{}) {
		return nil
	}
	if _, err := lockAssetStorage(tx); err != nil {
		return err
	}
	if len(deletingIDs) > 0 {
		return checkAssetChannelResources(tx, deletingIDs)
	}
	if next == nil || next.Id <= 0 {
		return nil
	}
	var current Channel
	if err := lockForUpdate(tx).First(&current, next.Id).Error; err != nil {
		return err
	}
	changed := next.Key != "" && next.Key != current.Key
	changed = changed || next.BaseURL != nil && next.GetBaseURL() != current.GetBaseURL()
	changed = changed || next.Type != 0 && next.Type != current.Type
	changed = changed || next.ChannelInfo.IsMultiKey
	if changed {
		return checkAssetChannelResources(tx, []int{next.Id})
	}
	return nil
}

func deleteAssetChannelOptions(tx *gorm.DB, ids []int) error {
	if !tx.Migrator().HasTable(&AssetGroupReplica{}) {
		return nil
	}
	keys := make([]any, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, AssetChannelOptionKey(id))
	}
	return tx.Session(&gorm.Session{Logger: logger.Discard}).Where(clause.IN{Column: clause.Column{Name: "key"}, Values: keys}).Delete(&Option{}).Error
}
