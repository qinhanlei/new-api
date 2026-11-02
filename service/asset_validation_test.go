package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func assetValidationFixture(t *testing.T) (dto.AssetActor, dto.AssetValidationCreate) {
	t.Helper()
	assetTestDatabase(t)
	previousAddress, previousRegistry, previousSecret := system_setting.ServerAddress, jsplugin.DefaultRegistry, common.CryptoSecret
	common.CryptoSecret = "asset-validation-isolated-secret-32-characters"
	system_setting.ServerAddress = "https://gateway.example"
	jsplugin.DefaultRegistry = jsplugin.NewRegistry()
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		common.CryptoSecret = previousSecret
		_ = jsplugin.DefaultRegistry.Unregister("doubao")
		jsplugin.DefaultRegistry = previousRegistry
	})
	source, err := os.ReadFile("../plugins/tasks/doubao/plugin.js")
	require.NoError(t, err)
	_, err = jsplugin.DefaultRegistry.Register(string(source), jsplugin.Options{})
	require.NoError(t, err)
	require.NoError(t, model.DB.Create(&model.User{Id: 1, Username: "validation-owner", Role: common.RoleCommonUser, Group: "default", Status: common.UserStatusEnabled, AuthVersion: 1}).Error)
	require.NoError(t, model.DB.Create(&model.Token{Id: 1, UserId: 1, Key: "validation-inference-key", Status: common.TokenStatusEnabled, ExpiredTime: -1}).Error)
	for _, id := range []int{1, 2} {
		require.NoError(t, model.DB.Create(&model.Ability{ChannelId: id, Model: "doubao-seedance-2-0-260128", Group: "default", Enabled: true}).Error)
		config, err := model.LoadAssetChannelConfig(t.Context(), id)
		require.NoError(t, err)
		config.AuthType, config.AccessKey, config.SecretKey = "aksk", "validation-ak", "validation-signing-secret"
		require.NoError(t, model.SaveAssetChannelConfig(t.Context(), id, config))
	}
	return dto.AssetActor{UserID: 1, InitiatorID: 1, TokenID: 1, ChannelIDs: []int{1, 2}},
		dto.AssetValidationCreate{Name: "Portrait owner", ProjectName: "default", IdempotencyKey: "fixture-request-1"}
}

func TestAssetValidationOrdinaryUserCannotChooseChannel(t *testing.T) {
	actor, input := assetValidationFixture(t)
	calls := assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
	input.ChannelID = 1
	_, err := CreateAssetValidationSession(t.Context(), actor, input)
	var failure *AssetError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, http.StatusForbidden, failure.HTTPStatus)
	assert.Empty(t, *calls, "a denied manual selection must not contact the provider")
	_, err = ListAssetValidationChannels(t.Context(), actor)
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, http.StatusForbidden, failure.HTTPStatus)
}

func TestAssetValidationChannelPermissionsUseInitiatorAndCredential(t *testing.T) {
	assetValidationFixture(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CasbinRule{}, &model.AuthzRole{}))
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() {
		common.IsMasterNode = previousMaster
		require.NoError(t, model.DB.Migrator().DropTable(&model.CasbinRule{}, &model.AuthzRole{}))
	})
	require.NoError(t, authz.Init(model.DB))
	for _, tc := range []struct {
		name   string
		role   int
		pat    bool
		relay  bool
		scopes []string
		allow  bool
	}{
		{name: "ordinary session", role: common.RoleCommonUser},
		{name: "administrator session", role: common.RoleAdminUser, allow: true},
		{name: "administrator PAT missing channel scope", role: common.RoleAdminUser, pat: true, scopes: []string{"asset:read", "asset:write"}},
		{name: "administrator scoped PAT", role: common.RoleAdminUser, pat: true, scopes: []string{"asset:write", "channel:read"}, allow: true},
		{name: "ordinary PAT cannot elevate with channel scope", role: common.RoleCommonUser, pat: true, scopes: []string{"channel:read"}},
		{name: "administrator relay token", role: common.RoleAdminUser, relay: true, allow: true},
		{name: "ordinary relay token", role: common.RoleCommonUser, relay: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", 1).Update("role", tc.role).Error)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			c.Set("id", 1)
			c.Set("role", tc.role)
			if tc.pat {
				c.Set("use_access_token", true)
				c.Set("access_token_id", 7)
				c.Set("access_token_scopes", tc.scopes)
			}
			if tc.relay {
				c.Set("token_id", 1)
				c.Set("role", 0) // TokenAuth supplies identity, not a dashboard role.
			}
			assert.Equal(t, tc.allow, CanReadAssetChannels(c))
			if tc.relay {
				actor, err := ResolveAssetActor(c)
				require.NoError(t, err)
				assert.Equal(t, tc.allow, actor.CanReadChannels)
			}
		})
	}
	require.NoError(t, authz.SetUserPermissions(1, authz.PermissionsMap{"channel": {"read": false}}))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Set("id", 1)
	c.Set("role", common.RoleAdminUser)
	assert.False(t, CanReadAssetChannels(c), "an administrator's explicit channel denial still applies")
}

func TestAssetValidationAutomaticAndManualChannelSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		manual    int
		canRead   bool
		pin       int
		invalidAK bool
		want      int
	}{
		{name: "automatic first eligible", want: 1},
		{name: "automatic skips invalid authentication", invalidAK: true, want: 2},
		{name: "administrator chooses eligible channel", manual: 2, canRead: true, want: 2},
		{name: "administrator relay pin", pin: 2, canRead: true, want: 2},
		{name: "manual choice conflicts with relay pin", manual: 1, pin: 2, canRead: true},
		{name: "manual choice outside owner eligibility", manual: 3, canRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			actor.CanReadChannels, input.ChannelID = tc.canRead, tc.manual
			if tc.pin != 0 {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
				c.Set("id", 1)
				c.Set("token_id", 1)
				require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", 1).Update("role", common.RoleRootUser).Error)
				GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: tc.pin, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
				var err error
				actor, err = ResolveAssetActor(c)
				require.NoError(t, err)
			}
			if tc.invalidAK {
				config, err := model.LoadAssetChannelConfig(t.Context(), 1)
				require.NoError(t, err)
				config.AccessKey = ""
				require.NoError(t, model.SaveAssetChannelConfig(t.Context(), 1, config))
			}
			calls := assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
			view, err := CreateAssetValidationSession(t.Context(), actor, input)
			if tc.want == 0 {
				var failure *AssetError
				require.ErrorAs(t, err, &failure)
				assert.Equal(t, http.StatusForbidden, failure.HTTPStatus)
				assert.Equal(t, "ValidationChannelUnavailable", failure.Code)
				assert.Empty(t, *calls)
				return
			}
			require.NoError(t, err)
			row, err := model.GetAssetValidationSession(t.Context(), 1, 1, view.BytedToken, "default")
			require.NoError(t, err)
			assert.Equal(t, tc.want, row.ChannelID)
			repeated, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			assert.Equal(t, view.BytedToken, repeated.BytedToken)
			assert.Equal(t, 1, (*calls)["CreateVisualValidateSession"])
		})
	}
}

func TestAssetValidationExistingManualRequestStillContinues(t *testing.T) {
	actor, input := assetValidationFixture(t)
	actor.CanReadChannels, input.ChannelID = true, 1
	calls := assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
	created, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	actor.CanReadChannels = false
	replayed, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	assert.Equal(t, created.BytedToken, replayed.BytedToken)
	assert.Zero(t, replayed.ChannelID)
	input.ChannelID = 2
	_, err = CreateAssetValidationSession(t.Context(), actor, input)
	assert.ErrorIs(t, err, model.ErrAssetConflict)
	input.IdempotencyKey = "new-manual-request"
	_, err = CreateAssetValidationSession(t.Context(), actor, input)
	var failure *AssetError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, http.StatusForbidden, failure.HTTPStatus)
	assert.Equal(t, 1, (*calls)["CreateVisualValidateSession"])
	allowAssetValidationPoll(t, created.BytedToken)
	completed, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Equal(t, model.AssetValidationCreated, completed.Status)
	assert.NotEmpty(t, completed.GroupID)
}

func TestAssetValidationDiagnosticsRequireChannelPermission(t *testing.T) {
	for _, tc := range []struct {
		name, action, publicCode string
		status                   int
	}{
		{"creation rejected", "CreateVisualValidateSession", "ValidationCreateFailed", http.StatusBadRequest},
		{"creation uncertain", "CreateVisualValidateSession", "ValidationResultUnknown", http.StatusBadGateway},
		{"result temporarily unavailable", "GetVisualValidateResult", "ValidationCheckUnavailable", http.StatusBadRequest},
		{"creation throttled", "CreateVisualValidateSession", "Throttling", http.StatusTooManyRequests},
		{"result throttled", "GetVisualValidateResult", "Throttling", http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			const detail = "Provider detail: account warehouse-alpha"
			calls := assetValidationUpstream(t, "", func(action string, _ map[string]any) (int, any) {
				if action == tc.action {
					return tc.status, map[string]any{"ResponseMetadata": map[string]any{"Error": dto.AssetFailure{Code: "ProviderDiagnostic", Message: detail}}}
				}
				return 0, nil
			})
			transport := httpClient.Transport
			httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(request)
				if response != nil && response.StatusCode == http.StatusTooManyRequests {
					response.Header.Set("Retry-After", "120")
				}
				return response, err
			})
			before := time.Now().Unix()
			view, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			if tc.action == "GetVisualValidateResult" {
				allowAssetValidationPoll(t, view.BytedToken)
				view, err = GetAssetValidationResult(t.Context(), actor, view.BytedToken, "default")
				require.NoError(t, err)
				assert.Equal(t, model.AssetValidationPending, view.Status)
				assert.True(t, view.CanPoll)
			}
			require.NotNil(t, view.Error)
			assert.Equal(t, tc.publicCode, view.Error.Code)
			assert.NotContains(t, view.Error.Message, detail)
			encoded, err := common.Marshal(view)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "ChannelId")
			assert.NotContains(t, string(encoded), "ChannelName")
			if tc.status == http.StatusTooManyRequests {
				assert.Equal(t, "Human verification is busy; retry shortly", view.Error.Message)
				assert.GreaterOrEqual(t, view.NextPollAt, before+120)
				assert.LessOrEqual(t, view.NextPollAt, time.Now().Unix()+120)
			}
			row, err := model.GetAssetValidationSession(t.Context(), 1, 1, view.BytedToken, "default")
			require.NoError(t, err)
			assert.Equal(t, detail, row.ErrorMessage)
			assert.Equal(t, view.NextPollAt, row.NextPollAt)
			public, err := GetAssetValidationRecord(t.Context(), actor, view.BytedToken, "default")
			require.NoError(t, err)
			require.NotNil(t, public.Error)
			assert.Equal(t, view.Error, public.Error)
			encoded, err = common.Marshal(public)
			require.NoError(t, err)
			for _, private := range []string{"ChannelId", "ChannelName", detail} {
				assert.NotContains(t, string(encoded), private)
			}
			actor.CanReadChannels = true
			admin, err := GetAssetValidationRecord(t.Context(), actor, view.BytedToken, "default")
			require.NoError(t, err)
			assert.Equal(t, 1, admin.ChannelID)
			assert.Equal(t, "channel-1", admin.ChannelName)
			require.NotNil(t, admin.Error)
			assert.Equal(t, detail, admin.Error.Message)
			actor.CanReadChannels = false
			var again dto.AssetValidationView
			if tc.action == "CreateVisualValidateSession" {
				again, err = CreateAssetValidationSession(t.Context(), actor, input)
			} else {
				again, err = GetAssetValidationResult(t.Context(), actor, view.BytedToken, "default")
			}
			require.NoError(t, err)
			assert.Equal(t, view, again)
			assert.Equal(t, 1, (*calls)[tc.action], "read-only history and early retries must not repeat an upstream operation")
		})
	}
}

func assetValidationBearerChannel(t *testing.T, id int, endpoint, key string) {
	t.Helper()
	channel, err := model.GetChannelById(id, true)
	require.NoError(t, err)
	channel.Key = key
	require.NoError(t, model.DB.Model(channel).Update("key", key).Error)
	config, err := model.LoadAssetChannelConfig(t.Context(), id)
	require.NoError(t, err)
	config.AuthType, config.AccessKey, config.SecretKey = "bearer", "", ""
	config.Endpoint, config.ChannelScope = endpoint, model.AssetChannelScope(channel)
	require.NoError(t, model.SaveAssetChannelConfig(t.Context(), id, config))
}

func assetValidationUpstream(t *testing.T, bearerKey string, override func(string, map[string]any) (int, any)) *map[string]int {
	t.Helper()
	calls := map[string]int{}
	httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
		action := request.URL.Query().Get("Action")
		calls[action]++
		var payload map[string]any
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		assert.Equal(t, "actual-project", payload["ProjectName"])
		if bearerKey == "" {
			assert.Contains(t, request.Header.Get("Authorization"), "HMAC-SHA256")
		} else {
			assert.Equal(t, "Bearer "+bearerKey, request.Header.Get("Authorization"))
		}
		status, response := override(action, payload)
		if response == nil {
			status = http.StatusOK
			switch action {
			case "CreateVisualValidateSession":
				response = map[string]any{"Result": map[string]any{"BytedToken": "upstream-portrait-token", "H5Link": "https://verify.example/h5?token=upstream-portrait-token", "CallbackURL": payload["CallbackURL"]}}
			case "GetVisualValidateResult":
				assert.Equal(t, "upstream-portrait-token", payload["BytedToken"])
				response = map[string]any{"Result": map[string]any{"GroupId": "upstream-human-group"}}
			case "GetAssetGroup":
				assert.Equal(t, "upstream-human-group", payload["Id"])
				response = map[string]any{"Result": map[string]any{"Id": "upstream-human-group", "GroupType": "LivenessFace", "ProjectName": "actual-project"}}
			default:
				t.Fatalf("unexpected upstream action %s", action)
			}
		}
		body, err := common.Marshal(response)
		require.NoError(t, err)
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
	})
	return &calls
}

// Move the durable poll/rate deadlines instead of depending on wall-clock sleeps.
func allowAssetValidationPoll(t *testing.T, id string) {
	t.Helper()
	require.NoError(t, model.DB.Model(&model.AssetValidationSession{}).Where("public_id = ?", id).Update("next_poll_at", 0).Error)
	require.NoError(t, model.DB.Model(&model.AssetValidationRateWindow{}).Where("1 = 1").Update("next_call_at", 0).Error)
}

func TestAssetValidationLifecycle(t *testing.T) {
	for _, authType := range []string{"aksk", "bearer"} {
		t.Run(authType, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			bearerKey := ""
			if authType == "bearer" {
				bearerKey = "channel-key"
				assetValidationBearerChannel(t, 1, "https://upstream.example", bearerKey)
			}
			callback := ""
			calls := assetValidationUpstream(t, bearerKey, func(action string, payload map[string]any) (int, any) {
				if action == "CreateVisualValidateSession" {
					callback = payload["CallbackURL"].(string)
				}
				return 0, nil
			})
			created, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			assert.Equal(t, "pending", created.Status)
			assert.True(t, created.CanPoll)
			assert.True(t, strings.HasPrefix(created.BytedToken, "validation-na-"))
			assert.NotEqual(t, "upstream-portrait-token", created.BytedToken)
			assert.Equal(t, callback, created.CallbackURL)
			row, err := model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
			require.NoError(t, err)
			encoded, err := common.Marshal(row)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "upstream-portrait-token")
			assert.NotContains(t, string(encoded), callback)
			duplicate, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			assert.Equal(t, created, duplicate)
			assert.Equal(t, 1, (*calls)["CreateVisualValidateSession"])
			input.Name = "Different request"
			_, err = CreateAssetValidationSession(t.Context(), actor, input)
			assert.ErrorIs(t, err, model.ErrAssetConflict)
			list, err := ListAssetValidationSessions(t.Context(), actor, "default")
			require.NoError(t, err)
			require.Len(t, list, 1)
			encoded, err = common.Marshal(list)
			require.NoError(t, err)
			for _, sensitive := range []string{"H5Link", "BytedToken", "CallbackURL", "upstream-portrait-token", callback} {
				assert.NotContains(t, string(encoded), sensitive)
			}
			callbackURL, err := url.Parse(callback)
			require.NoError(t, err)
			state := strings.TrimPrefix(callbackURL.Path, assetValidationCallbackPath)
			assert.NoError(t, ValidateAssetValidationCallback(t.Context(), state, "upstream-portrait-token"))
			assert.Error(t, ValidateAssetValidationCallback(t.Context(), state, "forged-token"))
			assert.Error(t, ValidateAssetValidationCallback(t.Context(), strings.Repeat("x", 43), "upstream-portrait-token"))
			var count int64
			require.NoError(t, model.DB.Model(&model.AssetGroup{}).Count(&count).Error)
			assert.Zero(t, count, "callbacks cannot create a local group")
			allowAssetValidationPoll(t, created.BytedToken)
			ready, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
			require.NoError(t, err)
			assert.Equal(t, "created", ready.Status)
			assert.False(t, ready.CanPoll)
			encoded, err = common.Marshal(ready)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), `"CanPoll":false`)
			assert.True(t, strings.HasPrefix(ready.GroupID, model.AssetGroupIDPrefix))
			assert.Empty(t, ready.H5Link)
			group, err := model.GetAssetGroup(t.Context(), 1, ready.GroupID, "default")
			require.NoError(t, err)
			assert.Equal(t, "LivenessFace", group.GroupType)
			assert.Equal(t, 1, group.InitialChannelID)
			replica, err := model.GetAssetGroupReplica(t.Context(), group.ID, 1)
			require.NoError(t, err)
			assert.Equal(t, "upstream-human-group", replica.UpstreamID)
			assert.Equal(t, model.AssetReady, replica.State)
			repeat, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
			require.NoError(t, err)
			assert.Equal(t, ready, repeat)
			assert.Equal(t, 1, (*calls)["GetVisualValidateResult"])
			assert.Equal(t, 1, (*calls)["GetAssetGroup"])
			row, err = model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
			require.NoError(t, err)
			assert.Empty(t, row.SecretCipher)
			assert.NoError(t, ValidateAssetValidationCallback(t.Context(), state, "upstream-portrait-token"), "a phone callback remains a neutral return page after desktop polling completes")
		})
	}
}

func TestAssetValidationRejectsUnauthorizedCompletion(t *testing.T) {
	for _, revoke := range []string{"owner", "owner group", "token", "token limits", "ability", "PAT scope", "session", "admin role", "channel"} {
		t.Run(revoke, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			if revoke == "PAT scope" || revoke == "admin role" {
				actor.TokenID, actor.AccessTokenID, actor.AuthSessionID = 0, 9, model.AccessTokenSessionID(9)
				actor.UserAuthVersion, actor.SessionVersion = 1, model.AccessTokenSessionVersion
				if revoke == "admin role" {
					actor.InitiatorID = 2
					require.NoError(t, model.DB.Create(&model.User{Id: 2, Username: "validation-admin", AffCode: "validation-admin", Group: "default", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, AuthVersion: 1}).Error)
				}
				pat := model.UserAccessToken{Id: 9, UserId: actor.InitiatorID, TokenHash: strings.Repeat("f", 64), ExpiresAt: time.Now().Add(time.Hour).Unix()}
				require.NoError(t, pat.SetScopes([]string{"asset:write", "user:write"}))
				require.NoError(t, model.DB.Create(&pat).Error)
			} else if revoke == "session" {
				actor.TokenID, actor.AuthSessionID, actor.UserAuthVersion, actor.SessionVersion = 0, "validation-browser", 1, 1
				require.NoError(t, model.DB.Create(&model.UserSession{SID: actor.AuthSessionID, UserID: 1, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, ExpiresAt: time.Now().Add(time.Hour).Unix()}).Error)
			}
			calls := assetValidationUpstream(t, "", func(action string, _ map[string]any) (int, any) {
				// Revoke while the final upstream request is in flight, not just before
				// entering the service, to protect the final binding boundary.
				if action == "GetAssetGroup" {
					switch revoke {
					case "owner":
						require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", 1).Update("status", common.UserStatusDisabled).Error)
					case "token":
						require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", 1).Update("status", common.TokenStatusDisabled).Error)
					case "owner group":
						require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", 1).Update("group", "no-validation-abilities").Error)
					case "token limits":
						require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", 1).Updates(map[string]any{"model_limits_enabled": true, "model_limits": "unrelated-model"}).Error)
					case "ability":
						require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", 1).Update("enabled", false).Error)
					case "PAT scope":
						require.NoError(t, model.DB.Model(&model.UserAccessToken{}).Where("id = ?", 9).Update("scopes", `["asset:read"]`).Error)
					case "session":
						require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", actor.AuthSessionID).Update("revoked_at", time.Now().Unix()).Error)
					case "admin role":
						require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", 2).Update("role", common.RoleCommonUser).Error)
					case "channel":
						require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 1).Update("status", common.ChannelStatusManuallyDisabled).Error)
					}
				}
				return 0, nil
			})
			created, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			allowAssetValidationPoll(t, created.BytedToken)
			_, err = GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
			require.Error(t, err)
			assert.Equal(t, 1, (*calls)["GetAssetGroup"])
			var groups int64
			require.NoError(t, model.DB.Model(&model.AssetGroup{}).Count(&groups).Error)
			assert.Zero(t, groups)
			row, err := model.GetAssetValidationSession(t.Context(), 1, actor.InitiatorID, created.BytedToken, "default")
			require.NoError(t, err)
			assert.Equal(t, "upstream-human-group", row.UpstreamGroupID, "known result remains available for reconciliation")
			assert.Empty(t, row.SecretCipher)
		})
	}
}

func TestAssetValidationPersistsResultBeforeBinding(t *testing.T) {
	actor, input := assetValidationFixture(t)
	failGroup := true
	calls := assetValidationUpstream(t, "", func(action string, _ map[string]any) (int, any) {
		if action == "GetAssetGroup" && failGroup {
			return http.StatusServiceUnavailable, map[string]any{"error": "temporarily unavailable"}
		}
		return 0, nil
	})
	created, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	allowAssetValidationPoll(t, created.BytedToken)
	unknown, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Equal(t, "unknown", unknown.Status)
	assert.True(t, unknown.CanPoll)
	row, err := model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Equal(t, "upstream-human-group", row.UpstreamGroupID)
	assert.Empty(t, row.SecretCipher)
	callback, err := url.Parse(created.CallbackURL)
	require.NoError(t, err)
	assert.NoError(t, ValidateAssetValidationCallback(t.Context(), strings.TrimPrefix(callback.Path, assetValidationCallbackPath), "upstream-portrait-token"), "a persisted result keeps the phone return page usable while binding is retried")
	require.NoError(t, model.DB.Model(row).Update("expires_at", time.Now().Add(-time.Minute).Unix()).Error)
	list, err := ListAssetValidationSessions(t.Context(), actor, "default")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.True(t, list[0].CanPoll, "a saved group remains recoverable after the H5 link expires")
	assert.True(t, list[0].BindingPending)
	assert.True(t, list[0].CanContinue)
	assert.False(t, list[0].CanArchive)
	// A different process can continue with the durable GroupId after the H5
	// credential and even the old encryption key have expired or been rotated.
	common.CryptoSecret = "another-application-secret"
	allowAssetValidationPoll(t, created.BytedToken)
	failGroup = false
	ready, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Equal(t, "created", ready.Status)
	assert.Equal(t, 1, (*calls)["GetVisualValidateResult"])
	assert.Equal(t, 2, (*calls)["GetAssetGroup"])
}

func TestAssetValidationDoesNotReplayUncertainCreate(t *testing.T) {
	actor, input := assetValidationFixture(t)
	calls := 0
	httpClient.Transport = assetRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("connection lost after accepting the request")
	})
	view, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	assert.Equal(t, "unknown", view.Status)
	assert.False(t, view.CanPoll)
	assert.Empty(t, view.H5Link)
	repeated, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	assert.Equal(t, view.BytedToken, repeated.BytedToken)
	assert.Equal(t, 1, calls)
	config, err := model.LoadAssetChannelConfig(t.Context(), 1)
	require.NoError(t, err)
	config.ProjectName = "other-project"
	assert.ErrorIs(t, model.SaveAssetChannelConfig(t.Context(), 1, config), model.ErrAssetInUse)
}

func TestAssetValidationExpiryAndIsolation(t *testing.T) {
	actor, input := assetValidationFixture(t)
	calls := assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
	created, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	other := actor
	other.UserID = 2
	_, err = GetAssetValidationResult(t.Context(), other, created.BytedToken, "default")
	require.Error(t, err)
	_, err = GetAssetValidationResult(t.Context(), actor, created.BytedToken, "other-project")
	require.Error(t, err)
	_, err = GetAssetValidationResult(t.Context(), actor, "upstream-portrait-token", "default")
	require.Error(t, err)
	row, err := model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(row).Update("expires_at", time.Now().Add(-time.Minute).Unix()).Error)
	require.NoError(t, model.ExpireAssetValidationSecrets(t.Context(), time.Now().Unix()))
	expired, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Equal(t, "expired", expired.Status)
	assert.False(t, expired.CanPoll)
	assert.Empty(t, expired.H5Link)
	assert.Zero(t, (*calls)["GetVisualValidateResult"])
	require.NoError(t, model.DB.First(row, row.ID).Error)
	assert.Empty(t, row.SecretCipher)
}

func TestAssetValidationRateAndLeaseFences(t *testing.T) {
	actor, input := assetValidationFixture(t)
	actor.CanReadChannels = true
	calls := assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
	// A window persisted by the AK/SK-only release must still limit new requests.
	legacyWindow := model.AssetValidationRateWindow{ScopeHash: "05f02e64ca5ead190c8f7ed676a136552fbd47d1766b339f9d298301a4efdbdd", NextCallAt: time.Now().Add(time.Minute).UnixMilli()}
	require.NoError(t, model.DB.Create(&legacyWindow).Error)
	_, err := CreateAssetValidationSession(t.Context(), actor, input)
	var failure *AssetError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, http.StatusTooManyRequests, failure.HTTPStatus)
	assert.Empty(t, *calls)
	require.NoError(t, model.DB.Model(&legacyWindow).Update("next_call_at", 0).Error)
	created, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.AssetValidationRateWindow{}).Where("1 = 1").Update("next_call_at", time.Now().Add(time.Minute).UnixMilli()).Error)
	input.IdempotencyKey, input.ChannelID = "fixture-request-2", 2
	_, err = CreateAssetValidationSession(t.Context(), actor, input)
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, http.StatusTooManyRequests, failure.HTTPStatus, "channels sharing an AK share the throttle")
	row, err := model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
	require.NoError(t, err)
	allowAssetValidationPoll(t, created.BytedToken)
	claimed, err := model.ClaimAssetValidationResult(t.Context(), row)
	require.NoError(t, err)
	require.True(t, claimed)
	stale := *row
	busy, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Equal(t, "verifying", busy.Status)
	assert.True(t, busy.CanPoll, "an active lease must not stop clients from checking for completion")
	assert.Zero(t, (*calls)["GetVisualValidateResult"])
	require.NoError(t, model.DB.Model(row).Update("expires_at", time.Now().Add(-time.Minute).Unix()).Error)
	busy, err = GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	assert.True(t, busy.CanPoll, "link expiry does not cancel another request's active lease")
	assert.True(t, busy.CanContinue)
	assert.False(t, busy.NeedsReview)
	assert.Zero(t, (*calls)["GetVisualValidateResult"])
	require.NoError(t, model.DB.Model(row).Update("expires_at", time.Now().Add(time.Minute).Unix()).Error)
	require.NoError(t, model.DB.Model(row).Update("lease_expires_at", time.Now().Add(-time.Minute).Unix()).Error)
	allowAssetValidationPoll(t, created.BytedToken)
	claimed, err = model.ClaimAssetValidationResult(t.Context(), row)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.ErrorIs(t, model.SaveAssetValidationGroupID(t.Context(), &stale, "stale-group"), model.ErrAssetConflict)
	assert.ErrorIs(t, model.SaveAssetValidationAttempt(t.Context(), &stale, 0), model.ErrAssetConflict)
}

func TestAssetValidationBearerRateScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		endpoint  string
		key       string
		throttled bool
	}{
		{"same endpoint and key", "https://upstream.example/materials", "channel-key", true},
		{"equivalent endpoint", "https://UPSTREAM.EXAMPLE:443/materials/", "channel-key", true},
		{"different key", "https://upstream.example/materials", "other-channel-key", false},
		{"different host", "https://other-upstream.example/materials", "channel-key", false},
		{"different path", "https://upstream.example/other-materials", "channel-key", false},
		{"different port", "https://upstream.example:8443/materials", "channel-key", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			actor.CanReadChannels = true
			assetValidationBearerChannel(t, 1, "https://upstream.example/materials", "channel-key")
			assetValidationBearerChannel(t, 2, tc.endpoint, tc.key)
			assetValidationUpstream(t, "channel-key", func(string, map[string]any) (int, any) { return 0, nil })
			first, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			require.Equal(t, "pending", first.Status)
			require.NoError(t, model.DB.Model(&model.AssetValidationRateWindow{}).Where("1 = 1").Update("next_call_at", time.Now().Add(time.Minute).UnixMilli()).Error)
			calls := assetValidationUpstream(t, tc.key, func(string, map[string]any) (int, any) { return 0, nil })
			input.IdempotencyKey, input.ChannelID = "fixture-request-2", 2
			second, err := CreateAssetValidationSession(t.Context(), actor, input)
			if tc.throttled {
				var failure *AssetError
				require.ErrorAs(t, err, &failure)
				assert.Equal(t, http.StatusTooManyRequests, failure.HTTPStatus)
				assert.Empty(t, *calls)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "pending", second.Status)
			assert.Equal(t, 1, (*calls)["CreateVisualValidateSession"])
		})
	}
}

func TestAssetValidationRejectsInvalidChannelAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name      string
		authType  string
		accessKey string
		secretKey string
	}{
		{"unsupported mode", "unsupported", "validation-ak", "validation-signing-secret"},
		{"empty mode", "", "validation-ak", "validation-signing-secret"},
		{"missing AK", "aksk", "", "validation-signing-secret"},
		{"missing SK", "aksk", "validation-ak", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			actor.CanReadChannels, input.ChannelID = true, 1
			config, err := model.LoadAssetChannelConfig(t.Context(), 1)
			require.NoError(t, err)
			config.AuthType, config.AccessKey, config.SecretKey = tc.authType, tc.accessKey, tc.secretKey
			require.NoError(t, model.SaveAssetChannelConfig(t.Context(), 1, config))
			calls := assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
			_, err = CreateAssetValidationSession(t.Context(), actor, input)
			var failure *AssetError
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, "ValidationChannelUnavailable", failure.Code)
			assert.Equal(t, "The selected channel is unavailable for human verification.", failure.Message)
			assert.Empty(t, *calls)
		})
	}
}

func TestAssetValidationBearerRejectsCredentialChanges(t *testing.T) {
	for _, change := range []string{"channel key", "authentication mode", "unsupported mode"} {
		t.Run(change, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			assetValidationBearerChannel(t, 1, "https://upstream.example", "channel-key")
			calls := assetValidationUpstream(t, "channel-key", func(string, map[string]any) (int, any) { return 0, nil })
			created, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			row, err := model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
			require.NoError(t, err)
			if change == "channel key" {
				channel, err := model.GetChannelById(1, true)
				require.NoError(t, err)
				channel.Key = "rotated-channel-key"
				assert.ErrorIs(t, channel.Update(), model.ErrAssetInUse)
				// Simulate an out-of-band credential change to exercise revalidation.
				require.NoError(t, model.DB.Model(channel).Update("key", channel.Key).Error)
			} else {
				config, err := model.LoadAssetChannelConfig(t.Context(), 1)
				require.NoError(t, err)
				config.AuthType, config.AccessKey, config.SecretKey = "aksk", "validation-ak", "validation-signing-secret"
				if change == "unsupported mode" {
					config.AuthType = "unsupported"
				}
				assert.ErrorIs(t, model.SaveAssetChannelConfig(t.Context(), 1, config), model.ErrAssetInUse)
				encoded, err := common.Marshal(config)
				require.NoError(t, err)
				require.NoError(t, model.DB.Model(&model.Option{}).Where(&model.Option{Key: model.AssetChannelOptionKey(1)}).Update("value", string(encoded)).Error)
			}
			assert.ErrorIs(t, model.ValidateAssetValidationSession(t.Context(), row), model.ErrAssetConflict)
			_, err = GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
			assert.Error(t, err)
			assert.Zero(t, (*calls)["GetVisualValidateResult"])
			assert.Zero(t, (*calls)["GetAssetGroup"])
		})
	}
}

func TestAssetValidationServerAddress(t *testing.T) {
	for _, tc := range []struct {
		name    string
		address string
		origin  string
	}{
		{"HTTP IP and port", "http://192.0.2.10:13000", "http://192.0.2.10:13000"},
		{"HTTP trailing slash", "http://gateway.example:13000/", "http://gateway.example:13000"},
		{"HTTPS", "https://gateway.example", "https://gateway.example"},
		{"HTTPS port", "https://gateway.example:8443/", "https://gateway.example:8443"},
		{"empty", "", ""},
		{"missing scheme", "192.0.2.10:13000", ""},
		{"unsupported scheme", "ftp://gateway.example", ""},
		{"missing host", "http:///callback", ""},
		{"credentials", "http://user:password@gateway.example", ""},
		{"path", "http://gateway.example/callback", ""},
		{"query", "http://gateway.example?redirect=other", ""},
		{"fragment", "http://gateway.example#callback", ""},
		{"invalid URL", "http://gateway.example:invalid", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			callback := ""
			calls := assetValidationUpstream(t, "", func(action string, payload map[string]any) (int, any) {
				if action == "CreateVisualValidateSession" {
					callback = payload["CallbackURL"].(string)
				}
				return 0, nil
			})
			system_setting.ServerAddress = tc.address
			created, err := CreateAssetValidationSession(t.Context(), actor, input)
			if tc.origin == "" {
				var failure *AssetError
				require.ErrorAs(t, err, &failure)
				assert.Equal(t, "ValidationConfigurationRequired", failure.Code)
				assert.Empty(t, *calls)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "pending", created.Status)
			assert.Equal(t, 1, (*calls)["CreateVisualValidateSession"])
			assert.Equal(t, callback, created.CallbackURL)
			assert.True(t, strings.HasPrefix(callback, tc.origin+"/api/assets/validation/callback/"))
			callbackURL, err := url.Parse(callback)
			require.NoError(t, err)
			state := strings.TrimPrefix(callbackURL.Path, assetValidationCallbackPath)
			assert.NoError(t, ValidateAssetValidationCallback(t.Context(), state, "upstream-portrait-token"))
			assert.Error(t, ValidateAssetValidationCallback(t.Context(), state, "forged-token"))
		})
	}
}

func TestAssetValidationUsesApplicationSecret(t *testing.T) {
	for _, tc := range []struct {
		name           string
		cryptoEnv      string
		sessionEnv     string
		applicationKey string
	}{
		{"short crypto secret", "short-crypto", "session-secret", "short-crypto"},
		{"short session fallback", "", "short-session", "short-session"},
		{"process secret without environment", "", "", "isolated-process-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			t.Setenv("CRYPTO_SECRET", tc.cryptoEnv)
			t.Setenv("SESSION_SECRET", tc.sessionEnv)
			// Use the effective key supplied by the application's initialization.
			common.CryptoSecret = tc.applicationKey
			assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
			created, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			require.Equal(t, "pending", created.Status)
			row, err := model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
			require.NoError(t, err)
			require.NotEmpty(t, row.SecretCipher)
			assert.NotContains(t, row.SecretCipher, "upstream-portrait-token")
			secrets, err := openAssetValidationSecrets(row)
			require.NoError(t, err)
			assert.Equal(t, "upstream-portrait-token", secrets.BytedToken)
			assert.Equal(t, created.H5Link, secrets.H5Link)

			common.CryptoSecret = "different-application-key"
			_, err = openAssetValidationSecrets(row)
			var failure *AssetError
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, "ValidationSecretUnavailable", failure.Code)
			allowAssetValidationPoll(t, created.BytedToken)
			unavailable, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
			require.NoError(t, err)
			assert.False(t, unavailable.CanPoll)
			assert.Empty(t, unavailable.H5Link)
			require.NotNil(t, unavailable.Error)
			assert.Equal(t, "ValidationSecretUnavailable", unavailable.Error.Code)
			common.CryptoSecret = tc.applicationKey

			otherSession := *row
			otherSession.PublicID = "validation-na-other-session"
			_, err = openAssetValidationSecrets(&otherSession)
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, "ValidationSecretUnavailable", failure.Code)
			allowAssetValidationPoll(t, created.BytedToken)
			ready, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
			require.NoError(t, err)
			assert.Equal(t, "created", ready.Status)
			assert.NotEmpty(t, ready.GroupID)
		})
	}
}

func TestAssetValidationRetainsCreateResultAfterClientDisconnect(t *testing.T) {
	actor, input := assetValidationFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	assetValidationUpstream(t, "", func(action string, _ map[string]any) (int, any) {
		if action == "CreateVisualValidateSession" {
			cancel()
		}
		return 0, nil
	})
	_, err := CreateAssetValidationSession(ctx, actor, input)
	require.Error(t, err)
	row, err := model.GetAssetValidationByRequest(t.Context(), 1, assetValidationHash(input.IdempotencyKey))
	require.NoError(t, err)
	assert.Equal(t, "pending", row.Status)
	assert.NotEmpty(t, row.SecretCipher)
	assert.Empty(t, row.LeaseToken)
}

func TestAssetValidationRejectsMismatchedUpstreamGroup(t *testing.T) {
	for _, field := range []string{"Id", "GroupType", "ProjectName"} {
		t.Run(field, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			assetValidationUpstream(t, "", func(action string, _ map[string]any) (int, any) {
				if action != "GetAssetGroup" {
					return 0, nil
				}
				group := map[string]any{"Id": "upstream-human-group", "GroupType": "LivenessFace", "ProjectName": "actual-project"}
				group[field] = "unexpected"
				return http.StatusOK, map[string]any{"Result": group}
			})
			created, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			allowAssetValidationPoll(t, created.BytedToken)
			result, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
			require.NoError(t, err)
			assert.Equal(t, "unknown", result.Status)
			require.NotNil(t, result.Error)
			assert.Equal(t, "ValidationGroupMismatch", result.Error.Code)
			assert.False(t, result.CanPoll)
			var count int64
			require.NoError(t, model.DB.Model(&model.AssetGroup{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestAssetValidationRedactsPendingErrors(t *testing.T) {
	actor, input := assetValidationFixture(t)
	actor.CanReadChannels = true
	state := ""
	assetValidationUpstream(t, "", func(action string, payload map[string]any) (int, any) {
		if action == "CreateVisualValidateSession" {
			callback, err := url.Parse(payload["CallbackURL"].(string))
			require.NoError(t, err)
			state = strings.TrimPrefix(callback.Path, assetValidationCallbackPath)
		}
		if action == "GetVisualValidateResult" {
			return http.StatusBadRequest, map[string]any{"ResponseMetadata": map[string]any{"Error": dto.AssetFailure{Code: "NotCompleted", Message: "Still pending: upstream-portrait-token " + state}}}
		}
		return 0, nil
	})
	created, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	system_setting.ServerAddress = "https://new-gateway.example"
	allowAssetValidationPoll(t, created.BytedToken)
	result, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Equal(t, "pending", result.Status)
	assert.True(t, result.CanPoll, "an undocumented business error is not a final verification decision")
	require.NotNil(t, result.Error)
	assert.NotContains(t, result.Error.Message, "upstream-portrait-token")
	assert.NotContains(t, result.Error.Message, state)
	list, err := ListAssetValidationSessions(t.Context(), actor, "default")
	require.NoError(t, err)
	encoded, err := common.Marshal(list)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "upstream-portrait-token")
	assert.NotContains(t, string(encoded), state)
}

func TestAssetValidationWaitingClearsPreviousErrors(t *testing.T) {
	actor, input := assetValidationFixture(t)
	calls := assetValidationUpstream(t, "", func(action string, _ map[string]any) (int, any) {
		if action == "GetVisualValidateResult" {
			return http.StatusOK, map[string]any{"Result": map[string]any{"GroupId": ""}}
		}
		return 0, nil
	})
	created, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	row, err := model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(row).Updates(map[string]any{"error_code": "ValidationPending", "error_message": "Human verification has not completed"}).Error)
	list, err := ListAssetValidationSessions(t.Context(), actor, "default")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Nil(t, list[0].Error, "old waiting records are not presented as failures")
	cached, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Nil(t, cached.Error)
	require.NoError(t, model.DB.Model(row).Updates(map[string]any{"error_code": "InvalidParameter", "error_message": "A previous query failed"}).Error)
	allowAssetValidationPoll(t, created.BytedToken)
	view, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Equal(t, "pending", view.Status)
	assert.True(t, view.CanPoll)
	assert.Nil(t, view.Error)
	assert.Empty(t, view.GroupID)
	assert.Zero(t, (*calls)["GetAssetGroup"])
	require.NoError(t, model.DB.First(row, row.ID).Error)
	assert.Empty(t, row.ErrorCode)
	assert.Empty(t, row.ErrorMessage)
	assert.Equal(t, row.UpdatedAt+5, view.NextPollAt)
	assert.Equal(t, row.NextPollAt, view.NextPollAt)
	list, err = ListAssetValidationSessions(t.Context(), actor, "default")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, view.NextPollAt, list[0].NextPollAt)
	assert.Equal(t, view.CanPoll, list[0].CanPoll)
}

func TestAssetValidationRejectsInvalidGroupIDWithoutBinding(t *testing.T) {
	for _, tc := range []struct{ name, id string }{
		{"surrounding whitespace", " upstream-human-group"},
		{"overlong ID", strings.Repeat("x", 192)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			invalid := true
			calls := assetValidationUpstream(t, "", func(action string, _ map[string]any) (int, any) {
				if action == "GetVisualValidateResult" && invalid {
					return http.StatusOK, map[string]any{"Result": map[string]any{"GroupId": tc.id}}
				}
				return 0, nil
			})
			created, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			allowAssetValidationPoll(t, created.BytedToken)
			view, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
			require.NoError(t, err)
			require.NotNil(t, view.Error)
			assert.Equal(t, "InvalidValidationGroup", view.Error.Code)
			assert.False(t, view.CanPoll)
			assert.Empty(t, view.GroupID)
			assert.Zero(t, (*calls)["GetAssetGroup"])
			invalid = false
			allowAssetValidationPoll(t, created.BytedToken)
			ready, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
			require.NoError(t, err)
			assert.Equal(t, "created", ready.Status, "manual recovery can reuse the original session after the upstream is repaired")
			assert.Equal(t, 1, (*calls)["CreateVisualValidateSession"])
		})
	}
}

func TestAssetValidationPersistsPollingBackoff(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadRequest, http.StatusBadGateway, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			actor.CanReadChannels = true
			failing := true
			calls := assetValidationUpstream(t, "", func(action string, _ map[string]any) (int, any) {
				if action == "GetVisualValidateResult" && failing {
					if status == http.StatusOK {
						return status, "invalid result envelope"
					}
					return status, map[string]any{"ResponseMetadata": map[string]any{"Error": dto.AssetFailure{Code: "InvalidParameter", Message: "liveness verification failed"}}}
				}
				return 0, nil
			})
			created, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			row, err := model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
			require.NoError(t, err)
			for _, delay := range []int64{10, 20, 40, 60, 60} {
				previousDelay := row.NextPollAt - row.UpdatedAt
				past := time.Now().Add(-time.Second).Unix()
				require.NoError(t, model.DB.Model(row).Updates(map[string]any{"next_poll_at": past, "updated_at": past - previousDelay}).Error)
				require.NoError(t, model.DB.Model(&model.AssetValidationRateWindow{}).Where("1 = 1").Update("next_call_at", 0).Error)
				view, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
				require.NoError(t, err)
				assert.Equal(t, "pending", view.Status)
				assert.True(t, view.CanPoll)
				require.NotNil(t, view.Error)
				if status == http.StatusTooManyRequests {
					assert.Equal(t, "Throttling", view.Error.Code)
				} else if status == http.StatusOK {
					assert.Equal(t, "UpstreamInvalidResponse", view.Error.Code, "a temporarily invalid response remains recoverable")
				} else {
					assert.Equal(t, "InvalidParameter", view.Error.Code)
				}
				require.NoError(t, model.DB.First(row, row.ID).Error)
				assert.Equal(t, row.UpdatedAt+delay, view.NextPollAt)
				assert.Equal(t, row.NextPollAt, view.NextPollAt)
				before := (*calls)["GetVisualValidateResult"]
				cached, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
				require.NoError(t, err)
				assert.Equal(t, view, cached, "a reopened client sees the same durable backoff")
				assert.Equal(t, before, (*calls)["GetVisualValidateResult"], "early checks do not hit the upstream")
			}
			failing = false
			allowAssetValidationPoll(t, created.BytedToken)
			ready, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
			require.NoError(t, err)
			assert.Equal(t, "created", ready.Status)
			assert.False(t, ready.CanPoll)
			assert.Nil(t, ready.Error)
		})
	}
}

func TestAssetValidationHonorsUpstreamRetryAfter(t *testing.T) {
	deadline := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header string
		minimum      int64
	}{
		{"seconds", "120", 120},
		{"HTTP date", deadline.Format(http.TimeFormat), 0},
		{"malformed", "soon", 10},
		{"overflow", "9999999999999999999999999", 10},
		{"negative", "-5", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, input := assetValidationFixture(t)
			assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
			created, err := CreateAssetValidationSession(t.Context(), actor, input)
			require.NoError(t, err)
			httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
				assert.Equal(t, "GetVisualValidateResult", request.URL.Query().Get("Action"))
				return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{tc.header}}, Body: io.NopCloser(strings.NewReader(`{"error":"busy"}`)), Request: request}, nil
			})
			allowAssetValidationPoll(t, created.BytedToken)
			before := time.Now().Unix()
			view, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
			require.NoError(t, err)
			require.NotNil(t, view.Error)
			assert.True(t, view.CanPoll)
			row, err := model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
			require.NoError(t, err)
			assert.Equal(t, row.NextPollAt, view.NextPollAt)
			if tc.minimum == 0 {
				assert.Equal(t, deadline.Unix(), view.NextPollAt)
			} else {
				assert.GreaterOrEqual(t, view.NextPollAt, before+tc.minimum)
				assert.LessOrEqual(t, view.NextPollAt, time.Now().Unix()+tc.minimum)
			}
		})
	}
}

func TestAssetValidationHistoryDistinguishesAnotherCredential(t *testing.T) {
	actor, input := assetValidationFixture(t)
	calls := assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
	created, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	other := actor
	other.TokenID = 2
	items, err := ListAssetValidationSessions(t.Context(), other, "default")
	require.NoError(t, err)
	require.Len(t, items, 1)
	encoded, err := common.Marshal(items[0])
	require.NoError(t, err)
	var record struct {
		CanContinue   *bool
		NeedsReview   bool
		CanArchive    bool
		ContinueError *dto.AssetFailure
	}
	require.NoError(t, common.Unmarshal(encoded, &record))
	require.NotNil(t, record.CanContinue, "history must describe whether the current credential can continue")
	assert.False(t, *record.CanContinue)
	assert.False(t, record.NeedsReview, "another credential does not change a waiting result")
	assert.False(t, record.CanArchive, "a waiting record cannot be archived by changing credentials")
	require.NotNil(t, record.ContinueError)
	assert.Equal(t, "ValidationCredentialMismatch", record.ContinueError.Code)
	assert.Equal(t, "pending", items[0].Status)
	_, err = GetAssetValidationResult(t.Context(), other, created.BytedToken, "default")
	require.Error(t, err)
	var failure *AssetError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, "ValidationCredentialMismatch", failure.Code)
	assert.Equal(t, map[string]int{"CreateVisualValidateSession": 1}, *calls)
}

func TestAssetValidationChannelsRespectCurrentEligibility(t *testing.T) {
	actor, _ := assetValidationFixture(t)
	actor.CanReadChannels = true
	channels, err := ListAssetValidationChannels(t.Context(), actor)
	require.NoError(t, err)
	assert.Equal(t, []dto.AssetValidationChannel{{ID: 1, Name: "channel-1"}, {ID: 2, Name: "channel-2"}}, channels)
	encoded, err := common.Marshal(channels)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"Id":1,"Name":"channel-1"},{"Id":2,"Name":"channel-2"}]`, string(encoded))
	actor.ChannelIDs = []int{1}
	channels, err = ListAssetValidationChannels(t.Context(), actor)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	assert.Equal(t, 1, channels[0].ID)
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", 1).Update("enabled", false).Error)
	channels, err = ListAssetValidationChannels(t.Context(), actor)
	require.NoError(t, err)
	assert.Empty(t, channels)
	actor.AllUsers = true
	_, err = ListAssetValidationChannels(t.Context(), actor)
	require.Error(t, err)
}

func TestAssetValidationHistoryPermissionsAndCompletedRecord(t *testing.T) {
	actor, input := assetValidationFixture(t)
	actor.TokenID, actor.AuthSessionID, actor.UserAuthVersion, actor.SessionVersion = 0, "validation-original-browser", 1, 1
	require.NoError(t, model.DB.Create(&model.UserSession{SID: actor.AuthSessionID, UserID: 1, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, ExpiresAt: time.Now().Add(time.Hour).Unix()}).Error)
	calls := assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
	created, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	assert.True(t, created.CanContinue)
	assert.Zero(t, created.LastCheckedAt)
	for _, tc := range []struct {
		name   string
		change func(*dto.AssetActor)
		code   string
	}{
		{"another login", func(value *dto.AssetActor) { value.AuthSessionID = "another-browser" }, "ValidationSessionMismatch"},
		{"revoked version", func(value *dto.AssetActor) { value.UserAuthVersion++ }, "ValidationAuthorizationRevoked"},
		{"read only", func(value *dto.AssetActor) { value.ReadOnly = true }, "ValidationReadOnly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := actor
			tc.change(&other)
			view, err := GetAssetValidationRecord(t.Context(), other, created.BytedToken, "default")
			require.NoError(t, err)
			assert.False(t, view.CanContinue)
			assert.False(t, view.NeedsReview)
			assert.False(t, view.CanArchive)
			require.NotNil(t, view.ContinueError)
			assert.Equal(t, tc.code, view.ContinueError.Code)
			_, err = GetAssetValidationResult(t.Context(), other, created.BytedToken, "default")
			var failure *AssetError
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, tc.code, failure.Code)
		})
	}
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", 1).Update("enabled", false).Error)
	view, err := GetAssetValidationRecord(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	require.NotNil(t, view.ContinueError)
	assert.Equal(t, "ValidationChannelUnavailable", view.ContinueError.Code)
	assert.False(t, view.CanContinue)
	assert.Equal(t, map[string]int{"CreateVisualValidateSession": 1}, *calls)
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", 1).Update("enabled", true).Error)
	allowAssetValidationPoll(t, created.BytedToken)
	ready, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Positive(t, ready.LastCheckedAt)
	other := actor
	other.AuthSessionID = "another-browser"
	completed, err := GetAssetValidationRecord(t.Context(), other, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Equal(t, ready.GroupID, completed.GroupID)
	assert.Equal(t, model.AssetValidationCreated, completed.Status)
	assert.False(t, completed.CanContinue)
	assert.Nil(t, completed.ContinueError, "viewing a finished record needs no original credential")
	assert.True(t, completed.CanArchive)
	assert.Equal(t, ready.LastCheckedAt, completed.LastCheckedAt)
	other.ReadOnly = true
	completed, err = GetAssetValidationRecord(t.Context(), other, created.BytedToken, "default")
	require.NoError(t, err)
	assert.False(t, completed.CanArchive)
}

func TestAssetValidationRecordsFilterOutcomeAndRemainReadOnly(t *testing.T) {
	actor, input := assetValidationFixture(t)
	calls := assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
	created, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	base, err := model.GetAssetValidationSession(t.Context(), actor.UserID, actor.InitiatorID, created.BytedToken, "default")
	require.NoError(t, err)
	now := time.Now().Unix()
	cases := []struct {
		name                    string
		status                  string
		category                string
		secret, upstream, group string
		possible                bool
		expires, lease          int64
		errorCode               string
		archived                int64
	}{
		{"completed", "expired", "completed", "", "known-group", "local-group", true, now - 60, 0, "", 0},
		{"binding", "expired", "pending", "", "known-group", "", true, now - 60, 0, "", 0},
		{"expired", "pending", "needs_review", base.SecretCipher, "", "", true, now - 60, 0, "", 0},
		{"unknown", "unknown", "needs_review", "", "", "", true, now + 60, 0, "", now - 10},
		{"definite_failure", "failed", "ended", "", "", "", false, now + 60, 0, "InvalidParameter", 0},
		{"active_create", "creating", "pending", "", "", "", true, now - 60, now + 60, "", 0},
		{"lost_create", "creating", "needs_review", "", "", "", true, now + 60, 0, "", 0},
		{"invalid_group", "pending", "needs_review", base.SecretCipher, "", "", true, now + 60, 0, "InvalidValidationGroup", 0},
	}
	for _, tc := range cases {
		row := *base
		row.ID, row.PublicID, row.Name = 0, "validation-na-"+tc.name, tc.name
		row.IdempotencyHash, row.StateHash = assetValidationHash(tc.name), assetValidationHash("state-"+tc.name)
		row.Status, row.SecretCipher, row.UpstreamGroupID, row.GroupID = tc.status, tc.secret, tc.upstream, tc.group
		row.ResourcePossible, row.ExpiresAt, row.LeaseExpiresAt = tc.possible, tc.expires, tc.lease
		row.ErrorCode, row.ArchivedAt = tc.errorCode, tc.archived
		require.NoError(t, model.DB.Create(&row).Error)
		assert.Equal(t, tc.category, model.AssetValidationRecordCategory(&row, now))
	}
	var before []model.AssetValidationSession
	require.NoError(t, model.DB.Order("id").Find(&before).Error)
	writes := 0
	callback := func(*gorm.DB) { writes++ }
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register("validation-history-readonly", callback))
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register("validation-history-readonly", callback))
	require.NoError(t, model.DB.Callback().Delete().Before("gorm:delete").Register("validation-history-readonly", callback))
	t.Cleanup(func() {
		_ = model.DB.Callback().Create().Remove("validation-history-readonly")
		_ = model.DB.Callback().Update().Remove("validation-history-readonly")
		_ = model.DB.Callback().Delete().Remove("validation-history-readonly")
	})
	for _, category := range []string{"pending", "completed", "needs_review", "ended"} {
		result, err := ListAssetValidationRecords(t.Context(), actor, "default", dto.AssetValidationRecordFilter{Status: category, Archived: "all", PageSize: 50})
		require.NoError(t, err)
		assert.EqualValues(t, 7, result.PendingCount)
		expected := 0
		for _, row := range before {
			if model.AssetValidationRecordCategory(&row, now) == category {
				expected++
			}
		}
		assert.EqualValues(t, expected, result.TotalCount)
		require.Len(t, result.Items, expected)
		for _, item := range result.Items {
			switch category {
			case "completed":
				assert.Equal(t, model.AssetValidationCreated, item.Status)
				assert.NotEmpty(t, item.GroupID)
			case "pending":
				assert.False(t, item.NeedsReview)
				assert.True(t, item.CanContinue)
			case "needs_review":
				assert.True(t, item.NeedsReview)
			case "ended":
				assert.False(t, item.NeedsReview)
				assert.False(t, item.CanContinue)
			}
			detail, err := GetAssetValidationRecord(t.Context(), actor, item.SessionID, "default")
			require.NoError(t, err)
			assert.Equal(t, item, detail)
		}
	}
	first, err := ListAssetValidationRecords(t.Context(), actor, "default", dto.AssetValidationRecordFilter{PageSize: 2})
	require.NoError(t, err)
	assert.EqualValues(t, 8, first.TotalCount)
	assert.EqualValues(t, 7, first.PendingCount)
	require.Len(t, first.Items, 2)
	assert.Equal(t, "invalid_group", first.Items[0].Name)
	second, err := ListAssetValidationRecords(t.Context(), actor, "default", dto.AssetValidationRecordFilter{Page: 2, PageSize: 2})
	require.NoError(t, err)
	require.Len(t, second.Items, 2)
	assert.NotEqual(t, first.Items[0].SessionID, second.Items[0].SessionID)
	filtered, err := ListAssetValidationRecords(t.Context(), actor, "default", dto.AssetValidationRecordFilter{Name: "_", Archived: "all"})
	require.NoError(t, err)
	assert.EqualValues(t, 4, filtered.TotalCount, "an underscore is literal name text")
	archived, err := ListAssetValidationRecords(t.Context(), actor, "default", dto.AssetValidationRecordFilter{Archived: "archived"})
	require.NoError(t, err)
	require.Len(t, archived.Items, 1)
	assert.True(t, archived.Items[0].NeedsReview)
	assert.True(t, archived.Items[0].CanArchive, "write permission allows restoring an archived record")
	assert.EqualValues(t, 7, archived.PendingCount)
	encoded, err := common.Marshal(archived)
	require.NoError(t, err)
	for _, secret := range []string{"SecretCipher", "BytedToken", "H5Link", "CallbackURL", "AuthSessionID", "upstream-portrait-token", base.SecretCipher} {
		assert.NotContains(t, string(encoded), secret)
	}
	other := actor
	other.InitiatorID = 2
	result, err := ListAssetValidationRecords(t.Context(), other, "default", dto.AssetValidationRecordFilter{})
	require.NoError(t, err)
	assert.Empty(t, result.Items)
	assert.Zero(t, result.TotalCount)
	_, err = GetAssetValidationRecord(t.Context(), other, created.BytedToken, "default")
	require.Error(t, err)
	var after []model.AssetValidationSession
	require.NoError(t, model.DB.Order("id").Find(&after).Error)
	assert.Equal(t, before, after)
	assert.Zero(t, writes)
	assert.Equal(t, map[string]int{"CreateVisualValidateSession": 1}, *calls)
}

func TestAssetValidationArchivePreservesResourcesAndRetryState(t *testing.T) {
	actor, input := assetValidationFixture(t)
	assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
	created, err := CreateAssetValidationSession(t.Context(), actor, input)
	require.NoError(t, err)
	base, err := model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
	require.NoError(t, err)
	archived := true
	request := dto.AssetValidationArchive{ProjectName: "default", SessionIDs: []string{created.BytedToken}, Archived: &archived}
	assert.Error(t, ArchiveAssetValidationRecords(t.Context(), actor, request), "pending sessions cannot be hidden as finished")
	completed := *base
	completed.ID, completed.PublicID, completed.IdempotencyHash, completed.StateHash = 0, "validation-na-finished", assetValidationHash("finished"), assetValidationHash("finished-state")
	completed.Status, completed.GroupID, completed.SecretCipher = model.AssetValidationCreated, "bound-local-group", ""
	require.NoError(t, model.DB.Create(&completed).Error)
	request.SessionIDs = []string{completed.PublicID, base.PublicID}
	assert.Error(t, ArchiveAssetValidationRecords(t.Context(), actor, request), "mixed batches are atomic")
	require.NoError(t, model.DB.First(&completed, completed.ID).Error)
	assert.Zero(t, completed.ArchivedAt)
	require.NoError(t, model.DB.Model(base).Updates(map[string]any{"expires_at": time.Now().Unix() - 60, "secret_cipher": "", "status": model.AssetValidationExpired, "last_checked_at": 100, "updated_at": 101, "next_poll_at": 121}).Error)
	require.NoError(t, model.DB.First(base, base.ID).Error)
	assert.Error(t, ArchiveAssetValidationRecords(t.Context(), actor, request), "unknown resources need individual acknowledgement")
	request.SessionIDs = []string{base.PublicID, base.PublicID}
	other := actor
	other.TokenID = 2
	require.NoError(t, ArchiveAssetValidationRecords(t.Context(), other, request), "display management does not need the old credential")
	var saved model.AssetValidationSession
	require.NoError(t, model.DB.First(&saved, base.ID).Error)
	assert.Positive(t, saved.ArchivedAt)
	archiveTime := saved.ArchivedAt
	saved.ArchivedAt = 0
	assert.Equal(t, *base, saved, "archive changes only ArchivedAt")
	require.NoError(t, ArchiveAssetValidationRecords(t.Context(), other, request))
	require.NoError(t, model.DB.First(&saved, base.ID).Error)
	assert.Equal(t, archiveTime, saved.ArchivedAt)
	assert.ErrorIs(t, model.PendingValidationChannelGuard(model.DB, []int{base.ChannelID}), model.ErrAssetInUse)
	result, err := ListAssetValidationRecords(t.Context(), actor, "default", dto.AssetValidationRecordFilter{})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.PendingCount)
	assert.EqualValues(t, 1, result.TotalCount)
	other.ReadOnly = true
	request.SessionIDs = []string{completed.PublicID}
	assert.Error(t, ArchiveAssetValidationRecords(t.Context(), other, request))
	other.ReadOnly = false
	request.SessionIDs = []string{completed.PublicID, "validation-na-missing"}
	assert.Error(t, ArchiveAssetValidationRecords(t.Context(), other, request))
	require.NoError(t, model.DB.First(&completed, completed.ID).Error)
	assert.Zero(t, completed.ArchivedAt)
	require.NoError(t, model.DB.Model(&completed).Update("lease_expires_at", time.Now().Unix()+60).Error)
	request.SessionIDs = []string{completed.PublicID}
	assert.Error(t, ArchiveAssetValidationRecords(t.Context(), other, request), "active leases are checked even for completed rows")
	archived = false
	request.SessionIDs = []string{base.PublicID, completed.PublicID}
	require.NoError(t, ArchiveAssetValidationRecords(t.Context(), other, request))
	require.NoError(t, model.DB.First(&saved, base.ID).Error)
	assert.Zero(t, saved.ArchivedAt)
	assert.Equal(t, *base, saved)
	assert.ErrorIs(t, model.PendingValidationChannelGuard(model.DB, []int{base.ChannelID}), model.ErrAssetInUse)
}
