package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	sqlitedriver "github.com/glebarez/go-sqlite"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const billingRateScale int64 = 1_000_000
const maxBillingRatePPM int64 = 100 * billingRateScale

// A null rate is an inheritance tombstone: retaining its version prevents a
// stale editor from recreating a cleared rule with an old expected version.
type BillingRateRule struct {
	ID          int64  `gorm:"primaryKey"`
	OwnerUserID int    `gorm:"not null;uniqueIndex:idx_billing_rate_owner_model,priority:1"`
	ModelName   string `gorm:"type:text;not null"`
	ModelKey    string `gorm:"type:char(64);not null;uniqueIndex:idx_billing_rate_owner_model,priority:2"`
	RatePPM     *int64 `gorm:"type:bigint"`
	Version     int64  `gorm:"type:bigint;not null"`
	UpdatedBy   int    `gorm:"not null"`
	UpdatedAt   int64  `gorm:"type:bigint;not null;autoUpdateTime"`
}

type BillingRateEntry struct {
	ModelName string  `json:"model_name"`
	Rate      *string `json:"rate"`
	Version   int64   `json:"version"`
}

type BillingRateRuleList struct {
	Rules            []BillingRateEntry `json:"rules"`
	GlobalRules      []BillingRateEntry `json:"global_rules"`
	BillingModelName string             `json:"billing_model_name,omitempty"`
}

type BillingRateChange struct {
	ModelName       string  `json:"model_name"`
	Rate            *string `json:"rate"`
	ExpectedVersion int64   `json:"expected_version"`
}

type BillingRateAuditChange struct {
	Before BillingRateEntry `json:"before"`
	After  BillingRateEntry `json:"after"`
}

var (
	ErrBillingRateConflict    = common.NewMessage("Billing rates changed; reload before saving")
	ErrBillingRateUser        = common.NewMessage("Billing rate target user does not exist")
	ErrBillingRateUnavailable = errors.New("billing rates are unavailable")
	billingRatePattern        = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,6})?$`)
	billingRateMutationMu     sync.Mutex
)

func billingRateModelKey(name string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
}

func ValidateBillingRateModelName(name string) error {
	if name == "" || len(name) > 512 || !utf8.ValidString(name) || strings.TrimSpace(name) != name || strings.ContainsFunc(name, unicode.IsControl) {
		return common.NewMessage("Invalid billing rate model name")
	}
	return nil
}

func (rule BillingRateRule) Entry() BillingRateEntry {
	entry := BillingRateEntry{ModelName: rule.ModelName, Version: rule.Version}
	if rule.RatePPM != nil {
		rate := decimal.NewFromInt(*rule.RatePPM).Shift(-6).StringFixed(6)
		entry.Rate = &rate
	}
	return entry
}

// BillingRateRules is a request-local view; it never enters a shared pricing
// cache. Keys use the exact billing model name, independent of DB collation.
type BillingRateRules struct {
	userID int
	rules  map[string]map[int]BillingRateRule
}

func LoadBillingRateRules(userID int, names []string) (*BillingRateRules, error) {
	if DB == nil {
		return nil, ErrBillingRateUnavailable
	}
	query := DB.Where("owner_user_id IN ?", []int{0, userID})
	if names != nil {
		keys := make([]string, 0, len(names))
		for _, name := range names {
			keys = append(keys, billingRateModelKey(name))
		}
		query = query.Where("model_key IN ?", keys)
	}
	var rows []BillingRateRule
	if err := query.Find(&rows).Error; err != nil {
		common.SysError(common.LogText("failed to load billing rates: %v", sanitizeDBError(err)))
		return nil, ErrBillingRateUnavailable
	}
	result := &BillingRateRules{userID: userID, rules: make(map[string]map[int]BillingRateRule)}
	for _, rule := range rows {
		if rule.ModelKey != billingRateModelKey(rule.ModelName) || rule.Version < 1 ||
			(rule.RatePPM != nil && (*rule.RatePPM < 0 || *rule.RatePPM > maxBillingRatePPM)) {
			common.SysError(common.LogText("invalid stored billing rate rule: %d", rule.ID))
			return nil, ErrBillingRateUnavailable
		}
		if result.rules[rule.ModelName] == nil {
			result.rules[rule.ModelName] = make(map[int]BillingRateRule)
		}
		result.rules[rule.ModelName][rule.OwnerUserID] = rule
	}
	return result, nil
}

func (rules *BillingRateRules) Resolve(modelName string, group types.GroupRatioInfo) *types.BillingRateSnapshot {
	snapshot := &types.BillingRateSnapshot{ModelName: modelName, Rate: group.GroupRatio, Source: "group"}
	if group.HasSpecialRatio {
		snapshot.Source = "group_special"
	}
	byOwner := rules.rules[modelName]
	for _, owner := range []int{rules.userID, 0} {
		rule, exists := byOwner[owner]
		if !exists || rule.RatePPM == nil {
			continue
		}
		snapshot.Rate = float64(*rule.RatePPM) / float64(billingRateScale)
		snapshot.Version = rule.Version
		snapshot.Source = "model"
		if owner > 0 {
			snapshot.Source = "user"
		}
		break
	}
	return snapshot
}

func ResolveBillingRate(userID int, modelName string, group types.GroupRatioInfo) (*types.BillingRateSnapshot, error) {
	rules, err := LoadBillingRateRules(userID, []string{modelName})
	if err != nil {
		return nil, err
	}
	return rules.Resolve(modelName, group), nil
}

func GetBillingRateRules(ownerUserID int) (*BillingRateRuleList, error) {
	if DB == nil {
		return nil, ErrBillingRateUnavailable
	}
	if ownerUserID < 0 {
		return nil, ErrBillingRateUser
	}
	if ownerUserID > 0 {
		var user User
		if err := DB.Select("id").First(&user, ownerUserID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrBillingRateUser
			}
			return nil, err
		}
	}
	return readBillingRateRuleList(DB, ownerUserID)
}

func readBillingRateRuleList(db *gorm.DB, ownerUserID int) (*BillingRateRuleList, error) {
	var rows []BillingRateRule
	if err := db.Where("owner_user_id IN ?", []int{0, ownerUserID}).Find(&rows).Error; err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(a, b BillingRateRule) int { return strings.Compare(a.ModelName, b.ModelName) })
	result := &BillingRateRuleList{Rules: []BillingRateEntry{}, GlobalRules: []BillingRateEntry{}}
	for _, row := range rows {
		if row.OwnerUserID == ownerUserID {
			result.Rules = append(result.Rules, row.Entry())
		} else {
			result.GlobalRules = append(result.GlobalRules, row.Entry())
		}
	}
	return result, nil
}

func UpdateBillingRateRules(ownerUserID, updatedBy int, changes []BillingRateChange) (*BillingRateRuleList, []BillingRateAuditChange, error) {
	if DB == nil {
		return nil, nil, ErrBillingRateUnavailable
	}
	if ownerUserID < 0 {
		return nil, nil, ErrBillingRateUser
	}
	if len(changes) == 0 || len(changes) > 200 {
		return nil, nil, common.NewMessage("Select between 1 and 200 billing rate changes")
	}
	rows := make([]BillingRateRule, 0, len(changes))
	seen := make(map[string]bool, len(changes))
	for _, change := range changes {
		name := change.ModelName
		if err := ValidateBillingRateModelName(name); err != nil {
			return nil, nil, err
		}
		if seen[name] {
			return nil, nil, common.NewMessage("Duplicate billing rate model")
		}
		seen[name] = true
		if change.ExpectedVersion < 0 || change.ExpectedVersion == math.MaxInt64 {
			return nil, nil, common.NewMessage("Invalid billing rate version")
		}
		row := BillingRateRule{OwnerUserID: ownerUserID, ModelName: name, ModelKey: billingRateModelKey(name), Version: change.ExpectedVersion + 1, UpdatedBy: updatedBy, UpdatedAt: common.GetTimestamp()}
		if change.Rate != nil {
			rate, err := decimal.NewFromString(*change.Rate)
			if err != nil || !billingRatePattern.MatchString(*change.Rate) || rate.IsNegative() || rate.GreaterThan(decimal.NewFromInt(100)) {
				return nil, nil, common.NewMessage("Billing rate must be between 0 and 100 with at most 6 decimal places")
			}
			ppm := rate.Shift(6).IntPart()
			row.RatePPM = &ppm
		}
		rows = append(rows, row)
	}
	// A stable write order avoids cross-model deadlocks. The local mutex also
	// prevents competing SQLite readers from upgrading to writers together;
	// versions and unique keys remain the cross-process concurrency controls.
	slices.SortFunc(rows, func(a, b BillingRateRule) int { return strings.Compare(a.ModelKey, b.ModelKey) })
	billingRateMutationMu.Lock()
	defer billingRateMutationMu.Unlock()
	var result *BillingRateRuleList
	audit := make([]BillingRateAuditChange, 0, len(rows))
	err := DB.Transaction(func(tx *gorm.DB) error {
		if ownerUserID > 0 {
			var user User
			if err := lockForUpdate(tx).Select("id").First(&user, ownerUserID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrBillingRateUser
				}
				return err
			}
		}
		for _, row := range rows {
			previous := BillingRateRule{ModelName: row.ModelName}
			if row.Version == 1 {
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			} else {
				query := tx.Where("owner_user_id = ? AND model_key = ? AND version = ?", ownerUserID, row.ModelKey, row.Version-1)
				if err := query.First(&previous).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return ErrBillingRateConflict
					}
					return err
				}
				update := tx.Model(&BillingRateRule{}).Where("owner_user_id = ? AND model_key = ? AND version = ?", ownerUserID, row.ModelKey, row.Version-1).
					Updates(map[string]any{"rate_ppm": row.RatePPM, "version": row.Version, "updated_by": row.UpdatedBy, "updated_at": row.UpdatedAt})
				if update.Error != nil {
					return update.Error
				}
				if update.RowsAffected != 1 {
					return ErrBillingRateConflict
				}
			}
			audit = append(audit, BillingRateAuditChange{Before: previous.Entry(), After: row.Entry()})
		}
		var err error
		result, err = readBillingRateRuleList(tx, ownerUserID)
		return err
	})
	if err != nil {
		// Drivers report unique-key conflicts differently; do not turn unrelated
		// database errors into a successful fallback or expose their SQL text.
		var mysqlErr *mysql.MySQLError
		var postgresErr *pgconn.PgError
		var sqliteErr *sqlitedriver.Error
		if errors.Is(err, gorm.ErrDuplicatedKey) ||
			(errors.As(err, &mysqlErr) && mysqlErr.Number == 1062) ||
			(errors.As(err, &postgresErr) && postgresErr.Code == "23505") ||
			(errors.As(err, &sqliteErr) && (sqliteErr.Code() == 2067 || sqliteErr.Code() == 1555)) {
			err = ErrBillingRateConflict
		}
		return nil, nil, err
	}
	return result, audit, nil
}
