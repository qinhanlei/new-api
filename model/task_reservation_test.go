package model

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// The optional DSNs must point to isolated test databases. The same contract is
// exercised against every enabled engine; no business/provider APIs are called.
func TestDurableTaskReservation(t *testing.T) {
	for _, dialect := range []common.DatabaseType{common.DatabaseTypeSQLite, common.DatabaseTypeMySQL, common.DatabaseTypePostgreSQL} {
		t.Run(string(dialect), func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case common.DatabaseTypeSQLite:
				driver = sqlite.Open(filepath.Join(t.TempDir(), "reservations.db") + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_txlock=immediate")
			case common.DatabaseTypeMySQL:
				dsn := os.Getenv("TASK_RESERVATION_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TASK_RESERVATION_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case common.DatabaseTypePostgreSQL:
				dsn := os.Getenv("TASK_RESERVATION_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TASK_RESERVATION_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(4)
			oldDB, oldLogDB := DB, LOG_DB
			oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
			oldEnabled, oldRedis, oldBatch := common.AssetLibraryEnabled, common.RedisEnabled, common.BatchUpdateEnabled
			DB, LOG_DB = db, db
			common.SetDatabaseTypes(dialect, dialect)
			common.AssetLibraryEnabled, common.RedisEnabled, common.BatchUpdateEnabled = true, false, false
			initCol()
			t.Cleanup(func() {
				DB, LOG_DB = oldDB, oldLogDB
				common.SetDatabaseTypes(oldMainType, oldLogType)
				common.AssetLibraryEnabled, common.RedisEnabled, common.BatchUpdateEnabled = oldEnabled, oldRedis, oldBatch
				initCol()
				assert.NoError(t, sqlDB.Close())
			})
			for range 2 {
				require.NoError(t, DB.AutoMigrate(&User{}, &Token{}, &Channel{}, &Task{}, &SubscriptionPlan{}, &UserSubscription{}, &SubscriptionPreConsumeRecord{}, &TaskReservation{}))
			}
			t.Run("wallet recovery and concurrent idempotency", testTaskReservationWallet)
			t.Run("funding and token rollback", testTaskReservationRollback)
			t.Run("subscription preferences and refund", testTaskReservationSubscription)
			t.Run("database authority with Redis and batching", testTaskReservationDatabaseAuthority)
		})
	}
}

func taskReservationFixture(t *testing.T, quota, tokenQuota int) (User, Token, Channel, TaskReservationRequest) {
	t.Helper()
	user := createReserveTestUser(t, quota)
	token := Token{UserId: user.Id, Key: common.GetRandomString(32), Name: "task-test", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: tokenQuota}
	require.NoError(t, DB.Create(&token).Error)
	channel := Channel{Name: "task-reservation-test", Type: 54, Status: common.ChannelStatusEnabled, Key: "test-only"}
	require.NoError(t, DB.Create(&channel).Error)
	request := TaskReservationRequest{TaskID: GenerateTaskID(), UserID: user.Id, TokenID: token.Id, ChannelID: channel.Id, Model: "test-video", Preference: "wallet_only", Quota: 60}
	return user, token, channel, request
}

func testTaskReservationWallet(t *testing.T) {
	user, token, channel, request := taskReservationFixture(t, 200, 200)
	task := &Task{TaskID: request.TaskID, UserId: user.Id, ChannelId: channel.Id, Status: TaskStatusQueued, PrivateData: TaskPrivateData{DurableBilling: true}}
	require.NoError(t, DB.Create(task).Error)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() { _, errs[i] = ReserveTaskQuota(context.Background(), request) })
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 140, getUserQuotaFromDB(t, user.Id))
	assert.Equal(t, 140, getTokenFromDB(t, token.Id).RemainQuota)
	_, _, err := AdjustTaskQuota(t.Context(), request.TaskID, 250, TaskReservationReserve)
	require.ErrorIs(t, err, ErrTaskReservationQuota)
	assert.Equal(t, 140, getUserQuotaFromDB(t, user.Id))
	assert.Equal(t, 140, getTokenFromDB(t, token.Id).RemainQuota)
	for range 2 {
		_, _, err := AdjustTaskQuota(t.Context(), request.TaskID, 80, TaskReservationReserve)
		require.NoError(t, err)
	}
	_, delta, err := AdjustTaskQuota(t.Context(), request.TaskID, 70, TaskReservationSettle)
	require.NoError(t, err)
	assert.Equal(t, -10, delta)
	_, delta, err = AdjustTaskQuota(t.Context(), request.TaskID, 5, TaskReservationSettle)
	require.NoError(t, err)
	assert.Zero(t, delta)
	assert.Equal(t, 130, getUserQuotaFromDB(t, user.Id))
	_, _, err = AdjustTaskQuota(t.Context(), request.TaskID, 40, TaskReservationRecalculate)
	require.NoError(t, err)
	require.NoError(t, DB.First(&user, user.Id).Error)
	require.NoError(t, DB.First(&channel, channel.Id).Error)
	require.NoError(t, DB.First(task, task.ID).Error)
	assert.Equal(t, 160, user.Quota)
	assert.Equal(t, 40, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.EqualValues(t, 40, channel.UsedQuota)
	assert.Equal(t, 40, task.Quota)
	for range 2 {
		_, _, err = AdjustTaskQuota(t.Context(), request.TaskID, 0, TaskReservationRefund)
		require.NoError(t, err)
	}
	_, delta, err = AdjustTaskQuota(t.Context(), request.TaskID, 100, TaskReservationRecalculate)
	require.NoError(t, err)
	assert.Zero(t, delta, "a late completion cannot recharge a refunded task")
	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Equal(t, 200, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 200, getTokenFromDB(t, token.Id).RemainQuota)
	_, err = ReserveTaskQuota(t.Context(), request)
	assert.Error(t, err, "a refunded task cannot be submitted again")
}

func testTaskReservationRollback(t *testing.T) {
	user, token, _, request := taskReservationFixture(t, 200, 10)
	_, err := ReserveTaskQuota(t.Context(), request)
	require.ErrorIs(t, err, ErrTaskReservationQuota)
	assert.Equal(t, 200, getUserQuotaFromDB(t, user.Id))
	assert.Equal(t, 10, getTokenFromDB(t, token.Id).RemainQuota)
	var count int64
	require.NoError(t, DB.Model(&TaskReservation{}).Where("task_id = ?", request.TaskID).Count(&count).Error)
	assert.Zero(t, count)
	request.Quota = common.MaxQuota + 1
	_, err = ReserveTaskQuota(t.Context(), request)
	assert.Error(t, err)
	request.Quota = -1
	_, err = ReserveTaskQuota(t.Context(), request)
	assert.Error(t, err)
	plan := SubscriptionPlan{Title: "task-rollback", DurationUnit: SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 100, QuotaResetPeriod: SubscriptionResetNever}
	require.NoError(t, DB.Create(&plan).Error)
	sub := UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 100, EndTime: common.GetTimestamp() + 3600, Status: "active"}
	require.NoError(t, DB.Create(&sub).Error)
	request.Quota, request.Preference = 60, "subscription_only"
	_, err = ReserveTaskQuota(t.Context(), request)
	require.ErrorIs(t, err, ErrTaskReservationQuota)
	require.NoError(t, DB.First(&sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)
	require.NoError(t, DB.Model(&SubscriptionPreConsumeRecord{}).Where("request_id = ?", request.TaskID).Count(&count).Error)
	assert.Zero(t, count, "subscription receipt rolls back with token failure")
}

func testTaskReservationSubscription(t *testing.T) {
	user, token, _, request := taskReservationFixture(t, 200, 200)
	plan := SubscriptionPlan{Title: "task-test", DurationUnit: SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 100, QuotaResetPeriod: SubscriptionResetNever}
	require.NoError(t, DB.Create(&plan).Error)
	sub := UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 100, EndTime: common.GetTimestamp() + 3600, Status: "active", AllowWalletOverflow: false}
	require.NoError(t, DB.Create(&sub).Error)
	request.Preference = "subscription_first"
	reservation, err := ReserveTaskQuota(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, "subscription", reservation.FundingSource)
	assert.Equal(t, sub.Id, reservation.SubscriptionID)
	assert.Equal(t, 200, getUserQuotaFromDB(t, user.Id))
	_, _, err = AdjustTaskQuota(t.Context(), request.TaskID, 50, TaskReservationSettle)
	require.NoError(t, err)
	require.NoError(t, DB.First(&sub, sub.Id).Error)
	assert.EqualValues(t, 50, sub.AmountUsed)
	// An exhausted subscription that forbids overflow cannot silently use money.
	second := request
	second.TaskID = GenerateTaskID()
	_, err = ReserveTaskQuota(t.Context(), second)
	assert.ErrorIs(t, err, ErrTaskReservationQuota)
	assert.Equal(t, 200, getUserQuotaFromDB(t, user.Id))
	require.NoError(t, DB.Model(&sub).Update("allow_wallet_overflow", true).Error)
	reservation, err = ReserveTaskQuota(t.Context(), second)
	require.NoError(t, err)
	assert.Equal(t, "wallet", reservation.FundingSource)
	_, _, err = AdjustTaskQuota(t.Context(), second.TaskID, 0, TaskReservationRefund)
	require.NoError(t, err)
	// Revoking the token after acceptance must not prevent restoring its money.
	require.NoError(t, DB.Delete(&token).Error)
	_, _, err = AdjustTaskQuota(t.Context(), request.TaskID, 0, TaskReservationRefund)
	require.NoError(t, err)
	require.NoError(t, DB.First(&sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)
	require.NoError(t, DB.Unscoped().First(&token, token.Id).Error)
	assert.Equal(t, 200, token.RemainQuota)
}

func testTaskReservationDatabaseAuthority(t *testing.T) {
	useUserCacheMiniRedis(t)
	oldBatch := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() { common.BatchUpdateEnabled = oldBatch })
	user, token, _, request := taskReservationFixture(t, 200, 200)
	require.NoError(t, populateUserCache(user))
	_, err := cacheInitToken(token)
	require.NoError(t, err)
	_, err = ReserveTaskQuota(t.Context(), request)
	require.NoError(t, err)
	quota, err := GetUserQuota(user.Id, false)
	require.NoError(t, err)
	assert.Equal(t, 140, quota)
	cachedUser, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 140, cachedUser.Quota)
	cachedToken, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, 140, cachedToken.RemainQuota)
	reserved, err := TryReserveUserQuota(user.Id, 150)
	require.NoError(t, err)
	assert.False(t, reserved)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 150, false)
	require.NoError(t, err)
	assert.False(t, reserved)
	require.NoError(t, IncreaseUserQuota(user.Id, 10, false))
	require.NoError(t, DecreaseUserQuota(user.Id, 20, false))
	require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 10))
	require.NoError(t, DecreaseTokenQuota(token.Id, token.Key, 20))
	assert.Equal(t, 130, getUserQuotaFromDB(t, user.Id))
	assert.Equal(t, 130, getTokenFromDB(t, token.Id).RemainQuota)
	// Metadata cache revocation fences remain authoritative in asset mode.
	require.NoError(t, SetUserAuthVersionFence(user.Id, user.AuthVersion+1))
	_, err = GetUserCache(user.Id)
	assert.ErrorIs(t, err, ErrUserAuthCachePending)
}
