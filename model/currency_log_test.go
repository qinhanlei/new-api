package model

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
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

func TestCNYLogDatabaseMatrix(t *testing.T) {
	for _, dialect := range []common.DatabaseType{common.DatabaseTypeSQLite, common.DatabaseTypeMySQL, common.DatabaseTypePostgreSQL} {
		t.Run(string(dialect), func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case common.DatabaseTypeSQLite:
				driver = sqlite.Open(":memory:")
			case common.DatabaseTypeMySQL:
				if os.Getenv("TEST_MYSQL_DSN") == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
			case common.DatabaseTypePostgreSQL:
				if os.Getenv("TEST_POSTGRES_DSN") == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(os.Getenv("TEST_POSTGRES_DSN"))
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "cny_log_"}, Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			connection, err := db.DB()
			require.NoError(t, err)
			connection.SetMaxOpenConns(1)
			unavailableLogs, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			logConnection, err := unavailableLogs.DB()
			require.NoError(t, err)
			require.NoError(t, logConnection.Close())
			oldDB, oldLogs := DB, LOG_DB
			oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
			oldConsume := common.LogConsumeEnabled
			DB, LOG_DB, common.LogConsumeEnabled = db, unavailableLogs, false
			// The main DB LIKE escaping must not follow the separate log dialect.
			common.SetDatabaseTypes(dialect, common.DatabaseTypeClickHouse)
			initCol()
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&CNYBillingRecord{}, &Channel{}))
				DB, LOG_DB, common.LogConsumeEnabled = oldDB, oldLogs, oldConsume
				common.SetDatabaseTypes(oldMainType, oldLogType)
				initCol()
				require.NoError(t, connection.Close())
			})
			require.NoError(t, db.AutoMigrate(&CNYBillingRecord{}, &Channel{}))
			var version string
			if dialect == common.DatabaseTypeSQLite {
				require.NoError(t, db.Raw("SELECT sqlite_version()").Scan(&version).Error)
			} else {
				require.NoError(t, db.Raw("SELECT version()").Scan(&version).Error)
			}
			t.Logf("%s version: %s", dialect, version)
			testCNYLogQueries(t)
			testCNYLogExport(t)
		})
	}
}

func testCNYLogQueries(t *testing.T) {
	now := common.GetTimestamp()
	owner := 1
	expression := `v2:CNY:u("tokens") * 10.43 / 1000000`
	snapshot, err := common.Marshal(billingexpr.BillingSnapshot{
		Currency: "CNY", BillingMode: "tiered_expr", ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), ExprVersion: 2,
		QuotaPerUnit: CNYQuotaPerUnit, GroupRatio: 1, EstimatedTier: "estimated", UsageFacts: map[string]any{"tokens": 1000000},
	})
	require.NoError(t, err)
	rows := []CNYBillingRecord{
		{RequestID: "settled", Status: CNYBillingSettled, Quota: 10000, ReservedQuota: 8000, PromptTokens: 10, CompletionTokens: 2, CreatedAt: now - 30, Other: `{"admin_info":{"admin":"visible"},"root_info":{"route":"private"},"channel_name":"legacy-private","matched_tier":"actual","exact":9007199254740993}`},
		{RequestID: "pending", Status: CNYBillingSubmitted, Quota: 99, ReservedQuota: 20000, CreatedAt: now - 20, TaskID: "task-pending"},
		{RequestID: "pending-free", Status: CNYBillingReserved, CreatedAt: now - 10},
		{RequestID: "settled-free", Status: CNYBillingSettled, CreatedAt: now - 100},
		{RequestID: "refunded", Status: CNYBillingRefunded, Quota: 9000, ReservedQuota: 3000, RefundQuota: 9000, PromptTokens: 3, CompletionTokens: 1, CreatedAt: now - 40},
		{RequestID: "adjustment", Kind: CNYBillingAdjustment, Status: CNYBillingSettled, DeltaQuota: 50000, BeforeQuota: 100, AfterQuota: 50100, Reason: "Manual CNY credit", CreatedAt: now - 5},
		{RequestID: "foreign", UserID: 2, Username: "aliceX1", ModelName: "videoXa", Status: CNYBillingSettled, Quota: 70000, PromptTokens: 90, CompletionTokens: 10, CreatedAt: now - 15},
		{RequestID: "old", Status: CNYBillingSettled, Quota: 5000, CreatedAt: now - 300, SettledAt: now},
	}
	for index := range rows {
		row := &rows[index]
		row.Currency, row.QuotaPerUnit, row.ChannelID, row.TokenID, row.TokenName, row.Group = "CNY", CNYQuotaPerUnit, 7, 11, "key-a", "paid"
		row.Snapshot = string(snapshot)
		if row.Kind == "" {
			row.Kind = CNYBillingRequest
		}
		if row.UserID == 0 {
			row.UserID, row.Username, row.ModelName = owner, "alice_1", "video_a"
		}
	}
	require.NoError(t, DB.Create(&rows).Error)
	require.NoError(t, DB.Create(&Channel{Id: 7, Name: "Native channel"}).Error)
	filter := LogFilter{StartTimestamp: now - 200, EndTimestamp: now}
	logs, total, err := GetUserCNYLogs(owner, filter, 0, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(6), total)
	require.Len(t, logs, 6)
	byRequest := make(map[string]*Log)
	for index, log := range logs {
		assert.Equal(t, index+1, log.Id)
		assert.Equal(t, owner, log.UserId)
		assert.Equal(t, "CNY", log.Currency)
		assert.Empty(t, log.ChannelName)
		assert.NotContains(t, log.Other, "admin_info")
		assert.NotContains(t, log.Other, "root_info")
		assert.NotContains(t, log.Other, "channel_name")
		byRequest[log.RequestId] = log
	}
	assert.Contains(t, byRequest["settled"].Other, "9007199254740993")
	assert.Contains(t, byRequest["settled"].Other, `"matched_tier":"actual"`)
	assert.Zero(t, byRequest["refunded"].Quota)
	assert.Equal(t, LogTypeRefund, byRequest["refunded"].Type)
	assert.Equal(t, LogTypeManage, byRequest["adjustment"].Type)
	assert.Equal(t, 50000, byRequest["adjustment"].Quota)
	var adjustment, pending map[string]any
	require.NoError(t, common.UnmarshalJsonStr(byRequest["adjustment"].Other, &adjustment))
	assert.Equal(t, float64(50000), adjustment["delta_quota"])
	assert.Equal(t, float64(100), adjustment["before_quota"])
	assert.Equal(t, float64(50100), adjustment["after_quota"])
	assert.Equal(t, "Manual CNY credit", adjustment["reason"])
	require.NoError(t, common.UnmarshalJsonStr(byRequest["pending"].Other, &pending))
	assert.Zero(t, byRequest["pending"].Quota)
	assert.Equal(t, "reserved", pending["billing_status"])
	assert.Equal(t, float64(20000), pending["reserved_quota"])
	assert.Equal(t, float64(CNYQuotaPerUnit), pending["quota_per_unit"])
	assert.Equal(t, "task-pending", pending["task_id"])
	assert.Equal(t, "estimated", pending["matched_tier"])
	assert.Equal(t, map[string]any{"tokens": float64(1000000)}, pending["usage_facts"])
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte(expression)), pending["expr_b64"])

	for _, tc := range []struct {
		name   string
		filter LogFilter
		count  int64
	}{
		{"consume", LogFilter{Type: LogTypeConsume}, 6},
		{"refund", LogFilter{Type: LogTypeRefund}, 1},
		{"adjustment", LogFilter{Type: LogTypeManage}, 1},
		{"unsupported type", LogFilter{Type: LogTypeLogin}, 0},
		{"model wildcard escapes underscore", LogFilter{ModelName: "video_a%"}, 7},
		{"username wildcard escapes underscore", LogFilter{Username: "alice_1%"}, 7},
		{"exact model", LogFilter{ModelName: "videoXa"}, 1},
		{"request", LogFilter{RequestId: "pending"}, 1},
		{"unpersisted upstream request", LogFilter{UpstreamRequestId: "missing"}, 0},
		{"wrong token", LogFilter{TokenName: "key-b"}, 0},
		{"wrong group", LogFilter{Group: "other"}, 0},
		{"wrong channel", LogFilter{Channel: 8}, 0},
		{"combined", LogFilter{Type: LogTypeConsume, ModelName: "video_a", TokenName: "key-a", Group: "paid", Channel: 7, StartTimestamp: now - 30, EndTimestamp: now - 10}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, total, err := GetAllCNYLogs(tc.filter, 0, 100)
			require.NoError(t, err)
			assert.Equal(t, tc.count, total)
			assert.Len(t, found, int(tc.count))
			for _, log := range found {
				assert.Equal(t, "Native channel", log.ChannelName)
			}
		})
	}
	_, _, err = GetAllCNYLogs(LogFilter{ModelName: "%%"}, 0, 10)
	require.Error(t, err)
	_, total, err = GetUserCNYLogs(owner, LogFilter{RequestId: "foreign"}, 0, 100)
	require.NoError(t, err)
	assert.Zero(t, total)
	_, total, err = GetUserCNYLogs(0, LogFilter{}, 0, 100)
	require.NoError(t, err)
	assert.Zero(t, total)
	stat, err := SumCNYUsedQuota(filter, &owner)
	require.NoError(t, err)
	assert.Equal(t, CNYLogStat{Currency: "CNY", Quota: 10000, PendingQuota: 20000, Count: 5, Rpm: 4, Tpm: 16, QuotaPerUnit: CNYQuotaPerUnit}, stat)
	stat, err = SumCNYUsedQuota(filter, nil)
	require.NoError(t, err)
	assert.Equal(t, CNYLogStat{Currency: "CNY", Quota: 80000, PendingQuota: 20000, Count: 6, Rpm: 5, Tpm: 116, QuotaPerUnit: CNYQuotaPerUnit}, stat)
	for _, empty := range []LogFilter{{Type: LogTypeManage}, {RequestId: "foreign", Username: "alice_1"}, {UpstreamRequestId: "missing"}, {RequestId: "refunded"}} {
		stat, err = SumCNYUsedQuota(empty, &owner)
		require.NoError(t, err)
		assert.Zero(t, stat.Quota)
		assert.Zero(t, stat.PendingQuota)
	}
	for _, role := range []int{common.RoleAdminUser, common.RoleRootUser} {
		require.NoError(t, StreamAllCNYLogs(t.Context(), LogFilter{RequestId: "settled"}, role, func(log *Log) error {
			assert.Contains(t, log.Other, "admin_info")
			if role == common.RoleRootUser {
				assert.Contains(t, log.Other, "root_info")
			} else {
				assert.NotContains(t, log.Other, "root_info")
			}
			return nil
		}))
	}
}

func testCNYLogExport(t *testing.T) {
	require.NoError(t, DB.Create(&CNYBillingRecord{Kind: CNYBillingRequest, RequestID: "foreign-export", UserID: 2,
		Currency: "CNY", QuotaPerUnit: CNYQuotaPerUnit, ModelName: "export", Status: CNYBillingSettled, CreatedAt: 100}).Error)
	// One row beyond the batch size exercises equal-timestamp cursor boundaries.
	rows := make([]CNYBillingRecord, 257)
	for index := range rows {
		rows[index] = CNYBillingRecord{Kind: CNYBillingRequest, RequestID: fmt.Sprintf("export-%03d", index), UserID: 1,
			Currency: "CNY", QuotaPerUnit: CNYQuotaPerUnit, ModelName: "export", Status: CNYBillingSettled, CreatedAt: 100}
	}
	require.NoError(t, DB.Create(&rows).Error)
	var requests []string
	err := StreamUserCNYLogs(t.Context(), 1, LogFilter{ModelName: "export"}, func(log *Log) error {
		requests = append(requests, log.RequestId)
		assert.Equal(t, len(requests), log.Id)
		if len(requests) == 1 {
			return DB.Create(&CNYBillingRecord{Kind: CNYBillingRequest, RequestID: "created-during-export", UserID: 1,
				Currency: "CNY", QuotaPerUnit: CNYQuotaPerUnit, ModelName: "export", Status: CNYBillingSettled, CreatedAt: 99}).Error
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, requests, len(rows))
	assert.Equal(t, "export-256", requests[0])
	assert.Equal(t, "export-000", requests[256])
	assert.NotContains(t, requests, "created-during-export")
	stop := errors.New("writer disconnected")
	err = StreamUserCNYLogs(t.Context(), 1, LogFilter{ModelName: "export"}, func(*Log) error { return stop })
	require.ErrorIs(t, err, stop)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = StreamUserCNYLogs(ctx, 1, LogFilter{ModelName: "export"}, func(*Log) error { t.Fatal("canceled export called writer"); return nil })
	require.ErrorIs(t, err, context.Canceled)
}
