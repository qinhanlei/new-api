package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestSpecialWalletDatabaseMatrix(t *testing.T) {
	for _, engine := range []struct {
		kind common.DatabaseType
		env  string
	}{
		{common.DatabaseTypeSQLite, ""}, {common.DatabaseTypeMySQL, "TEST_MYSQL_DSN"}, {common.DatabaseTypePostgreSQL, "TEST_POSTGRES_DSN"},
	} {
		t.Run(string(engine.kind), func(t *testing.T) {
			var driver gorm.Dialector = sqlite.Open(":memory:")
			if engine.env != "" {
				dsn := os.Getenv(engine.env)
				if dsn == "" {
					t.Skip(engine.env + " not configured")
				}
				if engine.kind == common.DatabaseTypeMySQL {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "special_wallet_matrix_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			oldDB, oldLogDB, oldLogs, oldScale := model.DB, model.LOG_DB, common.LogConsumeEnabled, common.QuotaPerUnit
			oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
			model.DB, model.LOG_DB, common.LogConsumeEnabled, common.QuotaPerUnit = db, nil, false, 500000
			common.SetDatabaseTypes(engine.kind, engine.kind)
			tables := []any{&model.Midjourney{}, &model.WalletFundingRecord{}, &model.CNYWallet{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{}, &model.User{}, &model.Token{}, &model.Channel{}}
			require.NoError(t, db.Migrator().DropTable(tables...))
			require.NoError(t, db.AutoMigrate(tables...))
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(tables...))
				require.NoError(t, sqlDB.Close())
				model.DB, model.LOG_DB, common.LogConsumeEnabled, common.QuotaPerUnit = oldDB, oldLogDB, oldLogs, oldScale
				common.SetDatabaseTypes(oldMain, oldLog)
			})
			policy := model.DefaultWalletExchangePolicy()
			policy.StaticRate, policy.CrossCurrencyEnabled = "7", true
			require.NoError(t, db.Create(&policy).Error)
			for _, test := range []struct {
				name string
				usd  int
			}{{"cny-only", 0}, {"mixed", 400}} {
				t.Run(test.name, func(t *testing.T) {
					c, info := specialWalletFixture(t, db, test.name, test.usd)
					require.NoError(t, ReserveMidjourneyWallet(c, info, 1000))
					require.NoError(t, MarkCNYUpstreamPending(info, true))
					record, err := model.GetWalletFundingRecord(info.RequestId)
					require.NoError(t, err)
					assert.Equal(t, (1000-test.usd)*7, record.AlternateReserved)
					require.NoError(t, db.Model(&model.WalletExchangePolicy{}).Where("id = 1").Updates(map[string]any{"static_rate": "2", "cross_currency_enabled": false}).Error)
					task := &model.Midjourney{UserId: info.UserId, ChannelId: info.ChannelId, MjId: test.name, Status: "SUBMITTED"}
					prepared, err := PrepareMidjourneyTaskBilling(info, task, 1000, true)
					require.NoError(t, err)
					require.True(t, prepared)
					// Rollback an accepted task's insertion: its money stays held.
					require.NoError(t, db.Callback().Create().Before("gorm:create").Register("fail_mj_insert", func(tx *gorm.DB) {
						if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Midjourney" {
							tx.AddError(fmt.Errorf("injected insert failure"))
						}
					}))
					_, err = PersistMidjourneyTaskBilling(info, task, prepared)
					require.Error(t, err)
					require.NoError(t, db.Callback().Create().Remove("fail_mj_insert"))
					info.Billing.Refund(c)
					record, err = model.GetWalletFundingRecord(info.RequestId)
					require.NoError(t, err)
					assert.True(t, record.UpstreamPending)
					assert.Equal(t, model.CNYBillingReserved, record.Status)
					created, err := PersistMidjourneyTaskBilling(info, task, prepared)
					require.NoError(t, err)
					require.True(t, created)
					created, err = PersistMidjourneyTaskBilling(info, task, prepared)
					require.NoError(t, err)
					assert.False(t, created)
					var user model.User
					require.NoError(t, db.First(&user, info.UserId).Error)
					assert.Equal(t, 1000, user.UsedQuota)
					assert.Equal(t, 1, user.RequestCount)
					task.Status, task.Progress = "FAILURE", "100%"
					require.NoError(t, db.Callback().Update().Before("gorm:update").Register("fail_mj_refund", func(tx *gorm.DB) {
						if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Channel" {
							tx.AddError(fmt.Errorf("injected refund failure"))
						}
					}))
					_, err = model.RefundWalletMidjourney(task, "SUBMITTED")
					require.Error(t, err)
					require.NoError(t, db.Callback().Update().Remove("fail_mj_refund"))
					var persisted model.Midjourney
					require.NoError(t, db.First(&persisted, task.Id).Error)
					assert.Equal(t, "SUBMITTED", persisted.Status)
					assert.Equal(t, 1000, persisted.Quota)
					// In-flight financial completion survives soft deletion.
					require.NoError(t, db.Delete(&user).Error)
					won, err := model.RefundWalletMidjourney(task, "SUBMITTED")
					require.NoError(t, err)
					assert.True(t, won)
					won, err = model.RefundWalletMidjourney(task, "SUBMITTED")
					require.NoError(t, err)
					assert.False(t, won)
					require.NoError(t, db.Unscoped().First(&user, info.UserId).Error)
					assert.Equal(t, test.usd, user.Quota)
					assert.Zero(t, user.UsedQuota)
					assert.Equal(t, 1, user.RequestCount)
					var wallet model.CNYWallet
					require.NoError(t, db.Where("user_id = ?", info.UserId).First(&wallet).Error)
					assert.Equal(t, 10000, wallet.Quota)
					assert.Zero(t, wallet.ReservedQuota)
					var token model.Token
					require.NoError(t, db.First(&token, info.TokenId).Error)
					assert.Equal(t, 10000, token.RemainQuota)
					assert.Zero(t, token.UsedQuota)
					require.NoError(t, db.Model(&model.WalletExchangePolicy{}).Where("id = 1").Updates(map[string]any{"static_rate": "7", "cross_currency_enabled": true}).Error)
				})
			}
			t.Run("unknown-submit-keeps-funds", func(t *testing.T) {
				InitHttpClient()
				c, info := specialWalletFixture(t, db, "unknown-submit", 0)
				require.NoError(t, ReserveMidjourneyWallet(c, info, 1000))
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
				}))
				defer server.Close()
				_, _, err := DoMidjourneyHttpRequest(c, time.Second, server.URL, info)
				require.Error(t, err)
				info.Billing.Refund(c)
				record, err := model.GetWalletFundingRecord(info.RequestId)
				require.NoError(t, err)
				assert.True(t, record.UpstreamPending)
				assert.Equal(t, model.CNYBillingReserved, record.Status)
			})
			t.Run("realtime-cumulative", func(t *testing.T) {
				c, info := specialWalletFixture(t, db, "realtime", 0)
				info.PriceData = hosttypes.PriceData{ModelRatio: 1, CompletionRatio: 1, AudioRatio: 2, AudioCompletionRatio: 3, QuotaToPreConsume: 100, GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}}
				require.NoError(t, ReserveMidjourneyWallet(c, info, 100))
				require.NoError(t, MarkCNYUpstreamPending(info, true))
				usage := &dto.RealtimeUsage{TotalTokens: 300, InputTokens: 300, InputTokenDetails: dto.InputTokenDetails{TextTokens: 300}}
				require.NoError(t, PreWssConsumeQuota(c, info, usage))
				require.NoError(t, PreWssConsumeQuota(c, info, usage))
				require.NoError(t, db.Model(&model.WalletExchangePolicy{}).Where("id = 1").Update("static_rate", "2").Error)
				PostWssConsumeQuota(c, info, "test", usage, nil)
				PostWssConsumeQuota(c, info, "test", usage, nil)
				record, err := model.GetWalletFundingRecord(info.RequestId)
				require.NoError(t, err)
				assert.Equal(t, 300, record.Quota)
				assert.Equal(t, 2100, record.AlternateCharged)
				var user model.User
				require.NoError(t, db.First(&user, info.UserId).Error)
				assert.Equal(t, 300, user.UsedQuota)
				assert.Equal(t, 1, user.RequestCount)
			})
		})
	}
}

func specialWalletFixture(t *testing.T, db *gorm.DB, name string, usd int) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	user := model.User{Username: name, AffCode: name, Status: common.UserStatusEnabled, Quota: usd}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Name: name, Key: fmt.Sprintf("%-48s", name), Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 10000}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: name, Key: "test"}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.CNYWallet{UserID: user.Id, Quota: 10000, QuotaPerUnit: 500000}).Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/mj/submit", strings.NewReader(`{"prompt":"test"}`))
	info := &relaycommon.RelayInfo{RequestId: name, UserId: user.Id, TokenId: token.Id, TokenKey: token.Key, StartTime: time.Now(), OriginModelName: "test", UsingGroup: "default", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id}}
	return c, info
}
