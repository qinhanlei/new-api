package model

import (
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
