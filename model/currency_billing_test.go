package model

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// The environment DSNs must name disposable test databases. Prefixing tables
// keeps this matrix separate from the integration suite using the same server.
func TestCNYBillingDatabaseMatrix(t *testing.T) {
	for _, dialect := range []common.DatabaseType{common.DatabaseTypeSQLite, common.DatabaseTypeMySQL, common.DatabaseTypePostgreSQL} {
		t.Run(string(dialect), func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case common.DatabaseTypeSQLite:
				driver = sqlite.Open(filepath.Join(t.TempDir(), "cny.db") + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_txlock=immediate")
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
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "cny_matrix_"}, Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(8)
			require.NoError(t, sqlDB.Ping())
			oldDB, oldMainType := DB, common.MainDatabaseType()
			oldRedis, oldBatch := common.RedisEnabled, common.BatchUpdateEnabled
			DB, common.RedisEnabled, common.BatchUpdateEnabled = db, false, false
			common.SetMainDatabaseType(dialect)
			initCol()
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&CNYBillingRecord{}, &WalletFundingRecord{}, &WalletExchangePolicy{}, &ExchangeRateObservation{}, &CNYTokenBudget{}, &CNYWallet{}, &Task{}, &Token{}, &User{}))
				DB, common.RedisEnabled, common.BatchUpdateEnabled = oldDB, oldRedis, oldBatch
				common.SetMainDatabaseType(oldMainType)
				initCol()
				require.NoError(t, sqlDB.Close())
			})
			var version string
			if dialect == common.DatabaseTypeSQLite {
				require.NoError(t, db.Raw("select sqlite_version()").Scan(&version).Error)
			} else {
				require.NoError(t, db.Raw("select version()").Scan(&version).Error)
			}
			t.Logf("%s version: %s", dialect, version)
			// CNY only adds tables. Existing USD user/token rows are the upgrade
			// fixture; re-running migration must preserve them and CNY history.
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Task{}))
			legacy := User{Username: "legacy", Password: "test", Quota: 321, UsedQuota: 123}
			require.NoError(t, db.Create(&legacy).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&CNYWallet{}, &CNYTokenBudget{}, &CNYBillingRecord{}, &WalletFundingRecord{}, &WalletExchangePolicy{}, &ExchangeRateObservation{}))
			}
			t.Run("adjustments", testCNYAdjustments)
			t.Run("adjustment_modes", testCNYAdjustmentModes)
			t.Run("concurrent_adjustment", testCNYAdjustmentConcurrency)
			t.Run("reservation_and_settlement", testCNYReservation)
			t.Run("frozen_budget_and_deleted_token", testCNYFrozenBudget)
			t.Run("authentication_and_ownership", testCNYAuthentication)
			t.Run("exhausted_cache", testCNYExhaustedCache)
			t.Run("task_atomicity", testCNYTaskAtomicity)
			t.Run("concurrent_idempotency", testCNYConcurrency)
			var recordsBefore, recordsAfter int64
			require.NoError(t, db.Model(&CNYBillingRecord{}).Count(&recordsBefore).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&CNYWallet{}, &CNYTokenBudget{}, &CNYBillingRecord{}, &WalletFundingRecord{}, &WalletExchangePolicy{}, &ExchangeRateObservation{}))
			}
			require.NoError(t, db.Model(&CNYBillingRecord{}).Count(&recordsAfter).Error)
			assert.Equal(t, recordsBefore, recordsAfter)
			var preserved User
			require.NoError(t, db.First(&preserved, legacy.Id).Error)
			assert.Equal(t, 321, preserved.Quota)
			assert.Equal(t, 123, preserved.UsedQuota)
		})
	}
}

func newCNYAccount(t *testing.T, name string, walletQuota, tokenQuota int, unlimited bool) (User, Token, CNYTokenBudget) {
	t.Helper()
	user := User{Username: name, Password: "test", AffCode: name, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 987, UsedQuota: 654}
	require.NoError(t, DB.Create(&user).Error)
	token := Token{UserId: user.Id, Key: "cny-test-" + name, Name: name, Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 432, UsedQuota: 123}
	require.NoError(t, DB.Create(&token).Error)
	_, err := AdjustCNYWallet(user.Id, 1, common.RoleRootUser, walletQuota, "credit-"+name, "test credit")
	require.NoError(t, err)
	budget, err := UpdateCNYTokenBudget(token.Id, user.Id, CNYTokenBudgetChange{Enabled: true, RemainQuota: tokenQuota, UnlimitedQuota: unlimited})
	require.NoError(t, err)
	return user, token, *budget
}

func testCNYAdjustments(t *testing.T) {
	user, token, _ := newCNYAccount(t, "adjust", 1000, 500, false)
	first, err := AdjustCNYWallet(user.Id, 1, common.RoleRootUser, 1000, "credit-adjust", "test credit")
	require.NoError(t, err)
	assert.Equal(t, 0, first.BeforeQuota)
	assert.Equal(t, 1000, first.AfterQuota)
	_, err = AdjustCNYWallet(user.Id, 1, common.RoleRootUser, 999, "credit-adjust", "test credit")
	assert.ErrorIs(t, err, ErrCNYConflict)
	_, err = AdjustCNYWallet(user.Id, 1, common.RoleCommonUser, 1, "forbidden-adjust", "test")
	assert.ErrorIs(t, err, ErrUserQuotaPermission)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).UpdateColumn("role", common.RoleAdminUser).Error)
	_, err = AdjustCNYWallet(user.Id, 2, common.RoleAdminUser, 1, "peer-adjust", "test")
	assert.ErrorIs(t, err, ErrUserQuotaPermission)
	_, err = AdjustCNYWallet(user.Id, 1, common.RoleRootUser, common.MaxWalletQuota, "overflow-adjust", "test")
	assert.ErrorIs(t, err, ErrWalletQuotaLimitExceeded)
	_, err = AdjustCNYWallet(user.Id, 1, common.RoleRootUser, 1, "bad operation key", "test")
	assert.ErrorIs(t, err, ErrCNYInvalid)
	// Request and administrative idempotency keys have separate namespaces.
	_, err = ReserveCNYBilling(CNYBillingParams{RequestID: "credit-adjust", UserID: user.Id, TokenID: token.Id, ModelName: "model"}, 0)
	require.NoError(t, err)
	wallet, err := GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 1000, wallet.Quota)
	assert.Equal(t, CNYQuotaPerUnit, wallet.QuotaPerUnit)
}

func testCNYAdjustmentModes(t *testing.T) {
	user, token, _ := newCNYAccount(t, "adjust-modes", 1000, 1000, false)
	_, err := ReserveCNYBilling(CNYBillingParams{RequestID: "before-adjustment", UserID: user.Id, TokenID: token.Id}, 100)
	require.NoError(t, err)
	_, err = SettleCNYBilling("before-adjustment", 60, CNYBillingUsage{})
	require.NoError(t, err)
	_, err = ReserveCNYBilling(CNYBillingParams{RequestID: "during-adjustment", UserID: user.Id, TokenID: token.Id}, 200)
	require.NoError(t, err)
	first, err := AdjustCNYWalletByMode(user.Id, 1, common.RoleRootUser, "override", 500, "override-available", "")
	require.NoError(t, err)
	assert.Equal(t, 740, first.BeforeQuota)
	assert.Equal(t, 500, first.AfterQuota)
	assert.Equal(t, -240, first.DeltaQuota)
	assert.Equal(t, "CNY wallet adjustment", first.Reason)
	wallet, err := GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 500, wallet.Quota)
	assert.Equal(t, 60, wallet.UsedQuota)
	assert.Equal(t, 200, wallet.ReservedQuota)
	budget, err := GetCNYTokenBudget(token.Id, user.Id)
	require.NoError(t, err)
	assert.Equal(t, 740, budget.RemainQuota)

	_, err = SettleCNYBilling("during-adjustment", 250, CNYBillingUsage{})
	require.NoError(t, err)
	replayed, err := AdjustCNYWalletByMode(user.Id, 1, common.RoleRootUser, "override", 500, "override-available", " \t")
	require.NoError(t, err)
	assert.Equal(t, first.ID, replayed.ID)
	assert.Equal(t, 500, replayed.AfterQuota)
	wallet, err = GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 450, wallet.Quota, "retry must preserve spending after the original override")
	assert.Equal(t, 310, wallet.UsedQuota)
	assert.Zero(t, wallet.ReservedQuota)
	_, err = AdjustCNYWalletByMode(user.Id, 1, common.RoleRootUser, "override", 501, "override-available", "")
	assert.ErrorIs(t, err, ErrCNYConflict)
	_, err = AdjustCNYWalletByMode(user.Id, 1, common.RoleRootUser, "add", 500, "override-available", "")
	assert.ErrorIs(t, err, ErrCNYConflict)
	_, err = AdjustCNYWallet(user.Id, 1, common.RoleRootUser, -240, "override-available", "")
	assert.ErrorIs(t, err, ErrCNYConflict)
	_, err = AdjustCNYWalletByMode(user.Id, 2, common.RoleRootUser, "override", 500, "override-available", "")
	assert.ErrorIs(t, err, ErrCNYConflict)

	for i, step := range []struct {
		mode        string
		value, want int
	}{
		{"override", 0, 0},
		{"override", -10, -10},
		{"add", 20, 10},
		{"subtract", 30, -20},
		{"override", -20, -20},
	} {
		record, err := AdjustCNYWalletByMode(user.Id, 1, common.RoleRootUser, step.mode, step.value, fmt.Sprintf("mode-step-%d", i), "")
		require.NoError(t, err)
		assert.Equal(t, step.want, record.AfterQuota)
	}
	for _, input := range []struct {
		mode  string
		value int
	}{
		{"add", 0}, {"subtract", 0}, {"add", -1}, {"subtract", -1}, {"delta", 1}, {"", 1},
	} {
		_, err = AdjustCNYWalletByMode(user.Id, 1, common.RoleRootUser, input.mode, input.value, "invalid-adjustment", "")
		assert.ErrorIs(t, err, ErrCNYInvalid)
	}
	_, err = AdjustCNYWalletByMode(user.Id, 1, common.RoleRootUser, "override", common.MaxWalletQuota+1, "oversized-override", "")
	assert.ErrorIs(t, err, ErrWalletQuotaLimitExceeded)
	_, err = AdjustCNYWalletByMode(user.Id, 1, common.RoleRootUser, "override", common.MaxWalletQuota, "oversized-delta", "")
	assert.ErrorIs(t, err, ErrWalletQuotaLimitExceeded)

	// Older ledger rows have no original-mode metadata and must remain retryable.
	legacy, err := AdjustCNYWallet(user.Id, 1, common.RoleRootUser, 30, "legacy-adjustment", "legacy reason ")
	require.NoError(t, err)
	require.NoError(t, DB.Model(legacy).Update("other", "").Error)
	legacyRetry, err := AdjustCNYWallet(user.Id, 1, common.RoleRootUser, 30, "legacy-adjustment", "legacy reason ")
	require.NoError(t, err)
	assert.Equal(t, legacy.ID, legacyRetry.ID)
	_, err = AdjustCNYWalletByMode(user.Id, 1, common.RoleRootUser, "add", 30, "legacy-adjustment", "legacy reason ")
	assert.ErrorIs(t, err, ErrCNYConflict)
	wallet, err = GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 10, wallet.Quota)
	assert.Equal(t, 310, wallet.UsedQuota)
	assert.Zero(t, wallet.ReservedQuota)
}

func testCNYAdjustmentConcurrency(t *testing.T) {
	user, _, _ := newCNYAccount(t, "concurrent-adjust", 1000, 500, false)
	start := make(chan struct{})
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			<-start
			_, err := AdjustCNYWalletByMode(user.Id, 1, common.RoleRootUser, "add", 100, "concurrent-adjustment", "")
			errors <- err
		})
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	wallet, err := GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 1100, wallet.Quota)
	var count int64
	require.NoError(t, DB.Model(&CNYBillingRecord{}).Where("kind = ? AND request_id = ?", CNYBillingAdjustment, "concurrent-adjustment").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func testCNYReservation(t *testing.T) {
	user, token, budget := newCNYAccount(t, "reserve", 1000, 500, false)
	params := CNYBillingParams{RequestID: "reserve-request", UserID: user.Id, TokenID: token.Id, ModelName: "model", ChannelID: 1, Snapshot: "first"}
	record, err := ReserveCNYBilling(params, 100)
	require.NoError(t, err)
	params.ChannelID, params.Snapshot = 2, "retry"
	record, err = ReserveCNYBilling(params, 80)
	require.NoError(t, err)
	assert.Equal(t, 100, record.ReservedQuota)
	assert.Equal(t, 2, record.ChannelID)
	assert.Equal(t, "retry", record.Snapshot)
	_, err = ReserveCNYBilling(params, 200)
	require.NoError(t, err)
	_, err = UpdateCNYTokenBudget(token.Id, user.Id, CNYTokenBudgetChange{Enabled: true, RemainQuota: 500, ExpectedVersion: budget.Version})
	assert.ErrorIs(t, err, ErrCNYVersion)
	record, err = SettleCNYBilling(params.RequestID, 1200, CNYBillingUsage{PromptTokens: 12, CompletionTokens: 34, Other: `{"currency":"CNY"}`, Snapshot: "settled"})
	require.NoError(t, err)
	assert.Equal(t, CNYBillingSettled, record.Status)
	assert.Equal(t, 1200, record.Quota)
	assert.Equal(t, "settled", record.Snapshot)
	_, err = SettleCNYBilling(params.RequestID, 1200, CNYBillingUsage{})
	require.NoError(t, err)
	_, err = SettleCNYBilling(params.RequestID, 1201, CNYBillingUsage{})
	assert.ErrorIs(t, err, ErrCNYConflict)
	wallet, err := GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, -200, wallet.Quota)
	assert.Equal(t, 1200, wallet.UsedQuota)
	assert.Zero(t, wallet.ReservedQuota)
	currentBudget, err := GetCNYTokenBudget(token.Id, user.Id)
	require.NoError(t, err)
	assert.Equal(t, -700, currentBudget.RemainQuota)
	assert.Equal(t, 1200, currentBudget.UsedQuota)
	_, err = ReserveCNYBilling(CNYBillingParams{RequestID: "overdraft-next", UserID: user.Id, TokenID: token.Id}, 1)
	assert.ErrorIs(t, err, ErrCNYQuota)
	_, err = RefundCNYBilling(params.RequestID, "refund")
	require.NoError(t, err)
	_, err = RefundCNYBilling(params.RequestID, "repeat")
	require.NoError(t, err)
	wallet, err = GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 1000, wallet.Quota)
	assert.Zero(t, wallet.UsedQuota)
	var preservedUser User
	var preservedToken Token
	require.NoError(t, DB.First(&preservedUser, user.Id).Error)
	require.NoError(t, DB.First(&preservedToken, token.Id).Error)
	assert.Equal(t, 987, preservedUser.Quota)
	assert.Equal(t, 654, preservedUser.UsedQuota)
	assert.Equal(t, 432, preservedToken.RemainQuota)
	assert.Equal(t, 123, preservedToken.UsedQuota)
}

func testCNYFrozenBudget(t *testing.T) {
	for _, initiallyUnlimited := range []bool{false, true} {
		t.Run(fmt.Sprint(initiallyUnlimited), func(t *testing.T) {
			name := fmt.Sprintf("frozen-%t", initiallyUnlimited)
			user, token, _ := newCNYAccount(t, name, 1000, 500, initiallyUnlimited)
			params := CNYBillingParams{RequestID: name, UserID: user.Id, TokenID: token.Id}
			_, err := ReserveCNYBilling(params, 100)
			require.NoError(t, err)
			budget, err := GetCNYTokenBudget(token.Id, user.Id)
			require.NoError(t, err)
			_, err = UpdateCNYTokenBudget(token.Id, user.Id, CNYTokenBudgetChange{Enabled: false, RemainQuota: 400, UnlimitedQuota: !initiallyUnlimited, ExpectedVersion: budget.Version})
			require.NoError(t, err)
			_, err = SettleCNYBilling(params.RequestID, 150, CNYBillingUsage{})
			require.NoError(t, err)
			budget, err = GetCNYTokenBudget(token.Id, user.Id)
			require.NoError(t, err)
			if initiallyUnlimited {
				assert.Equal(t, 400, budget.RemainQuota)
			} else {
				assert.Equal(t, 350, budget.RemainQuota)
			}
			require.NoError(t, DB.Delete(&token).Error)
			record, err := RefundCNYBilling(params.RequestID, "deleted token")
			require.NoError(t, err)
			assert.Equal(t, 150, record.Quota)
			assert.Equal(t, 150, record.RefundQuota)
			require.NoError(t, DB.Where("token_id = ?", token.Id).First(budget).Error)
			if initiallyUnlimited {
				assert.Equal(t, 400, budget.RemainQuota)
			} else {
				assert.Equal(t, 500, budget.RemainQuota)
			}
			assert.Zero(t, budget.UsedQuota)
			wallet, err := GetCNYWallet(user.Id)
			require.NoError(t, err)
			assert.Equal(t, 1000, wallet.Quota)
		})
	}
}

func testCNYAuthentication(t *testing.T) {
	for _, status := range []int{common.TokenStatusExhausted, common.TokenStatusDisabled, common.TokenStatusExpired} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			name := fmt.Sprintf("auth-%d", status)
			user, token, budget := newCNYAccount(t, name, 1000, 500, false)
			require.NoError(t, DB.Model(&Token{}).Where("id = ?", token.Id).Updates(map[string]any{"status": status, "remain_quota": 0}).Error)
			_, err := UpdateCNYTokenBudget(token.Id, user.Id+999, CNYTokenBudgetChange{Enabled: true, RemainQuota: 500})
			assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
			_, err = UpdateCNYTokenBudget(token.Id, user.Id, CNYTokenBudgetChange{Enabled: true, RemainQuota: 500, ExpectedVersion: budget.Version})
			require.NoError(t, err)
			_, err = ValidateUserToken(token.Key)
			if status == common.TokenStatusExhausted {
				require.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, ErrTokenInvalid)
			}
			var current Token
			require.NoError(t, DB.First(&current, token.Id).Error)
			if status == common.TokenStatusExhausted {
				assert.Equal(t, common.TokenStatusEnabled, current.Status)
			} else {
				assert.Equal(t, status, current.Status)
			}
			require.NoError(t, DB.Model(&Token{}).Where("id = ?", token.Id).Updates(map[string]any{"status": common.TokenStatusExhausted, "expired_time": common.GetTimestamp() - 1}).Error)
			_, err = ValidateUserToken(token.Key)
			assert.ErrorIs(t, err, ErrTokenInvalid)
			_, err = ReserveCNYBilling(CNYBillingParams{RequestID: name + "-expired", UserID: user.Id, TokenID: token.Id}, 1)
			assert.ErrorIs(t, err, ErrTokenInvalid)
		})
	}
	user, token, _ := newCNYAccount(t, "disabled-user", 1000, 500, false)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).UpdateColumn("status", common.UserStatusDisabled).Error)
	_, err := ReserveCNYBilling(CNYBillingParams{RequestID: "disabled-user", UserID: user.Id, TokenID: token.Id}, 1)
	assert.ErrorIs(t, err, ErrUserQuotaPermission)
}

func cnyTestTask(user User, token Token, requestID string) *Task {
	return &Task{UserId: user.Id, TaskID: "task-" + requestID, Status: TaskStatusSubmitted, PrivateData: TaskPrivateData{TokenId: token.Id,
		Execution: &TaskExecutionSnapshot{RequestID: requestID}, BillingContext: &TaskBillingContext{TieredSnapshot: &billingexpr.BillingSnapshot{Currency: "CNY"}}}}
}

func testCNYExhaustedCache(t *testing.T) {
	user, token, budget := newCNYAccount(t, "cache", 1000, 500, false)
	server := useUserCacheMiniRedis(t)
	token.Status, token.RemainQuota = common.TokenStatusExhausted, 0
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", token.Id).Updates(map[string]any{"status": token.Status, "remain_quota": 0}).Error)
	require.NoError(t, cacheSetTokenForTest(token))
	_, err := UpdateCNYTokenBudget(token.Id, user.Id, CNYTokenBudgetChange{Enabled: true, RemainQuota: 500, ExpectedVersion: budget.Version})
	require.NoError(t, err)
	_, err = cacheGetTokenByKey(token.Key)
	require.Error(t, err)
	published, err := cacheInitToken(token)
	require.NoError(t, err)
	assert.Zero(t, published, "an old reader must not republish through the mutation fence")
	validated, err := ValidateUserToken(token.Key)
	require.NoError(t, err)
	assert.Equal(t, common.TokenStatusEnabled, validated.Status)
	// A stale exhausted entry after an exceptionally slow reader still cannot
	// block the native CNY budget: authentication consults the primary database.
	server.Del(getTokenCacheFenceKey(token.Key))
	require.NoError(t, cacheSetTokenForTest(token))
	_, err = ValidateUserToken(token.Key)
	require.NoError(t, err)
}

func testCNYTaskAtomicity(t *testing.T) {
	user, token, _ := newCNYAccount(t, "task", 1000, 500, false)
	_, err := ReserveCNYBilling(CNYBillingParams{RequestID: "task-pending", UserID: user.Id, TokenID: token.Id}, 100)
	require.NoError(t, err)
	task := cnyTestTask(user, token, "task-pending")
	require.NoError(t, InsertCNYTask(context.Background(), task, nil))
	record, err := GetCNYBillingRecord("task-pending")
	require.NoError(t, err)
	assert.Equal(t, CNYBillingSubmitted, record.Status)
	assert.Zero(t, record.Quota)
	_, err = SettleCNYBilling("task-pending", 100, CNYBillingUsage{})
	assert.ErrorIs(t, err, ErrCNYState)
	_, err = RefundCNYBilling("task-pending", "wrong transaction boundary")
	assert.ErrorIs(t, err, ErrCNYState)
	budget, err := GetCNYTokenBudget(token.Id, user.Id)
	require.NoError(t, err)
	_, err = UpdateCNYTokenBudget(token.Id, user.Id, CNYTokenBudgetChange{Enabled: true, RemainQuota: common.MaxWalletQuota, ExpectedVersion: budget.Version})
	require.NoError(t, err)
	task.Status = TaskStatusFailure
	updated, err := FinalizeCNYTask(task, TaskStatusSubmitted, CNYTaskSettlement{Refund: true})
	assert.False(t, updated)
	assert.ErrorIs(t, err, ErrWalletQuotaLimitExceeded)
	assert.Equal(t, 100, task.Quota, "failed finalization must preserve the caller's reservation")
	var persisted Task
	require.NoError(t, DB.First(&persisted, task.ID).Error)
	assert.Equal(t, TaskStatus(TaskStatusSubmitted), persisted.Status)
	assert.Equal(t, "CNY", persisted.Currency)
	wallet, err := GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 900, wallet.Quota)
	assert.Equal(t, 100, wallet.ReservedQuota)
	budget, err = GetCNYTokenBudget(token.Id, user.Id)
	require.NoError(t, err)
	_, err = UpdateCNYTokenBudget(token.Id, user.Id, CNYTokenBudgetChange{Enabled: true, RemainQuota: 400, ExpectedVersion: budget.Version})
	require.NoError(t, err)
	updated, err = FinalizeCNYTask(task, TaskStatusSubmitted, CNYTaskSettlement{Refund: true})
	require.NoError(t, err)
	assert.True(t, updated)
	updated, err = FinalizeCNYTask(task, TaskStatusSubmitted, CNYTaskSettlement{Refund: true})
	require.NoError(t, err)
	assert.False(t, updated)
	_, err = ReserveCNYBilling(CNYBillingParams{RequestID: "task-free", UserID: user.Id, TokenID: token.Id}, 0)
	require.NoError(t, err)
	freeTask := cnyTestTask(user, token, "task-free")
	freeTask.Status = TaskStatusSuccess
	require.NoError(t, InsertCNYTask(context.Background(), freeTask, &CNYTaskSettlement{}))
	freeTask.Quota = 999
	require.NoError(t, InsertCNYTask(context.Background(), freeTask, &CNYTaskSettlement{}))
	assert.Zero(t, freeTask.Quota, "idempotent insert restores the persisted amount")
	assert.ErrorIs(t, InsertCNYTask(context.Background(), freeTask, &CNYTaskSettlement{Quota: 1}), ErrCNYConflict)
	record, err = GetCNYBillingRecord("task-free")
	require.NoError(t, err)
	assert.Equal(t, CNYBillingSettled, record.Status)
	assert.Zero(t, record.Quota)
	var count int64
	require.NoError(t, DB.Model(&Task{}).Where("task_id = ?", freeTask.TaskID).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	_, err = ReserveCNYBilling(CNYBillingParams{RequestID: "task-insert-failure", UserID: user.Id, TokenID: token.Id}, 100)
	require.NoError(t, err)
	failedInsert := cnyTestTask(user, token, "task-insert-failure")
	failedInsert.ID, failedInsert.Status, failedInsert.Quota = freeTask.ID, TaskStatusSuccess, 100
	require.Error(t, InsertCNYTask(context.Background(), failedInsert, &CNYTaskSettlement{Quota: 150}))
	assert.Equal(t, 100, failedInsert.Quota)
	record, err = GetCNYBillingRecord("task-insert-failure")
	require.NoError(t, err)
	assert.Equal(t, CNYBillingReserved, record.Status)
	assert.Empty(t, record.TaskID)
	wallet, err = GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 900, wallet.Quota)
	assert.Equal(t, 100, wallet.ReservedQuota)
	assert.Zero(t, wallet.UsedQuota)
}

func testCNYConcurrency(t *testing.T) {
	user, token, _ := newCNYAccount(t, "concurrent", 1000, 500, false)
	params := CNYBillingParams{RequestID: "concurrent-request", UserID: user.Id, TokenID: token.Id}
	start := make(chan struct{})
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() { <-start; _, err := ReserveCNYBilling(params, 100); errors <- err })
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	wallet, err := GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 900, wallet.Quota)
	assert.Equal(t, 100, wallet.ReservedQuota)
	start = make(chan struct{})
	errors = make(chan error, 2)
	for range 2 {
		workers.Go(func() { <-start; _, err := SettleCNYBilling(params.RequestID, 120, CNYBillingUsage{}); errors <- err })
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	wallet, err = GetCNYWallet(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 880, wallet.Quota)
	assert.Equal(t, 120, wallet.UsedQuota)
	var count int64
	require.NoError(t, DB.Model(&CNYBillingRecord{}).Where("kind = ? AND request_id = ?", CNYBillingRequest, params.RequestID).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}
