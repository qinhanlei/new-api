package router

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type unreadAssetUploadBody struct {
	reads int
}

func (b *unreadAssetUploadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func (*unreadAssetUploadBody) Close() error { return nil }

// accessTokenExemptRoutes are /api routes that never reach dashboard
// authentication, so a personal access token has no rule to satisfy there.
var accessTokenExemptRoutes = []string{
	// Anonymous pages and setup.
	"GET /api/setup",
	"POST /api/setup",
	"GET /api/status",
	"GET /api/uptime/status",
	"GET /api/notice",
	"GET /api/user-agreement",
	"GET /api/privacy-policy",
	"GET /api/about",
	"GET /api/home_page_content",
	"GET /api/ratio_config",
	"GET /api/user/groups",

	// Anonymous account entry points.
	"GET /api/verification",
	"GET /api/reset_password",
	"POST /api/user/reset",
	"POST /api/user/register",
	"GET /api/user/login/encryption-key",
	"POST /api/user/login",
	"POST /api/user/login/2fa",
	"POST /api/user/login/verify",
	"POST /api/user/login/passkey/begin",
	"POST /api/user/login/passkey/finish",
	"POST /api/user/passkey/login/begin",
	"POST /api/user/passkey/login/finish",
	"GET /api/oauth/wechat",
	"GET /api/oauth/telegram/login",
	"GET /api/oauth/telegram/bind/:flow_token",

	// Browser session cookie endpoints.
	"POST /api/user/auth/refresh",
	"POST /api/user/auth/logout",

	// Payment callbacks.
	"POST /api/stripe/webhook",
	"POST /api/creem/webhook",
	"POST /api/waffo/webhook",
	"POST /api/waffo-pancake/webhook/:env",
	"GET /api/user/epay/notify",
	"POST /api/user/epay/notify",
	"GET /api/subscription/epay/notify",
	"POST /api/subscription/epay/notify",
	"GET /api/subscription/epay/return",
	"POST /api/subscription/epay/return",

	// API key authentication (TokenAuthReadOnly).
	"GET /api/usage/token/",
	"GET /api/log/token",
	"POST /api/v3/contents/generations/tasks",
	"GET /api/v3/contents/generations/tasks/:task_id",
}

// accessTokenHelperRoutes are declared through handlePermissionRoute outside
// channelPermissionRoutes.
var accessTokenHelperRoutes = map[string]authz.Permission{
	"POST /api/asset-reconciliation/assets/:id/providers/:space_id": authz.ChannelSensitiveWrite,
	"POST /api/asset-reconciliation/tasks/:task_id":                 authz.ChannelSensitiveWrite,
	"GET /api/channel/:id/assets":                                   authz.ChannelRead,
	"POST /api/channel/:id/assets/cleanup":                          authz.ChannelSensitiveWrite,
	"POST /api/asset-reconciliation/channels/:id":                   authz.ChannelSensitiveWrite,
}

func newAccessTokenScopeTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// A permission route that is also in the static table panics here.
	require.NotPanics(t, func() {
		SetApiRouter(engine)
		SetAssetRouter(engine)
		SetArkVideoRouter(engine)
		SetDashboardRouter(engine)
		SetRelayRouter(engine)
		SetTaskPluginProtocolRouter(engine)
		SetVideoRouter(engine)
		SetTaskRouter(engine)
	})
	return engine
}

func TestAccessTokenRouteRulesCoverEveryDashboardRoute(t *testing.T) {
	engine := newAccessTokenScopeTestEngine(t)
	exempt := map[string]bool{}
	for _, key := range accessTokenExemptRoutes {
		exempt[key] = true
	}
	registered := map[string]bool{}
	panel := map[string]bool{}
	for _, route := range engine.Routes() {
		if !strings.HasPrefix(route.Path, "/api/") && !strings.HasPrefix(route.Path, "/pg/") {
			continue
		}
		key := route.Method + " " + route.Path
		registered[key] = true
		if !exempt[key] {
			panel[key] = true
		}
	}
	for _, key := range accessTokenExemptRoutes {
		assert.True(t, registered[key], "exempt route %s is no longer registered", key)
	}

	declared := map[string]int{}
	for _, key := range middleware.AccessTokenRouteRuleKeys() {
		declared[key]++
	}
	for key := range panel {
		assert.Equal(t, 1, declared[key], "panel route %s must be declared exactly once", key)
	}
	for key := range declared {
		assert.True(t, panel[key], "declared route %s is not a registered panel route", key)
	}

	permissionRoutes := map[string]authz.Permission{}
	for _, route := range channelPermissionRoutes {
		permissionRoutes[route.method+" "+joinPaths("/api/channel", route.path)] = route.permission
	}
	for key, permission := range accessTokenHelperRoutes {
		permissionRoutes[key] = permission
	}
	for key, permission := range permissionRoutes {
		rule, ok := middleware.AccessTokenRouteRule(key)
		require.True(t, ok, key)
		assert.Equal(t, "scope", rule.Kind(), key)
		assert.Equal(t, service.AccessTokenScopeOf(permission), rule.Scope(), key)
	}
}

func TestLocalAssetContentRouteUsesSignedAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Asset{}, &model.Task{}, &model.TaskAssetUse{}))
	previousDB, previousEnabled, previousMaster := model.DB, common.AssetLibraryEnabled, common.IsMasterNode
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousPublicAddress := system_setting.TaskPublicAddress
	model.DB, common.AssetLibraryEnabled, common.IsMasterNode = db, true, true
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, previousLog)
	system_setting.TaskPublicAddress = "https://assets.example.com"
	t.Cleanup(func() {
		model.DB, common.AssetLibraryEnabled, common.IsMasterNode = previousDB, previousEnabled, previousMaster
		common.SetDatabaseTypes(previousMain, previousLog)
		system_setting.TaskPublicAddress = previousPublicAddress
		require.NoError(t, sqlDB.Close())
	})
	t.Setenv("ASSET_STORAGE", "local")
	t.Setenv("ASSET_LOCAL_DIR", t.TempDir())
	store, err := service.GetAssetStore()
	require.NoError(t, err)
	user := model.User{Username: "asset-content-owner", AffCode: "asset-content-owner"}
	require.NoError(t, db.Create(&user).Error)
	var source bytes.Buffer
	require.NoError(t, png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	asset, err := service.CreateAsset(t.Context(), user.Id, "original.png", bytes.NewReader(source.Bytes()), store)
	require.NoError(t, err)
	signedURL, err := store.Presign(t.Context(), asset.ObjectKey)
	require.NoError(t, err)
	parsed, err := url.Parse(signedURL)
	require.NoError(t, err)

	engine := gin.New()
	middleware.SetUpLogger(engine)
	SetAssetRouter(engine)
	for _, tc := range []struct {
		name       string
		method     string
		rangeValue string
		wantStatus int
		wantBody   []byte
	}{
		{"download", http.MethodGet, "", http.StatusOK, source.Bytes()},
		{"repeat download", http.MethodGet, "", http.StatusOK, source.Bytes()},
		{"head", http.MethodHead, "", http.StatusOK, nil},
		{"range", http.MethodGet, "bytes=0-7", http.StatusPartialContent, source.Bytes()[:8]},
		{"unsatisfiable range", http.MethodGet, "bytes=10000-", http.StatusRequestedRangeNotSatisfiable, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, parsed.RequestURI(), nil)
			request.Header.Set("Range", tc.rangeValue)
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			assert.Equal(t, tc.wantStatus, response.Code)
			assert.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
			assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
			if tc.wantStatus < 400 {
				assert.Equal(t, "image/png", response.Header().Get("Content-Type"))
				assert.Equal(t, tc.wantBody, response.Body.Bytes())
			}
			if tc.method == http.MethodHead {
				assert.Equal(t, strconv.Itoa(source.Len()), response.Header().Get("Content-Length"))
			}
			if tc.wantStatus == http.StatusPartialContent {
				assert.Equal(t, "bytes 0-7/"+strconv.Itoa(source.Len()), response.Header().Get("Content-Range"))
			}
		})
	}

	t.Run("slave cannot access local storage", func(t *testing.T) {
		previous := common.IsMasterNode
		common.IsMasterNode = false
		t.Cleanup(func() { common.IsMasterNode = previous })
		directory := filepath.Join(t.TempDir(), "must-not-exist")
		t.Setenv("ASSET_LOCAL_DIR", directory)
		var before int64
		require.NoError(t, db.Model(&model.Asset{}).Count(&before).Error)
		uploads := gin.New()
		// These handlers receive the identity supplied by authentication. This
		// case tests storage rejection, not API key or browser authentication.
		for _, path := range []string{"/v1/assets", "/api/assets"} {
			uploads.POST(path, func(c *gin.Context) {
				c.Set("id", user.Id)
				controller.CreateAsset(c)
			})
			body := &unreadAssetUploadBody{}
			request := httptest.NewRequest(http.MethodPost, path, body)
			request.Header.Set("Content-Type", "multipart/form-data; boundary=unread")
			response := httptest.NewRecorder()
			uploads.ServeHTTP(response, request)
			assert.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), "asset_storage_unavailable")
			assert.Zero(t, body.reads, "a slave must reject before parsing the upload")
		}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(method, parsed.RequestURI(), nil))
			assert.Equal(t, http.StatusServiceUnavailable, response.Code)
		}
		var after int64
		require.NoError(t, db.Model(&model.Asset{}).Count(&after).Error)
		assert.Equal(t, before, after)
		_, err := os.Stat(directory)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	for _, tc := range []struct {
		name   string
		method string
		query  string
	}{
		{"missing capability", http.MethodGet, "expires=" + parsed.Query().Get("expires")},
		{"tampered capability", http.MethodGet, "expires=" + parsed.Query().Get("expires") + "&access=" + strings.Repeat("A", 43)},
		{"expired capability", http.MethodGet, "expires=1&access=" + parsed.Query().Get("access")},
		{"duplicate capability", http.MethodGet, parsed.RawQuery + "&access=" + parsed.Query().Get("access")},
		{"duplicate expiry", http.MethodGet, parsed.RawQuery + "&expires=" + parsed.Query().Get("expires")},
		{"malformed query", http.MethodGet, parsed.RawQuery + "&bad=%zz"},
		{"unexpected parameter", http.MethodGet, parsed.RawQuery + "&extra=yes"},
		{"unsupported method", http.MethodPost, parsed.RawQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, parsed.Path+"?"+tc.query, nil)
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			assert.Equal(t, http.StatusNotFound, response.Code)
			assert.NotContains(t, response.Body.String(), parsed.Query().Get("access"))
		})
	}

	// Deletion prevents fresh access, but an accepted consumer retains its source.
	require.NoError(t, model.DeleteUserAsset(t.Context(), user.Id, asset.ID))
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, parsed.RequestURI(), nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
	task := model.Task{TaskID: "task_asset_content", UserId: user.Id, Status: model.TaskStatusSubmitted, CreatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Create(&model.TaskAssetUse{TaskID: task.TaskID, AssetID: asset.ID}).Error)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, parsed.RequestURI(), nil))
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, source.Bytes(), response.Body.Bytes())
	require.NoError(t, db.Model(&task).Update("status", model.TaskStatusSuccess).Error)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, parsed.RequestURI(), nil))
	assert.Equal(t, http.StatusNotFound, response.Code)

	// Exhausted invalid-access budgets are rejected before any storage access,
	// even if the configured local directory no longer exists.
	request := httptest.NewRequest(http.MethodGet, parsed.RequestURI(), nil)
	request.RemoteAddr = "198.51.100.77:1234"
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	for range system_setting.LoadTaskArtifactAccessLimits().InvalidRatePerMinute {
		require.True(t, middleware.AllowInvalidContentAccessAttempt(context))
	}
	missingDirectory := filepath.Join(t.TempDir(), "missing")
	t.Setenv("ASSET_LOCAL_DIR", missingDirectory)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	_, err = os.Stat(missingDirectory)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestAccessTokenCatalogCoversEveryPermission(t *testing.T) {
	catalog := service.AccessTokenCatalog(1, common.RoleRootUser)
	scopes := map[string]bool{}
	resources := map[string]int{}
	for _, group := range catalog {
		for _, resource := range group.Resources {
			resources[resource.Resource]++
			for _, action := range resource.Actions {
				scopes[resource.Resource+":"+action.Action] = true
			}
		}
	}
	for _, permission := range authz.AllPermissions() {
		assert.True(t, scopes[service.AccessTokenScopeOf(permission)], "root catalog lacks %s", service.AccessTokenScopeOf(permission))
	}
	for resource, count := range resources {
		assert.Equal(t, 1, count, "resource %s appears in more than one catalog entry", resource)
	}
}

func TestAccessTokenVerificationScopesAreAllListed(t *testing.T) {
	files, err := filepath.Glob("../service/*.go")
	require.NoError(t, err)
	fileSet := token.NewFileSet()
	var scopes []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, file, nil, 0)
		require.NoError(t, err)
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				value := spec.(*ast.ValueSpec)
				for i, name := range value.Names {
					if !strings.HasPrefix(name.Name, "VerificationScope") || i >= len(value.Values) {
						continue
					}
					literal, ok := value.Values[i].(*ast.BasicLit)
					require.True(t, ok, name.Name)
					scope, err := strconv.Unquote(literal.Value)
					require.NoError(t, err)
					scopes = append(scopes, scope)
				}
			}
		}
	}
	require.NotEmpty(t, scopes)
	for _, scope := range scopes {
		_, listed := service.AccessTokenVerificationScope(scope)
		assert.True(t, listed, "verification scope %s has no access token rule", scope)
	}
}
