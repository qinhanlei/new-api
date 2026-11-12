package model

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestWalletFundingDatabaseMatrix(t *testing.T) {
	for _, dialect := range []common.DatabaseType{common.DatabaseTypeSQLite, common.DatabaseTypeMySQL, common.DatabaseTypePostgreSQL} {
		t.Run(string(dialect), func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case common.DatabaseTypeSQLite:
				driver = sqlite.Open(filepath.Join(t.TempDir(), "funding.db") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
			case common.DatabaseTypeMySQL:
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case common.DatabaseTypePostgreSQL:
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "wallet_funding_matrix_"}})
			require.NoError(t, err)
			previousDB, previousType, previousScale := DB, common.MainDatabaseType(), common.QuotaPerUnit
			DB = db
			common.SetMainDatabaseType(dialect)
			common.QuotaPerUnit = 500000
			t.Cleanup(func() {
				DB, common.QuotaPerUnit = previousDB, previousScale
				common.SetMainDatabaseType(previousType)
				sqlDB, _ := db.DB()
				_ = sqlDB.Close()
			})
			tables := []any{&WalletFundingRecord{}, &WalletExchangePolicy{}, &ExchangeRateObservation{}, &CNYWallet{}, &CNYTokenBudget{}, &CNYBillingRecord{}, &User{}, &Token{}, &Task{}}
			require.NoError(t, db.Migrator().DropTable(tables...))
			for range 2 {
				require.NoError(t, db.AutoMigrate(tables...))
			}
			policy := DefaultWalletExchangePolicy()
			policy.StaticRate, policy.CrossCurrencyEnabled = "6.7", true
			_, err = SaveWalletExchangePolicy(policy, nil)
			require.NoError(t, err)
			t.Run("USD partial funding frozen refund and deleted key", testWalletFundingUSD)
			t.Run("CNY native cost and USD funding", testWalletFundingCNY)
			t.Run("settlement debt and repeated finalization", testWalletFundingDebt)
			t.Run("concurrent wallets and duplicate reservations", testWalletFundingConcurrent)
			t.Run("deleted user settlement", testWalletFundingDeletedUser)
			t.Run("stale exchange and stream admission", testWalletFundingAdmission)
			t.Run("confirmed usage exceeds key budget", testWalletFundingIncurredUsage)
			t.Run("CNY key fallback allocation", testWalletFundingCNYTokenFallback)
			t.Run("CNY key fallback admission", testWalletFundingCNYFallbackAdmission)
			t.Run("CNY key fallback frozen settlement", testWalletFundingCNYFallbackFrozen)
			t.Run("CNY key fallback tasks", testWalletFundingCNYFallbackTasks)
			t.Run("CNY key fallback concurrent budget", testWalletFundingCNYFallbackConcurrent)
			t.Run("CNY key fallback quota bounds", testWalletFundingCNYFallbackBounds)
			for range 2 {
				require.NoError(t, db.AutoMigrate(tables...))
			}
			var records int64
			require.NoError(t, db.Model(&WalletFundingRecord{}).Count(&records).Error)
			assert.Positive(t, records)
		})
	}
}

func testWalletFundingIncurredUsage(t *testing.T) {
	user, token := seedWalletFunding(t, "incurred", 500, 0, 100)
	params := WalletFundingParams{RequestID: "incurred", UserID: user.Id, TokenID: token.Id, Currency: "USD"}
	_, err := ReserveWalletFunding(params, 50)
	require.NoError(t, err)
	require.NoError(t, MarkWalletFundingPending(params.RequestID, true))
	params.AllowDebt = true
	record, err := ReserveWalletFunding(params, 200)
	require.NoError(t, err, "authoritative usage must be charged even after a key budget is exhausted")
	assert.Equal(t, 200, record.ReservedQuota)
	require.NoError(t, DB.First(&token, token.Id).Error)
	assert.Equal(t, -100, token.RemainQuota)
	assert.Equal(t, 200, token.UsedQuota)
	params.AllowDebt, params.ContinueStream = false, true
	_, err = ReserveWalletFunding(params, 201)
	assert.ErrorIs(t, err, ErrWalletTokenInsufficient)
	require.NoError(t, DB.Delete(&token).Error)
	require.NoError(t, DB.Delete(&user).Error)
	params.AllowDebt, params.ContinueStream = true, false
	_, err = ReserveWalletFunding(params, 250)
	require.NoError(t, err, "deleting an account cannot erase usage already incurred")
	record, err = SettleWalletFunding(params.RequestID, 250, false)
	require.NoError(t, err)
	assert.Equal(t, CNYBillingSettled, record.Status)
	params.RequestID = "new-debt-not-allowed"
	_, err = ReserveWalletFunding(params, 1)
	assert.ErrorIs(t, err, ErrCNYState)
}

func testWalletFundingAdmission(t *testing.T) {
	user, token := seedWalletFunding(t, "admission", 1000, 67000, 20000)
	policy, err := GetWalletExchangePolicy()
	require.NoError(t, err)
	policy.DynamicEnabled = true
	policy, err = SaveWalletExchangePolicy(*policy, &ExchangeRateObservation{Rate: "6.7", Source: "frankfurter", SourceTime: time.Now().Unix()})
	require.NoError(t, err)
	params := WalletFundingParams{RequestID: "admission", UserID: user.Id, TokenID: token.Id, Currency: "USD"}
	_, err = ReserveWalletFunding(params, 2000)
	require.NoError(t, err)
	require.NoError(t, MarkWalletFundingPending(params.RequestID, true))
	_, err = ReserveWalletFunding(params, 2000)
	assert.ErrorIs(t, err, ErrCNYState, "an unknown submission must not be retried even at the same quota")
	params.ContinueStream = true
	record, err := ReserveWalletFunding(params, 3000)
	require.NoError(t, err)
	assert.True(t, record.UpstreamPending)
	assert.Zero(t, record.ReservedDebtQuota)
	_, err = ReserveWalletFunding(params, 12000)
	assert.ErrorIs(t, err, ErrWalletFundingInsufficient)
	record, err = GetWalletFundingRecord(params.RequestID)
	require.NoError(t, err)
	assert.Equal(t, 3000, record.ReservedQuota)
	assert.Zero(t, record.ReservedDebtQuota)

	require.NoError(t, DB.Model(&ExchangeRateObservation{}).Where("id = ?", policy.ActiveRateID).Update("source_time", time.Now().Unix()-8*86400).Error)
	native, _ := seedWalletFunding(t, "native-stale", 1000, 67000, 20000)
	_, err = ReserveWalletFunding(WalletFundingParams{RequestID: "native-stale", UserID: native.Id, Currency: "USD"}, 500)
	require.NoError(t, err, "stale FX does not block sufficient native funds")
	_, err = ReserveWalletFunding(WalletFundingParams{RequestID: "cross-stale", UserID: native.Id, Currency: "USD"}, 1000)
	assert.ErrorIs(t, err, ErrExchangeRateUnavailable)
	record, err = SettleWalletFunding(params.RequestID, 2500, false)
	require.NoError(t, err, "an admitted request settles with its frozen quote after the source expires")
	assert.Equal(t, "6.7", record.Exchange.Rate)
	assert.Equal(t, 10050, record.AlternateCharged)
	policy.DynamicEnabled = false
	_, err = SaveWalletExchangePolicy(*policy, nil)
	require.NoError(t, err)
}

func testWalletFundingDeletedUser(t *testing.T) {
	for _, currency := range []string{"USD", "CNY"} {
		t.Run(currency, func(t *testing.T) {
			user, token := seedWalletFunding(t, "deleted-user-"+currency, 10000, 10000, 10000)
			requestID := "deleted-user-" + currency
			if currency == "CNY" {
				_, err := UpdateCNYTokenBudget(token.Id, user.Id, CNYTokenBudgetChange{Enabled: true, RemainQuota: 10000})
				require.NoError(t, err)
				_, err = ReserveCNYBilling(CNYBillingParams{RequestID: requestID, UserID: user.Id, TokenID: token.Id}, 1000)
				require.NoError(t, err)
			} else {
				_, err := ReserveWalletFunding(WalletFundingParams{RequestID: requestID, UserID: user.Id, TokenID: token.Id, Currency: currency}, 1000)
				require.NoError(t, err)
			}
			require.NoError(t, DB.Delete(&user).Error)
			if currency == "CNY" {
				_, err := SettleCNYBilling(requestID, 500, CNYBillingUsage{})
				require.NoError(t, err)
				_, err = RefundCNYBilling(requestID, "confirmed refund")
				require.NoError(t, err)
			} else {
				_, err := SettleWalletFunding(requestID, 500, false)
				require.NoError(t, err)
				_, err = SettleWalletFunding(requestID, 0, true)
				require.NoError(t, err)
			}
			require.NoError(t, DB.Unscoped().First(&user, user.Id).Error)
			assert.True(t, user.DeletedAt.Valid)
			assert.Equal(t, 10000, user.Quota)
			var wallet CNYWallet
			require.NoError(t, DB.First(&wallet, "user_id = ?", user.Id).Error)
			assert.Equal(t, 10000, wallet.Quota)
			assert.Zero(t, wallet.ReservedQuota)
			_, err := ReserveWalletFunding(WalletFundingParams{RequestID: requestID + "-new", UserID: user.Id, TokenID: token.Id, Currency: currency}, 1)
			assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
		})
	}
}

func seedWalletFunding(t *testing.T, name string, usd, cny, keyQuota int) (User, Token) {
	t.Helper()
	user := User{Username: name, AffCode: name, Status: common.UserStatusEnabled, Quota: usd}
	require.NoError(t, DB.Create(&user).Error)
	token := Token{UserId: user.Id, Key: name, Name: name, Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: keyQuota}
	require.NoError(t, DB.Create(&token).Error)
	require.NoError(t, DB.Create(&CNYWallet{UserID: user.Id, Currency: "CNY", QuotaPerUnit: CNYQuotaPerUnit, Quota: cny}).Error)
	return user, token
}

func testWalletFundingUSD(t *testing.T) {
	user, token := seedWalletFunding(t, "usd-split", 100000, 3350000, 2000000)
	params := WalletFundingParams{RequestID: "usd-split", UserID: user.Id, TokenID: token.Id, Currency: "USD"}
	record, err := ReserveWalletFunding(params, 500000)
	require.NoError(t, err)
	assert.Equal(t, 100000, record.NativeReserved)
	assert.Equal(t, 2680000, record.AlternateReserved)
	policy, err := GetWalletExchangePolicy()
	require.NoError(t, err)
	policy.StaticRate, policy.CrossCurrencyEnabled = "2", false
	_, err = SaveWalletExchangePolicy(*policy, nil)
	require.NoError(t, err)
	// A new policy and global scale do not alter the existing request.
	common.QuotaPerUnit = 1000000
	record, err = ReserveWalletFunding(params, 550000)
	require.NoError(t, err)
	assert.Equal(t, 3015000, record.AlternateReserved)
	require.NoError(t, DB.Delete(&token).Error)
	record, err = SettleWalletFunding(params.RequestID, 300000, false)
	require.NoError(t, err)
	assert.Equal(t, 100000, record.NativeCharged)
	assert.Equal(t, 1340000, record.AlternateCharged)
	wallet, err := GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 2010000, wallet.Quota)
	assert.Zero(t, wallet.ReservedQuota)
	assert.Zero(t, wallet.UsedQuota, "a USD model must not inflate CNY model consumption")
	_, err = SettleWalletFunding(params.RequestID, 300000, false)
	require.NoError(t, err)
	_, err = SettleWalletFunding(params.RequestID, 0, true)
	require.NoError(t, err)
	_, err = SettleWalletFunding(params.RequestID, 0, true)
	require.NoError(t, err)
	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Equal(t, 100000, user.Quota)
	wallet, err = GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 3350000, wallet.Quota)
	require.NoError(t, DB.Unscoped().First(&token, token.Id).Error)
	assert.Equal(t, 2000000, token.RemainQuota)
	assert.True(t, token.DeletedAt.Valid)
	common.QuotaPerUnit = 500000
	policy, err = GetWalletExchangePolicy()
	require.NoError(t, err)
	policy.StaticRate, policy.CrossCurrencyEnabled = "6.7", true
	_, err = SaveWalletExchangePolicy(*policy, nil)
	require.NoError(t, err)
}

func testWalletFundingCNY(t *testing.T) {
	user, token := seedWalletFunding(t, "cny-split", 500000, 100000, 2000000)
	params := WalletFundingParams{RequestID: "cny-split", UserID: user.Id, TokenID: token.Id, Currency: "CNY"}
	record, err := ReserveWalletFunding(params, 3450000)
	require.NoError(t, err)
	assert.Equal(t, 100000, record.NativeReserved)
	assert.Equal(t, 500000, record.AlternateReserved)
	record, err = SettleWalletFunding(params.RequestID, 1775000, false)
	require.NoError(t, err)
	assert.Equal(t, 250000, record.AlternateCharged)
	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Equal(t, 250000, user.Quota)
	require.NoError(t, DB.First(&token, token.Id).Error)
	assert.Equal(t, 2000000, token.RemainQuota, "CNY spending does not debit the USD key budget")
	// New requests with unavailable FX still admit fully native funding.
	policy, err := GetWalletExchangePolicy()
	require.NoError(t, err)
	policy.CrossCurrencyEnabled = false
	_, err = SaveWalletExchangePolicy(*policy, nil)
	require.NoError(t, err)
	_, err = ReserveWalletFunding(WalletFundingParams{RequestID: "disabled-cross", UserID: user.Id, Currency: "CNY"}, 1)
	assert.ErrorIs(t, err, ErrWalletFundingInsufficient)
	_, err = ReserveWalletFunding(WalletFundingParams{RequestID: "native-only", UserID: user.Id, Currency: "USD"}, 100)
	require.NoError(t, err)
	policy, err = GetWalletExchangePolicy()
	require.NoError(t, err)
	policy.CrossCurrencyEnabled = true
	_, err = SaveWalletExchangePolicy(*policy, nil)
	require.NoError(t, err)
}

func testWalletFundingDebt(t *testing.T) {
	user, token := seedWalletFunding(t, "debt", 100000, 670000, 2000000)
	params := WalletFundingParams{RequestID: "debt", UserID: user.Id, TokenID: token.Id, Currency: "USD"}
	_, err := ReserveWalletFunding(params, 150000)
	require.NoError(t, err)
	record, err := SettleWalletFunding(params.RequestID, 300000, false)
	require.NoError(t, err)
	assert.Equal(t, 670000, record.AlternateCharged)
	assert.Equal(t, 100000, record.DebtQuota)
	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Equal(t, -100000, user.Quota)
	require.NoError(t, DB.First(&token, token.Id).Error)
	assert.Equal(t, 300000, token.UsedQuota)
	_, err = SettleWalletFunding(params.RequestID, 300000, false)
	require.NoError(t, err)
	_, err = SettleWalletFunding(params.RequestID, 300001, false)
	assert.ErrorIs(t, err, ErrCNYConflict)
	params.RequestID = "debt-reject-next"
	_, err = ReserveWalletFunding(params, 1)
	assert.ErrorIs(t, err, ErrWalletFundingInsufficient)
}

func testWalletFundingConcurrent(t *testing.T) {
	user, token := seedWalletFunding(t, "concurrent", 100000, 670000, 1000000)
	start := make(chan struct{})
	errors := make(chan error, 3)
	var workers sync.WaitGroup
	for i := range 3 {
		workers.Go(func() {
			<-start
			_, err := ReserveWalletFunding(WalletFundingParams{RequestID: fmt.Sprintf("compete-%d", i), UserID: user.Id, TokenID: token.Id, Currency: "USD"}, 100000)
			errors <- err
		})
	}
	close(start)
	workers.Wait()
	close(errors)
	succeeded, rejected := 0, 0
	for err := range errors {
		if err == nil {
			succeeded++
		} else {
			require.ErrorIs(t, err, ErrWalletFundingInsufficient)
			rejected++
		}
	}
	assert.Equal(t, 2, succeeded)
	assert.Equal(t, 1, rejected)
	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Zero(t, user.Quota)
	wallet, err := GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Zero(t, wallet.Quota)
	assert.Equal(t, 670000, wallet.ReservedQuota)
	require.NoError(t, DB.First(&token, token.Id).Error)
	assert.Equal(t, 800000, token.RemainQuota)
}

func testWalletFundingCNYTokenFallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		budget    *CNYTokenBudgetChange
		unlimited bool
	}{
		{name: "missing"},
		{name: "disabled", budget: &CNYTokenBudgetChange{RemainQuota: 1000}},
		{name: "insufficient", budget: &CNYTokenBudgetChange{Enabled: true, RemainQuota: 100}},
		{name: "unlimited-usd", unlimited: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "cny-fallback-" + tc.name
			user, token := seedWalletFunding(t, name, 1000, 10000, 1000)
			if tc.unlimited {
				require.NoError(t, DB.Model(&token).Updates(map[string]any{"unlimited_quota": true, "remain_quota": 0}).Error)
			}
			var initialBudget *CNYTokenBudget
			if tc.budget != nil {
				var err error
				initialBudget, err = UpdateCNYTokenBudget(token.Id, user.Id, *tc.budget)
				require.NoError(t, err)
			}
			server := useUserCacheMiniRedis(t)
			require.NoError(t, cacheSetTokenForTest(token))
			params := CNYBillingParams{RequestID: name, UserID: user.Id, TokenID: token.Id}
			_, err := ReserveCNYBilling(params, 670)
			require.NoError(t, err)
			assert.False(t, server.Exists(getTokenCacheKey(token.Key)))
			// Each cumulative conversion is rounded once, including small increases.
			_, err = ReserveCNYBilling(params, 671)
			require.NoError(t, err)
			funding, err := GetWalletFundingRecord(name)
			require.NoError(t, err)
			assert.Equal(t, "USD", funding.TokenCurrency)
			assert.Equal(t, !tc.unlimited, funding.TokenLimited)
			assert.Zero(t, funding.NativeReserved)
			assert.Equal(t, 100, funding.AlternateReserved)
			require.NoError(t, DB.First(&user, user.Id).Error)
			assert.Equal(t, 900, user.Quota)
			require.NoError(t, cacheSetTokenForTest(token))
			_, err = SettleCNYBilling(name, 335, CNYBillingUsage{})
			require.NoError(t, err)
			assert.False(t, server.Exists(getTokenCacheKey(token.Key)))
			require.NoError(t, DB.First(&token, token.Id).Error)
			assert.Equal(t, 50, token.UsedQuota)
			require.NoError(t, cacheSetTokenForTest(token))
			_, err = RefundCNYBilling(name, "confirmed refund")
			require.NoError(t, err)
			assert.False(t, server.Exists(getTokenCacheKey(token.Key)))
			_, err = RefundCNYBilling(name, "replayed refund")
			require.NoError(t, err)
			require.NoError(t, DB.First(&user, user.Id).Error)
			assert.Equal(t, 1000, user.Quota)
			require.NoError(t, DB.First(&token, token.Id).Error)
			assert.Zero(t, token.UsedQuota)
			if tc.unlimited {
				assert.Zero(t, token.RemainQuota)
			} else {
				assert.Equal(t, 1000, token.RemainQuota)
			}
			wallet, err := GetCNYWallet(user.Id)
			require.NoError(t, err)
			assert.Equal(t, 10000, wallet.Quota)
			assert.Zero(t, wallet.ReservedQuota)
			assert.Zero(t, wallet.UsedQuota)
			var budget CNYTokenBudget
			err = DB.Where("token_id = ?", token.Id).First(&budget).Error
			if initialBudget == nil {
				assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
			} else {
				require.NoError(t, err)
				assert.Equal(t, *initialBudget, budget)
			}
		})
	}
}

func testWalletFundingCNYFallbackAdmission(t *testing.T) {
	for _, tc := range []struct {
		name            string
		usd, key, quota int
		cross, stale    bool
		want            error
	}{
		{"disabled-cross", 1000, 1000, 670, false, false, ErrCNYTokenQuota},
		{"short-usd-key", 1000, 99, 670, true, false, ErrWalletTokenInsufficient},
		{"short-usd-wallet", 99, 1000, 670, true, false, ErrCNYQuota},
		{"zero-key", 1000, 0, 0, true, false, ErrWalletTokenInsufficient},
		{"zero-wallet", 0, 1000, 0, true, false, ErrCNYQuota},
		{"zero-funded", 1000, 1000, 0, true, false, nil},
		{"zero-stale-rate", 1000, 1000, 0, true, true, ErrExchangeRateUnavailable},
		{"stale-rate", 1000, 1000, 670, true, true, ErrExchangeRateUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "fallback-admit-" + tc.name
			user, token := seedWalletFunding(t, name, tc.usd, 10000, tc.key)
			policy, err := GetWalletExchangePolicy()
			require.NoError(t, err)
			policy.CrossCurrencyEnabled, policy.DynamicEnabled, policy.StaticRate = tc.cross, tc.stale, "6.7"
			var observation *ExchangeRateObservation
			if tc.stale {
				observation = &ExchangeRateObservation{Rate: "6.7", Source: "frankfurter", SourceTime: common.GetTimestamp()}
			}
			policy, err = SaveWalletExchangePolicy(*policy, observation)
			require.NoError(t, err)
			if tc.stale {
				require.NoError(t, DB.Model(&ExchangeRateObservation{}).Where("id = ?", policy.ActiveRateID).UpdateColumn("source_time", common.GetTimestamp()-8*86400).Error)
			}
			_, err = ReserveCNYBilling(CNYBillingParams{RequestID: name, UserID: user.Id, TokenID: token.Id}, tc.quota)
			if tc.want == nil {
				require.NoError(t, err)
				_, err = SettleCNYBilling(name, 670, CNYBillingUsage{})
				require.NoError(t, err)
				require.NoError(t, DB.First(&user, user.Id).Error)
				assert.Equal(t, 900, user.Quota)
				require.NoError(t, DB.First(&token, token.Id).Error)
				assert.Equal(t, 900, token.RemainQuota)
				return
			}
			assert.ErrorIs(t, err, tc.want)
			_, err = GetWalletFundingRecord(name)
			assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
			_, err = GetCNYBillingRecord(name)
			assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
			require.NoError(t, DB.First(&user, user.Id).Error)
			assert.Equal(t, tc.usd, user.Quota)
			require.NoError(t, DB.First(&token, token.Id).Error)
			assert.Equal(t, tc.key, token.RemainQuota)
			assert.Zero(t, token.UsedQuota)
			wallet, err := GetCNYWallet(user.Id)
			require.NoError(t, err)
			assert.Equal(t, 10000, wallet.Quota)
			assert.Zero(t, wallet.ReservedQuota)
		})
	}
	policy, err := GetWalletExchangePolicy()
	require.NoError(t, err)
	policy.CrossCurrencyEnabled, policy.DynamicEnabled = true, false
	_, err = SaveWalletExchangePolicy(*policy, nil)
	require.NoError(t, err)
}

func testWalletFundingCNYFallbackFrozen(t *testing.T) {
	user, token := seedWalletFunding(t, "fallback-frozen", 1000, 10000, 200)
	params := CNYBillingParams{RequestID: "fallback-frozen", UserID: user.Id, TokenID: token.Id}
	_, err := ReserveCNYBilling(params, 670)
	require.NoError(t, err)
	_, err = UpdateCNYTokenBudget(token.Id, user.Id, CNYTokenBudgetChange{Enabled: true, UnlimitedQuota: true})
	require.NoError(t, err)
	require.NoError(t, DB.Model(&token).UpdateColumn("unlimited_quota", true).Error)
	policy, err := GetWalletExchangePolicy()
	require.NoError(t, err)
	policy.CrossCurrencyEnabled, policy.StaticRate = false, "2"
	_, err = SaveWalletExchangePolicy(*policy, nil)
	require.NoError(t, err)
	common.QuotaPerUnit = 1000000
	_, err = ReserveCNYBilling(params, 1340)
	require.NoError(t, err)
	_, err = ReserveCNYBilling(params, 1347)
	assert.ErrorIs(t, err, ErrWalletTokenInsufficient, "neither a new CNY budget nor mutable unlimited mode replaces the selected USD budget")
	require.NoError(t, DB.Delete(&token).Error)
	_, err = SettleCNYBilling(params.RequestID, 8040, CNYBillingUsage{})
	require.NoError(t, err)
	funding, err := GetWalletFundingRecord(params.RequestID)
	require.NoError(t, err)
	assert.Equal(t, 1200, funding.AlternateCharged)
	assert.Zero(t, funding.NativeCharged)
	assert.Equal(t, 200, funding.DebtQuota)
	assert.Equal(t, "USD", funding.LogSummary().DebtCurrency)
	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Equal(t, -200, user.Quota)
	assert.Zero(t, user.UsedQuota, "model consumption stays in CNY")
	require.NoError(t, DB.Unscoped().First(&token, token.Id).Error)
	assert.Equal(t, -1000, token.RemainQuota)
	assert.Equal(t, 1200, token.UsedQuota)
	_, err = SettleCNYBilling(params.RequestID, 8040, CNYBillingUsage{})
	require.NoError(t, err)
	_, err = RefundCNYBilling(params.RequestID, "deleted key refund")
	require.NoError(t, err)
	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Equal(t, 1000, user.Quota)
	require.NoError(t, DB.Unscoped().First(&token, token.Id).Error)
	assert.Equal(t, 200, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.True(t, token.DeletedAt.Valid)
	var budget CNYTokenBudget
	require.NoError(t, DB.Where("token_id = ?", token.Id).First(&budget).Error)
	assert.Zero(t, budget.UsedQuota)
	common.QuotaPerUnit = 500000
	policy, err = GetWalletExchangePolicy()
	require.NoError(t, err)
	policy.CrossCurrencyEnabled, policy.StaticRate = true, "6.7"
	_, err = SaveWalletExchangePolicy(*policy, nil)
	require.NoError(t, err)

	// Old native-budget records must not switch to the new fallback on retry.
	user, token, _ = newCNYAccount(t, "fallback-legacy", 10000, 1000, false)
	params = CNYBillingParams{RequestID: "fallback-legacy", UserID: user.Id, TokenID: token.Id}
	_, err = ReserveCNYBilling(params, 670)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&WalletFundingRecord{}).Where("request_id = ?", params.RequestID).UpdateColumn("token_currency", "").Error)
	_, err = ReserveCNYBilling(params, 1340)
	assert.ErrorIs(t, err, ErrCNYTokenQuota)
	_, err = SettleCNYBilling(params.RequestID, 335, CNYBillingUsage{})
	require.NoError(t, err)
	legacyBudget, err := GetCNYTokenBudget(token.Id, user.Id)
	require.NoError(t, err)
	assert.Equal(t, 665, legacyBudget.RemainQuota)
	require.NoError(t, DB.First(&token, token.Id).Error)
	assert.Equal(t, 432, token.RemainQuota)
}

func testWalletFundingCNYFallbackTasks(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		for _, refund := range []bool{false, true} {
			name := fmt.Sprintf("fallback-task-%t-%t", immediate, refund)
			t.Run(name, func(t *testing.T) {
				user, token := seedWalletFunding(t, name, 1000, 10000, 1000)
				_, err := ReserveCNYBilling(CNYBillingParams{RequestID: name, UserID: user.Id, TokenID: token.Id}, 670)
				require.NoError(t, err)
				server := useUserCacheMiniRedis(t)
				require.NoError(t, cacheSetTokenForTest(token))
				task := cnyTestTask(user, token, name)
				final := CNYTaskSettlement{Quota: 335, Refund: refund}
				status := TaskStatus(TaskStatusSuccess)
				if refund {
					status = TaskStatusFailure
				}
				if immediate {
					task.Status = status
					require.NoError(t, InsertCNYTask(context.Background(), task, &final))
				} else {
					require.NoError(t, InsertCNYTask(context.Background(), task, nil))
					assert.False(t, server.Exists(getTokenCacheKey(token.Key)))
					// Polling reloads only durable task/funding state, with no live session.
					require.NoError(t, DB.First(task, task.ID).Error)
					require.NoError(t, cacheSetTokenForTest(token))
					task.Status = status
					won, err := FinalizeCNYTask(task, TaskStatusSubmitted, final)
					require.NoError(t, err)
					assert.True(t, won)
					won, err = FinalizeCNYTask(task, TaskStatusSubmitted, final)
					require.NoError(t, err)
					assert.False(t, won)
				}
				assert.False(t, server.Exists(getTokenCacheKey(token.Key)))
				require.NoError(t, DB.First(&user, user.Id).Error)
				require.NoError(t, DB.First(&token, token.Id).Error)
				spent := 50
				if refund {
					spent = 0
				}
				assert.Equal(t, 1000-spent, user.Quota)
				assert.Equal(t, 1000-spent, token.RemainQuota)
				assert.Equal(t, spent, token.UsedQuota)
				wallet, err := GetCNYWallet(user.Id)
				require.NoError(t, err)
				assert.Equal(t, 10000, wallet.Quota)
				assert.Zero(t, wallet.ReservedQuota)
			})
		}
	}
}

func testWalletFundingCNYFallbackConcurrent(t *testing.T) {
	user, token := seedWalletFunding(t, "fallback-concurrent", 1000, 10000, 100)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for i := range 2 {
		workers.Go(func() {
			<-start
			_, err := ReserveCNYBilling(CNYBillingParams{RequestID: fmt.Sprintf("fallback-compete-%d", i), UserID: user.Id, TokenID: token.Id}, 670)
			results <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else {
			assert.ErrorIs(t, err, ErrWalletTokenInsufficient)
		}
	}
	assert.Equal(t, 1, succeeded)
	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Equal(t, 900, user.Quota)
	require.NoError(t, DB.First(&token, token.Id).Error)
	assert.Zero(t, token.RemainQuota)
}

func testWalletFundingCNYFallbackBounds(t *testing.T) {
	user, token := seedWalletFunding(t, "fallback-bounds", 1000, 10000, 1000)
	params := CNYBillingParams{RequestID: "fallback-bounds", UserID: user.Id, TokenID: token.Id}
	require.NoError(t, DB.Model(&token).UpdateColumn("used_quota", common.MaxWalletQuota).Error)
	_, err := ReserveCNYBilling(params, 670)
	assert.ErrorIs(t, err, ErrWalletQuotaLimitExceeded)
	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Equal(t, 1000, user.Quota, "an unsafe key update rolls back the wallet reservation")
	require.NoError(t, DB.Model(&token).UpdateColumn("used_quota", 0).Error)
	_, err = ReserveCNYBilling(params, 670)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&token).UpdateColumn("remain_quota", common.MaxWalletQuota).Error)
	_, err = RefundCNYBilling(params.RequestID, "refund exceeds key bound")
	assert.ErrorIs(t, err, ErrWalletQuotaLimitExceeded)
	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Equal(t, 900, user.Quota)
	record, err := GetCNYBillingRecord(params.RequestID)
	require.NoError(t, err)
	assert.Equal(t, CNYBillingReserved, record.Status)

	policy, err := GetWalletExchangePolicy()
	require.NoError(t, err)
	policy.StaticRate = "0.01"
	_, err = SaveWalletExchangePolicy(*policy, nil)
	require.NoError(t, err)
	user, token = seedWalletFunding(t, "fallback-large", common.MaxWalletQuota, 10000, common.MaxWalletQuota)
	params = CNYBillingParams{RequestID: "fallback-large", UserID: user.Id, TokenID: token.Id}
	_, err = ReserveCNYBilling(params, common.MaxQuota)
	require.NoError(t, err, "conversion uses the wallet bound, not a second native request clamp")
	funding, err := GetWalletFundingRecord(params.RequestID)
	require.NoError(t, err)
	assert.Equal(t, common.MaxQuota*100, funding.AlternateReserved)
	_, err = RefundCNYBilling(params.RequestID, "large converted reservation")
	require.NoError(t, err)
	require.NoError(t, DB.First(&token, token.Id).Error)
	assert.Equal(t, common.MaxWalletQuota, token.RemainQuota)
	previousScale := common.QuotaPerUnit
	common.QuotaPerUnit = float64(common.MaxWalletQuota)
	t.Cleanup(func() { common.QuotaPerUnit = previousScale })
	params.RequestID = "fallback-conversion-overflow"
	_, err = ReserveCNYBilling(params, common.MaxQuota)
	require.Error(t, err)
	_, err = GetWalletFundingRecord(params.RequestID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Equal(t, common.MaxWalletQuota, user.Quota)
}
