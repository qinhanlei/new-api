package model

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrExchangeRateUnavailable = errors.New("No valid exchange rate is available")
	ErrExchangePolicyConflict  = errors.New("Currency settings have changed; reload before saving")
	exchangeDecimalPattern     = regexp.MustCompile(`^[0-9]{1,8}(\.[0-9]{1,12})?$`)
)

// WalletExchangePolicy governs wallet funding only. Payment gateway rates and
// the model's pricing currency are deliberately independent of this policy.
type WalletExchangePolicy struct {
	ID                   int    `json:"-" gorm:"primaryKey;autoIncrement:false"`
	CrossCurrencyEnabled bool   `json:"cross_currency_enabled"`
	DynamicEnabled       bool   `json:"dynamic_enabled"`
	StaticRate           string `json:"static_rate" gorm:"type:varchar(64);not null"`
	IntervalHours        int    `json:"interval_hours"`
	MaxQuoteAgeHours     int    `json:"max_quote_age_hours"`
	MaxChangePercent     string `json:"max_change_percent" gorm:"type:varchar(32)"`
	MaxWeekChangePercent string `json:"max_week_change_percent" gorm:"type:varchar(32)"`
	Version              int64  `json:"version"`
	ActiveRateID         int64  `json:"active_rate_id"`
	BaselineRateID       int64  `json:"-"`
	UpdatedAt            int64  `json:"updated_at"`
}

// ExchangeRateObservation is append-only. Re-fetching an old observation never
// changes its source timestamp or extends its validity.
type ExchangeRateObservation struct {
	ID            int64  `json:"id" gorm:"primaryKey"`
	Rate          string `json:"rate" gorm:"type:varchar(64);not null"`
	Source        string `json:"source" gorm:"type:varchar(32);not null"`
	SourceTime    int64  `json:"source_time" gorm:"index"`
	ObservedAt    int64  `json:"observed_at" gorm:"index"`
	PolicyVersion int64  `json:"policy_version"`
	Accepted      bool   `json:"accepted"`
	Reason        string `json:"reason" gorm:"type:text"`
}

// WalletExchangeSnapshot travels with a request, including across task restarts.
// Rate always means CNY per USD; reverse conversion divides by this same value.
type WalletExchangeSnapshot struct {
	Enabled       bool   `json:"enabled"`
	RateID        int64  `json:"rate_id,omitempty"`
	Rate          string `json:"rate,omitempty"`
	Source        string `json:"source,omitempty"`
	SourceTime    int64  `json:"source_time,omitempty"`
	PolicyVersion int64  `json:"policy_version"`
}

func DefaultWalletExchangePolicy() WalletExchangePolicy {
	return WalletExchangePolicy{ID: 1, StaticRate: strconv.FormatFloat(operation_setting.USDExchangeRate, 'f', -1, 64), IntervalHours: 6, MaxQuoteAgeHours: 168, MaxChangePercent: "3", MaxWeekChangePercent: "10"}
}

func GetWalletExchangePolicy() (*WalletExchangePolicy, error) {
	policy := DefaultWalletExchangePolicy()
	err := DB.First(&policy, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	return &policy, err
}

func ValidateWalletExchangePolicy(policy WalletExchangePolicy) error {
	if !exchangeDecimalPattern.MatchString(policy.StaticRate) {
		return errors.New("Exchange rate must be between 0.01 and 1000")
	}
	rate, err := decimal.NewFromString(policy.StaticRate)
	if err != nil || !rate.IsPositive() || rate.LessThan(decimal.NewFromFloat(0.01)) || rate.GreaterThan(decimal.NewFromInt(1000)) || len(policy.StaticRate) > 64 {
		return errors.New("Exchange rate must be between 0.01 and 1000")
	}
	if policy.IntervalHours < 1 || policy.IntervalHours > 168 || policy.MaxQuoteAgeHours < policy.IntervalHours || policy.MaxQuoteAgeHours > 720 {
		return errors.New("Invalid exchange rate update interval or maximum age")
	}
	for _, value := range []string{policy.MaxChangePercent, policy.MaxWeekChangePercent} {
		if !exchangeDecimalPattern.MatchString(value) {
			return errors.New("Exchange rate change limits must be between 0 and 50 percent")
		}
		limit, err := decimal.NewFromString(value)
		if err != nil || !limit.IsPositive() || limit.GreaterThan(decimal.NewFromInt(50)) || len(value) > 32 {
			return errors.New("Exchange rate change limits must be between 0 and 50 percent")
		}
	}
	return nil
}

// SaveWalletExchangePolicy uses optimistic concurrency so a delayed refresh
// cannot overwrite a subsequent admin choice. A dynamic baseline must have
// been fetched and explicitly submitted with this settings save.
func SaveWalletExchangePolicy(next WalletExchangePolicy, baseline *ExchangeRateObservation) (*WalletExchangePolicy, error) {
	if err := ValidateWalletExchangePolicy(next); err != nil {
		return nil, err
	}
	var saved WalletExchangePolicy
	err := DB.Transaction(func(tx *gorm.DB) error {
		initial := DefaultWalletExchangePolicy()
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).First(&saved, 1).Error; err != nil {
			return err
		}
		if saved.Version != next.Version {
			return ErrExchangePolicyConflict
		}
		now := common.GetTimestamp()
		activeID := saved.ActiveRateID
		baselineID := saved.BaselineRateID
		if next.DynamicEnabled && !saved.DynamicEnabled {
			if baseline == nil || baseline.Source != "frankfurter" || validateExchangeObservation(*baseline, next, now) != nil {
				return ErrExchangeRateUnavailable
			}
			row := *baseline
			row.ID, row.PolicyVersion, row.ObservedAt, row.Accepted, row.Reason = 0, saved.Version+1, now, true, ""
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			activeID = row.ID
			baselineID = row.ID
		}
		saved = next
		saved.ID, saved.ActiveRateID, saved.UpdatedAt = 1, activeID, now
		saved.BaselineRateID = baselineID
		saved.Version++
		return tx.Save(&saved).Error
	})
	return &saved, err
}

func validateExchangeObservation(row ExchangeRateObservation, policy WalletExchangePolicy, now int64) error {
	if !exchangeDecimalPattern.MatchString(row.Rate) {
		return errors.New("Exchange rate is outside the accepted range")
	}
	rate, err := decimal.NewFromString(row.Rate)
	if err != nil || len(row.Rate) > 64 || !rate.IsPositive() || rate.LessThan(decimal.NewFromFloat(0.01)) || rate.GreaterThan(decimal.NewFromInt(1000)) {
		return errors.New("Exchange rate is outside the accepted range")
	}
	if row.SourceTime <= 0 || row.SourceTime > now+300 || now-row.SourceTime > int64(policy.MaxQuoteAgeHours)*3600 {
		return errors.New("Exchange rate observation is stale or has an invalid date")
	}
	return nil
}

func GetWalletExchangeSnapshot(tx *gorm.DB, now int64) (WalletExchangeSnapshot, error) {
	policy := DefaultWalletExchangePolicy()
	if err := tx.First(&policy, 1).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return WalletExchangeSnapshot{}, err
	}
	snapshot := WalletExchangeSnapshot{Enabled: policy.CrossCurrencyEnabled, PolicyVersion: policy.Version}
	if !policy.DynamicEnabled {
		if err := ValidateWalletExchangePolicy(policy); err != nil {
			return snapshot, ErrExchangeRateUnavailable
		}
		snapshot.Rate, snapshot.Source, snapshot.SourceTime = policy.StaticRate, "manual", policy.UpdatedAt
		return snapshot, nil
	}
	var row ExchangeRateObservation
	if policy.ActiveRateID == 0 {
		return snapshot, ErrExchangeRateUnavailable
	}
	if err := tx.First(&row, policy.ActiveRateID).Error; err != nil {
		return snapshot, err
	}
	if !row.Accepted || validateExchangeObservation(row, policy, now) != nil {
		return snapshot, ErrExchangeRateUnavailable
	}
	snapshot.RateID, snapshot.Rate, snapshot.Source, snapshot.SourceTime = row.ID, row.Rate, row.Source, row.SourceTime
	return snapshot, nil
}

func RecordExchangeObservation(expectedVersion int64, row ExchangeRateObservation) (*ExchangeRateObservation, error) {
	err := DB.Transaction(func(tx *gorm.DB) error {
		// The write-first operation also serializes SQLite writers.
		if err := tx.Model(&WalletExchangePolicy{}).Where("id = ?", 1).UpdateColumn("id", 1).Error; err != nil {
			return err
		}
		var policy WalletExchangePolicy
		if err := lockForUpdate(tx).First(&policy, 1).Error; err != nil {
			return err
		}
		if !policy.DynamicEnabled || policy.Version != expectedVersion {
			return ErrExchangePolicyConflict
		}
		now := common.GetTimestamp()
		row.ID, row.ObservedAt, row.PolicyVersion, row.Accepted = 0, now, expectedVersion, false
		if err := validateExchangeObservation(row, policy, now); err != nil && row.Reason == "" {
			row.Reason = err.Error()
		}
		if row.Reason == "" {
			var recent []ExchangeRateObservation
			if err := tx.Where("accepted = ? AND id >= ? AND observed_at >= ?", true, policy.BaselineRateID, now-int64(7*24*time.Hour/time.Second)).Find(&recent).Error; err != nil {
				return err
			}
			var active ExchangeRateObservation
			if err := tx.First(&active, policy.ActiveRateID).Error; err != nil {
				return err
			}
			recent = append(recent, active)
			candidate, _ := decimal.NewFromString(row.Rate)
			for _, old := range recent {
				if row.SourceTime < old.SourceTime {
					row.Reason = "Exchange rate source date moved backwards"
					break
				}
				limitText := policy.MaxWeekChangePercent
				if old.ID == active.ID || old.ObservedAt >= now-86400 {
					limitText = policy.MaxChangePercent
				}
				limit, _ := decimal.NewFromString(limitText)
				previous, err := decimal.NewFromString(old.Rate)
				if err != nil || !previous.IsPositive() {
					return ErrExchangeRateUnavailable
				}
				change := candidate.Sub(previous).Abs().Div(previous).Mul(decimal.NewFromInt(100))
				if change.GreaterThan(limit) {
					row.Reason = fmt.Sprintf("Exchange rate change exceeds %s%%", limitText)
					break
				}
			}
		}
		row.Accepted = row.Reason == ""
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if row.Accepted {
			return tx.Model(&policy).Update("active_rate_id", row.ID).Error
		}
		return nil
	})
	return &row, err
}
