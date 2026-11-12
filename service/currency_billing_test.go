package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func TestWalletViolationFeeUsesFrozenFunding(t *testing.T) {
	truncate(t)
	seedUser(t, 1, 100)
	seedToken(t, 1, 1, "fee-key", 10000)
	seedChannel(t, 1)
	previousScale, previousSettings := common.QuotaPerUnit, *model_setting.GetGrokSettings()
	t.Cleanup(func() { common.QuotaPerUnit = previousScale; *model_setting.GetGrokSettings() = previousSettings })
	common.QuotaPerUnit = 500000
	*model_setting.GetGrokSettings() = model_setting.GrokSettings{ViolationDeductionEnabled: true, ViolationDeductionAmount: 0.001}
	_, err := model.AdjustCNYWallet(1, 99, common.RoleRootUser, 67000, "fee-seed", "test")
	require.NoError(t, err)
	policy := model.DefaultWalletExchangePolicy()
	policy.StaticRate, policy.CrossCurrencyEnabled = "6.7", true
	_, err = model.SaveWalletExchangePolicy(policy, nil)
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{UserId: 1, TokenId: 1, TokenKey: "fee-key", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1}, RequestId: "wallet-fee", StartTime: time.Now(), PriceData: hosttypes.PriceData{GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}}}
	session, apiErr := NewWalletBillingSession(c, info, 1000)
	require.Nil(t, apiErr)
	info.Billing = session
	require.NoError(t, MarkCNYUpstreamPending(info, true))
	assert.Error(t, session.ReserveBeforeSubmit(1000), "pending retry is blocked even without an increased estimate")
	common.QuotaPerUnit = 1000000
	providerErr := types.NewErrorWithStatusCode(io.EOF, types.ErrorCodeViolationFeeGrokCSAM, http.StatusBadRequest)
	require.True(t, ChargeViolationFeeIfNeeded(c, info, providerErr))
	session.Refund(c)
	assert.False(t, ChargeViolationFeeIfNeeded(c, info, providerErr))
	record, err := model.GetWalletFundingRecord(info.RequestId)
	require.NoError(t, err)
	assert.Equal(t, model.CNYBillingSettled, record.Status)
	assert.Equal(t, 500, record.Quota)
	assert.Equal(t, 100, record.NativeCharged)
	assert.Equal(t, 2680, record.AlternateCharged)
	assert.Equal(t, 9500, getTokenRemainQuota(t, 1))
	used, count := getUserUsageAccounting(t, 1)
	assert.Equal(t, 500, used)
	assert.Equal(t, 1, count)
}

func TestCNYServiceDatabaseMatrix(t *testing.T) {
	for _, database := range []struct {
		kind common.DatabaseType
		env  string
	}{
		{common.DatabaseTypeSQLite, ""}, {common.DatabaseTypeMySQL, "TEST_MYSQL_DSN"}, {common.DatabaseTypePostgreSQL, "TEST_POSTGRES_DSN"},
	} {
		t.Run(string(database.kind), func(t *testing.T) {
			var driver gorm.Dialector = sqlite.Open(":memory:")
			if database.env != "" {
				dsn := os.Getenv(database.env)
				if dsn == "" {
					t.Skip(database.env + " not configured")
				}
				if database.kind == common.DatabaseTypeMySQL {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "cny_service_"}, Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousLogs, previousEnabled := model.DB, model.LOG_DB, common.LogConsumeEnabled
			mainKind, logKind := common.MainDatabaseType(), common.LogDatabaseType()
			previousFX := operation_setting.USDExchangeRate
			model.DB, model.LOG_DB, common.LogConsumeEnabled = db, db, false
			common.SetDatabaseTypes(database.kind, database.kind)
			tables := []any{&model.CNYBillingRecord{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{}, &model.CNYTokenBudget{}, &model.CNYWallet{}, &model.Task{}, &model.Channel{}, &model.Token{}, &model.BillingRateRule{}, &model.User{}, &model.Log{}}
			require.NoError(t, db.Migrator().DropTable(tables...))
			require.NoError(t, db.AutoMigrate(tables...))
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(tables...))
				require.NoError(t, sqlDB.Close())
				model.DB, model.LOG_DB, common.LogConsumeEnabled = previousDB, previousLogs, previousEnabled
				common.SetDatabaseTypes(mainKind, logKind)
				operation_setting.USDExchangeRate = previousFX
			})
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "cny_service_user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 123456, UsedQuota: 234}).Error)
			require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: strings.Repeat("a", 48), Name: "cny_service_token", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 0}).Error)
			require.NoError(t, db.Create(&model.Channel{Id: 1, Name: "cny_service_channel", Key: "test", UsedQuota: 456}).Error)
			_, err = model.AdjustCNYWallet(1, 99, common.RoleRootUser, 500000, "initial", "test")
			require.NoError(t, err)
			_, err = model.UpdateCNYTokenBudget(1, 1, model.CNYTokenBudgetChange{Enabled: true, RemainQuota: 500000})
			require.NoError(t, err)

			c, info := cnyServiceRequest("text", "v2:CNY:p * 10 + c * 20", 10000)
			require.Nil(t, PreConsumeBilling(c, 10000, info))
			operation_setting.USDExchangeRate = 100
			PostTextConsumeQuota(c, info, &dto.Usage{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100}, nil)
			// Replayed usage is idempotent, even after the display rate changes.
			operation_setting.USDExchangeRate = 0.5
			PostTextConsumeQuota(c, info, &dto.Usage{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100}, nil)
			record, err := model.GetCNYBillingRecord("text")
			require.NoError(t, err)
			assert.Equal(t, model.CNYBillingSettled, record.Status)
			assert.Equal(t, 6000, record.Quota)
			assert.Contains(t, record.Other, "expr_b64")
			wallet, err := model.GetCNYWallet(1)
			require.NoError(t, err)
			assert.Equal(t, 494000, wallet.Quota)
			assert.Equal(t, 6000, wallet.UsedQuota)
			assert.Zero(t, wallet.ReservedQuota)
			budget, err := model.GetCNYTokenBudget(1, 1)
			require.NoError(t, err)
			assert.Equal(t, 494000, budget.RemainQuota)

			c, info = cnyServiceRequest("free", "v2:CNY:0", 0)
			require.Nil(t, PreConsumeBilling(c, 0, info))
			PostTextConsumeQuota(c, info, &dto.Usage{PromptTokens: 1, TotalTokens: 1}, nil)
			record, err = model.GetCNYBillingRecord("free")
			require.NoError(t, err)
			assert.Equal(t, model.CNYBillingSettled, record.Status)
			assert.Zero(t, record.Quota)

			c, info = cnyServiceRequest("unknown", "v2:CNY:p * 10", 500)
			require.Nil(t, PreConsumeBilling(c, 500, info))
			MarkCNYUpstreamPending(info, true)
			info.BillingUsageUnknown = true
			PostTextConsumeQuota(c, info, &dto.Usage{PromptTokens: 100, CompletionTokens: 100, TotalTokens: 200}, nil)
			info.Billing.Refund(c)
			assert.Error(t, info.Billing.ReserveBeforeSubmit(500))
			record, err = model.GetCNYBillingRecord("unknown")
			require.NoError(t, err)
			assert.Equal(t, model.CNYBillingReserved, record.Status)
			ObserveCNYHTTPResponse(info, 429)
			info.Billing.Refund(c)
			info.Billing.Refund(c)
			record, err = model.GetCNYBillingRecord("unknown")
			require.NoError(t, err)
			assert.Equal(t, model.CNYBillingRefunded, record.Status)

			for _, failed := range []bool{false, true} {
				id := "task-success"
				if failed {
					id = "task-failure"
				}
				_, _, err = model.UpdateBillingRateRules(1, 99, []model.BillingRateChange{{ModelName: id, Rate: common.GetPointer("0.5")}})
				require.NoError(t, err)
				rate, err := model.ResolveBillingRate(1, id, hosttypes.GroupRatioInfo{GroupRatio: 0})
				require.NoError(t, err)
				c, info = cnyServiceRequest(id, `v2:CNY:u("tokens") * 10 / 1000000`, 5000)
				info.OriginModelName = id
				info.PriceData.GroupRatioInfo.GroupRatio = 0
				info.PriceData.BillingRate = rate
				info.TieredBillingSnapshot.GroupRatio = 0
				info.TieredBillingSnapshot.BillingRate = rate
				info.TieredBillingSnapshot.TaskUsageBilling = true
				info.TieredBillingSnapshot.UsageFacts = map[string]any{"tokens": float64(2000)}
				require.Nil(t, PreConsumeBilling(c, 5000, info))
				task := &model.Task{TaskID: id, UserId: 1, ChannelId: 1, Status: model.TaskStatusSubmitted, SubmitTime: time.Now().Unix() - 3600,
					PrivateData: model.TaskPrivateData{TokenId: 1, Execution: &model.TaskExecutionSnapshot{RequestID: id}, BillingContext: &model.TaskBillingContext{BillingRate: rate, TieredSnapshot: info.TieredBillingSnapshot}}}
				require.NoError(t, model.InsertCNYTask(context.Background(), task, nil))
				record, err = model.GetCNYBillingRecord(id)
				require.NoError(t, err)
				assert.Equal(t, model.CNYBillingSubmitted, record.Status)
				assert.Equal(t, 5000, record.ReservedQuota)
				// Completion after a rule edit must use the persisted submission rate.
				_, _, err = model.UpdateBillingRateRules(1, 99, []model.BillingRateChange{{ModelName: id, Rate: common.GetPointer("1.5"), ExpectedVersion: 1}})
				require.NoError(t, err)
				require.NoError(t, db.First(task, task.ID).Error)
				// Local expiry and failed polls must not infer a vendor failure.
				oldTimeout := constant.TaskTimeoutMinutes
				constant.TaskTimeoutMinutes = 1
				sweepTimedOutTasks(context.Background())
				constant.TaskTimeoutMinutes = oldTimeout
				require.NoError(t, failTaskFromPoll(context.Background(), nil, task, model.TaskStatusSubmitted, "query failed"))
				var persisted model.Task
				require.NoError(t, db.First(&persisted, task.ID).Error)
				assert.Equal(t, model.TaskStatus(model.TaskStatusAwaitingReview), persisted.Status)
				assert.Empty(t, model.GetAllUnFinishSyncTasks(1))
				assert.Empty(t, model.GetTimedOutUnfinishedTasks(time.Now().Unix(), 1))
				result := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, UsageFacts: map[string]any{"tokens": float64(1000)}}
				task.Status = model.TaskStatusSuccess
				if failed {
					task.Status = model.TaskStatusFailure
					result.Status = model.TaskStatusFailure
				}
				task.FinishTime = time.Now().Unix()
				require.NoError(t, finalizeCNYTask(context.Background(), task, model.TaskStatusAwaitingReview, result))
				// A competing poller losing the CAS cannot apply the funding delta twice.
				require.NoError(t, finalizeCNYTask(context.Background(), task, model.TaskStatusAwaitingReview, result))
				record, err = model.GetCNYBillingRecord(id)
				require.NoError(t, err)
				if failed {
					assert.Equal(t, model.CNYBillingRefunded, record.Status)
				} else {
					assert.Equal(t, model.CNYBillingSettled, record.Status)
					assert.Equal(t, 2500, record.Quota)
				}
				assert.Equal(t, billingexpr.CurrencyCNY, record.Currency)
				var storedSnapshot billingexpr.BillingSnapshot
				require.NoError(t, common.UnmarshalJsonStr(record.Snapshot, &storedSnapshot))
				assert.Equal(t, rate, storedSnapshot.BillingRate)
				var other struct {
					BillingRate *hosttypes.BillingRateSnapshot `json:"billing_rate"`
				}
				logs, total, err := model.GetUserCNYLogs(1, model.LogFilter{RequestId: id}, 0, 10)
				require.NoError(t, err)
				require.Equal(t, int64(1), total)
				require.Len(t, logs, 1)
				require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
				assert.Equal(t, rate, other.BillingRate)
			}
			// An output-only expression estimates zero, but still needs available CNY funds.
			require.NoError(t, db.Model(&model.CNYWallet{}).Where("user_id = ?", 1).Update("quota", 0).Error)
			c, info = cnyServiceRequest("empty-wallet", "v2:CNY:c * 8", 0)
			info.TieredBillingSnapshot.GroupRatio = 0
			info.TieredBillingSnapshot.BillingRate = &hosttypes.BillingRateSnapshot{Rate: 0.5, Source: "user"}
			require.NotNil(t, PreConsumeBilling(c, 0, info))
			require.NoError(t, db.Model(&model.CNYWallet{}).Where("user_id = ?", 1).Update("quota", 100).Error)
			require.NoError(t, db.Model(&model.CNYTokenBudget{}).Where("token_id = ?", 1).Update("remain_quota", 0).Error)
			c, info = cnyServiceRequest("empty-budget", "v2:CNY:c * 8", 0)
			info.TieredBillingSnapshot.GroupRatio = 0
			info.TieredBillingSnapshot.BillingRate = &hosttypes.BillingRateSnapshot{Rate: 0.5, Source: "user"}
			require.NotNil(t, PreConsumeBilling(c, 0, info))
			require.NoError(t, db.Model(&model.CNYWallet{}).Where("user_id = ?", 1).Update("quota", 0).Error)
			c, info = cnyServiceRequest("known-free", "v2:CNY:0", 0)
			require.Nil(t, PreConsumeBilling(c, 0, info))
			PostTextConsumeQuota(c, info, &dto.Usage{}, nil)
			record, err = model.GetCNYBillingRecord("known-free")
			require.NoError(t, err)
			assert.Equal(t, model.CNYBillingSettled, record.Status)
			assert.Zero(t, record.Quota)
			c, info = cnyServiceRequest("free-override", "v2:CNY:p * 10", 0)
			info.PriceData.BillingRate = &hosttypes.BillingRateSnapshot{Rate: 0, Source: "user"}
			info.TieredBillingSnapshot.BillingRate = info.PriceData.BillingRate
			require.Nil(t, PrepareBillingForSelectedGroup(c, info))
			require.NotNil(t, info.Billing)
			PostTextConsumeQuota(c, info, &dto.Usage{PromptTokens: 1000, TotalTokens: 1000}, nil)
			record, err = model.GetCNYBillingRecord("free-override")
			require.NoError(t, err)
			assert.Equal(t, model.CNYBillingSettled, record.Status)
			assert.Zero(t, record.Quota)
			// A free override still requires an enabled CNY token budget.
			require.NoError(t, db.Model(&model.CNYTokenBudget{}).Where("token_id = ?", 1).Update("enabled", false).Error)
			c, info = cnyServiceRequest("free-disabled-budget", "v2:CNY:p * 10", 0)
			info.PriceData.BillingRate = &hosttypes.BillingRateSnapshot{Rate: 0, Source: "user"}
			info.TieredBillingSnapshot.BillingRate = info.PriceData.BillingRate
			require.NotNil(t, PrepareBillingForSelectedGroup(c, info))
			var user model.User
			var token model.Token
			var channel model.Channel
			require.NoError(t, db.First(&user, 1).Error)
			require.NoError(t, db.First(&token, 1).Error)
			require.NoError(t, db.First(&channel, 1).Error)
			assert.Equal(t, 123456, user.Quota)
			assert.Equal(t, 234, user.UsedQuota)
			assert.Zero(t, user.RequestCount)
			assert.Zero(t, token.RemainQuota)
			assert.Zero(t, token.UsedQuota)
			assert.Equal(t, int64(456), channel.UsedQuota)

			policy := model.DefaultWalletExchangePolicy()
			policy.CrossCurrencyEnabled, policy.StaticRate = true, "6.7"
			_, err = model.SaveWalletExchangePolicy(policy, nil)
			require.NoError(t, err)
			require.NoError(t, db.Where("token_id = ?", 1).Delete(&model.CNYTokenBudget{}).Error)
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 1).UpdateColumn("remain_quota", 100).Error)
			c, info = cnyServiceRequest("fallback-key-limit", "v2:CNY:p * 13.4", 677)
			apiErr := PreConsumeBilling(c, 677, info)
			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
			assert.Equal(t, types.ErrorCodePreConsumeTokenQuotaFailed, apiErr.GetErrorCode())
			c, info = cnyServiceRequest("fallback-text", "v2:CNY:p * 13.4", 670)
			require.Nil(t, PreConsumeBilling(c, 670, info))
			PostTextConsumeQuota(c, info, &dto.Usage{PromptTokens: 50, TotalTokens: 50}, nil)
			record, err = model.GetCNYBillingRecord("fallback-text")
			require.NoError(t, err)
			assert.Equal(t, 335, record.Quota)
			require.NoError(t, db.First(&user, 1).Error)
			assert.Equal(t, 123406, user.Quota)
			require.NoError(t, db.First(&token, 1).Error)
			assert.Equal(t, 50, token.RemainQuota)
			assert.Equal(t, 50, token.UsedQuota)
		})
	}
}

func cnyServiceRequest(id, expression string, estimate int) (*gin.Context, *relaycommon.RelayInfo) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	c.Set("token_usd_exhausted", true)
	info := &relaycommon.RelayInfo{UserId: 1, TokenId: 1, RequestId: id, OriginModelName: "cny-test", UsingGroup: "default", StartTime: time.Now(), RelayMode: relayconstant.RelayModeChatCompletions,
		ChannelMeta:           &relaycommon.ChannelMeta{ChannelId: 1},
		PriceData:             hosttypes.PriceData{Currency: billingexpr.CurrencyCNY, GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{Currency: billingexpr.CurrencyCNY, BillingMode: "tiered_expr", ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000, EstimatedQuotaAfterGroup: estimate}}
	return c, info
}

func TestCNYAdmissionAndFinalBodyGuard(t *testing.T) {
	c, info := cnyServiceRequest("admission", "v2:CNY:p", 1)
	info.FundingCurrency = billingexpr.CurrencyUSD
	require.NotNil(t, ValidateBillingCurrency(c, info))
	info.FundingCurrency = ""
	info.UserSetting.BillingPreference = "subscription_only"
	require.NotNil(t, ValidateBillingCurrency(c, info))
	info.UserSetting.BillingPreference = "wallet_only"
	info.RelayMode = relayconstant.RelayModeImagesGenerations
	require.NotNil(t, ValidateBillingCurrency(c, info))
	info.RelayMode = relayconstant.RelayModeChatCompletions
	for _, body := range []string{
		`{"tools":[{"type":"web_search"}]}`, `{"tools":[{"googleSearch":{}}]}`, `{"web_search_options":{}}`, `{"enable_search":true}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "http://upstream.invalid", strings.NewReader(body))
		assert.Error(t, PrepareCNYHTTPRequest(c, info, request), body)
	}
	body := `{"tools":[{"type":"function","function":{"name":"local","parameters":{}}}],"enable_search":false}`
	request := httptest.NewRequest(http.MethodPost, "http://upstream.invalid", strings.NewReader(body))
	require.NoError(t, PrepareCNYHTTPRequest(c, info, request))
	data, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	assert.Equal(t, body, string(data))
	// A key admitted only because it has CNY quota must not gain free USD access.
	info.FundingCurrency = ""
	info.TieredBillingSnapshot = nil
	info.PriceData.Currency = "USD"
	require.NotNil(t, ValidateBillingCurrency(c, info))
}
