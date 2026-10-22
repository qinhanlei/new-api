package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskArtifactAccessIsRedactedAndVerifiedBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "task-artifact-middleware-secret"
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	access, err := service.IssueTaskArtifactAccess("task-1", "video-main")
	require.NoError(t, err)

	router := gin.New()
	router.Use(redactTaskArtifactAccessQuery())
	router.GET(
		"/v1/tasks/:key/artifacts/:artifact_key/content",
		TokenOrTaskArtifactAccessAuth("key", "artifact_key"),
		func(c *gin.Context) {
			assert.True(t, IsTaskArtifactAccess(c))
			assert.NotContains(t, c.Request.URL.RawQuery, service.TaskArtifactAccessQueryParameter)
			assert.Equal(t, "kept", c.Query("keep"))
			c.Status(http.StatusNoContent)
		},
	)
	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/tasks/task-1/artifacts/video-main/content?access="+urlQueryEscape(access)+"&keep=kept",
		nil,
	)
	request.RemoteAddr = "192.0.2.1:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestTaskArtifactAccessRejectsTamperedAndEmptyCapabilitiesAsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "task-artifact-middleware-reject-secret"
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	router := gin.New()
	router.Use(redactTaskArtifactAccessQuery())
	router.GET(
		"/v1/tasks/:key/artifacts/:artifact_key/content",
		TokenOrTaskArtifactAccessAuth("key", "artifact_key"),
		func(c *gin.Context) { c.Status(http.StatusNoContent) },
	)

	for _, query := range []string{
		"?access=",
		"?access=invalid",
		"?access=first&access=second",
		"?access=" + strings.Repeat("x", 1024),
		"?access=%20" + strings.Repeat("A", 43) + "%20",
	} {
		request := httptest.NewRequest(
			http.MethodGet,
			"/v1/tasks/task-1/artifacts/video-main/content"+query,
			nil,
		)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusNotFound, recorder.Code)
	}
}

func TestTaskArtifactAccessLimiterDefaults(t *testing.T) {
	limits := system_setting.TaskArtifactAccessLimits{
		InvalidRatePerMinute: system_setting.DefaultTaskArtifactInvalidRateLimitPerMinute,
		GlobalConcurrency:    system_setting.DefaultTaskArtifactGlobalConcurrency,
		IPConcurrency:        system_setting.DefaultTaskArtifactIPConcurrency,
		ObjectConcurrency:    system_setting.DefaultTaskArtifactObjectConcurrency,
	}
	limiter := newTaskArtifactAccessLimiter(limits)
	now := time.Unix(1000, 0)
	releases := make([]func(), 0, limits.ObjectConcurrency)
	for i := 0; i < limits.ObjectConcurrency; i++ {
		release, ok := limiter.acquire("192.0.2.1", "task-1", "video")
		require.True(t, ok)
		releases = append(releases, release)
	}
	_, ok := limiter.acquire("192.0.2.2", "task-1", "video")
	assert.False(t, ok, "task+key concurrency is shared across IPs")
	for _, release := range releases {
		release()
	}

	rateLimiter := newTaskArtifactAccessLimiter(limits)
	for i := 0; i < limits.InvalidRatePerMinute; i++ {
		assert.True(t, rateLimiter.invalidAttempt(now, "192.0.2.10"))
	}
	assert.False(t, rateLimiter.invalidAttempt(now, "192.0.2.10"))
	assert.True(t, rateLimiter.invalidAttempt(now.Add(time.Minute), "192.0.2.10"))
}

func TestRedactTaskArtifactAccessAlsoCoversLegacyVideoRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(redactTaskArtifactAccessQuery())
	router.GET("/v1/videos/:task_id/content", func(c *gin.Context) {
		assert.NotContains(t, c.Request.URL.RawQuery, "access")
		assert.NotContains(t, c.Request.RequestURI, "secret-capability")
		assert.Equal(t, "ok", c.Query("keep"))
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/videos/task-1/content?access=secret-capability&keep=ok",
		nil,
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestSetUpLoggerNeverWritesTaskArtifactAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousWriter := gin.DefaultWriter
	var output bytes.Buffer
	gin.DefaultWriter = &output
	t.Cleanup(func() { gin.DefaultWriter = previousWriter })

	router := gin.New()
	SetUpLogger(router)
	router.GET("/v1/tasks/:key/artifacts/:artifact_key/content", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/tasks/task-1/artifacts/video/content?access=never-log-this&keep=ok",
		nil,
	)
	router.ServeHTTP(httptest.NewRecorder(), request)

	assert.False(t, strings.Contains(output.String(), "never-log-this"))
}

func TestLocalAssetContentAccessQueryValidationAndRedaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousWriter := gin.DefaultWriter
	var output bytes.Buffer
	gin.DefaultWriter = &output
	t.Cleanup(func() { gin.DefaultWriter = previousWriter })

	for _, tc := range []struct {
		name  string
		query string
		valid bool
	}{
		{"valid", "expires=1800000000&access=secret-capability", true},
		{"encoded key", "expires=1800000000&%61ccess=secret-capability", true},
		{"missing capability", "expires=1800000000", false},
		{"empty capability", "expires=1800000000&access=", false},
		{"missing expiry", "access=secret-capability", false},
		{"duplicate capability", "expires=1800000000&access=secret-capability&access=other", false},
		{"duplicate expiry", "expires=1800000000&expires=1800000001&access=secret-capability", false},
		{"extra query", "expires=1800000000&access=secret-capability&extra=yes", false},
		{"malformed escape", "expires=%zz&access=secret-capability", false},
		{"malformed separator", "expires=1800000000;extra=yes&access=secret-capability", false},
		{"malformed capability", "expires=1800000000&access=secret-capability%zz", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, logger := range []bool{false, true} {
				router := gin.New()
				if logger {
					SetUpLogger(router)
				}
				router.GET("/v1/assets/:id/content", func(c *gin.Context) {
					expires, access := LocalAssetContentAccessQuery(c)
					if tc.valid {
						assert.Equal(t, "1800000000", expires)
						assert.Equal(t, "secret-capability", access)
					} else {
						assert.Empty(t, expires)
						assert.Empty(t, access)
					}
					assert.NotContains(t, c.Request.URL.RawQuery, "secret-capability")
					assert.NotContains(t, c.Request.RequestURI, "secret-capability")
					c.Status(http.StatusNoContent)
				})
				request := httptest.NewRequest(http.MethodGet, "/v1/assets/na_asset_test/content?"+tc.query, nil)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				assert.Equal(t, http.StatusNoContent, response.Code)
			}
		})
	}
	assert.NotContains(t, output.String(), "secret-capability")
}

func TestLocalAssetContentSharesAnonymousArtifactLimits(t *testing.T) {
	previousLimiter := taskArtifactAnonymousLimiter
	taskArtifactAnonymousLimiter = newTaskArtifactAccessLimiter(system_setting.TaskArtifactAccessLimits{
		InvalidRatePerMinute: 1,
		GlobalConcurrency:    1,
		IPConcurrency:        1,
		ObjectConcurrency:    1,
	})
	t.Cleanup(func() { taskArtifactAnonymousLimiter = previousLimiter })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/assets/na_asset_test/content", nil)
	c.Request.RemoteAddr = "192.0.2.1:1234"

	releaseArtifact, ok := taskArtifactAnonymousLimiter.acquire("192.0.2.2", "task-1", "video")
	require.True(t, ok)
	_, ok = AcquireAnonymousContentAccess(c, "na_asset_test", "source")
	assert.False(t, ok, "asset downloads share the global task artifact budget")
	releaseArtifact()

	releaseAsset, ok := AcquireAnonymousContentAccess(c, "na_asset_test", "source")
	require.True(t, ok)
	releaseAsset()
	releaseAsset()
	releaseAsset, ok = AcquireAnonymousContentAccess(c, "na_asset_test", "source")
	require.True(t, ok, "release is idempotent and restores concurrency")
	releaseAsset()

	assert.True(t, AllowInvalidContentAccessAttempt(c))
	assert.False(t, taskArtifactAnonymousLimiter.invalidAttempt(time.Now(), "192.0.2.1"), "invalid capabilities share the existing per-IP budget")
	_, ok = AcquireAnonymousContentAccess(c, "na_asset_test", "source")
	assert.False(t, ok, "exhausted invalid-access budget blocks before opening storage")
	assert.False(t, taskArtifactAnonymousLimiter.invalidRateExceeded(time.Now().Add(taskArtifactAccessRateWindow), "192.0.2.1"), "the failed-access budget expires after its window")
}

func urlQueryEscape(value string) string {
	replacer := strings.NewReplacer("+", "%2B", "=", "%3D")
	return replacer.Replace(value)
}
