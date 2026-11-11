package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

const (
	AssetValidationCreating  = "creating"
	AssetValidationPending   = "pending"
	AssetValidationVerifying = "verifying"
	AssetValidationCreated   = "created"
	AssetValidationFailed    = "failed"
	AssetValidationExpired   = "expired"
	AssetValidationUnknown   = "unknown"
)

var ErrAssetValidationThrottled = errors.New("human portrait validation is rate limited")
var ErrAssetValidationActive = errors.New("verification is still active")
var ErrAssetValidationReviewBatch = errors.New("unconfirmed verification requires individual archival")

var assetValidationPollBlockingCodes = []string{"UpstreamConfigurationInvalid", "ValidationSecretUnavailable", "InvalidValidationGroup", "ValidationGroupMismatch"}

// AssetValidationSession retains credential references and encrypted short-lived
// provider data. It never stores identity documents, portraits, or biometric data.
type AssetValidationSession struct {
	ID               int64  `gorm:"primaryKey"`
	PublicID         string `gorm:"size:64;uniqueIndex"`
	UserID           int    `gorm:"index:,composite:owner"`
	ProjectName      string `gorm:"size:64;index:,composite:owner"`
	InitiatorID      int    `gorm:"uniqueIndex:,composite:request"`
	IdempotencyHash  string `gorm:"size:64;uniqueIndex:,composite:request"`
	RequestHash      string `gorm:"size:64"`
	TokenID          int
	AccessTokenID    int
	AuthSessionID    string `gorm:"size:64"`
	UserAuthVersion  int64
	SessionVersion   int64
	ChannelID        int    `gorm:"index"`
	ChannelScope     string `gorm:"size:64"`
	ChannelConfig    string `gorm:"size:64"`
	UpstreamProject  string `gorm:"size:64"`
	RateScope        string `gorm:"size:64"`
	Name             string `gorm:"size:64"`
	Description      string `gorm:"size:300"`
	StateHash        string `gorm:"size:64;uniqueIndex"`
	SecretCipher     string `gorm:"type:text"`
	Status           string `gorm:"size:16"`
	ResourcePossible bool
	UpstreamGroupID  string `gorm:"size:191"`
	GroupID          string `gorm:"size:64"`
	ErrorCode        string `gorm:"size:128"`
	ErrorMessage     string `gorm:"type:text"`
	LeaseToken       string `gorm:"size:64"`
	LeaseExpiresAt   int64
	NextPollAt       int64
	ExpiresAt        int64 `gorm:"index"`
	CreatedAt        int64
	UpdatedAt        int64
	ArchivedAt       int64 `gorm:"not null;default:0"`
	LastCheckedAt    int64 `gorm:"not null;default:0"`
}

func (AssetValidationSession) TableName(namer schema.Namer) string {
	return namer.TableName("ArkAssetValidationSession")
}

// Requests sharing an AK or an endpoint and Bearer key share this database gate
// across nodes and channels.
// A conservative 400 ms interval also stays below the provider's 3 QPS limit.
type AssetValidationRateWindow struct {
	ScopeHash  string `gorm:"size:64;primaryKey"`
	NextCallAt int64
}

func (AssetValidationRateWindow) TableName(namer schema.Namer) string {
	return namer.TableName("ArkAssetValidationRateWindow")
}

func AssetValidationConfigHash(config system_setting.AssetChannelConfig) string {
	data := config.Endpoint + "\x00" + config.AuthType + "\x00" + config.AccessKey + "\x00" + config.ProjectName + "\x00" + config.ChannelScope
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func GetAssetValidationSession(ctx context.Context, userID, initiatorID int, id, project string) (*AssetValidationSession, error) {
	var session AssetValidationSession
	err := protectedAssetDB(ctx).Where("user_id = ? AND initiator_id = ? AND public_id = ? AND project_name = ?", userID, initiatorID, id, project).First(&session).Error
	return &session, err
}

func GetAssetValidationByRequest(ctx context.Context, initiatorID int, hash string) (*AssetValidationSession, error) {
	var session AssetValidationSession
	err := protectedAssetDB(ctx).Where("initiator_id = ? AND idempotency_hash = ?", initiatorID, hash).First(&session).Error
	return &session, err
}

func ListAssetValidationSessions(ctx context.Context, userID, initiatorID int, project string) ([]AssetValidationSession, error) {
	items := []AssetValidationSession{}
	err := protectedAssetDB(ctx).Where("user_id = ? AND initiator_id = ? AND project_name = ?", userID, initiatorID, project).
		Order("id DESC").Limit(50).Find(&items).Error
	return items, err
}

func AssetValidationPollBlocked(code string) bool {
	return slices.Contains(assetValidationPollBlockingCodes, code)
}

// AssetValidationRecordCategory describes the resource outcome, independently
// of whether the current caller can continue its original authorization.
func AssetValidationRecordCategory(session *AssetValidationSession, now int64) string {
	if session.GroupID != "" {
		return "completed"
	}
	if session.UpstreamGroupID != "" || session.LeaseExpiresAt > now {
		return "pending"
	}
	if session.Status != AssetValidationFailed && session.SecretCipher != "" && session.ExpiresAt > now && !AssetValidationPollBlocked(session.ErrorCode) {
		return "pending"
	}
	if session.ResourcePossible {
		return "needs_review"
	}
	return "ended"
}

// SQL uses the same outcome precedence as AssetValidationRecordCategory; this
// expression is shared by both counts and page filtering. Regression cases
// compare each returned category against the summary on every database dialect.
func assetValidationRecordCategorySQL(now int64) clause.Expr {
	return gorm.Expr(`CASE
		WHEN COALESCE(group_id, '') <> '' THEN 'completed'
		WHEN COALESCE(upstream_group_id, '') <> '' OR lease_expires_at > ? THEN 'pending'
		WHEN COALESCE(status, '') <> ? AND COALESCE(secret_cipher, '') <> '' AND expires_at > ? AND
			COALESCE(error_code, '') NOT IN ? THEN 'pending'
		WHEN resource_possible = ? THEN 'needs_review'
		ELSE 'ended' END`, now, AssetValidationFailed, now,
		assetValidationPollBlockingCodes, true)
}

func ListAssetValidationRecords(ctx context.Context, userID, initiatorID int, project string, filter dto.AssetValidationRecordFilter, now int64) ([]AssetValidationSession, int64, int64, error) {
	query := protectedAssetDB(ctx).Model(&AssetValidationSession{}).
		Where("user_id = ? AND initiator_id = ? AND project_name = ?", userID, initiatorID, project)
	category := assetValidationRecordCategorySQL(now)
	var pending, total int64
	if err := query.Session(&gorm.Session{}).Where("(?) IN ?", category, []string{"pending", "needs_review"}).Count(&pending).Error; err != nil {
		return nil, 0, 0, err
	}
	if filter.Name != "" {
		// Use one explicit escape character on all three databases; wildcard
		// characters in a person's name are literal search text.
		name := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(filter.Name)
		query = query.Where("name LIKE ? ESCAPE '!'", "%"+name+"%")
	}
	if filter.Status != "all" {
		query = query.Where("(?) = ?", category, filter.Status)
	}
	switch filter.Archived {
	case "active":
		query = query.Where("archived_at = 0")
	case "archived":
		query = query.Where("archived_at > 0")
	}
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, 0, err
	}
	items := []AssetValidationSession{}
	err := query.Order("id DESC").Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize).Find(&items).Error
	return items, total, pending, err
}

func ArchiveAssetValidationRecords(ctx context.Context, userID, initiatorID int, project string, ids []string, archived bool) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		var items []AssetValidationSession
		if err := lockForUpdate(tx).Where("user_id = ? AND initiator_id = ? AND project_name = ? AND public_id IN ?", userID, initiatorID, project, ids).
			Order("id").Find(&items).Error; err != nil {
			return err
		}
		if len(items) != len(ids) {
			return gorm.ErrRecordNotFound
		}
		now := time.Now().Unix()
		archivedAt := int64(0)
		if archived {
			archivedAt = now
			for _, item := range items {
				category := AssetValidationRecordCategory(&item, now)
				if item.LeaseExpiresAt > now || category == "pending" {
					return ErrAssetValidationActive
				}
				if len(items) > 1 && category == "needs_review" {
					return ErrAssetValidationReviewBatch
				}
			}
		}
		query := tx.Model(&AssetValidationSession{}).
			Where("user_id = ? AND initiator_id = ? AND project_name = ? AND public_id IN ?", userID, initiatorID, project, ids)
		if archived {
			query = query.Where("archived_at = 0")
		}
		// Archival is display management only. UpdatedAt also participates in
		// provider backoff, so it must not be touched by this operation.
		return query.UpdateColumn("archived_at", archivedAt).Error
	})
}

func GetAssetValidationByState(ctx context.Context, hash string) (*AssetValidationSession, error) {
	var session AssetValidationSession
	err := protectedAssetDB(ctx).Where("state_hash = ?", hash).First(&session).Error
	return &session, err
}

// PendingValidationChannelGuard also protects unknown or expired sessions:
// expiration of the H5 token does not prove that no portrait group was created.
func PendingValidationChannelGuard(tx *gorm.DB, ids []int) error {
	if !tx.Migrator().HasTable(&AssetValidationSession{}) {
		return nil
	}
	var count int64
	err := tx.Model(&AssetValidationSession{}).Where("channel_id IN ? AND resource_possible = ? AND group_id = ''", ids, true).Count(&count).Error
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrAssetInUse
	}
	return nil
}

func validateAssetValidationIdentity(tx *gorm.DB, session *AssetValidationSession) error {
	if session.UserID <= 0 || session.InitiatorID <= 0 {
		return ErrUserSessionInactive
	}
	var initiator User
	if err := lockForUpdate(tx).First(&initiator, session.InitiatorID).Error; err != nil {
		return err
	}
	if initiator.Status != common.UserStatusEnabled || (session.InitiatorID != session.UserID && initiator.Role < common.RoleAdminUser) {
		return ErrUserSessionInactive
	}
	if session.TokenID > 0 {
		if session.InitiatorID != session.UserID || session.AccessTokenID != 0 || session.AuthSessionID != "" {
			return ErrUserSessionInactive
		}
		var token Token
		if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", session.TokenID, session.UserID).First(&token).Error; err != nil {
			return err
		}
		if token.Status != common.TokenStatusEnabled || (token.ExpiredTime != -1 && token.ExpiredTime <= time.Now().Unix()) {
			return ErrUserSessionInactive
		}
	} else {
		identity := AuthSessionIdentity{UserID: session.InitiatorID, SessionID: session.AuthSessionID, UserAuthVersion: session.UserAuthVersion, SessionVersion: session.SessionVersion}
		if err := ValidateAuthSessionWithTx(tx, identity); err != nil {
			return err
		}
		patID, isPAT := ParseAccessTokenSessionID(session.AuthSessionID)
		if isPAT != (session.AccessTokenID > 0) || (isPAT && patID != session.AccessTokenID) {
			return ErrUserSessionInactive
		}
		if isPAT {
			var token UserAccessToken
			if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", patID, session.InitiatorID).First(&token).Error; err != nil {
				return err
			}
			scopes := token.GetScopes()
			if !slices.Contains(scopes, "asset:write") || (session.InitiatorID != session.UserID && !slices.Contains(scopes, "user:write")) {
				return ErrUserSessionInactive
			}
		}
	}
	var owner User
	if err := lockForUpdate(tx).First(&owner, session.UserID).Error; err != nil {
		return err
	}
	if owner.Status != common.UserStatusEnabled {
		return ErrUserSessionInactive
	}
	return nil
}

func validateAssetValidationChannel(tx *gorm.DB, session *AssetValidationSession) error {
	config, err := loadAssetChannelConfig(tx, session.ChannelID)
	if err != nil {
		return err
	}
	var channel Channel
	if err = lockForUpdate(tx).First(&channel, session.ChannelID).Error; err != nil {
		return err
	}
	validAuth := config.AuthType == "bearer" || (config.AuthType == "aksk" && config.AccessKey != "" && config.SecretKey != "")
	if !config.Enabled || !validAuth || channel.Status != common.ChannelStatusEnabled ||
		channel.ChannelInfo.IsMultiKey || len(channel.GetKeys()) != 1 || AssetChannelScope(&channel) != session.ChannelScope ||
		AssetValidationConfigHash(config) != session.ChannelConfig || config.ProjectName != session.UpstreamProject {
		return ErrAssetConflict
	}
	return nil
}

// ValidateAssetValidationSession checks the original credential, not just the
// owner left in the caller context after an administrative ownership selection.
func ValidateAssetValidationSession(ctx context.Context, session *AssetValidationSession) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockAssetStorage(tx); err != nil {
			return err
		}
		if err := validateAssetValidationIdentity(tx, session); err != nil {
			return err
		}
		return validateAssetValidationChannel(tx, session)
	})
}

func reserveAssetValidationRate(tx *gorm.DB, scope string, now int64) error {
	window := AssetValidationRateWindow{ScopeHash: scope}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&window).Error; err != nil {
		return err
	}
	result := tx.Model(&AssetValidationRateWindow{}).Where("scope_hash = ? AND next_call_at <= ?", scope, now).Update("next_call_at", now+400)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAssetValidationThrottled
	}
	return nil
}

// ReserveAssetValidationSession serializes idempotency and account changes before
// the first remote call. A failed/uncertain create is never automatically replayed.
func ReserveAssetValidationSession(ctx context.Context, session *AssetValidationSession, authorize func(*gorm.DB) error) (bool, error) {
	if authorize == nil {
		return false, ErrUserSessionInactive
	}
	created := false
	err := protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		config, err := lockAssetStorage(tx)
		if err != nil {
			return err
		}
		if !config.Enabled {
			return ErrAssetStorageDisabled
		}
		var existing AssetValidationSession
		err = tx.Where("initiator_id = ? AND idempotency_hash = ?", session.InitiatorID, session.IdempotencyHash).First(&existing).Error
		if err == nil {
			if existing.RequestHash != session.RequestHash || existing.UserID != session.UserID {
				return ErrAssetConflict
			}
			*session = existing
			return validateAssetValidationIdentity(tx, session)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err = validateAssetValidationIdentity(tx, session); err != nil {
			return err
		}
		if err = validateAssetValidationChannel(tx, session); err != nil {
			return err
		}
		if err = authorize(lockForUpdate(tx)); err != nil {
			return err
		}
		if err = reserveAssetValidationRate(tx, session.RateScope, time.Now().UnixMilli()); err != nil {
			return err
		}
		session.Status = AssetValidationCreating
		session.ResourcePossible = true
		session.LeaseToken = common.GetUUID()
		session.LeaseExpiresAt = time.Now().Add(90 * time.Second).Unix()
		if err = tx.Create(session).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}

func SaveAssetValidationAttempt(ctx context.Context, session *AssetValidationSession, minimumDelaySeconds int64) error {
	now := time.Now().Unix()
	nextPollAt := max(session.NextPollAt, now+max(5, minimumDelaySeconds))
	values := map[string]any{"status": session.Status, "secret_cipher": session.SecretCipher, "resource_possible": session.ResourcePossible,
		"error_code": session.ErrorCode, "error_message": session.ErrorMessage, "lease_token": "", "lease_expires_at": 0,
		"next_poll_at": nextPollAt, "updated_at": now}
	result := protectedAssetDB(ctx).Model(&AssetValidationSession{}).Where("id = ? AND lease_token = ?", session.ID, session.LeaseToken).Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAssetConflict
	}
	session.LeaseToken = ""
	session.LeaseExpiresAt = 0
	session.NextPollAt = nextPollAt
	session.UpdatedAt = now
	return nil
}

func ClaimAssetValidationResult(ctx context.Context, session *AssetValidationSession) (bool, error) {
	claimed := false
	err := protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		config, err := lockAssetStorage(tx)
		if err != nil {
			return err
		}
		if !config.Enabled {
			return ErrAssetStorageDisabled
		}
		var current AssetValidationSession
		if err = lockForUpdate(tx).First(&current, session.ID).Error; err != nil {
			return err
		}
		*session = current
		if err = validateAssetValidationIdentity(tx, session); err != nil {
			return err
		}
		if err = validateAssetValidationChannel(tx, session); err != nil {
			return err
		}
		now := time.Now().Unix()
		if session.Status == AssetValidationCreated || session.LeaseExpiresAt > now || session.NextPollAt > now ||
			(session.UpstreamGroupID == "" && (session.SecretCipher == "" || session.ExpiresAt <= now)) {
			return nil
		}
		if session.UpstreamGroupID == "" {
			if err = reserveAssetValidationRate(tx, session.RateScope, time.Now().UnixMilli()); err != nil {
				return err
			}
		}
		session.LeaseToken = common.GetUUID()
		session.LeaseExpiresAt = now + 90
		session.Status = AssetValidationVerifying
		session.LastCheckedAt = now
		// Keep the previous attempt's timestamps in memory for the next backoff;
		// GORM would overwrite UpdatedAt if this update used session as its model.
		result := tx.Model(&AssetValidationSession{}).Where("id = ?", session.ID).
			Updates(map[string]any{"lease_token": session.LeaseToken, "lease_expires_at": session.LeaseExpiresAt, "status": session.Status, "updated_at": now, "last_checked_at": now})
		claimed = result.Error == nil && result.RowsAffected == 1
		return result.Error
	})
	return claimed, err
}

// SaveAssetValidationGroupID is committed before a second network call or group
// binding. The provider token may expire after this point without losing the ID.
func SaveAssetValidationGroupID(ctx context.Context, session *AssetValidationSession, id string) error {
	result := protectedAssetDB(ctx).Model(&AssetValidationSession{}).
		Where("id = ? AND lease_token = ? AND (upstream_group_id = '' OR upstream_group_id = ?)", session.ID, session.LeaseToken, id).
		Updates(map[string]any{"upstream_group_id": id, "secret_cipher": "", "updated_at": time.Now().Unix()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAssetConflict
	}
	session.UpstreamGroupID = id
	session.SecretCipher = ""
	return nil
}

func CompleteAssetValidationSession(ctx context.Context, session *AssetValidationSession, authorize func(*gorm.DB) error) error {
	if authorize == nil {
		return ErrUserSessionInactive
	}
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		storage, err := lockAssetStorage(tx)
		if err != nil {
			return err
		}
		if !storage.Enabled {
			return ErrAssetStorageDisabled
		}
		var current AssetValidationSession
		if err = lockForUpdate(tx).First(&current, session.ID).Error; err != nil {
			return err
		}
		if current.Status == AssetValidationCreated {
			*session = current
			return nil
		}
		if current.LeaseToken != session.LeaseToken || current.LeaseExpiresAt <= time.Now().Unix() || current.UpstreamGroupID == "" || current.UpstreamGroupID != session.UpstreamGroupID {
			return ErrAssetConflict
		}
		if err = validateAssetValidationIdentity(tx, &current); err != nil {
			return err
		}
		if err = validateAssetValidationChannel(tx, &current); err != nil {
			return err
		}
		// Evaluate the host's live group/model policy using this transaction while
		// the original user, credential and channel rows remain locked.
		if err = authorize(lockForUpdate(tx)); err != nil {
			return err
		}
		var count int64
		if err = tx.Model(&AssetGroupReplica{}).Where("channel_id = ? AND upstream_id = ?", current.ChannelID, current.UpstreamGroupID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrAssetConflict
		}
		group := AssetGroup{PublicID: NewAssetGroupID(), UserID: current.UserID, ProjectName: current.ProjectName, Name: current.Name,
			Description: current.Description, GroupType: AssetGroupLivenessFace, Lifecycle: AssetGroupActive, InitialChannelID: current.ChannelID}
		if err = tx.Create(&group).Error; err != nil {
			return err
		}
		replica := AssetGroupReplica{GroupID: group.ID, ChannelID: current.ChannelID, UpstreamID: current.UpstreamGroupID, State: AssetReady}
		if err = tx.Create(&replica).Error; err != nil {
			return err
		}
		if err = tx.Model(&current).Updates(map[string]any{"group_id": group.PublicID, "status": AssetValidationCreated, "secret_cipher": "", "error_code": "", "error_message": "", "lease_token": "", "lease_expires_at": 0, "next_poll_at": 0, "updated_at": time.Now().Unix()}).Error; err != nil {
			return err
		}
		session.GroupID = group.PublicID
		session.Status = AssetValidationCreated
		session.SecretCipher = ""
		session.ErrorCode, session.ErrorMessage = "", ""
		session.LeaseToken, session.LeaseExpiresAt = "", 0
		session.NextPollAt = 0
		return nil
	})
}

// ExpireAssetValidationSecrets can run on every worker node. Active leases finish
// first; an expired H5 token does not expire a known portrait authorization.
func ExpireAssetValidationSecrets(ctx context.Context, now int64) error {
	return protectedAssetDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&AssetValidationSession{}).Where("expires_at <= ? AND lease_expires_at <= ? AND secret_cipher <> '' AND upstream_group_id = ''", now, now).
			Updates(map[string]any{"secret_cipher": "", "status": AssetValidationExpired, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&AssetValidationSession{}).Where("expires_at <= ? AND lease_expires_at <= ? AND secret_cipher <> '' AND upstream_group_id <> ''", now, now).
			Updates(map[string]any{"secret_cipher": "", "updated_at": now}).Error
	})
}
