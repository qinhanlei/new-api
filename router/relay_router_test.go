package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssetActionsKeepArkErrorsAndDoNotShadowVideoRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.RequestId())
	SetAssetRouter(engine)
	source, err := plugins.Source("doubao")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	_, err = registry.RegisterFactory(source, jsplugin.Options{Key: "doubao"})
	require.NoError(t, err)
	builder := newPluginGenerationBuilder(engine.Routes(), nil, testPluginRouteHandlers(
		func(c *gin.Context, _ *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
			c.String(http.StatusAccepted, binding.Plugin.Meta.Key)
		},
	))
	require.NoError(t, registry.SetGenerationPreparer(builder.prepare))
	require.Empty(t, registry.RoutingErrors())
	engine.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)

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

	for _, path := range []string{"/api/v3/contents/generations/tasks", "/doubao/api/v3/contents/generations/tasks"} {
		response = httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		assert.Equal(t, http.StatusAccepted, response.Code, path)
		assert.Equal(t, "doubao", response.Body.String(), path)
	}
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

func TestAssetManagementEndpointKeepsOtherHostRouteProtections(t *testing.T) {
	for _, testCase := range []struct {
		name, hostMethod, hostPath, pluginKey, pluginPath string
		blocked                                           bool
		wantStatus                                        int
	}{
		{name: "another method still reserves the namespace", hostMethod: http.MethodGet, hostPath: "/doubao/", pluginKey: "doubao", pluginPath: "/probe/jobs", blocked: true},
		{name: "another path still reserves the namespace", hostMethod: http.MethodPost, hostPath: "/doubao/other", pluginKey: "doubao", pluginPath: "/probe/jobs", blocked: true},
		{name: "management path stays with the host", hostMethod: http.MethodPost, hostPath: "/doubao/", pluginKey: "asset-route-probe", pluginPath: "/doubao/", wantStatus: http.StatusUnauthorized},
		{name: "management redirect stays with the host", hostMethod: http.MethodPost, hostPath: "/doubao/", pluginKey: "asset-route-probe", pluginPath: "/doubao", wantStatus: http.StatusTemporaryRedirect},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			engine := gin.New()
			SetAssetRouter(engine)
			if testCase.blocked {
				engine.Handle(testCase.hostMethod, testCase.hostPath, func(c *gin.Context) { c.Status(http.StatusNoContent) })
			}
			registry := jsplugin.NewRegistry()
			source := routerPluginSource(testCase.pluginKey, "1.0.0", fmt.Sprintf(`[{method:"POST",path:%q,type:"submit"}]`, testCase.pluginPath))
			plugin, err := registry.RegisterFactory(source, jsplugin.Options{})
			require.NoError(t, err)
			builder := newPluginGenerationBuilder(engine.Routes(), nil, testPluginRouteHandlers(
				func(c *gin.Context, _ *jsplugin.RoutingGeneration, _ jsplugin.RouteBinding) {
					c.String(http.StatusAccepted, "plugin")
				},
			))
			require.NoError(t, registry.SetGenerationPreparer(builder.prepare))
			engine.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)
			if testCase.blocked {
				assert.Contains(t, registry.RoutingErrors()[testCase.pluginKey], `plugin key "doubao" is a host path segment`)
				_, active := registry.Get(testCase.pluginKey)
				assert.False(t, active)
				return
			}
			require.Empty(t, registry.RoutingErrors())
			assert.Equal(t, []jsplugin.SkippedRoute{{PluginKey: testCase.pluginKey, Route: plugin.Meta.Routes[0], Reason: "host_route"}}, registry.Generation().SkippedRoutes())
			response := performPluginRequest(engine, http.MethodPost, testCase.pluginPath)
			assert.Equal(t, testCase.wantStatus, response.Code)
			assert.NotEqual(t, "plugin", response.Body.String())
			response = performPluginRequest(engine, http.MethodPost, "/"+testCase.pluginKey+testCase.pluginPath)
			assert.Equal(t, http.StatusAccepted, response.Code)
			assert.Equal(t, "plugin", response.Body.String())
		})
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

func TestGeminiRelayHandsLongRunningPredictionToPluginRoute(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	user := model.User{Username: "veo-user", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "veotestkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)

	engine := gin.New()
	SetRelayRouter(engine)
	source, err := plugins.Source("google")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	_, err = registry.RegisterFactory(source, jsplugin.Options{Key: "google"})
	require.NoError(t, err)
	// The plugin route authenticates as in production, then names itself.
	handlers := func(generation *jsplugin.RoutingGeneration, group jsplugin.RouteGroup) []gin.HandlerFunc {
		production := productionPluginRouteHandlers(generation, group)
		return []gin.HandlerFunc{production[0], production[1], production[2], func(c *gin.Context) {
			c.String(http.StatusOK, "plugin %s for user %d", group.Bindings[0].Plugin.Meta.Key, c.GetInt("id"))
		}}
	}
	require.NoError(t, registry.SetGenerationPreparer(newPluginGenerationBuilder(engine.Routes(), nil, handlers).prepare))
	require.Empty(t, registry.RoutingErrors())
	previousRegistry := defaultPluginDispatcher.registry
	defaultPluginDispatcher.registry = registry
	t.Cleanup(func() { defaultPluginDispatcher.registry = previousRegistry })
	engine.NoRoute(defaultPluginDispatcher.dispatch)

	for _, testCase := range []struct {
		method   string
		path     string
		toPlugin bool
	}{
		{method: http.MethodPost, path: "/v1beta/models/veo-3.1-generate-preview:predictLongRunning", toPlugin: true},
		{method: http.MethodGet, path: "/v1beta/models/veo-3.1-generate-preview/operations/task_sample", toPlugin: true},
		{method: http.MethodPost, path: "/v1beta/models/gemini-2.5-flash:generateContent"},
	} {
		t.Run(testCase.path, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(`{}`))
			request.Header.Set("x-goog-api-key", "veotestkey")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			if testCase.toPlugin {
				assert.Equal(t, fmt.Sprintf("plugin google for user %d", user.Id), recorder.Body.String())
				return
			}
			// The relay looks for a Gemini channel serving the model; there is none.
			assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"model_not_found"`)
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
