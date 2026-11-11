package model

import (
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	ErrWalletFundingInsufficient = errors.New("Wallet funds are insufficient")
	ErrWalletTokenInsufficient   = errors.New("API key quota is insufficient")
)

// WalletFundingRecord records money movements, independently of the model's
// native consumption log. Native and Alternate refer to the pricing currency
// and the other wallet, respectively. No balance is created by conversion.
type WalletFundingRecord struct {
	ID                      int64                  `json:"id" gorm:"primaryKey"`
	RequestID               string                 `json:"request_id" gorm:"type:varchar(191);uniqueIndex;not null"`
	UserID                  int                    `json:"user_id" gorm:"index;not null"`
	TokenID                 int                    `json:"-" gorm:"index"`
	Currency                string                 `json:"currency" gorm:"type:varchar(3);not null"`
	USDQuotaPerUnit         float64                `json:"usd_quota_per_unit"`
	CNYQuotaPerUnit         int                    `json:"cny_quota_per_unit"`
	Exchange                WalletExchangeSnapshot `json:"exchange" gorm:"embedded;embeddedPrefix:fx_"`
	Status                  string                 `json:"status" gorm:"type:varchar(16);index;not null"`
	UpstreamPending         bool                   `json:"upstream_pending"`
	TaskID                  string                 `json:"task_id,omitempty" gorm:"type:varchar(191);index"`
	ReservedQuota           int                    `json:"reserved_quota" gorm:"type:bigint"`
	NativeReserved          int                    `json:"native_reserved" gorm:"type:bigint"`
	ReservedDebtQuota       int                    `json:"reserved_debt_quota" gorm:"type:bigint"`
	AlternateNativeReserved int                    `json:"alternate_native_reserved" gorm:"type:bigint"`
	AlternateReserved       int                    `json:"alternate_reserved" gorm:"type:bigint"`
	Quota                   int                    `json:"quota" gorm:"type:bigint"`
	NativeCharged           int                    `json:"native_charged" gorm:"type:bigint"`
	AlternateCharged        int                    `json:"alternate_charged" gorm:"type:bigint"`
	DebtQuota               int                    `json:"debt_quota" gorm:"type:bigint"`
	TokenLimited            bool                   `json:"-"`
	CreatedAt               int64                  `json:"created_at"`
	UpdatedAt               int64                  `json:"updated_at"`
}

type WalletFundingParams struct {
	RequestID          string
	UserID, TokenID    int
	Currency           string
	Free               bool
	SkipInitialReserve bool
	AllowDebt          bool // Only an already admitted request's non-submit adjustment.
	ContinueStream     bool // New input on the same live stream; never a retry or debt admission.
}

// WalletFundingLog is the user-visible payment breakdown. Model consumption
// stays in its native currency; these amounts are the wallets actually used.
type WalletFundingLog struct {
	Currency        string                 `json:"currency"`
	Status          string                 `json:"status"`
	USDQuota        int                    `json:"usd_quota"`
	CNYQuota        int                    `json:"cny_quota"`
	USDQuotaPerUnit float64                `json:"usd_quota_per_unit"`
	CNYQuotaPerUnit int                    `json:"cny_quota_per_unit"`
	DebtQuota       int                    `json:"debt_quota"`
	Exchange        WalletExchangeSnapshot `json:"exchange"`
}

func (record *WalletFundingRecord) LogSummary() WalletFundingLog {
	native, alternate := record.NativeReserved, record.AlternateReserved
	debt := record.ReservedDebtQuota
	if record.Status == CNYBillingSettled || (record.Status == CNYBillingRefunded && record.Quota > 0) {
		native, alternate = record.NativeCharged, record.AlternateCharged
		debt = record.DebtQuota
	}
	if record.Status == CNYBillingRefunded {
		debt = 0
	}
	usd, cny := native, alternate
	if record.Currency == "CNY" {
		usd, cny = alternate, native
	}
	return WalletFundingLog{Currency: record.Currency, Status: record.Status, USDQuota: usd, CNYQuota: cny,
		USDQuotaPerUnit: record.USDQuotaPerUnit, CNYQuotaPerUnit: record.CNYQuotaPerUnit, DebtQuota: debt, Exchange: record.Exchange}
}

func AppendWalletFundingLog(other *LogOther, requestID string, userID int) {
	if other == nil || requestID == "" {
		return
	}
	record, err := GetWalletFundingRecord(requestID)
	if err == nil && record.UserID == userID {
		other.SetPublic("wallet_funding", record.LogSummary())
		other.SetPublic("quota_per_unit", record.USDQuotaPerUnit)
	}
}

func GetWalletFundingRecord(requestID string) (*WalletFundingRecord, error) {
	var record WalletFundingRecord
	err := DB.Where("request_id = ?", requestID).First(&record).Error
	return &record, err
}

func ReserveWalletFunding(params WalletFundingParams, target int) (*WalletFundingRecord, error) {
	var record *WalletFundingRecord
	err := DB.Transaction(func(tx *gorm.DB) error {
		wallet, err := lockCNYWallet(tx, params.UserID)
		if err != nil {
			return err
		}
		var user User
		userQuery := tx
		if params.AllowDebt {
			userQuery = tx.Unscoped()
		}
		if err = lockForUpdate(userQuery).First(&user, params.UserID).Error; err != nil {
			return err
		}
		record, err = reserveWalletFunding(tx, wallet, &user, params, target)
		return err
	})
	if err == nil {
		InvalidateUserQuotaCache(params.UserID)
		if params.Currency == "USD" && params.TokenID > 0 {
			var token Token
			if DB.Select("key").Unscoped().First(&token, params.TokenID).Error == nil {
				InvalidateTokenQuotaCache(token.Key)
			}
		}
	}
	return record, err
}

// Every caller acquires CNYWallet (including SQLite's writer lock), User, then
// Token in this order. Both wallets and the USD key budget commit together.
// CNY key budgets are updated by the native CNY caller in this transaction.
func reserveWalletFunding(tx *gorm.DB, wallet *CNYWallet, user *User, params WalletFundingParams, target int) (*WalletFundingRecord, error) {
	if !cnyOperationID.MatchString(params.RequestID) || params.UserID <= 0 || params.TokenID < 0 || target < 0 || target > common.MaxQuota || (params.Currency != "USD" && params.Currency != "CNY") {
		return nil, ErrCNYInvalid
	}
	if !params.AllowDebt && user.Status != common.UserStatusEnabled {
		return nil, ErrUserQuotaPermission
	}
	var token Token
	if params.Currency == "USD" && params.TokenID > 0 {
		tokenQuery := tx
		if params.AllowDebt {
			tokenQuery = tx.Unscoped()
		}
		if err := lockForUpdate(tokenQuery).Where("id = ? AND user_id = ?", params.TokenID, params.UserID).First(&token).Error; err != nil {
			return nil, err
		}
		if !params.AllowDebt && ((token.Status != common.TokenStatusEnabled && token.Status != common.TokenStatusExhausted) || (token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp())) {
			return nil, ErrTokenInvalid
		}
	}
	var record WalletFundingRecord
	err := tx.Where("request_id = ?", params.RequestID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if params.AllowDebt {
			return nil, ErrCNYState
		}
		if common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
			return nil, ErrCNYInvalid
		}
		snapshot, snapshotErr := GetWalletExchangeSnapshot(tx, common.GetTimestamp())
		if snapshotErr != nil && !errors.Is(snapshotErr, ErrExchangeRateUnavailable) {
			return nil, snapshotErr
		}
		// An unavailable quote must not reject a request fully funded natively.
		// The empty frozen rate also prevents later retries from adopting a new rate.
		if snapshotErr != nil {
			snapshot.Rate = ""
		}
		record = WalletFundingRecord{RequestID: params.RequestID, UserID: params.UserID, TokenID: params.TokenID,
			Currency: params.Currency, USDQuotaPerUnit: common.QuotaPerUnit, CNYQuotaPerUnit: wallet.QuotaPerUnit,
			Exchange: snapshot, Status: CNYBillingReserved, TokenLimited: params.Currency == "USD" && params.TokenID > 0 && !token.UnlimitedQuota, CreatedAt: common.GetTimestamp()}
		if params.SkipInitialReserve && !snapshot.Enabled && params.Currency == "USD" && user.Quota >= target {
			target = 0
		}
	} else if err != nil {
		return nil, err
	} else if record.UserID != params.UserID || record.TokenID != params.TokenID || record.Currency != params.Currency {
		return nil, ErrCNYConflict
	} else if record.Status != CNYBillingReserved || (record.UpstreamPending && !params.AllowDebt && !params.ContinueStream) {
		return nil, ErrCNYState
	}
	primary, alternate := user.Quota, wallet.Quota
	if record.Currency == "CNY" {
		primary, alternate = wallet.Quota, user.Quota
	}
	if params.ContinueStream && (primary < 0 || record.ReservedDebtQuota > 0) {
		return nil, ErrWalletFundingInsufficient
	}
	if !params.AllowDebt && !params.Free && record.ReservedQuota == 0 {
		if primary < 0 || (primary == 0 && (alternate <= 0 || !record.Exchange.Enabled)) {
			return nil, ErrWalletFundingInsufficient
		}
		if primary == 0 && record.Exchange.Rate == "" {
			return nil, ErrExchangeRateUnavailable
		}
		if record.TokenLimited && token.RemainQuota <= 0 {
			return nil, ErrWalletTokenInsufficient
		}
	}
	delta := max(0, target-record.ReservedQuota)
	if !params.AllowDebt && record.TokenLimited && token.RemainQuota < delta {
		return nil, ErrWalletTokenInsufficient
	}
	nativeDelta := min(max(0, primary), delta)
	alternateNative := record.AlternateNativeReserved + delta - nativeDelta
	alternateTotal, err := record.convertAlternateQuota(alternateNative)
	debt := 0
	if params.AllowDebt && record.ID > 0 && (errors.Is(err, ErrWalletFundingInsufficient) || errors.Is(err, ErrExchangeRateUnavailable) || alternateTotal-record.AlternateReserved > max(0, alternate)) {
		fundable := record.affordableAlternateNative(alternateNative, max(0, alternate)+record.AlternateReserved)
		debt = alternateNative - fundable
		alternateNative = fundable
		alternateTotal, err = record.convertAlternateQuota(alternateNative)
		nativeDelta += debt
	}
	if err != nil {
		return nil, err
	}
	alternateDelta := alternateTotal - record.AlternateReserved
	if alternateDelta > max(0, alternate) {
		return nil, ErrWalletFundingInsufficient
	}
	if err = moveWalletFunds(tx, wallet, user, record.Currency, -nativeDelta, -alternateDelta, true); err != nil {
		return nil, err
	}
	if record.Currency == "USD" && record.TokenID > 0 && delta != 0 {
		if err = updateWalletTokenQuota(tx, record.TokenID, delta); err != nil {
			return nil, err
		}
	}
	record.ReservedQuota += delta
	record.NativeReserved += nativeDelta
	record.ReservedDebtQuota += debt
	record.AlternateNativeReserved, record.AlternateReserved = alternateNative, alternateTotal
	record.UpdatedAt = common.GetTimestamp()
	if err = tx.Save(&record).Error; err != nil {
		return nil, err
	}
	return &record, nil
}

func (record *WalletFundingRecord) affordableAlternateNative(maxNative, alternateAvailable int) int {
	low, high := 0, maxNative
	for low < high {
		mid := low + (high-low+1)/2
		converted, err := record.convertAlternateQuota(mid)
		if err == nil && converted <= alternateAvailable {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return low
}

// Persist the submission boundary before an upstream call. A process restart
// must never turn an uncertain paid request into an automatic refund/retry.
func MarkWalletFundingPending(requestID string, pending bool) error {
	result := DB.Model(&WalletFundingRecord{}).Where("request_id = ? AND status = ?", requestID, CNYBillingReserved).
		Updates(map[string]any{"upstream_pending": pending, "updated_at": common.GetTimestamp()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var record WalletFundingRecord
		if err := DB.Where("request_id = ?", requestID).First(&record).Error; err != nil {
			return err
		}
		if record.Status != CNYBillingReserved || record.UpstreamPending != pending {
			return ErrCNYState
		}
	}
	return nil
}

// Conversion is performed on the cumulative amount using the request's
// frozen scale and rate, rather than rounding each incremental reservation.
func (record *WalletFundingRecord) convertAlternateQuota(nativeQuota int) (int, error) {
	if nativeQuota == 0 {
		return 0, nil
	}
	if nativeQuota < 0 || !record.Exchange.Enabled {
		return 0, ErrWalletFundingInsufficient
	}
	if record.Exchange.Rate == "" {
		return 0, ErrExchangeRateUnavailable
	}
	rate, err := decimal.NewFromString(record.Exchange.Rate)
	if err != nil || !rate.IsPositive() || record.USDQuotaPerUnit <= 0 || record.CNYQuotaPerUnit <= 0 {
		return 0, ErrExchangeRateUnavailable
	}
	value := decimal.NewFromInt(int64(nativeQuota))
	usdScale, cnyScale := decimal.NewFromFloat(record.USDQuotaPerUnit), decimal.NewFromInt(int64(record.CNYQuotaPerUnit))
	if record.Currency == "USD" {
		value = value.Div(usdScale).Mul(rate).Mul(cnyScale)
	} else {
		value = value.Div(cnyScale).Div(rate).Mul(usdScale)
	}
	quota, err := common.WalletQuotaFromDecimalStrict(value.Round(0))
	if err != nil {
		return 0, err
	}
	return max(1, quota), nil
}

func moveWalletFunds(tx *gorm.DB, wallet *CNYWallet, user *User, currency string, nativeDelta, alternateDelta int, reserve bool) error {
	usdDelta, cnyDelta := nativeDelta, alternateDelta
	if currency == "CNY" {
		usdDelta, cnyDelta = alternateDelta, nativeDelta
	}
	var err error
	user.Quota, err = cnyBalanceAfter(user.Quota, usdDelta)
	if err != nil {
		return err
	}
	wallet.Quota, err = cnyBalanceAfter(wallet.Quota, cnyDelta)
	if err != nil {
		return err
	}
	if reserve {
		wallet.ReservedQuota, err = cnyBalanceAfter(wallet.ReservedQuota, -cnyDelta)
		if err != nil {
			return err
		}
	}
	if usdDelta != 0 {
		if err = tx.Unscoped().Model(&User{}).Where("id = ?", user.Id).UpdateColumn("quota", user.Quota).Error; err != nil {
			return err
		}
	}
	wallet.Version++
	return tx.Save(wallet).Error
}

// A deleted or subsequently disabled key cannot block returning money. This
// changes only financial fields and never restores a credential's authority.
func updateWalletTokenQuota(tx *gorm.DB, tokenID, delta int) error {
	return tx.Unscoped().Model(&Token{}).Where("id = ?", tokenID).Updates(map[string]any{
		"remain_quota": gorm.Expr("remain_quota - ?", delta), "used_quota": gorm.Expr("used_quota + ?", delta), "accessed_time": common.GetTimestamp(),
	}).Error
}

func SettleWalletFunding(requestID string, actual int, refund bool) (*WalletFundingRecord, error) {
	record, err := GetWalletFundingRecord(requestID)
	if err != nil {
		return nil, err
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		wallet, err := lockCNYWallet(tx, record.UserID)
		if err != nil {
			return err
		}
		var user User
		if err = lockForUpdate(tx.Unscoped()).First(&user, record.UserID).Error; err != nil {
			return err
		}
		if err = tx.First(record, record.ID).Error; err != nil {
			return err
		}
		if record.TaskID != "" {
			return ErrCNYState
		}
		return finalizeWalletFunding(tx, wallet, &user, record, actual, refund)
	})
	if err == nil {
		InvalidateUserQuotaCache(record.UserID)
		if record.Currency == "USD" && record.TokenID > 0 {
			var token Token
			if DB.Select("key").Unscoped().First(&token, record.TokenID).Error == nil {
				InvalidateTokenQuotaCache(token.Key)
			}
		}
	}
	return record, err
}

func finalizeWalletFunding(tx *gorm.DB, wallet *CNYWallet, user *User, record *WalletFundingRecord, actual int, refund bool) error {
	if actual < 0 || actual > common.MaxQuota {
		return ErrCNYInvalid
	}
	if record.Status == CNYBillingRefunded {
		if refund {
			return nil
		}
		return ErrCNYState
	}
	if record.Status == CNYBillingSettled && !refund {
		if record.Quota == actual {
			return nil
		}
		return ErrCNYConflict
	}
	if record.Status != CNYBillingReserved && record.Status != CNYBillingSubmitted && record.Status != CNYBillingSettled {
		return ErrCNYState
	}
	previousNative, previousAlternate, previousQuota := record.NativeReserved, record.AlternateReserved, record.ReservedQuota
	if record.Status == CNYBillingSettled {
		previousNative, previousAlternate, previousQuota = record.NativeCharged, record.AlternateCharged, record.Quota
	}
	nativeCharge, alternateCharge, debt := 0, 0, 0
	if !refund {
		nativeCharge = min(actual, record.NativeReserved-record.ReservedDebtQuota)
		alternateNative := min(max(0, actual-nativeCharge), record.AlternateNativeReserved)
		remaining := actual - nativeCharge - alternateNative
		if remaining > 0 {
			primary, alternate := user.Quota, wallet.Quota
			if record.Currency == "CNY" {
				primary, alternate = wallet.Quota, user.Quota
			}
			primary += record.ReservedDebtQuota
			extra := min(remaining, max(0, primary))
			nativeCharge += extra
			remaining -= extra
			if remaining > 0 && record.Exchange.Enabled && record.Exchange.Rate != "" && alternate > 0 {
				// Find the largest native amount covered by actual available
				// alternate funds, including the existing held amount. Integer
				// search uses the same cumulative rounding as reservation.
				fundable := record.affordableAlternateNative(alternateNative+remaining, alternate+record.AlternateReserved)
				remaining -= fundable - alternateNative
				alternateNative = fundable
			}
			debt = remaining
			nativeCharge += debt
		}
		var err error
		alternateCharge, err = record.convertAlternateQuota(alternateNative)
		if err != nil {
			return err
		}
	}
	if err := moveWalletFunds(tx, wallet, user, record.Currency, previousNative-nativeCharge, previousAlternate-alternateCharge, false); err != nil {
		return err
	}
	if record.Status != CNYBillingSettled {
		cnyHeld := record.AlternateReserved
		if record.Currency == "CNY" {
			cnyHeld = record.NativeReserved
		}
		var err error
		wallet.ReservedQuota, err = cnyBalanceAfter(wallet.ReservedQuota, -cnyHeld)
		if err != nil {
			return err
		}
		if err = tx.Save(wallet).Error; err != nil {
			return err
		}
	}
	finalQuota := actual
	if refund {
		finalQuota = 0
	}
	if record.Currency == "USD" && record.TokenID > 0 && finalQuota != previousQuota {
		if err := updateWalletTokenQuota(tx, record.TokenID, finalQuota-previousQuota); err != nil {
			return err
		}
	}
	if refund {
		record.Status = CNYBillingRefunded
	} else {
		record.Status = CNYBillingSettled
		record.Quota, record.NativeCharged, record.AlternateCharged, record.DebtQuota = actual, nativeCharge, alternateCharge, debt
	}
	record.UpdatedAt = common.GetTimestamp()
	record.UpstreamPending = false
	return tx.Save(record).Error
}
