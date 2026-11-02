package controller

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssetValidationChannelAccessHTTP(t *testing.T) {
	db := setupManageUserTestDB(t)
	previousMaster, previousAddress := common.IsMasterNode, system_setting.ServerAddress
	common.IsMasterNode, system_setting.ServerAddress = true, "http://gateway.example:13000"
	t.Cleanup(func() {
		common.IsMasterNode, system_setting.ServerAddress = previousMaster, previousAddress
	})
	tables := append(model.AssetModels(), &model.Option{}, &model.Channel{}, &model.Ability{}, &model.Token{})
	require.NoError(t, db.AutoMigrate(tables...))
	// Initialize dialect-aware token columns against this isolated database.
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode = false
	require.NoError(t, model.InitLogDB())
	common.IsMasterNode = true
	require.NoError(t, authz.Init(db))
	for _, user := range []model.User{
		{Id: 51001, Username: "validation-user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "v1"},
		{Id: 51002, Username: "validation-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "v2"},
		{Id: 51003, Username: "validation-restricted-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "v3"},
	} {
		require.NoError(t, db.Create(&user).Error)
	}
	require.NoError(t, authz.SetUserPermissions(51003, authz.PermissionsMap{"channel": {"read": false}}))
	require.NoError(t, db.Create(&model.Channel{Id: 7, Name: "private-provider-name", Key: "synthetic-key", Type: 54, Status: common.ChannelStatusEnabled}).Error)
	for _, owner := range []int{51001, 51002, 51003} {
		record := model.AssetValidationSession{
			PublicID: fmt.Sprintf("validation-na-http-%d", owner), UserID: owner, InitiatorID: owner,
			ProjectName: "default", Name: "Synthetic verification", Status: model.AssetValidationFailed,
			ChannelID: 7, StateHash: fmt.Sprint(owner), IdempotencyHash: fmt.Sprint(owner),
			ErrorCode: "UpstreamError", ErrorMessage: "private-provider-diagnostic", ExpiresAt: time.Now().Unix() + 60,
		}
		require.NoError(t, db.Create(&record).Error)
	}
	newPAT := func(userID int, scopes ...string) string {
		suffix, err := common.GenerateRandomCharsKey(43)
		require.NoError(t, err)
		raw := model.AccessTokenPrefix + suffix
		token := &model.UserAccessToken{Name: "isolated-validation-test", TokenHash: model.AccessTokenFingerprint(raw), ExpiresAt: time.Now().Unix() + 3600}
		require.NoError(t, token.SetScopes(scopes))
		require.NoError(t, model.CreateUserAccessToken(userID, token, service.AccessTokenMaxPerUser))
		return raw
	}
	userPAT := newPAT(51001, "asset:read", "asset:write")
	adminAssetPAT := newPAT(51002, "asset:read", "asset:write")
	adminChannelPAT := newPAT(51002, "asset:read", "asset:write", "channel:read")
	adminReadPAT := newPAT(51002, "asset:read", "channel:read")
	restrictedPAT := newPAT(51003, "asset:read", "asset:write", "channel:read")
	userSession, err := service.CreateLoginSession(51001, "password", "127.0.0.1", "test")
	require.NoError(t, err)
	adminSession, err := service.CreateLoginSession(51002, "password", "127.0.0.1", "test")
	require.NoError(t, err)
	for _, owner := range []int{51001, 51002} {
		require.NoError(t, db.Create(&model.Token{UserId: owner, Key: fmt.Sprintf("validationrelay%d", owner), Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	assets := router.Group("/api/assets", middleware.UserAuth())
	assets.GET("/validation/channels", ListAssetValidationChannels)
	assets.GET("/validation/records/:id", GetAssetValidationRecord)
	assets.POST("/actions", ManageAssetAction)
	router.POST("/doubao/", middleware.TokenAuth(), ArkAssets)
	createPath := "/api/assets/actions?Action=CreateVisualValidateSession&Version=2024-01-01"
	relayPath := "/doubao/?Action=CreateVisualValidateSession&Version=2024-01-01"
	manualBody := `{"Name":"Person","ChannelId":7,"IdempotencyKey":"http-access-fixture"}`
	automaticBody := `{"Name":"Person","IdempotencyKey":"http-access-fixture"}`
	tests := []struct {
		name, method, path, credential, session, body string
		status                                        int
		contains                                      string
		private                                       bool
	}{
		{"user cannot enumerate channels", "GET", "/api/assets/validation/channels", userPAT, "", "", 403, "", false},
		{"admin PAT needs channel scope", "GET", "/api/assets/validation/channels", adminAssetPAT, "", "", 403, "", false},
		{"admin PAT can enumerate with channel scope", "GET", "/api/assets/validation/channels", adminChannelPAT, "", "", 200, "", false},
		{"restricted admin cannot enumerate", "GET", "/api/assets/validation/channels", restrictedPAT, "", "", 403, "", false},
		{"user session cannot select channel", "POST", createPath, userSession.AccessToken, userSession.Session.SID, manualBody, 403, "You do not have permission to select a verification channel.", false},
		{"user PAT cannot select channel", "POST", createPath, userPAT, "", manualBody, 403, "You do not have permission to select a verification channel.", false},
		{"admin PAT without channel scope cannot select", "POST", createPath, adminAssetPAT, "", manualBody, 403, "You do not have permission to select a verification channel.", false},
		{"read-only admin cannot create", "POST", createPath, adminReadPAT, "", manualBody, 403, "", false},
		{"admin session still needs eligible channel", "POST", createPath, adminSession.AccessToken, adminSession.Session.SID, manualBody, 403, "ValidationChannelUnavailable", false},
		{"admin PAT still needs eligible channel", "POST", createPath, adminChannelPAT, "", manualBody, 403, "ValidationChannelUnavailable", false},
		{"automatic choice still checks eligibility", "POST", createPath, userPAT, "", automaticBody, 403, "NoAvailableChannel", false},
		{"user relay cannot select channel", "POST", relayPath, "sk-validationrelay51001", "", manualBody, 403, "You do not have permission to select a verification channel.", false},
		{"admin relay retains eligibility checks", "POST", relayPath, "sk-validationrelay51002", "", manualBody, 403, "ValidationChannelUnavailable", false},
		{"user history excludes operator details", "GET", "/api/assets/validation/records/validation-na-http-51001", userPAT, "", "", 200, "", false},
		{"admin PAT without channel scope excludes operator details", "GET", "/api/assets/validation/records/validation-na-http-51002", adminAssetPAT, "", "", 200, "", false},
		{"admin PAT can inspect operator details", "GET", "/api/assets/validation/records/validation-na-http-51002", adminChannelPAT, "", "", 200, "private-provider-diagnostic", true},
		{"restricted admin excludes operator details", "GET", "/api/assets/validation/records/validation-na-http-51003", restrictedPAT, "", "", 200, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+test.credential)
			request.Header.Set("Content-Type", "application/json")
			if test.session != "" {
				request.Header.Set("X-Auth-Session", test.session)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, test.status, response.Code, response.Body.String())
			if test.contains != "" {
				assert.Contains(t, response.Body.String(), test.contains)
			}
			if !test.private {
				for _, private := range []string{"private-provider-name", "private-provider-diagnostic", `"ChannelId"`, `"ChannelName"`} {
					assert.NotContains(t, response.Body.String(), private)
				}
			}
		})
	}
	var count int64
	require.NoError(t, db.Model(&model.AssetValidationSession{}).Count(&count).Error)
	assert.Equal(t, int64(3), count, "denied requests must not create verification sessions")
}
