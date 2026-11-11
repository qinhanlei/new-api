package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type exchangeTestTransport func(*http.Request) (*http.Response, error)

func (transport exchangeTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestWalletExchangeAdminEndpoints(t *testing.T) {
	admin, _ := setupAccessTokenAudit(t)
	require.NoError(t, model.DB.AutoMigrate(&model.WalletExchangePolicy{}, &model.ExchangeRateObservation{}))
	owner := model.User{Username: "fx-owner", AffCode: "fx-owner", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&owner).Error)
	adminWrite, _ := createScopedAccessToken(t, admin.Id, 0, "billing:read", "billing:write")
	adminRead, _ := createScopedAccessToken(t, admin.Id, 0, "billing:read")
	userWrite, _ := createScopedAccessToken(t, owner.Id, 0, "billing:read", "billing:write")
	router := gin.New()
	router.Use(middleware.RequestId(), middleware.AccessTokenAudit())
	router.GET("/api/currency-billing", middleware.AdminAuth(), GetWalletExchangeSettings)
	router.GET("/api/currency-billing/preview", middleware.AdminAuth(), PreviewWalletExchangeRate)
	router.PUT("/api/currency-billing", middleware.AdminAuth(), UpdateWalletExchangeSettings)
	for _, endpoint := range []struct{ method, path string }{{http.MethodGet, "/api/currency-billing"}, {http.MethodGet, "/api/currency-billing/preview"}, {http.MethodPut, "/api/currency-billing"}} {
		response := accessTokenRequest(router, endpoint.method, endpoint.path, userWrite, "", `{}`)
		assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
		response = accessTokenRequest(router, endpoint.method, endpoint.path, "", "", `{}`)
		assert.Equal(t, http.StatusUnauthorized, response.Code)
	}
	response := accessTokenRequest(router, http.MethodPut, "/api/currency-billing", adminRead, "", `{}`)
	assert.Equal(t, http.StatusForbidden, response.Code)
	response = accessTokenRequest(router, http.MethodGet, "/api/currency-billing", adminRead, "", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var state struct {
		Success bool `json:"success"`
		Data    struct {
			Policy model.WalletExchangePolicy   `json:"policy"`
			Rate   model.WalletExchangeSnapshot `json:"rate"`
			Status string                       `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &state))
	assert.False(t, state.Data.Policy.CrossCurrencyEnabled)
	assert.False(t, state.Data.Policy.DynamicEnabled)
	oldDisplayRate := operation_setting.USDExchangeRate
	state.Data.Policy.StaticRate = "6.7"
	state.Data.Policy.CrossCurrencyEnabled = true
	body, err := common.Marshal(map[string]any{"policy": state.Data.Policy})
	require.NoError(t, err)
	response = accessTokenRequest(router, http.MethodPut, "/api/currency-billing", adminWrite, "", string(body))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &state))
	assert.True(t, state.Success)
	assert.Equal(t, "6.7", state.Data.Rate.Rate)
	assert.Equal(t, oldDisplayRate, operation_setting.USDExchangeRate, "payment/display rate must not be changed")
	response = accessTokenRequest(router, http.MethodPut, "/api/currency-billing", adminWrite, "", string(body))
	assert.Equal(t, http.StatusConflict, response.Code)
	assert.Contains(t, response.Body.String(), "message_key")

	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	date := time.Now().UTC().Format("2006-01-02")
	providerBody := fmt.Sprintf(`{"date":%q,"base":"USD","quote":"CNY","rate":6.72}`, date)
	providerStatus, calls := http.StatusOK, 0
	http.DefaultTransport = exchangeTestTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, "https://api.frankfurter.dev/v2/rate/USD/CNY", request.URL.String())
		return &http.Response{StatusCode: providerStatus, Body: io.NopCloser(strings.NewReader(providerBody)), Header: make(http.Header), Request: request}, nil
	})
	response = accessTokenRequest(router, http.MethodGet, "/api/currency-billing/preview", adminWrite, "", "")
	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `"rate":"6.72"`)
	state.Data.Policy.DynamicEnabled = true
	body, err = common.Marshal(map[string]any{"policy": state.Data.Policy, "baseline_rate": "6.72"})
	require.NoError(t, err)
	response = accessTokenRequest(router, http.MethodPut, "/api/currency-billing", adminWrite, "", string(body))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &state))
	assert.Equal(t, "6.72", state.Data.Rate.Rate)
	assert.Equal(t, 2, calls, "the server verifies the administrator's preview independently")
	providerStatus = http.StatusServiceUnavailable
	observation, err := service.RefreshWalletExchangeRate(context.Background())
	require.Error(t, err)
	assert.False(t, observation.Accepted)
	response = accessTokenRequest(router, http.MethodGet, "/api/currency-billing", adminRead, "", "")
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &state))
	assert.Equal(t, "last_good", state.Data.Status)
	assert.Equal(t, "6.72", state.Data.Rate.Rate)
	assert.Contains(t, response.Body.String(), "Exchange rate provider is unavailable")
	providerStatus = http.StatusOK
	for _, invalidBody := range []string{`{`, `{"date":"invalid","base":"USD","quote":"CNY","rate":6.7}`, fmt.Sprintf(`{"date":%q,"base":"CNY","quote":"USD","rate":6.7}`, date)} {
		providerBody = invalidBody
		_, err := service.FetchWalletExchangeRate(context.Background())
		assert.Error(t, err)
	}
}

func TestCNYCurrencyEndpointsAuthorization(t *testing.T) {
	admin, _ := setupAccessTokenAudit(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.CNYWallet{}, &model.CNYTokenBudget{}, &model.CNYBillingRecord{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{}))
	owner := model.User{Username: "cny-owner", Password: "test", AffCode: "cny-owner", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 1234}
	other := model.User{Username: "cny-other", Password: "test", AffCode: "cny-other", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	root := model.User{Username: "cny-root", Password: "test", AffCode: "cny-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	for _, user := range []*model.User{&owner, &other, &root} {
		require.NoError(t, model.DB.Create(user).Error)
	}
	apiToken := model.Token{UserId: owner.Id, Key: "raw-api-secret-must-not-leak", Name: "CNY key", Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, model.DB.Create(&apiToken).Error)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestId(), middleware.AccessTokenAudit())
	users := router.Group("/api/user")
	users.GET("/self/currency/:currency", middleware.UserAuth(), GetSelfCurrencyWallet)
	users.GET("/:id/currency/:currency", middleware.AdminAuth(), GetUserCurrencyWallet)
	users.POST("/:id/currency/:currency", middleware.AdminAuth(), AdjustUserCurrencyWallet)
	tokens := router.Group("/api/token", middleware.UserAuth(), middleware.TokenOperationAudit())
	tokens.GET("/:id/currency/:currency", GetTokenCurrencyBudget)
	tokens.PUT("/:id/currency/:currency", UpdateTokenCurrencyBudget)
	selfPath, adminPath, budgetPath := "/api/user/self/currency/CNY", fmt.Sprintf("/api/user/%d/currency/CNY", owner.Id), fmt.Sprintf("/api/token/%d/currency/CNY", apiToken.Id)
	ownerRead, _ := createScopedAccessToken(t, owner.Id, 0, "wallet:read", "api_key:read")
	ownerWrite, _ := createScopedAccessToken(t, owner.Id, 0, "api_key:write")
	adminRead, _ := createScopedAccessToken(t, admin.Id, 0, "billing:read")
	adminWrite, _ := createScopedAccessToken(t, admin.Id, 0, "billing:write")
	rootWrite, _ := createScopedAccessToken(t, root.Id, 0, "billing:write")
	wrongScope, _ := createScopedAccessToken(t, admin.Id, 0, "profile:read")
	_, session := createAccessTokenTestSession(t, owner.Id, "cny-session")

	t.Run("native_defaults_and_session", func(t *testing.T) {
		for _, credential := range []string{ownerRead, session} {
			response := accessTokenRequest(router, http.MethodGet, selfPath, credential, "", "")
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var payload struct {
				Success bool            `json:"success"`
				Data    model.CNYWallet `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			assert.True(t, payload.Success)
			assert.Equal(t, "CNY", payload.Data.Currency)
			assert.Equal(t, model.CNYQuotaPerUnit, payload.Data.QuotaPerUnit)
			assert.Zero(t, payload.Data.Quota)
		}
		response := accessTokenRequest(router, http.MethodGet, budgetPath, ownerRead, "", "")
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var payload struct {
			Data model.CNYTokenBudget `json:"data"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
		assert.False(t, payload.Data.Enabled)
		assert.Zero(t, payload.Data.Version)
		assert.NotContains(t, response.Body.String(), apiToken.Key)
	})

	t.Run("scopes_and_authentication", func(t *testing.T) {
		for _, endpoint := range []struct{ method, path, scope string }{
			{http.MethodGet, selfPath, "wallet:read"}, {http.MethodGet, adminPath, "billing:read"}, {http.MethodPost, adminPath, "billing:write"},
			{http.MethodGet, budgetPath, "api_key:read"}, {http.MethodPut, budgetPath, "api_key:write"},
		} {
			response := accessTokenRequest(router, endpoint.method, endpoint.path, wrongScope, "", `{}`)
			assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), "ACCESS_TOKEN_SCOPE_DENIED")
			assert.Contains(t, response.Body.String(), endpoint.scope)
		}
		response := accessTokenRequest(router, http.MethodGet, adminPath, ownerRead, "", "")
		assert.Equal(t, http.StatusForbidden, response.Code)
		response = accessTokenRequest(router, http.MethodGet, selfPath, "", "", "")
		assert.Equal(t, http.StatusUnauthorized, response.Code)
		expired, _ := createScopedAccessToken(t, owner.Id, time.Now().Unix()-1, "wallet:read")
		response = accessTokenRequest(router, http.MethodGet, selfPath, expired, "", "")
		assert.Equal(t, http.StatusUnauthorized, response.Code)
	})

	t.Run("admin_role_boundary_and_idempotency", func(t *testing.T) {
		response := accessTokenRequest(router, http.MethodGet, adminPath, adminRead, "", "")
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		for _, targetID := range []int{admin.Id, other.Id, root.Id} {
			path := fmt.Sprintf("/api/user/%d/currency/CNY", targetID)
			response = accessTokenRequest(router, http.MethodGet, path, adminRead, "", "")
			assert.Equal(t, http.StatusForbidden, response.Code)
			response = accessTokenRequest(router, http.MethodPost, path, adminWrite, "", `{"delta_quota":1,"operation_id":"forbidden","reason":"test"}`)
			assert.Equal(t, http.StatusForbidden, response.Code)
		}
		body := `{"delta_quota":250000,"operation_id":"api-credit","reason":"test adjustment"}`
		for range 2 {
			response = accessTokenRequest(router, http.MethodPost, adminPath, adminWrite, "", body)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var payload struct {
				Data model.CNYWallet `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			assert.Equal(t, 250000, payload.Data.Quota)
		}
		response = accessTokenRequest(router, http.MethodPost, adminPath, adminWrite, "", strings.Replace(body, "250000", "250001", 1))
		assert.Equal(t, http.StatusConflict, response.Code)
		assert.Contains(t, response.Body.String(), `"message_key":"CNY billing operation conflicts with its existing record"`)
		response = accessTokenRequest(router, http.MethodPost, fmt.Sprintf("/api/user/%d/currency/CNY", other.Id), rootWrite, "", `{"delta_quota":1,"operation_id":"root-credit","reason":"test"}`)
		assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
		response = accessTokenRequest(router, http.MethodPost, adminPath, adminWrite, "", `{"operation_id":"missing-quota","reason":"test"}`)
		assert.Equal(t, http.StatusBadRequest, response.Code)
		var preserved model.User
		require.NoError(t, model.DB.First(&preserved, owner.Id).Error)
		assert.Equal(t, 1234, preserved.Quota)
	})

	t.Run("token_ownership_and_version", func(t *testing.T) {
		otherToken, _ := createScopedAccessToken(t, other.Id, 0, "api_key:read", "api_key:write")
		body := `{"enabled":true,"remain_quota":200000,"unlimited_quota":false,"expected_version":0}`
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			response := accessTokenRequest(router, method, budgetPath, otherToken, "", body)
			assert.Equal(t, http.StatusNotFound, response.Code)
			assert.NotContains(t, response.Body.String(), apiToken.Key)
		}
		response := accessTokenRequest(router, http.MethodPut, budgetPath, ownerWrite, "", body)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var payload struct {
			Data model.CNYTokenBudget `json:"data"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
		assert.True(t, payload.Data.Enabled)
		assert.Equal(t, int64(1), payload.Data.Version)
		assert.NotContains(t, response.Body.String(), apiToken.Key)
		response = accessTokenRequest(router, http.MethodPut, budgetPath, ownerWrite, "", body)
		assert.Equal(t, http.StatusConflict, response.Code)
		assert.Contains(t, response.Body.String(), `"message_key":"CNY token budget has changed; reload before saving"`)
		response = accessTokenRequest(router, http.MethodPut, budgetPath, ownerWrite, "", `{"enabled":false,"remain_quota":0,"unlimited_quota":false}`)
		assert.Equal(t, http.StatusBadRequest, response.Code)
		response = accessTokenRequest(router, http.MethodPut, budgetPath, ownerWrite, "", `{"enabled":false,"remain_quota":0,"unlimited_quota":false,"expected_version":1,"user_id":999,"key":"injected"}`)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var unchanged model.Token
		require.NoError(t, model.DB.First(&unchanged, apiToken.Id).Error)
		assert.Equal(t, owner.Id, unchanged.UserId)
		assert.Equal(t, apiToken.Key, unchanged.Key)
	})

	t.Run("adjustment_modes_without_a_reason", func(t *testing.T) {
		for _, step := range []struct {
			body string
			want int
		}{
			{`{"mode":"override","value":500,"operation_id":"api-mode-override"}`, 500},
			{`{"mode":"add","value":100,"operation_id":"api-mode-add"}`, 600},
			{`{"mode":"subtract","value":50,"operation_id":"api-mode-subtract"}`, 550},
			{`{"mode":"override","value":500,"operation_id":"api-mode-override"}`, 550},
			{`{"mode":"override","value":0,"operation_id":"api-mode-zero"}`, 0},
			{`{"mode":"override","value":-50,"operation_id":"api-mode-negative"}`, -50},
		} {
			response := accessTokenRequest(router, http.MethodPost, adminPath, adminWrite, "", step.body)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var payload struct {
				Data model.CNYWallet `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			assert.Equal(t, step.want, payload.Data.Quota)
		}
		for _, body := range []string{
			`{"mode":"add","value":0,"operation_id":"zero-add"}`,
			`{"mode":"subtract","value":-1,"operation_id":"negative-subtract"}`,
			`{"mode":"override","operation_id":"missing-value"}`,
			`{"value":1,"operation_id":"missing-mode"}`,
			`{"mode":"delta","value":1,"operation_id":"invalid-mode"}`,
			`{"mode":"override","value":1,"delta_quota":1,"operation_id":"mixed-contract"}`,
			`{"mode":"","delta_quota":1,"operation_id":"mixed-mode"}`,
			`{"value":0,"delta_quota":1,"operation_id":"mixed-value"}`,
		} {
			response := accessTokenRequest(router, http.MethodPost, adminPath, adminWrite, "", body)
			assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		}
		response := accessTokenRequest(router, http.MethodPost, adminPath, adminWrite, "", `{"mode":"override","value":501,"operation_id":"api-mode-override"}`)
		assert.Equal(t, http.StatusConflict, response.Code)
		wallet, err := model.GetCNYWallet(owner.Id)
		require.NoError(t, err)
		assert.Equal(t, -50, wallet.Quota)
		var record model.CNYBillingRecord
		require.NoError(t, model.DB.Where("kind = ? AND request_id = ?", model.CNYBillingAdjustment, "api-mode-override").First(&record).Error)
		assert.Equal(t, "CNY wallet adjustment", record.Reason)
	})

	t.Run("currency_and_audit", func(t *testing.T) {
		response := accessTokenRequest(router, http.MethodGet, "/api/user/self/currency/USD", ownerRead, "", "")
		assert.Equal(t, http.StatusBadRequest, response.Code)
		assert.Contains(t, response.Body.String(), `"message_key":"Unsupported billing currency"`)
		var audits []model.AuditLog
		require.NoError(t, model.LOG_DB.Where("action = ?", "user.cny_wallet_adjust").Find(&audits).Error)
		require.NotEmpty(t, audits)
		var successful *model.AuditLog
		var successfulParams map[string]any
		for i := range audits {
			encoded, err := common.Marshal(audits[i].Other.Op.Params)
			require.NoError(t, err)
			var params map[string]any
			require.NoError(t, common.Unmarshal(encoded, &params))
			if audits[i].Success && params["operation_id"] == "api-credit" {
				successful = &audits[i]
				successfulParams = params
				break
			}
		}
		require.NotNil(t, successful)
		assert.NotEmpty(t, successful.RequestId)
		assert.Equal(t, admin.Id, successful.UserId)
		assert.Equal(t, "CNY", successfulParams["currency"])
		assert.EqualValues(t, owner.Id, successfulParams["target_user_id"])
		assert.EqualValues(t, 250000, successfulParams["delta_quota"])
		encoded, err := common.Marshal(audits)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), adminWrite)
		assert.NotContains(t, string(encoded), apiToken.Key)
	})

	t.Run("disabled_account", func(t *testing.T) {
		require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", owner.Id).UpdateColumn("status", common.UserStatusDisabled).Error)
		for _, credential := range []string{ownerRead, session} {
			response := accessTokenRequest(router, http.MethodGet, selfPath, credential, "", "")
			assert.Equal(t, http.StatusUnauthorized, response.Code)
		}
	})
}
