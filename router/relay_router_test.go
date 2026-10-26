package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssetActionsKeepArkErrorsAndDoNotShadowVideoRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.RequestId())
	SetAssetRouter(engine)
	engine.POST("/doubao/api/v3/contents/generations/tasks", func(c *gin.Context) { c.Status(http.StatusAccepted) })

	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/doubao/?Action=CreateAsset&Version=2024-01-01", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusUnauthorized, response.Code)
	var payload struct {
		Metadata struct {
			RequestID string `json:"RequestId"`
			Action    string `json:"Action"`
			Version   string `json:"Version"`
			Error     struct {
				Code string `json:"Code"`
			} `json:"Error"`
		} `json:"ResponseMetadata"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.NotEmpty(t, payload.Metadata.RequestID)
	assert.Equal(t, "CreateAsset", payload.Metadata.Action)
	assert.Equal(t, "2024-01-01", payload.Metadata.Version)
	assert.Equal(t, "AuthenticationError", payload.Metadata.Error.Code)

	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/doubao/api/v3/contents/generations/tasks", strings.NewReader(`{}`)))
	assert.Equal(t, http.StatusAccepted, response.Code)
}

func TestAssetPollingDoesNotConsumeMutationRateLimit(t *testing.T) {
	previousEnabled, previousLimit, previousRedis := common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.RedisEnabled
	common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.RedisEnabled = true, 1, false
	t.Cleanup(func() {
		common.CriticalRateLimitEnable = previousEnabled
		common.CriticalRateLimitNum = previousLimit
		common.RedisEnabled = previousRedis
	})
	engine := gin.New()
	engine.POST("/actions", func(c *gin.Context) { c.Set("id", 981761) }, assetMutationRateLimit(), func(c *gin.Context) { c.Status(http.StatusOK) })
	for _, tc := range []struct {
		action string
		status int
	}{
		{"ListAssets", http.StatusOK}, {"GetAsset", http.StatusOK},
		{"CreateAsset", http.StatusOK}, {"CreateAsset", http.StatusTooManyRequests},
		{"ListAssets", http.StatusOK},
	} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/actions?Action="+tc.action, nil))
		assert.Equal(t, tc.status, response.Code, tc.action)
	}
}

func TestAssetEnvelopePreservesPanicRecovery(t *testing.T) {
	engine := gin.New()
	engine.Use(gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, _ any) {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}))
	engine.POST("/doubao/", assetActionEnvelope(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"partial": true})
		panic("test panic")
	})
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/doubao/", nil))
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.JSONEq(t, `{"error":"internal error"}`, response.Body.String())
}

func TestListModelsSupportsOpenAIAndGeminiAuthentication(t *testing.T) {
	setupRelayRouterTestDB(t)

	user := model.User{
		Username: "models-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "modelstestkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	tests := []struct {
		name           string
		path           string
		headerName     string
		expectedObject string
		expectedField  string
	}{
		{
			name:           "OpenAI bearer token",
			path:           "/v1/models",
			headerName:     "Authorization",
			expectedObject: "list",
			expectedField:  "data",
		},
		{
			name:          "Gemini API key header",
			path:          "/v1/models",
			headerName:    "x-goog-api-key",
			expectedField: "models",
		},
		{
			name:          "Gemini API key query",
			path:          "/v1/models?key=modelstestkey",
			expectedField: "models",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.headerName != "" {
				value := "modelstestkey"
				if test.headerName == "Authorization" {
					value = "Bearer " + value
				}
				request.Header.Set(test.headerName, value)
			}

			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload, test.expectedField)
			assert.NotContains(t, payload, "error")
			if test.expectedObject != "" {
				assert.Equal(t, test.expectedObject, payload["object"])
			}
		})
	}
}

func setupRelayRouterTestDB(t *testing.T) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalIsMasterNode := common.IsMasterNode
	originalRedisEnabled := common.RedisEnabled
	originalSQLitePath := common.SQLitePath
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	common.IsMasterNode = false
	common.RedisEnabled = false
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.Ability{}))

	t.Cleanup(func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		common.IsMasterNode = originalIsMasterNode
		common.RedisEnabled = originalRedisEnabled
		common.SQLitePath = originalSQLitePath
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
	})
}
