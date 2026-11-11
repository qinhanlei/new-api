package controller

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBillingRateManagementAndPersonalPricing(t *testing.T) {
	database := modelManagementDB(t, "sqlite", "")
	require.NoError(t, database.AutoMigrate(&model.BillingRateRule{}, &model.Log{}))
	root := model.User{Username: "rate-root", AffCode: "rate-root", Password: "fixture", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default"}
	user := model.User{Username: "rate-user", AffCode: "rate-user", Password: "fixture", Status: common.UserStatusEnabled, Group: "default"}
	require.NoError(t, database.Create(&root).Error)
	require.NoError(t, database.Create(&user).Error)
	patch := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
		context.Set("id", root.Id)
		context.Set("role", common.RoleRootUser)
		if strings.HasPrefix(path, "/api/user/") {
			context.Params = gin.Params{{Key: "id", Value: strconv.Itoa(user.Id)}}
		}
		UpdateBillingRates(context)
		return recorder
	}
	const modelName = "billing-rate-public-model"
	globalResponse := patch("/api/option/billing_rates", `{"changes":[{"model_name":"billing-rate-public-model","rate":"0.8","expected_version":0}]}`)
	require.Equal(t, http.StatusOK, globalResponse.Code, globalResponse.Body.String())
	userPath := fmt.Sprintf("/api/user/%d/billing_rates", user.Id)
	userResponse := patch(userPath, `{"changes":[{"model_name":"billing-rate-public-model","rate":"0.6","expected_version":0}]}`)
	require.Equal(t, http.StatusOK, userResponse.Code, userResponse.Body.String())
	var saved struct {
		Success bool
		Data    model.BillingRateRuleList
	}
	require.NoError(t, common.Unmarshal(userResponse.Body.Bytes(), &saved))
	require.True(t, saved.Success)
	require.Len(t, saved.Data.Rules, 1)
	require.Len(t, saved.Data.GlobalRules, 1)
	assert.Equal(t, "0.600000", *saved.Data.Rules[0].Rate)
	assert.Equal(t, "0.800000", *saved.Data.GlobalRules[0].Rate)
	for _, body := range []string{
		`{"changes":[{"model_name":"billing-rate-public-model","expected_version":1}]}`,
		`{"changes":[{"model_name":"billing-rate-public-model","rate":null}]}`,
		`{"changes":[{"model_name":"billing-rate-public-model","rate":0.1,"expected_version":1}]}`,
		`{"changes":[{"model_name":"billing-rate-public-model","rate":"-0.1","expected_version":1}]}`,
	} {
		response := patch(userPath, body)
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
	conflict := patch(userPath, `{"changes":[{"model_name":"billing-rate-public-model","rate":null,"expected_version":0}]}`)
	assert.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())

	previousGroup := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":0.9}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroup)) })
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"billing-rate-public-model":2}`))
	channel := model.Channel{Name: "billing-rate-pricing", Type: 1, Key: "fixture", Models: modelName, Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, database.Create(&channel).Error)
	require.NoError(t, database.Create(&model.Ability{Group: "default", Model: modelName, ChannelId: channel.Id, Enabled: true}).Error)
	model.InvalidatePricingCache()
	t.Cleanup(model.InvalidatePricingCache)
	for _, tc := range []struct {
		userID int
		rate   float64
		source string
	}{{user.Id, 0.6, "user"}, {root.Id, 0.8, "model"}, {0, 0.8, "model"}, {user.Id, 0.6, "user"}} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodGet, "/api/pricing?user_id="+strconv.Itoa(user.Id), nil)
		if tc.userID != 0 {
			context.Set("id", tc.userID)
		}
		GetPricing(context)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
		var response struct {
			Data []struct {
				ModelName        string                                   `json:"model_name"`
				BillingModelName string                                   `json:"billing_model_name"`
				ModelRatio       float64                                  `json:"model_ratio"`
				EffectiveRates   map[string]hosttypes.BillingRateSnapshot `json:"effective_rates"`
			}
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.Len(t, response.Data, 1)
		assert.Equal(t, modelName, response.Data[0].BillingModelName)
		assert.Equal(t, 2.0, response.Data[0].ModelRatio, "the shared base-price cache remains unchanged")
		assert.Equal(t, tc.rate, response.Data[0].EffectiveRates["default"].Rate)
		assert.Equal(t, tc.source, response.Data[0].EffectiveRates["default"].Source)
	}
	cleared := patch(userPath, `{"changes":[{"model_name":"billing-rate-public-model","rate":null,"expected_version":1}]}`)
	require.Equal(t, http.StatusOK, cleared.Code, cleared.Body.String())
	require.NoError(t, common.Unmarshal(cleared.Body.Bytes(), &saved))
	assert.Nil(t, saved.Data.Rules[0].Rate)
	assert.EqualValues(t, 2, saved.Data.Rules[0].Version)
}

func TestBillingRateModelSelectionDoesNotDependOnPublicPricing(t *testing.T) {
	database := modelManagementDB(t, "sqlite", "")
	require.NoError(t, database.AutoMigrate(&model.Log{}))
	root := model.User{Username: "rate-model-root", AffCode: "rate-model-root", Password: "fixture", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default"}
	user := model.User{Username: "rate-model-user", AffCode: "rate-model-user", Password: "fixture", Status: common.UserStatusEnabled, Group: "default"}
	require.NoError(t, database.Create(&root).Error)
	require.NoError(t, database.Create(&user).Error)
	previousUsable := setting.UserUsableGroups2JSONString()
	previousBlacklist := model_setting.GetGlobalSettings().ThinkingModelBlacklist
	specialGroups := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup
	previousSpecialGroups := specialGroups.ReadAll()
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
	model_setting.GetGlobalSettings().ThinkingModelBlacklist = nil
	specialGroups.Clear()
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsable))
		model_setting.GetGlobalSettings().ThinkingModelBlacklist = previousBlacklist
		specialGroups.Clear()
		specialGroups.AddAll(previousSpecialGroups)
		model.InvalidatePricingCache()
	})
	const alias, billingModel = "deepseek-v4-pro-max", "deepseek-v4-pro"
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"deepseek-v4-pro":1}`))
	channel := model.Channel{Name: "hidden-rate-model", Type: 1, Key: "fixture", Models: alias, Group: "billing-rates-hidden", Status: common.ChannelStatusEnabled}
	require.NoError(t, database.Create(&channel).Error)
	require.NoError(t, database.Create(&model.Ability{Group: channel.Group, Model: alias, ChannelId: channel.Id, Enabled: true}).Error)
	model.InvalidatePricingCache()
	publicRecorder := httptest.NewRecorder()
	publicContext, _ := gin.CreateTestContext(publicRecorder)
	publicContext.Request = httptest.NewRequest(http.MethodGet, "/api/pricing", nil)
	publicContext.Set("id", root.Id)
	GetPricing(publicContext)
	require.Equal(t, http.StatusOK, publicRecorder.Code, publicRecorder.Body.String())
	var publicPrices struct{ Data []model.Pricing }
	require.NoError(t, common.Unmarshal(publicRecorder.Body.Bytes(), &publicPrices))
	assert.Empty(t, publicPrices.Data, "the configured model is hidden from this administrator's usable groups")

	request := func(method, path, query, body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(method, path, strings.NewReader(body))
		context.Request.URL.RawQuery = query
		context.Set("id", root.Id)
		context.Set("role", common.RoleRootUser)
		if strings.HasPrefix(path, "/api/user/") {
			context.Params = gin.Params{{Key: "id", Value: strconv.Itoa(user.Id)}}
		}
		if method == http.MethodGet {
			GetBillingRates(context)
		} else {
			UpdateBillingRates(context)
		}
		return recorder
	}
	for _, path := range []string{"/api/option/billing_rates", fmt.Sprintf("/api/user/%d/billing_rates", user.Id)} {
		for _, method := range []string{http.MethodGet, http.MethodPatch} {
			response := request(method, path, "model_name="+alias, `{"changes":[{"model_name":"deepseek-v4-pro","rate":"0.6","expected_version":0}]}`)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var resolved struct{ Data model.BillingRateRuleList }
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &resolved))
			assert.Equal(t, billingModel, resolved.Data.BillingModelName, method)
		}
		for _, method := range []string{http.MethodGet, http.MethodPatch} {
			response := request(method, path, "", `{"changes":[{"model_name":"deepseek-v4-pro-max","rate":"0.7","expected_version":0}]}`)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.NotContains(t, response.Body.String(), `"billing_model_name"`, "no query preserves the original response contract")
			if method == http.MethodPatch {
				var exact struct{ Data model.BillingRateRuleList }
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &exact))
				assert.Contains(t, exact.Data.Rules, model.BillingRateEntry{ModelName: alias, Rate: new("0.700000"), Version: 1}, "batch rule names remain exact and are not canonicalized")
			}
		}
		for _, malformed := range []string{
			"model_name=", "model_name=" + strings.Repeat("a", 513), "model_name=%FF", "model_name=%00",
			"model_name=model%0Aname", "model_name=%20model", "model_name=a&model_name=b", "model_name=%GG",
		} {
			for _, method := range []string{http.MethodGet, http.MethodPatch} {
				response := request(method, path, malformed, `{"changes":[{"model_name":"deepseek-v4-pro","rate":"0","expected_version":1}]}`)
				assert.Equal(t, http.StatusBadRequest, response.Code, "%s %s: %s", method, malformed, response.Body.String())
				assert.Contains(t, response.Body.String(), `"message_key":"Invalid billing rate model name"`)
			}
		}
	}
	for _, owner := range []int{0, user.Id} {
		rules, err := model.GetBillingRateRules(owner)
		require.NoError(t, err)
		assert.Contains(t, rules.Rules, model.BillingRateEntry{ModelName: billingModel, Rate: new("0.600000"), Version: 1}, "malformed PATCH queries cannot mutate an existing rule")
	}
}

func TestUpdateOptionRejectsInvalidTaskBillingExpressions(t *testing.T) {
	const pluginKey = "billing-save-probe"
	const modelName = "billing-save-model"
	source := `
export const meta = {
  apiVersion: 1, key: "billing-save-probe", name: "Billing Save Probe", version: "1.0.0", author: {name: "Test"},
  models: ["billing-save-model"], fetchMode: "per_task",
  usageSchema: {seconds: {type: "number", unit: "second"}}
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(pluginKey) })

	tests := []struct {
		name       string
		expression string
		errorText  string
	}{
		{
			name:       "invalid syntax",
			expression: `tier("base",`,
			errorText:  "expr compile error",
		},
		{
			name:       "undeclared usage key",
			expression: `tier("base", u("clips") * 0.1)`,
			errorText:  `usage key \"clips\" is not declared`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			expressions, marshalErr := common.Marshal(map[string]string{modelName: testCase.expression})
			require.NoError(t, marshalErr)
			body, marshalErr := common.Marshal(OptionUpdateRequest{
				Key:   "billing_setting.billing_expr",
				Value: string(expressions),
			})
			require.NoError(t, marshalErr)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(body)))

			UpdateOption(context)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), modelName)
			assert.Contains(t, recorder.Body.String(), testCase.errorText)
		})
	}
}

func TestUpdateOptionRejectsUsageExpressionWithoutTaskPlugin(t *testing.T) {
	const modelName = "billing-save-model-without-plugin"
	expressions, err := common.Marshal(map[string]string{
		modelName: `u("mode") == "std" ? 1 : 2`,
	})
	require.NoError(t, err)
	body, err := common.Marshal(OptionUpdateRequest{
		Key:   "billing_setting.billing_expr",
		Value: string(expressions),
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/",
		strings.NewReader(string(body)),
	)

	UpdateOption(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	assert.Contains(t, recorder.Body.String(), modelName)
	assert.Contains(t, recorder.Body.String(), "mode")
	assert.Contains(t, recorder.Body.String(), "no task plugin usage schema")
}

func TestUpdateOptionAliasBillingExprUsesPluginSchema(t *testing.T) {
	database := modelManagementDB(t, "sqlite", "")
	require.NoError(t, database.AutoMigrate(&model.Log{}))
	const pluginKey = "billing-alias-probe"
	source := `
export const meta = {
  apiVersion: 1, key: "billing-alias-probe", name: "Billing Alias Probe", version: "1.0.0", author: {name: "Test"},
  models: ["declared-model"], fetchMode: "per_task",
  usageSchema: {seconds: {type: "number", unit: "second"}, image_count: {type: "number", unit: "count"}},
  usageProfiles: [{models: ["declared-model"], schema: {seconds: {type: "number", unit: "second"}}}]
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(pluginKey) })

	mapping := `{"alias-model":"declared-model"}`
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:           1,
		Type:         54,
		Key:          "key-1",
		Status:       common.ChannelStatusEnabled,
		Name:         "ch-1",
		Group:        "default",
		Models:       "alias-model,declared-model",
		ModelMapping: &mapping,
	}).Error)
	model.InitChannelCache()

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	putExpr := func(modelName, expression string) *httptest.ResponseRecorder {
		t.Helper()
		expressions, marshalErr := common.Marshal(map[string]string{modelName: expression})
		require.NoError(t, marshalErr)
		body, marshalErr := common.Marshal(OptionUpdateRequest{
			Key:   "billing_setting.billing_expr",
			Value: string(expressions),
		})
		require.NoError(t, marshalErr)
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(body)))
		UpdateOption(context)
		return recorder
	}

	accepted := putExpr("alias-model", `u("seconds")`)
	assert.Equal(t, http.StatusOK, accepted.Code)
	assert.Contains(t, accepted.Body.String(), `"success":true`)

	rejectedKey := putExpr("alias-model", `u("image_count")`)
	assert.Equal(t, http.StatusOK, rejectedKey.Code)
	assert.Contains(t, rejectedKey.Body.String(), `"success":false`)
	assert.Contains(t, rejectedKey.Body.String(), `usage key \"image_count\" is not declared`)

	rejectedDeclared := putExpr("declared-model", `u("image_count")`)
	assert.Contains(t, rejectedDeclared.Body.String(), `"success":false`)
	assert.Contains(t, rejectedDeclared.Body.String(), `usage key \"image_count\" is not declared`)

	unresolvable := putExpr("unknown-alias-model", `u("seconds")`)
	assert.Equal(t, http.StatusOK, unresolvable.Code)
	assert.Contains(t, unresolvable.Body.String(), `"success":false`)
	assert.Contains(t, unresolvable.Body.String(), "no task plugin usage schema")
}

// Covers configuration persistence, validation, reservation and reconciliation
// through the same public entry points used by the relay.
func TestPreConsumePolicyDatabaseMatrix(t *testing.T) {
	previousConfig := config.GlobalConfig.ExportAllConfigs()
	previousUnit, previousBatch := common.QuotaPerUnit, common.BatchUpdateEnabled
	common.QuotaPerUnit, common.BatchUpdateEnabled = 500000, false
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(previousConfig))
		common.QuotaPerUnit, common.BatchUpdateEnabled = previousUnit, previousBatch
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.Token{}, &model.CNYWallet{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{}))
			for key, value := range map[string]string{
				"quota_setting.trust_quota_usd":        "10.5",
				"quota_setting.pre_consume_multiplier": "1.5",
			} {
				var response struct{ Success bool }
				modelManagementRequest(t, UpdateOption, http.MethodPut, "/api/option/", OptionUpdateRequest{Key: key, Value: value}, &response)
				require.True(t, response.Success)
				var saved model.Option
				require.NoError(t, db.Where(&model.Option{Key: key}).First(&saved).Error)
				assert.Equal(t, value, saved.Value)
			}
			var savedOptions []model.Option
			require.NoError(t, db.Find(&savedOptions).Error)
			stored := map[string]string{}
			for _, option := range savedOptions {
				stored[option.Key] = option.Value
			}
			for range 2 {
				operation_setting.GetQuotaSetting().TrustQuotaUSD = 10
				operation_setting.GetQuotaSetting().PreConsumeMultiplier = 1
				require.NoError(t, config.GlobalConfig.LoadFromDB(stored))
				assert.Equal(t, 10.5, operation_setting.GetQuotaSetting().TrustQuotaUSD)
				assert.Equal(t, 1.5, operation_setting.GetQuotaSetting().PreConsumeMultiplier)
			}
			for _, tc := range []struct{ key, value string }{
				{"trust_quota_usd", "-1"}, {"trust_quota_usd", "NaN"}, {"trust_quota_usd", "+Inf"},
				{"pre_consume_multiplier", "0"}, {"pre_consume_multiplier", "-0.5"},
				{"pre_consume_multiplier", "NaN"}, {"pre_consume_multiplier", "Inf"},
				{"pre_consume_multiplier", "1e309"}, {"pre_consume_multiplier", ""},
			} {
				key := "quota_setting." + tc.key
				var response struct{ Success bool }
				modelManagementRequest(t, UpdateOption, http.MethodPut, "/api/option/", OptionUpdateRequest{Key: key, Value: tc.value}, &response)
				assert.False(t, response.Success, "%s=%q", key, tc.value)
				var saved model.Option
				require.NoError(t, db.Where(&model.Option{Key: key}).First(&saved).Error)
				assert.Equal(t, stored[key], saved.Value, "invalid saves must preserve the previous value")
			}
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"billing_setting.billing_mode":    `{"policy-model":"tiered_expr"}`,
				"billing_setting.billing_expr":    `{"policy-model":"tier(\"base\", p * 3 + c * 15)"}`,
				"group_ratio_setting.group_ratio": `{"default":1}`,
			}))
			cases := []struct {
				name                  string
				threshold, multiplier float64
				wallet, token         int
				force, unlimited      bool
				wantHeld              int
			}{
				{"above threshold", 10, 1, 5500000, 5500000, false, false, 0},
				{"custom threshold", 20, 1, 5500000, 5500000, false, false, 1500},
				{"zero disables bypass", 0, 1, 5500000, 5500000, false, false, 1500},
				{"wallet equals threshold", 10, 1, 5000000, 5500000, false, false, 1500},
				{"token equals threshold", 10, 1, 5500000, 5000000, false, false, 1500},
				{"fractional threshold", 10.5, 1, 5250001, 5250001, false, false, 0},
				{"unlimited token", 10, 1, 5500000, 0, false, true, 0},
				{"forced reservation", 10, 1, 5500000, 5500000, true, false, 1500},
				{"half input cost", 0, 0.5, 100000, 100000, false, false, 750},
				{"fractional multiple refunds excess", 0, 2.5, 100000, 100000, false, false, 3750},
			}
			for i, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					require.NoError(t, model.UpdateOption("quota_setting.trust_quota_usd", strconv.FormatFloat(tc.threshold, 'f', -1, 64)))
					require.NoError(t, model.UpdateOption("quota_setting.pre_consume_multiplier", strconv.FormatFloat(tc.multiplier, 'f', -1, 64)))
					user := model.User{Username: fmt.Sprintf("policy-user-%d", i), Quota: tc.wallet, Group: "default", AffCode: fmt.Sprintf("policy-aff-%d", i)}
					require.NoError(t, db.Create(&user).Error)
					token := model.Token{UserId: user.Id, Key: fmt.Sprintf("policy-token-%d", i), RemainQuota: tc.token, UnlimitedQuota: tc.unlimited, Status: common.TokenStatusEnabled, ExpiredTime: -1}
					require.NoError(t, db.Create(&token).Error)
					ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
					ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
					ctx.Set("token_quota", tc.token)
					info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, TokenKey: token.Key, TokenUnlimited: tc.unlimited, ForcePreConsume: tc.force, OriginModelName: "policy-model", UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{}}
					info.UserSetting.BillingPreference = "wallet_only"
					price, err := helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 10000})
					require.NoError(t, err)
					require.Nil(t, service.PreConsumeBilling(ctx, price.QuotaToPreConsume, info))
					assert.Equal(t, tc.wantHeld, info.FinalPreConsumedQuota)
					require.NoError(t, db.First(&user, user.Id).Error)
					assert.Equal(t, tc.wallet-tc.wantHeld, user.Quota)
					if !tc.unlimited {
						require.NoError(t, db.First(&token, token.Id).Error)
						assert.Equal(t, tc.token-tc.wantHeld, token.RemainQuota)
					}
					_, actual, _ := service.TryTieredSettle(info, billingexpr.TokenParams{P: 1000, C: 100, Len: 1000})
					assert.Equal(t, 2250, actual)
					require.NoError(t, info.Billing.Settle(actual))
					require.NoError(t, info.Billing.Settle(actual))
					require.NoError(t, db.First(&user, user.Id).Error)
					assert.Equal(t, tc.wallet-actual, user.Quota)
					if !tc.unlimited {
						require.NoError(t, db.First(&token, token.Id).Error)
						assert.Equal(t, tc.token-actual, token.RemainQuota)
					}
				})
			}
		})
	}
}

func TestPreConsumeMultiplierRejectsInvalidRuntimeAndOverflow(t *testing.T) {
	previous := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(previous)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"policy-overflow":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"policy-overflow":"tier(\"base\", p * 3)"}`,
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))
	for _, multiplier := range []float64{0, -0.5, math.NaN(), math.Inf(1), math.MaxFloat64} {
		operation_setting.GetQuotaSetting().PreConsumeMultiplier = multiplier
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		info := &relaycommon.RelayInfo{OriginModelName: "policy-overflow", UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{}}
		_, err := helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.Error(t, err)
		assert.Nil(t, info.Billing)
	}
}
