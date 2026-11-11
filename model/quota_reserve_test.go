package model

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func TestUSDQuotaDatabaseAuthority(t *testing.T) {
	for _, dialect := range []common.DatabaseType{common.DatabaseTypeSQLite, common.DatabaseTypeMySQL, common.DatabaseTypePostgreSQL} {
		t.Run(string(dialect), func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case common.DatabaseTypeSQLite:
				driver = sqlite.Open(filepath.Join(t.TempDir(), "quota.db") + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_txlock=immediate")
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
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "usd_authority_"}, Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(8)
			oldDB, oldType := DB, common.MainDatabaseType()
			DB = db
			common.SetMainDatabaseType(dialect)
			initCol()
			t.Cleanup(func() {
				assert.NoError(t, db.Migrator().DropTable(&CNYTokenBudget{}, &Token{}, &User{}))
				DB = oldDB
				common.SetMainDatabaseType(oldType)
				initCol()
				assert.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &CNYTokenBudget{}))
			var version string
			query := "select version()"
			if dialect == common.DatabaseTypeSQLite {
				query = "select sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			for _, cacheState := range []string{"available", "evicted", "disconnected", "invalidation_error"} {
				t.Run(cacheState, func(t *testing.T) { testUSDQuotaCacheAndBatch(t, cacheState) })
			}
			t.Run("database_failure", testUSDQuotaDatabaseFailure)
			t.Run("deleted_records", testUSDQuotaDeletedRecords)
			t.Run("quota_boundaries", testUSDQuotaBoundaries)
			t.Run("authentication", testUSDQuotaAuthentication)
		})
	}
}

func testUSDQuotaCacheAndBatch(t *testing.T, cacheState string) {
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	user, token := createReserveTestUser(t, 100), createReserveTestToken(t, 100)
	_, err := GetUserCache(user.Id)
	require.NoError(t, err)
	_, err = GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	switch cacheState {
	case "evicted":
		server.FlushAll()
	case "disconnected":
		require.NoError(t, common.RDB.Close())
	case "invalidation_error":
		server.SetError("ERR injected cache failure")
	}

	// Both contenders see the same initial budget; only one reservation for
	// each account may commit, regardless of the cache and batch settings.
	start := make(chan struct{})
	type reservationResult struct {
		user, token       bool
		userErr, tokenErr error
	}
	results := make(chan reservationResult, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			<-start
			userReserved, userErr := TryReserveUserQuota(user.Id, 60)
			tokenReserved, tokenErr := TryReserveTokenQuota(token.Id, token.Key, 60, false)
			results <- reservationResult{userReserved, tokenReserved, userErr, tokenErr}
		})
	}
	close(start)
	workers.Wait()
	close(results)
	userReservations, tokenReservations := 0, 0
	for result := range results {
		require.NoError(t, result.userErr)
		require.NoError(t, result.tokenErr)
		if result.user {
			userReservations++
		}
		if result.token {
			tokenReservations++
		}
	}
	assert.Equal(t, 1, userReservations)
	assert.Equal(t, 1, tokenReservations)
	assert.Equal(t, 40, getUserQuotaFromDB(t, user.Id))
	assert.Equal(t, 40, getTokenFromDB(t, token.Id).RemainQuota)

	require.NoError(t, IncreaseUserQuota(user.Id, 10, false))
	require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 10))
	require.NoError(t, DecreaseUserQuota(user.Id, 5, false))
	require.NoError(t, DecreaseTokenQuota(token.Id, token.Key, 5))
	UpdateUserUsedQuotaAndRequestCount(user.Id, 55)
	batchUpdate()
	assert.Equal(t, 45, getUserQuotaFromDB(t, user.Id))
	storedToken := getTokenFromDB(t, token.Id)
	assert.Equal(t, 45, storedToken.RemainQuota)
	assert.Equal(t, 55, storedToken.UsedQuota)
	var storedUser User
	require.NoError(t, DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, 55, storedUser.UsedQuota)
	assert.Equal(t, 1, storedUser.RequestCount)
	batchUpdate()
	assert.Equal(t, 45, getUserQuotaFromDB(t, user.Id), "flushing statistics must never apply balances twice")
	quota, err := GetUserQuota(user.Id, false)
	require.NoError(t, err)
	assert.Equal(t, 45, quota)

	// Recovery may expose a pre-mutation cache that failed to invalidate.
	if cacheState == "disconnected" {
		common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	}
	server.SetError("")
	for _, staleQuota := range []int{0, 1000} {
		require.NoError(t, common.RDB.HSet(t.Context(), getUserCacheKey(user.Id), "Quota", staleQuota).Err())
		require.NoError(t, common.RDB.HSet(t.Context(), getTokenCacheKey(token.Key), "RemainQuota", staleQuota).Err())
		cachedUser, err := GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 45, cachedUser.Quota)
		validated, err := ValidateUserToken(token.Key)
		require.NoError(t, err)
		assert.Equal(t, 45, validated.RemainQuota)
		reserved, err := TryReserveUserQuota(user.Id, 46)
		require.NoError(t, err)
		assert.False(t, reserved)
		reserved, err = TryReserveTokenQuota(token.Id, token.Key, 46, true)
		require.NoError(t, err)
		assert.False(t, reserved, "a stale unlimited snapshot cannot bypass the stored budget")
	}
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", token.Id).Update("unlimited_quota", true).Error)
	reserved, err := TryReserveTokenQuota(token.Id, token.Key, 46, false)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, -1, getTokenFromDB(t, token.Id).RemainQuota)
}

func testUSDQuotaDatabaseFailure(t *testing.T) {
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	user, token := createReserveTestUser(t, 100), createReserveTestToken(t, 100)
	_, err := GetUserCache(user.Id)
	require.NoError(t, err)
	_, err = GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	failure := errors.New("injected database failure")
	require.NoError(t, DB.Callback().Query().Before("gorm:query").Register("quota_read_failure", func(tx *gorm.DB) { tx.AddError(failure) }))
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("quota_write_failure", func(tx *gorm.DB) { tx.AddError(failure) }))
	t.Cleanup(func() {
		assert.NoError(t, DB.Callback().Query().Remove("quota_read_failure"))
		assert.NoError(t, DB.Callback().Update().Remove("quota_write_failure"))
	})
	_, err = GetUserQuota(user.Id, false)
	assert.ErrorIs(t, err, failure)
	_, err = GetUserCache(user.Id)
	assert.ErrorIs(t, err, failure)
	_, err = ValidateUserToken(token.Key)
	assert.ErrorIs(t, err, ErrDatabase)
	reserved, err := TryReserveUserQuota(user.Id, 10)
	assert.ErrorIs(t, err, failure)
	assert.False(t, reserved)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 10, true)
	assert.ErrorIs(t, err, failure)
	assert.False(t, reserved)
	assert.ErrorIs(t, IncreaseUserQuota(user.Id, 10, false), failure)
	assert.ErrorIs(t, DecreaseUserQuota(user.Id, 10, false), failure)
	assert.ErrorIs(t, IncreaseTokenQuota(token.Id, token.Key, 10), failure)
	assert.ErrorIs(t, DecreaseTokenQuota(token.Id, token.Key, 10), failure)
}

func testUSDQuotaAuthentication(t *testing.T) {
	useUserCacheMiniRedis(t)
	for _, tc := range []struct {
		name               string
		status, quota      int
		unlimited, allowed bool
	}{
		{"enabled", common.TokenStatusEnabled, 10, false, true},
		{"refunded_exhausted", common.TokenStatusExhausted, 10, false, true},
		{"unlimited_exhausted", common.TokenStatusExhausted, 0, true, true},
		{"still_exhausted", common.TokenStatusExhausted, 0, false, false},
		{"disabled", common.TokenStatusDisabled, 10, true, false},
		{"expired", common.TokenStatusExpired, 10, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := createReserveTestToken(t, 10)
			_, err := GetTokenByKey(token.Key, false)
			require.NoError(t, err)
			require.NoError(t, DB.Model(&Token{}).Where("id = ?", token.Id).Updates(map[string]any{
				"status": tc.status, "remain_quota": tc.quota, "unlimited_quota": tc.unlimited,
			}).Error)
			_, err = ValidateUserToken(token.Key)
			if tc.allowed {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, ErrTokenInvalid)
			}
		})
	}
	t.Run("concurrent_disable", func(t *testing.T) {
		oldRedis := common.RedisEnabled
		common.RedisEnabled = false
		defer func() { common.RedisEnabled = oldRedis }()
		token := createReserveTestToken(t, 0)
		var disabled atomic.Bool
		require.NoError(t, DB.Callback().Query().After("gorm:query").Register("disable_after_quota_read", func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Token" && disabled.CompareAndSwap(false, true) {
				tx.AddError(DB.Model(&Token{}).Where("id = ?", token.Id).Update("status", common.TokenStatusDisabled).Error)
			}
		}))
		defer func() { require.NoError(t, DB.Callback().Query().Remove("disable_after_quota_read")) }()
		_, err := ValidateUserToken(token.Key)
		assert.ErrorIs(t, err, ErrTokenInvalid)
		require.True(t, disabled.Load())
		require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 10))
		assert.Equal(t, common.TokenStatusDisabled, getTokenFromDB(t, token.Id).Status)
		_, err = ValidateUserToken(token.Key)
		assert.ErrorIs(t, err, ErrTokenInvalid)
	})
}

func createReserveTestUser(t *testing.T, quota int) User {
	t.Helper()
	user := User{
		Username:    "reserve-user-" + common.GetRandomString(6),
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
		Quota:       quota,
		AffCode:     "reserve-aff-" + common.GetRandomString(8),
	}
	require.NoError(t, DB.Create(&user).Error)
	return user
}

func createReserveTestToken(t *testing.T, remainQuota int) Token {
	t.Helper()
	token := Token{
		UserId:      1,
		Key:         "reserve-token-" + common.GetRandomString(8),
		Name:        "reserve-test",
		Status:      common.TokenStatusEnabled,
		ExpiredTime: -1,
		RemainQuota: remainQuota,
	}
	require.NoError(t, token.Insert())
	return token
}

func getUserQuotaFromDB(t *testing.T, id int) int {
	t.Helper()
	var user User
	require.NoError(t, DB.Select("quota").First(&user, id).Error)
	return user.Quota
}

func getTokenFromDB(t *testing.T, id int) Token {
	t.Helper()
	var token Token
	require.NoError(t, DB.First(&token, id).Error)
	return token
}

func resetBatchUpdateTestState(t *testing.T) {
	t.Helper()
	oldBatchEnabled := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = false
	for i := range BatchUpdateTypeCount {
		batchUpdateLocks[i].Lock()
		batchUpdateStores[i] = make(map[int]int)
		batchUpdateLocks[i].Unlock()
	}
	t.Cleanup(func() {
		common.BatchUpdateEnabled = oldBatchEnabled
		for i := range BatchUpdateTypeCount {
			batchUpdateLocks[i].Lock()
			batchUpdateStores[i] = make(map[int]int)
			batchUpdateLocks[i].Unlock()
		}
	})
}

func TestTryReserveQuotaWithoutRedis(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)

	user := createReserveTestUser(t, 100)
	reserved, err := TryReserveUserQuota(user.Id, 60)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 40, getUserQuotaFromDB(t, user.Id))

	reserved, err = TryReserveUserQuota(user.Id, 41)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 40, getUserQuotaFromDB(t, user.Id))

	token := createReserveTestToken(t, 80)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 25, false)
	require.NoError(t, err)
	assert.True(t, reserved)
	reloaded := getTokenFromDB(t, token.Id)
	assert.Equal(t, 55, reloaded.RemainQuota)
	assert.Equal(t, 25, reloaded.UsedQuota)

	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 56, false)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 55, getTokenFromDB(t, token.Id).RemainQuota)
}

func TestRedisBatchReserveImmediatelyCommitsDatabaseBalance(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true

	user := createReserveTestUser(t, 10)
	reserved, err := TryReserveUserQuota(user.Id, 8)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 2, getUserQuotaFromDB(t, user.Id), "wallet reservations must commit before returning, even with batching")

	reserved, err = TryReserveUserQuota(user.Id, 3)
	require.NoError(t, err)
	assert.False(t, reserved, "stale DB balance must not authorize a second spend")
	cachedUser, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 2, cachedUser.Quota)

	token := createReserveTestToken(t, 9)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 7, false)
	require.NoError(t, err)
	assert.True(t, reserved)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 3, false)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 2, getTokenFromDB(t, token.Id).RemainQuota)

	batchUpdate()
	assert.Equal(t, 2, getUserQuotaFromDB(t, user.Id))
	reloadedToken := getTokenFromDB(t, token.Id)
	assert.Equal(t, 2, reloadedToken.RemainQuota)
	assert.Equal(t, 7, reloadedToken.UsedQuota)
}

func TestWalletChargesCommitBeforeStatisticsFlush(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	common.BatchUpdateEnabled = true

	user := createReserveTestUser(t, common.MaxQuota*2+100)
	require.NoError(t, DecreaseUserQuota(user.Id, common.MaxQuota, false))
	require.NoError(t, DecreaseUserQuota(user.Id, common.MaxQuota, false))
	assert.Equal(t, 100, getUserQuotaFromDB(t, user.Id))

	batchUpdate()
	assert.Equal(t, 100, getUserQuotaFromDB(t, user.Id))
}

func TestBatchUpdateAccumulatorSaturatesOverflow(t *testing.T) {
	resetBatchUpdateTestState(t)

	addNewRecord(BatchUpdateTypeUsedQuota, 1, math.MaxInt)
	addNewRecord(BatchUpdateTypeUsedQuota, 1, 1)
	batchUpdateLocks[BatchUpdateTypeUsedQuota].Lock()
	assert.Equal(t, math.MaxInt, batchUpdateStores[BatchUpdateTypeUsedQuota][1])
	batchUpdateLocks[BatchUpdateTypeUsedQuota].Unlock()

	batchUpdateLocks[BatchUpdateTypeUsedQuota].Lock()
	batchUpdateStores[BatchUpdateTypeUsedQuota] = make(map[int]int)
	batchUpdateLocks[BatchUpdateTypeUsedQuota].Unlock()
	addNewRecord(BatchUpdateTypeUsedQuota, 1, math.MinInt)
	addNewRecord(BatchUpdateTypeUsedQuota, 1, -1)
	batchUpdateLocks[BatchUpdateTypeUsedQuota].Lock()
	assert.Equal(t, math.MinInt, batchUpdateStores[BatchUpdateTypeUsedQuota][1])
	batchUpdateLocks[BatchUpdateTypeUsedQuota].Unlock()
}

func TestReserveCommitsWhenRedisIsUnavailable(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)

	user := createReserveTestUser(t, 20)
	require.NoError(t, populateUserCache(user))
	server.Close()

	// Redis availability does not change the reservation authority.
	reserved, err := TryReserveUserQuota(user.Id, 5)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 15, getUserQuotaFromDB(t, user.Id))

	reserved, err = TryReserveUserQuota(user.Id, 16)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 15, getUserQuotaFromDB(t, user.Id))
}

func testUSDQuotaDeletedRecords(t *testing.T) {
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)

	user := createReserveTestUser(t, 10)
	require.NoError(t, populateUserCache(user))
	require.NoError(t, DB.Delete(&user).Error)

	reserved, err := TryReserveUserQuota(user.Id, 6)
	assert.False(t, reserved)
	assert.NoError(t, err)
	_, err = GetUserCache(user.Id)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	assert.ErrorIs(t, IncreaseUserQuota(user.Id, 6, false), gorm.ErrRecordNotFound)

	token := createReserveTestToken(t, 12)
	_, err = GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	require.NoError(t, DB.Delete(&token).Error)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 7, false)
	assert.False(t, reserved)
	assert.NoError(t, err)
	_, err = GetTokenByKey(token.Key, false)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	assert.ErrorIs(t, IncreaseTokenQuota(token.Id, token.Key, 7), gorm.ErrRecordNotFound)
}

func TestTokenCacheStaleQuotaIsNotAuthoritativeAndFenceBlocksSnapshots(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)

	token := createReserveTestToken(t, 100)
	loaded, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	stale := *loaded

	reserved, err := TryReserveTokenQuota(token.Id, token.Key, 70, false)
	require.NoError(t, err)
	require.True(t, reserved)

	// A delayed pre-reservation cache fill cannot authorize the old balance.
	code, err := cacheInitToken(stale)
	require.NoError(t, err)
	assert.Equal(t, 1, code)
	cached, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, 30, cached.RemainQuota)

	// 变更期间：fence 删除缓存并拦截并发读者手中的过期快照。
	require.NoError(t, invalidateTokenCacheForMutation(token.Key))
	code, err = cacheInitToken(stale)
	require.NoError(t, err)
	assert.Zero(t, code, "the pre-mutation snapshot must not be published while fenced")
	_, err = cacheGetTokenByKey(token.Key)
	assert.Error(t, err)

	// fence 过期后可重新从数据库水合。
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	fresh, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, 30, fresh.RemainQuota)
	cached, err = cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, 30, cached.RemainQuota)
}

func testUSDQuotaBoundaries(t *testing.T) {
	user, token := createReserveTestUser(t, 100), createReserveTestToken(t, 100)
	for _, quota := range []int{-1, common.MaxWalletQuota + 1} {
		reserved, err := TryReserveUserQuota(user.Id, quota)
		assert.Error(t, err)
		assert.False(t, reserved)
		reserved, err = TryReserveTokenQuota(token.Id, token.Key, quota, true)
		assert.Error(t, err)
		assert.False(t, reserved)
		assert.Error(t, IncreaseUserQuota(user.Id, quota, false))
		assert.Error(t, DecreaseUserQuota(user.Id, quota, false))
		assert.Error(t, IncreaseTokenQuota(token.Id, token.Key, quota))
		assert.Error(t, DecreaseTokenQuota(token.Id, token.Key, quota))
	}
	assert.ErrorIs(t, DeltaUpdateUserQuota(user.Id, math.MinInt), ErrWalletQuotaLimitExceeded)
	assert.Equal(t, 100, getUserQuotaFromDB(t, user.Id))
	assert.Equal(t, 100, getTokenFromDB(t, token.Id).RemainQuota)
	// Settlement can exceed the reservation; keep the existing debt semantics.
	require.NoError(t, DecreaseUserQuota(user.Id, 101, false))
	require.NoError(t, DecreaseTokenQuota(token.Id, token.Key, 101))
	assert.Equal(t, -1, getUserQuotaFromDB(t, user.Id))
	assert.Equal(t, -1, getTokenFromDB(t, token.Id).RemainQuota)
	require.NoError(t, IncreaseUserQuota(user.Id, 101, false))
	require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 101))
	assert.Equal(t, 100, getUserQuotaFromDB(t, user.Id))
	assert.Equal(t, 100, getTokenFromDB(t, token.Id).RemainQuota)
}
