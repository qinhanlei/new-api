package model

import (
	"errors"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	CNYQuotaPerUnit      = 500000
	CNYBillingRequest    = "request"
	CNYBillingAdjustment = "adjustment"
	CNYBillingReserved   = "reserved"
	CNYBillingSubmitted  = "submitted"
	CNYBillingSettled    = "settled"
	CNYBillingRefunded   = "refunded"
)

var (
	ErrCNYInvalid    = errors.New("invalid CNY billing parameters")
	ErrCNYConflict   = errors.New("CNY billing operation conflicts with its existing record")
	ErrCNYQuota      = errors.New("insufficient CNY wallet quota")
	ErrCNYTokenQuota = errors.New("CNY token budget is disabled or insufficient")
	ErrCNYVersion    = errors.New("CNY token budget has changed; reload before saving")
	ErrCNYState      = errors.New("CNY billing record is already final")
	cnyOperationID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
)

// CNY balances never share USD quota columns or Redis quota counters.
type CNYWallet struct {
	UserID        int    `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	Currency      string `json:"currency" gorm:"type:varchar(3);not null"`
	QuotaPerUnit  int    `json:"quota_per_unit" gorm:"not null"`
	Quota         int    `json:"quota" gorm:"type:bigint;not null"`
	UsedQuota     int    `json:"used_quota" gorm:"type:bigint;not null"`
	ReservedQuota int    `json:"reserved_quota" gorm:"type:bigint;not null"`
	Version       int64  `json:"version" gorm:"not null"`
}

// Budget rows survive token deletion so in-flight requests can settle or refund.
type CNYTokenBudget struct {
	TokenID        int    `json:"token_id" gorm:"primaryKey;autoIncrement:false"`
	UserID         int    `json:"user_id" gorm:"index;not null"`
	Currency       string `json:"currency" gorm:"type:varchar(3);not null"`
	QuotaPerUnit   int    `json:"quota_per_unit" gorm:"not null"`
	Enabled        bool   `json:"enabled"`
	RemainQuota    int    `json:"remain_quota" gorm:"type:bigint;not null"`
	UnlimitedQuota bool   `json:"unlimited_quota"`
	UsedQuota      int    `json:"used_quota" gorm:"type:bigint;not null"`
	Version        int64  `json:"version" gorm:"not null"`
}

// One main-database record is the durable state of a request, including free
// requests. Kind separates administrator operation IDs from server request IDs.
type CNYBillingRecord struct {
	ID             int64  `json:"id" gorm:"primaryKey"`
	Kind           string `json:"kind" gorm:"type:varchar(16);uniqueIndex:idx_cny_billing_operation,priority:1;not null"`
	RequestID      string `json:"request_id" gorm:"type:varchar(128);uniqueIndex:idx_cny_billing_operation,priority:2;not null"`
	Currency       string `json:"currency" gorm:"type:varchar(3);not null"`
	QuotaPerUnit   int    `json:"quota_per_unit" gorm:"not null"`
	UserID         int    `json:"user_id" gorm:"index;not null"`
	TokenID        int    `json:"token_id" gorm:"index"`
	OperatorID     int    `json:"operator_id"`
	Username       string `json:"username" gorm:"type:varchar(191)"`
	TokenName      string `json:"token_name" gorm:"type:varchar(191)"`
	ModelName      string `json:"model_name" gorm:"type:varchar(255);index"`
	ChannelID      int    `json:"channel_id" gorm:"index"`
	Group          string `json:"group" gorm:"type:varchar(128)"`
	TaskID         string `json:"task_id" gorm:"type:varchar(191);index"`
	Status         string `json:"status" gorm:"type:varchar(16);index;not null"`
	ReservedQuota  int    `json:"reserved_quota" gorm:"type:bigint;not null"`
	Quota          int    `json:"quota" gorm:"type:bigint;not null"`
	RefundQuota    int    `json:"refund_quota" gorm:"type:bigint;not null"`
	FundingVersion int    `json:"-"`
	// Freeze the actual token allocation, not the token's mutable current policy.
	TokenLimited       bool   `json:"-"`
	TokenReservedQuota int    `json:"-" gorm:"type:bigint;not null"`
	TokenQuota         int    `json:"-" gorm:"type:bigint;not null"`
	DeltaQuota         int    `json:"delta_quota" gorm:"type:bigint;not null"`
	BeforeQuota        int    `json:"before_quota" gorm:"type:bigint;not null"`
	AfterQuota         int    `json:"after_quota" gorm:"type:bigint;not null"`
	Reason             string `json:"reason" gorm:"type:text"`
	PromptTokens       int    `json:"prompt_tokens"`
	CompletionTokens   int    `json:"completion_tokens"`
	UseTime            int    `json:"use_time"`
	IsStream           bool   `json:"is_stream"`
	Other              string `json:"other" gorm:"type:text"`
	Snapshot           string `json:"snapshot" gorm:"type:text"`
	CreatedAt          int64  `json:"created_at" gorm:"index"`
	UpdatedAt          int64  `json:"updated_at"`
	SettledAt          int64  `json:"settled_at" gorm:"index"`
}

type CNYBillingParams struct {
	RequestID                                       string
	UserID, TokenID, ChannelID                      int
	IsPlayground                                    bool
	Username, TokenName, ModelName, Group, Snapshot string
	Free                                            bool // Proven free pricing, not merely a zero estimated reservation.
}

type CNYBillingUsage struct {
	PromptTokens, CompletionTokens, UseTime int
	IsStream                                bool
	Other, Snapshot                         string
}

type CNYTokenBudgetChange struct {
	Enabled         bool  `json:"enabled"`
	RemainQuota     int   `json:"remain_quota"`
	UnlimitedQuota  bool  `json:"unlimited_quota"`
	ExpectedVersion int64 `json:"expected_version"`
}

func GetCNYWallet(userID int) (*CNYWallet, error) {
	if userID <= 0 {
		return nil, ErrCNYInvalid
	}
	var user User
	if err := DB.Select("id").First(&user, userID).Error; err != nil {
		return nil, err
	}
	wallet := CNYWallet{UserID: userID, Currency: "CNY", QuotaPerUnit: CNYQuotaPerUnit}
	err := DB.First(&wallet, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	return &wallet, err
}

// The initial INSERT also acquires SQLite's writer lock before any transaction
// reads. This avoids deferred read-to-write upgrades racing other reservations.
func lockCNYWallet(tx *gorm.DB, userID int) (*CNYWallet, error) {
	wallet := CNYWallet{UserID: userID, Currency: "CNY", QuotaPerUnit: CNYQuotaPerUnit}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&wallet).Error; err != nil {
		return nil, err
	}
	if err := lockForUpdate(tx).First(&wallet, "user_id = ?", userID).Error; err != nil {
		return nil, err
	}
	return &wallet, nil
}

func cnyBalanceAfter(before, delta int) (int, error) {
	if before > common.MaxWalletQuota || before < -common.MaxWalletQuota || delta > common.MaxWalletQuota || delta < -common.MaxWalletQuota {
		return 0, ErrWalletQuotaLimitExceeded
	}
	result, err := common.WalletQuotaFromDecimalStrict(decimal.NewFromInt(int64(before)).Add(decimal.NewFromInt(int64(delta))))
	if err != nil {
		return 0, ErrWalletQuotaLimitExceeded
	}
	return result, nil
}

type cnyWalletAdjustment struct {
	Mode  string `json:"mode"`
	Value int    `json:"value"`
}

func AdjustCNYWallet(userID, operatorID, operatorRole, deltaQuota int, operationID, reason string) (*CNYBillingRecord, error) {
	return adjustCNYWallet(userID, operatorID, operatorRole, cnyWalletAdjustment{Mode: "delta", Value: deltaQuota}, operationID, reason)
}

func AdjustCNYWalletByMode(userID, operatorID, operatorRole int, mode string, value int, operationID, reason string) (*CNYBillingRecord, error) {
	if mode != "add" && mode != "subtract" && mode != "override" {
		return nil, ErrCNYInvalid
	}
	if mode != "override" && value <= 0 {
		return nil, ErrCNYInvalid
	}
	return adjustCNYWallet(userID, operatorID, operatorRole, cnyWalletAdjustment{Mode: mode, Value: value}, operationID, reason)
}

func adjustCNYWallet(userID, operatorID, operatorRole int, change cnyWalletAdjustment, operationID, reason string) (*CNYBillingRecord, error) {
	if userID <= 0 || operatorID <= 0 || !cnyOperationID.MatchString(operationID) || len(reason) > 2000 {
		return nil, ErrCNYInvalid
	}
	if change.Value > common.MaxWalletQuota || change.Value < -common.MaxWalletQuota {
		return nil, ErrWalletQuotaLimitExceeded
	}
	if operatorRole != common.RoleRootUser && operatorRole != common.RoleAdminUser {
		return nil, ErrUserQuotaPermission
	}
	if strings.TrimSpace(reason) == "" {
		reason = "CNY wallet adjustment"
	}
	var record CNYBillingRecord
	err := DB.Transaction(func(tx *gorm.DB) error {
		wallet, err := lockCNYWallet(tx, userID)
		if err != nil {
			return err
		}
		var user User
		if err = lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if operatorRole != common.RoleRootUser && operatorRole <= user.Role {
			return ErrUserQuotaPermission
		}
		err = tx.Where("kind = ? AND request_id = ?", CNYBillingAdjustment, operationID).First(&record).Error
		if err == nil {
			if record.UserID != userID || record.OperatorID != operatorID || record.Reason != reason {
				return ErrCNYConflict
			}
			var metadata struct {
				Adjustment *cnyWalletAdjustment `json:"wallet_adjustment"`
			}
			if record.Other != "" {
				if err = common.UnmarshalJsonStr(record.Other, &metadata); err != nil {
					return ErrCNYConflict
				}
			}
			if metadata.Adjustment != nil {
				if *metadata.Adjustment != change {
					return ErrCNYConflict
				}
			} else if change.Mode != "delta" || record.DeltaQuota != change.Value {
				// Historical adjustments only recorded a delta. Never interpret
				// one as a replayable absolute balance override.
				return ErrCNYConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		deltaQuota := change.Value
		if change.Mode == "subtract" {
			deltaQuota = -deltaQuota
		} else if change.Mode == "override" {
			// Derive the delta from the locked available balance, preserving
			// in-flight reservations and already settled consumption.
			deltaQuota, err = cnyBalanceAfter(change.Value, -wallet.Quota)
			if err != nil {
				return err
			}
		}
		after, err := cnyBalanceAfter(wallet.Quota, deltaQuota)
		if err != nil {
			return err
		}
		metadata, err := common.Marshal(map[string]any{"wallet_adjustment": change})
		if err != nil {
			return err
		}
		now := common.GetTimestamp()
		record = CNYBillingRecord{Kind: CNYBillingAdjustment, RequestID: operationID, Currency: "CNY", QuotaPerUnit: CNYQuotaPerUnit,
			UserID: userID, Username: user.Username, OperatorID: operatorID, Status: CNYBillingSettled,
			DeltaQuota: deltaQuota, BeforeQuota: wallet.Quota, AfterQuota: after, Reason: reason, Other: string(metadata), CreatedAt: now, UpdatedAt: now, SettledAt: now}
		wallet.Quota = after
		wallet.Version++
		if err = tx.Save(wallet).Error; err != nil {
			return err
		}
		return tx.Create(&record).Error
	})
	return &record, err
}

func GetCNYTokenBudget(tokenID, userID int) (*CNYTokenBudget, error) {
	if tokenID <= 0 || userID <= 0 {
		return nil, ErrCNYInvalid
	}
	var token Token
	if err := DB.Where("id = ? AND user_id = ?", tokenID, userID).First(&token).Error; err != nil {
		return nil, err
	}
	budget := CNYTokenBudget{TokenID: tokenID, UserID: userID, Currency: "CNY", QuotaPerUnit: CNYQuotaPerUnit}
	err := DB.Where("token_id = ? AND user_id = ?", tokenID, userID).First(&budget).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	return &budget, err
}

func UpdateCNYTokenBudget(tokenID, userID int, change CNYTokenBudgetChange) (*CNYTokenBudget, error) {
	if tokenID <= 0 || userID <= 0 || change.RemainQuota < 0 || change.RemainQuota > common.MaxWalletQuota || change.ExpectedVersion < 0 {
		return nil, ErrCNYInvalid
	}
	var budget CNYTokenBudget
	var restoredTokenKey string
	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, err := lockCNYWallet(tx, userID); err != nil {
			return err
		}
		var token Token
		if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", tokenID, userID).First(&token).Error; err != nil {
			return err
		}
		err := lockForUpdate(tx).Where("token_id = ? AND user_id = ?", tokenID, userID).First(&budget).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			budget = CNYTokenBudget{TokenID: tokenID, UserID: userID, Currency: "CNY", QuotaPerUnit: CNYQuotaPerUnit}
		} else if err != nil {
			return err
		}
		if budget.Version != change.ExpectedVersion {
			return ErrCNYVersion
		}
		budget.Enabled, budget.RemainQuota, budget.UnlimitedQuota = change.Enabled, change.RemainQuota, change.UnlimitedQuota
		budget.Version++
		if err = tx.Save(&budget).Error; err != nil {
			return err
		}
		if change.Enabled && (change.UnlimitedQuota || change.RemainQuota > 0) && token.Status == common.TokenStatusExhausted && (token.ExpiredTime == -1 || token.ExpiredTime >= common.GetTimestamp()) {
			restoredTokenKey = token.Key
			if err = invalidateTokenCacheForMutation(token.Key); err != nil {
				common.SysLog(common.LogText("failed to invalidate token cache before status update: %s", err.Error()))
			}
			return tx.Model(&Token{}).Where("id = ? AND status = ?", tokenID, common.TokenStatusExhausted).UpdateColumn("status", common.TokenStatusEnabled).Error
		}
		return nil
	})
	// Renew the fence after commit as well: even a slow transaction must not
	// leave metadata published by a reader that saw the old exhausted state.
	if err == nil && restoredTokenKey != "" {
		if cacheErr := invalidateTokenCacheForMutation(restoredTokenKey); cacheErr != nil {
			common.SysLog(common.LogText("failed to invalidate token cache before status update: %s", cacheErr.Error()))
		}
	}
	return &budget, err
}

func HasCNYTokenQuota(tokenID int) (bool, error) {
	var budget CNYTokenBudget
	err := DB.Where("token_id = ?", tokenID).First(&budget).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil && budget.Enabled && (budget.UnlimitedQuota || budget.RemainQuota > 0), err
}

func GetCNYBillingRecord(requestID string) (*CNYBillingRecord, error) {
	if !cnyOperationID.MatchString(requestID) {
		return nil, ErrCNYInvalid
	}
	var record CNYBillingRecord
	err := DB.Where("kind = ? AND request_id = ?", CNYBillingRequest, requestID).First(&record).Error
	return &record, err
}

func ReserveCNYBilling(params CNYBillingParams, targetQuota int) (*CNYBillingRecord, error) {
	if !cnyOperationID.MatchString(params.RequestID) || params.UserID <= 0 || targetQuota < 0 || targetQuota > common.MaxQuota || params.TokenID < 0 || (params.TokenID == 0 && !params.IsPlayground) {
		return nil, ErrCNYInvalid
	}
	var record CNYBillingRecord
	err := DB.Transaction(func(tx *gorm.DB) error {
		wallet, err := lockCNYWallet(tx, params.UserID)
		if err != nil {
			return err
		}
		var user User
		if err = lockForUpdate(tx).First(&user, params.UserID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return ErrUserQuotaPermission
		}
		var budget CNYTokenBudget
		var token Token
		if params.TokenID > 0 {
			if err = lockForUpdate(tx).Where("id = ? AND user_id = ?", params.TokenID, params.UserID).First(&token).Error; err != nil {
				return err
			}
			if (token.Status != common.TokenStatusEnabled && token.Status != common.TokenStatusExhausted) || (token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp()) {
				return ErrTokenInvalid
			}
			if err = tx.Where("token_id = ? AND user_id = ?", params.TokenID, params.UserID).First(&budget).Error; errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCNYTokenQuota
			} else if err != nil {
				return err
			}
			if !budget.Enabled {
				return ErrCNYTokenQuota
			}
		}
		err = tx.Where("kind = ? AND request_id = ?", CNYBillingRequest, params.RequestID).First(&record).Error
		if err == nil {
			if record.UserID != params.UserID || record.TokenID != params.TokenID || record.ModelName != params.ModelName {
				return ErrCNYConflict
			}
			if record.Status != CNYBillingReserved {
				return ErrCNYState
			}
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			record = CNYBillingRecord{Kind: CNYBillingRequest, RequestID: params.RequestID, Currency: "CNY", QuotaPerUnit: CNYQuotaPerUnit,
				UserID: params.UserID, TokenID: params.TokenID, Username: user.Username, TokenName: token.Name, ModelName: params.ModelName,
				Status: CNYBillingReserved, FundingVersion: 1, TokenLimited: params.TokenID > 0 && !budget.UnlimitedQuota, CreatedAt: common.GetTimestamp()}
		} else {
			return err
		}
		// A zero estimate (for example output-only pricing) does not authorize
		// spending an exhausted wallet or budget. Existing funded reservations
		// can still retry without requiring the same money a second time.
		if !params.Free && record.ReservedQuota == 0 {
			if record.FundingVersion == 0 && wallet.Quota <= 0 {
				return ErrCNYQuota
			}
		}
		delta := max(0, targetQuota-record.ReservedQuota)
		if record.FundingVersion == 0 && wallet.Quota < delta {
			return ErrCNYQuota
		}
		if record.FundingVersion == 1 {
			_, err = reserveWalletFunding(tx, wallet, &user, WalletFundingParams{RequestID: params.RequestID, UserID: params.UserID, TokenID: params.TokenID, Currency: "CNY", Free: params.Free}, targetQuota)
			if errors.Is(err, ErrWalletFundingInsufficient) {
				return ErrCNYQuota
			}
			if err != nil {
				return err
			}
		} else {
			wallet.Quota, err = cnyBalanceAfter(wallet.Quota, -delta)
			if err != nil {
				return err
			}
			wallet.ReservedQuota, err = cnyBalanceAfter(wallet.ReservedQuota, delta)
			if err != nil {
				return err
			}
			wallet.Version++
			if err = tx.Save(wallet).Error; err != nil {
				return err
			}
		}
		if params.TokenID > 0 {
			if record.TokenLimited && (budget.RemainQuota < delta || (!params.Free && record.ReservedQuota == 0 && budget.RemainQuota <= 0)) {
				return ErrCNYTokenQuota
			}
			if record.TokenLimited {
				budget.RemainQuota, err = cnyBalanceAfter(budget.RemainQuota, -delta)
				if err != nil {
					return err
				}
				record.TokenReservedQuota += delta
			}
			budget.Version++
			if err = tx.Save(&budget).Error; err != nil {
				return err
			}
		}
		record.ReservedQuota += delta
		record.ChannelID, record.Group, record.Snapshot = params.ChannelID, params.Group, params.Snapshot
		record.UpdatedAt = common.GetTimestamp()
		return tx.Save(&record).Error
	})
	if err == nil {
		InvalidateUserQuotaCache(params.UserID)
	}
	return &record, err
}

func SettleCNYBilling(requestID string, actualQuota int, usage CNYBillingUsage) (*CNYBillingRecord, error) {
	record, err := GetCNYBillingRecord(requestID)
	if err != nil {
		return nil, err
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		wallet, err := lockCNYWallet(tx, record.UserID)
		if err != nil {
			return err
		}
		if err = tx.First(record, record.ID).Error; err != nil {
			return err
		}
		if record.TaskID != "" {
			return ErrCNYState
		}
		return finalizeCNYBilling(tx, wallet, record, actualQuota, false, usage, "")
	})
	if err == nil {
		InvalidateUserQuotaCache(record.UserID)
	}
	return record, err
}

func RefundCNYBilling(requestID, reason string) (*CNYBillingRecord, error) {
	record, err := GetCNYBillingRecord(requestID)
	if err != nil {
		return nil, err
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		wallet, err := lockCNYWallet(tx, record.UserID)
		if err != nil {
			return err
		}
		if err = tx.First(record, record.ID).Error; err != nil {
			return err
		}
		if record.TaskID != "" {
			return ErrCNYState
		}
		return finalizeCNYBilling(tx, wallet, record, 0, true, CNYBillingUsage{}, reason)
	})
	if err == nil {
		InvalidateUserQuotaCache(record.UserID)
	}
	return record, err
}

// Caller holds the user's wallet lock. Neither token deletion nor edits to its
// current budget policy can change the allocation frozen by the reservation.
func finalizeCNYBilling(tx *gorm.DB, wallet *CNYWallet, record *CNYBillingRecord, actualQuota int, refund bool, usage CNYBillingUsage, reason string) error {
	if actualQuota < 0 || actualQuota > common.MaxQuota {
		return ErrCNYInvalid
	}
	if record.Status == CNYBillingRefunded {
		if refund {
			return nil
		}
		return ErrCNYState
	}
	if record.Status == CNYBillingSettled && !refund {
		if record.Quota != actualQuota {
			return ErrCNYConflict
		}
		return nil
	}
	if record.Status != CNYBillingReserved && record.Status != CNYBillingSubmitted && record.Status != CNYBillingSettled {
		return ErrCNYState
	}
	walletDelta, usedDelta, reservedDelta := record.ReservedQuota-actualQuota, actualQuota, -record.ReservedQuota
	tokenCharge := 0
	if record.TokenLimited {
		tokenCharge = actualQuota
	}
	tokenDelta := record.TokenReservedQuota - tokenCharge
	if refund {
		walletDelta, usedDelta, tokenDelta = record.ReservedQuota, 0, record.TokenReservedQuota
		record.RefundQuota = record.ReservedQuota
		if record.Status == CNYBillingSettled {
			walletDelta, usedDelta, reservedDelta, tokenDelta = record.Quota, -record.Quota, 0, record.TokenQuota
			record.RefundQuota = record.Quota
		}
	}
	var err error
	if record.FundingVersion == 1 {
		var user User
		if err = lockForUpdate(tx.Unscoped()).First(&user, record.UserID).Error; err != nil {
			return err
		}
		var funding WalletFundingRecord
		if err = tx.Where("request_id = ? AND user_id = ?", record.RequestID, record.UserID).First(&funding).Error; err != nil {
			return err
		}
		if err = finalizeWalletFunding(tx, wallet, &user, &funding, actualQuota, refund); err != nil {
			return err
		}
		walletDelta, reservedDelta = 0, 0
	}
	wallet.Quota, err = cnyBalanceAfter(wallet.Quota, walletDelta)
	if err != nil {
		return err
	}
	wallet.UsedQuota, err = cnyBalanceAfter(wallet.UsedQuota, usedDelta)
	if err != nil {
		return err
	}
	wallet.ReservedQuota, err = cnyBalanceAfter(wallet.ReservedQuota, reservedDelta)
	if err != nil {
		return err
	}
	wallet.Version++
	if err = tx.Save(wallet).Error; err != nil {
		return err
	}
	if record.TokenID > 0 {
		var budget CNYTokenBudget
		if err = tx.Where("token_id = ? AND user_id = ?", record.TokenID, record.UserID).First(&budget).Error; err != nil {
			return err
		}
		budget.RemainQuota, err = cnyBalanceAfter(budget.RemainQuota, tokenDelta)
		if err != nil {
			return err
		}
		budget.UsedQuota, err = cnyBalanceAfter(budget.UsedQuota, usedDelta)
		if err != nil {
			return err
		}
		budget.Version++
		if err = tx.Save(&budget).Error; err != nil {
			return err
		}
	}
	if refund {
		record.Status, record.Reason = CNYBillingRefunded, reason
	} else {
		record.Status, record.Quota, record.TokenQuota = CNYBillingSettled, actualQuota, tokenCharge
		record.PromptTokens, record.CompletionTokens, record.UseTime, record.IsStream = usage.PromptTokens, usage.CompletionTokens, usage.UseTime, usage.IsStream
		record.Other = usage.Other
		if usage.Snapshot != "" {
			record.Snapshot = usage.Snapshot
		}
	}
	record.UpdatedAt = common.GetTimestamp()
	if record.SettledAt == 0 {
		record.SettledAt = record.UpdatedAt
	}
	return tx.Save(record).Error
}
