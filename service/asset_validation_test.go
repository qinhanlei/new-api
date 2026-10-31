package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, model.DB.Create(&model.User{Id: 1, Username: "validation-owner", Group: "default", Status: common.UserStatusEnabled, AuthVersion: 1}).Error)
	require.NoError(t, model.DB.Create(&model.Token{Id: 1, UserId: 1, Key: "validation-inference-key", Status: common.TokenStatusEnabled, ExpiredTime: -1}).Error)
	for _, id := range []int{1, 2} {
		require.NoError(t, model.DB.Create(&model.Ability{ChannelId: id, Model: "doubao-seedance-2-0-260128", Group: "default", Enabled: true}).Error)
		config, err := model.LoadAssetChannelConfig(t.Context(), id)
		require.NoError(t, err)
		config.AuthType, config.AccessKey, config.SecretKey = "aksk", "validation-ak", "validation-signing-secret"
		require.NoError(t, model.SaveAssetChannelConfig(t.Context(), id, config))
	}
	return dto.AssetActor{UserID: 1, InitiatorID: 1, TokenID: 1, ChannelIDs: []int{1, 2}},
		dto.AssetValidationCreate{Name: "Portrait owner", ProjectName: "default", ChannelID: 1, IdempotencyKey: "fixture-request-1"}
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
	row, err := model.GetAssetValidationSession(t.Context(), 1, 1, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Equal(t, "upstream-human-group", row.UpstreamGroupID)
	assert.Empty(t, row.SecretCipher)
	callback, err := url.Parse(created.CallbackURL)
	require.NoError(t, err)
	assert.NoError(t, ValidateAssetValidationCallback(t.Context(), strings.TrimPrefix(callback.Path, assetValidationCallbackPath), "upstream-portrait-token"), "a persisted result keeps the phone return page usable while binding is retried")
	require.NoError(t, model.DB.Model(row).Update("expires_at", time.Now().Add(-time.Minute).Unix()).Error)
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
	assert.Empty(t, expired.H5Link)
	assert.Zero(t, (*calls)["GetVisualValidateResult"])
	require.NoError(t, model.DB.First(row, row.ID).Error)
	assert.Empty(t, row.SecretCipher)
}

func TestAssetValidationRateAndLeaseFences(t *testing.T) {
	actor, input := assetValidationFixture(t)
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
	assert.Zero(t, (*calls)["GetVisualValidateResult"])
	require.NoError(t, model.DB.Model(row).Update("lease_expires_at", time.Now().Add(-time.Minute).Unix()).Error)
	allowAssetValidationPoll(t, created.BytedToken)
	claimed, err = model.ClaimAssetValidationResult(t.Context(), row)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.ErrorIs(t, model.SaveAssetValidationGroupID(t.Context(), &stale, "stale-group"), model.ErrAssetConflict)
	assert.ErrorIs(t, model.SaveAssetValidationAttempt(t.Context(), &stale), model.ErrAssetConflict)
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
			config, err := model.LoadAssetChannelConfig(t.Context(), 1)
			require.NoError(t, err)
			config.AuthType, config.AccessKey, config.SecretKey = tc.authType, tc.accessKey, tc.secretKey
			require.NoError(t, model.SaveAssetChannelConfig(t.Context(), 1, config))
			calls := assetValidationUpstream(t, "", func(string, map[string]any) (int, any) { return 0, nil })
			_, err = CreateAssetValidationSession(t.Context(), actor, input)
			var failure *AssetError
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, "NoAvailableChannel", failure.Code)
			assert.Equal(t, "No material channel is available for human verification", failure.Message)
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
			assert.Equal(t, "UpstreamInvalidResponse", result.Error.Code)
			var count int64
			require.NoError(t, model.DB.Model(&model.AssetGroup{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestAssetValidationRedactsPendingErrors(t *testing.T) {
	actor, input := assetValidationFixture(t)
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
	allowAssetValidationPoll(t, created.BytedToken)
	result, err := GetAssetValidationResult(t.Context(), actor, created.BytedToken, "default")
	require.NoError(t, err)
	assert.Equal(t, "pending", result.Status)
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
