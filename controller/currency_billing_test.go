package controller

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCNYCurrencyEndpointsAuthorization(t *testing.T) {
	admin, _ := setupAccessTokenAudit(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.CNYWallet{}, &model.CNYTokenBudget{}, &model.CNYBillingRecord{}))
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
