package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	appdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// normalizeResponsesWSTestMessage runs the read-loop envelope parse followed by
// request normalization, exactly as the session does for one response.create.
func normalizeResponsesWSTestMessage(message []byte) (responsesWSCreateRequest, string, error) {
	envelope, streamID, err := parseResponsesWSEnvelope(message)
	if err != nil {
		return responsesWSCreateRequest{StreamID: streamID}, envelope.EventID, err
	}
	create, err := normalizeResponsesWSCreateEvent(message, envelope, streamID)
	return create, envelope.EventID, err
}

func TestNormalizeResponsesWSMaxOutputTokens(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{value: "0", valid: true},
		{value: "1073741823", valid: true},
		{value: "1073741824"},
		{value: "18446744073686646784"},
		{value: "-1"},
	} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrapped=%t", tc.value, wrapped), func(t *testing.T) {
				fields := `"model":"gpt-5.1","input":"hi","max_output_tokens":` + tc.value
				payload := `{"type":"response.create",` + fields + `}`
				if wrapped {
					payload = `{"type":"response.create","response":{` + fields + `}}`
				}
				create, _, err := normalizeResponsesWSTestMessage([]byte(payload))
				if !tc.valid {
					require.Error(t, err)
					assert.Equal(t, http.StatusBadRequest, newResponsesWSInvalidRequestError(err).StatusCode)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, create.Request.MaxOutputTokens)
				assert.Equal(t, tc.value, fmt.Sprint(*create.Request.MaxOutputTokens))
			})
		}
	}
}

func TestSelectResponsesWSChannelHonorsPinsAndFilters(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	enabled := &model.Channel{Name: "enabled", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI}
	enabled.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	wsDisabled := &model.Channel{Name: "ws-disabled", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI}
	disabled := &model.Channel{Name: "disabled", Key: "sk-test", Status: common.ChannelStatusManuallyDisabled, Type: constant.ChannelTypeOpenAI}
	filtered := &model.Channel{Name: "filtered", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeAdvancedCustom}
	for _, channel := range []*model.Channel{enabled, disabled, filtered, wsDisabled} {
		require.NoError(t, database.Create(channel).Error)
	}
	for _, tc := range []struct {
		name      string
		channelID int
		status    int
	}{
		{name: "token pin overrides origin pin", channelID: enabled.Id},
		{name: "disabled pin rejects", channelID: disabled.Id, status: http.StatusForbidden},
		{name: "pin cannot bypass websocket switch", channelID: wsDisabled.Id, status: http.StatusBadRequest},
		{name: "pin cannot bypass path filter", channelID: filtered.Id, status: http.StatusBadRequest},
		{name: "missing pin rejects", channelID: 99999, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			constraints := service.GetChannelConstraints(c)
			constraints.AddPin(appdto.ChannelPin{ChannelId: disabled.Id, Source: appdto.PinSourceOriginTask, Rank: appdto.PinRankOriginTask, RetryMode: appdto.PinRetrySameChannel})
			constraints.AddPin(appdto.ChannelPin{ChannelId: tc.channelID, Source: appdto.PinSourceToken, Rank: appdto.PinRankToken, RetryMode: appdto.PinRetrySingleAttempt})
			constraints.AddFilter(appdto.ChannelFilter{Kind: appdto.FilterRequestPath, RequestPath: c.Request.URL.Path})
			channel, apiErr := selectResponsesWSChannel(c, "gpt-5.1", &service.RetryParam{Ctx: c, ModelName: "gpt-5.1", TokenGroup: "default"})
			if tc.status != 0 {
				require.NotNil(t, apiErr)
				assert.Equal(t, tc.status, apiErr.StatusCode)
				assert.Nil(t, channel)
				assert.False(t, service.ShouldRetryRelayError(c, apiErr, 2))
				return
			}
			require.Nil(t, apiErr)
			require.NotNil(t, channel)
			assert.Equal(t, enabled.Id, channel.Id)
			assert.Equal(t, enabled.Id, common.GetContextKeyInt(c, constant.ContextKeyChannelId))
			assert.False(t, service.ShouldRetryRelayError(c, types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeDoRequestFailed, 503), 2))
		})
	}
}

// The WebSocket relay must admit the same model names as the HTTP distributor
// when a token restricts models (reasoning suffixes and @modifiers included).
func TestCheckResponsesWSModelAccessMatchesHTTPTokenLimits(t *testing.T) {
	for _, tc := range []struct {
		model  string
		status int
	}{
		{model: "gpt-5.1"},
		{model: "gpt-5.1-high"},
		{model: "gpt-5.1@thinking:on"},
		{model: "gpt-4o", status: http.StatusForbidden},
	} {
		t.Run(tc.model, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			common.SetContextKey(c, constant.ContextKeyTokenModelLimitEnabled, true)
			common.SetContextKey(c, constant.ContextKeyTokenModelLimit, map[string]bool{"gpt-5.1": true})
			apiErr := checkResponsesWSModelAccess(c, tc.model)
			if tc.status == 0 {
				assert.Nil(t, apiErr)
				return
			}
			require.NotNil(t, apiErr)
			assert.Equal(t, tc.status, apiErr.StatusCode)
			assert.False(t, service.ShouldRetryRelayError(c, apiErr, 2))
		})
	}
}

func TestSelectResponsesWSChannelAcceptsNativeResponsesChannelTypes(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	nativeRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses"}}}
	noneRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses", Converter: "none"}}}
	convertedRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/chat/completions", Converter: "openai_responses_to_openai_chat_completions"}}}
	chatRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/v1/chat/completions"}}}
	for _, tc := range []struct {
		name        string
		channelType int
		advanced    *dto.AdvancedCustomConfig
		rejectedBy  appdto.ChannelFilterKind
	}{
		{name: "azure", channelType: constant.ChannelTypeAzure},
		{name: "new api", channelType: constant.ChannelTypeNewAPI},
		{name: "sub2api", channelType: constant.ChannelTypeSub2API},
		{name: "advanced custom native responses route", channelType: constant.ChannelTypeAdvancedCustom, advanced: nativeRoute},
		{name: "advanced custom explicit none converter", channelType: constant.ChannelTypeAdvancedCustom, advanced: noneRoute},
		{name: "advanced custom converter route", channelType: constant.ChannelTypeAdvancedCustom, advanced: convertedRoute, rejectedBy: appdto.FilterResponsesWebSocket},
		{name: "advanced custom without responses route", channelType: constant.ChannelTypeAdvancedCustom, advanced: chatRoute, rejectedBy: appdto.FilterRequestPath},
		{name: "anthropic", channelType: constant.ChannelTypeAnthropic, rejectedBy: appdto.FilterResponsesWebSocket},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &model.Channel{Name: tc.name, Key: "sk-test", Status: common.ChannelStatusEnabled, Type: tc.channelType}
			channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
			channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: tc.advanced})
			require.NoError(t, database.Create(channel).Error)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			constraints := service.GetChannelConstraints(c)
			constraints.AddPin(appdto.ChannelPin{ChannelId: channel.Id, Source: appdto.PinSourceToken, Rank: appdto.PinRankToken, RetryMode: appdto.PinRetrySingleAttempt})
			constraints.AddFilter(appdto.ChannelFilter{Kind: appdto.FilterRequestPath, RequestPath: c.Request.URL.Path})
			selected, apiErr := selectResponsesWSChannel(c, "ws-model", &service.RetryParam{Ctx: c, ModelName: "ws-model", TokenGroup: "default"})
			if tc.rejectedBy != "" {
				require.NotNil(t, apiErr)
				assert.Nil(t, selected)
				assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
				assert.Equal(t, types.ErrorCode(tc.rejectedBy), apiErr.GetErrorCode())
				return
			}
			require.Nil(t, apiErr)
			require.NotNil(t, selected)
			assert.Equal(t, channel.Id, selected.Id)
		})
	}
}

func TestResponsesWSAzureDialUsesAzureResponsesURLAndAPIKey(t *testing.T) {
	for _, tc := range []struct {
		name           string
		host           string
		responsesVer   string
		wantPath       string
		wantAPIVersion string
		channelAPIVer  string
	}{
		{name: "openai v1 default preview", wantPath: "/openai/v1/responses", wantAPIVersion: "preview"},
		{name: "channel responses version", responsesVer: "2025-04-01-preview", wantPath: "/openai/v1/responses", wantAPIVersion: "2025-04-01-preview"},
		{name: "cognitiveservices uses channel api version", host: "cognitiveservices.azure.com", channelAPIVer: "2025-03-01-preview", wantPath: "/openai/responses", wantAPIVersion: "2025-03-01-preview"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handshake := make(chan *http.Request, 1)
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handshake <- r
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				_ = conn.Close()
			}))
			defer server.Close()

			// The host check only inspects the base URL text, so a path segment
			// is enough to select the cognitiveservices URL form.
			baseURL := server.URL
			if tc.host != "" {
				baseURL += "/" + tc.host
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			info := &relaycommon.RelayInfo{
				RelayMode:      relayconstant.RelayModeResponses,
				RequestURLPath: "/v1/responses",
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:          constant.ChannelTypeAzure,
					ChannelBaseUrl:       baseURL,
					ApiType:              constant.APITypeOpenAI,
					ApiVersion:           tc.channelAPIVer,
					ApiKey:               "azure-key",
					UpstreamModelName:    "gpt-test",
					ChannelOtherSettings: dto.ChannelOtherSettings{AzureResponsesVersion: tc.responsesVer},
				},
			}
			adaptor := GetAdaptor(info.ApiType)
			adaptor.Init(info)
			target, err := relaychannel.DoWssRequest(adaptor, c, info, nil)
			require.NoError(t, err)
			_ = target.Close()

			r := <-handshake
			assert.Equal(t, strings.TrimPrefix(baseURL, server.URL)+tc.wantPath, r.URL.Path)
			assert.Equal(t, tc.wantAPIVersion, r.URL.Query().Get("api-version"))
			assert.Equal(t, "azure-key", r.Header.Get("api-key"))
			assert.Empty(t, r.Header.Get("Authorization"))
		})
	}
}

func TestRestoreConnectionContextRejectsChangedAdvancedCustomRoute(t *testing.T) {
	database := setupRelayChannelDB(t)
	baseURL := "http://upstream.example"
	channel := &model.Channel{Name: "advanced", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeAdvancedCustom, BaseURL: &baseURL}
	channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses"}}}})
	require.NoError(t, database.Create(channel).Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	service.GetChannelConstraints(c).AddPin(appdto.ChannelPin{ChannelId: channel.Id, Source: appdto.PinSourceToken, Rank: appdto.PinRankToken, RetryMode: appdto.PinRetrySingleAttempt})
	route, ok := channel.GetOtherSettings().AdvancedCustom.MatchPathForModel("/v1/responses", "ws-model")
	require.True(t, ok)
	session := &responsesWSSession{lockedChannelID: channel.Id, lockedModel: "ws-model", lockedKey: "sk-test", lockedRoute: route,
		lockedContext: map[constant.ContextKey]any{constant.ContextKeyChannelBaseUrl: channel.GetBaseURL(), constant.ContextKeyChannelHeaderOverride: channel.GetHeaderOverride()}}
	require.Nil(t, session.restoreConnectionContext(c, "ws-model"))

	// Request-level edits (model list, explicit none converter) keep the connection.
	channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses", Converter: "none", Models: []string{"ws-model", "other-model"}}}}})
	require.NoError(t, database.Model(channel).Update("settings", channel.OtherSettings).Error)
	require.Nil(t, session.restoreConnectionContext(c, "ws-model"))

	channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v2/responses"}}}})
	require.NoError(t, database.Model(channel).Update("settings", channel.OtherSettings).Error)
	apiErr := session.restoreConnectionContext(c, "ws-model")
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.ErrorContains(t, apiErr, "upstream route changed")
}

func TestResponsesWSChannelRoutingRequiresExplicitOptIn(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.Ability{}))
	legacy := &model.Channel{Name: "legacy-http", Key: "sk-test", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(10))}
	enabled := &model.Channel{Name: "websocket", Key: "sk-test", Type: constant.ChannelTypeCodex, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(0))}
	unsupported := &model.Channel{Name: "unsupported", Key: "sk-test", Type: constant.ChannelTypeAnthropic, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(5))}
	enabled.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	unsupported.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	for _, channel := range []*model.Channel{legacy, enabled, unsupported} {
		require.NoError(t, database.Create(channel).Error)
		require.NoError(t, database.Create(&model.Ability{ChannelId: channel.Id, Model: "ws-model", Group: "default", Enabled: true, Priority: channel.Priority}).Error)
	}
	previousCache := common.MemoryCacheEnabled
	t.Cleanup(func() {
		defer func() { common.MemoryCacheEnabled = previousCache }()
		ids := []int{legacy.Id, enabled.Id, unsupported.Id}
		require.NoError(t, database.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error)
		require.NoError(t, database.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
		model.InitChannelCache()
	})
	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	params := &service.RetryParam{Ctx: c, ModelName: "ws-model", TokenGroup: "default"}
	channel, apiErr := selectResponsesWSChannel(c, "ws-model", params)
	require.Nil(t, apiErr)
	require.NotNil(t, channel)
	assert.Equal(t, enabled.Id, channel.Id)
	httpChannel, err := model.GetRandomSatisfiedChannel("default", "ws-model", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, httpChannel)
	assert.Equal(t, legacy.Id, httpChannel.Id)

	// Disabling the saved setting takes effect for the next create on an existing session.
	enabled.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: false})
	require.NoError(t, database.Model(enabled).Update("setting", enabled.Setting).Error)
	model.InitChannelCache()
	session := &responsesWSSession{lockedChannelID: enabled.Id, lockedModel: "ws-model"}
	apiErr = session.restoreConnectionContext(c, "ws-model")
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	channel, apiErr = selectResponsesWSChannel(c, "ws-model", params)
	require.NotNil(t, apiErr)
	assert.Nil(t, channel)
}

// Session affinity follows the HTTP distributor: a strict binding to an
// unusable channel fails the request, a prefer binding falls back, and an
// unusable binding is dropped from the cache either way.
func TestSelectResponsesWSChannelHonorsStrictSessionBinding(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.Ability{}))
	bound := &model.Channel{Name: "bound", Key: "sk-test", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusAutoDisabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(10))}
	fallback := &model.Channel{Name: "fallback", Key: "sk-test", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(0))}
	for _, channel := range []*model.Channel{bound, fallback} {
		channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
		require.NoError(t, database.Create(channel).Error)
		require.NoError(t, database.Create(&model.Ability{ChannelId: channel.Id, Model: "ws-model", Group: "default", Enabled: channel.Status == common.ChannelStatusEnabled, Priority: channel.Priority}).Error)
	}
	previousCache := common.MemoryCacheEnabled
	t.Cleanup(func() {
		defer func() { common.MemoryCacheEnabled = previousCache }()
		ids := []int{bound.Id, fallback.Id}
		require.NoError(t, database.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error)
		require.NoError(t, database.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
		model.InitChannelCache()
	})
	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	affinity := operation_setting.GetChannelAffinitySetting()
	previousAffinity := *affinity
	t.Cleanup(func() { *affinity = previousAffinity })
	snapshot, err := model.BuildRequestPolicy(map[string]string{
		"channel_affinity_setting.enabled":      "true",
		"channel_affinity_setting.session_mode": "strict",
		"channel_affinity_setting.rules":        `[{"name":"session","model_regex":[".*"],"key_sources":[{"type":"request_header","key":"X-Session"}],"session_mode":"inherit"}]`,
	})
	require.NoError(t, err)
	*affinity = snapshot.Affinity
	newSessionContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		c.Request.Header.Set("X-Session", t.Name())
		return c
	}
	seed := newSessionContext()
	_, found := service.GetPreferredChannelByAffinity(seed, "ws-model", "default")
	require.False(t, found)
	seed.Set("channel_id", bound.Id)
	service.RecordChannelAffinity(seed, bound.Id)
	t.Cleanup(func() { service.ClearCurrentChannelAffinityCache(seed) })

	strict := newSessionContext()
	channel, apiErr := selectResponsesWSChannel(strict, "ws-model", &service.RetryParam{Ctx: strict, ModelName: "ws-model", TokenGroup: "default", Retry: common.GetPointer(0)})
	require.NotNil(t, apiErr)
	assert.Nil(t, channel)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	assert.False(t, service.ShouldRetryRelayError(strict, apiErr, 2))
	_, found = service.GetPreferredChannelByAffinity(seed, "ws-model", "default")
	assert.False(t, found, "an unusable binding is cleared unless keep_on_channel_disabled is set")

	affinity.SessionMode = "prefer"
	service.RecordChannelAffinity(seed, bound.Id)
	prefer := newSessionContext()
	channel, apiErr = selectResponsesWSChannel(prefer, "ws-model", &service.RetryParam{Ctx: prefer, ModelName: "ws-model", TokenGroup: "default", Retry: common.GetPointer(0)})
	require.Nil(t, apiErr)
	require.NotNil(t, channel)
	assert.Equal(t, fallback.Id, channel.Id)
}

func TestNormalizeResponsesWSCreateEventWrapper(t *testing.T) {
	message := []byte(`{
		"type": "response.create",
		"event_id": "evt_1",
		"generate": false,
		"response": {
			"model": "gpt-5.3-codex-spark",
			"input": "hi",
			"store": false,
			"stream": true,
			"stream_options": {"include_usage": true}
		}
	}`)

	create, eventID, err := normalizeResponsesWSTestMessage(message)
	if err != nil {
		t.Fatalf("normalizeResponsesWSTestMessage() error = %v", err)
	}
	req := create.Request
	if eventID != "evt_1" {
		t.Fatalf("eventID = %q, want evt_1", eventID)
	}
	if req.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("model = %q", req.Model)
	}
	if strings.TrimSpace(string(create.Generate)) != "false" {
		t.Fatalf("generate = %s, want false", create.Generate)
	}
	if req.Stream != nil {
		t.Fatalf("stream = %v, want nil", req.Stream)
	}
	if req.StreamOptions != nil {
		t.Fatalf("stream_options = %#v, want nil", req.StreamOptions)
	}
	if strings.TrimSpace(string(req.Store)) != "false" {
		t.Fatalf("store = %s, want false", req.Store)
	}
}

func TestNormalizeResponsesWSCreateEventFlat(t *testing.T) {
	message := []byte(`{
		"type": "response.create",
		"event_id": "evt_2",
		"model": "gpt-5.3-codex-spark",
		"input": "hi",
		"generate": false,
		"stream": true,
		"background": true,
		"stream_options": {"include_usage": true}
	}`)

	create, eventID, err := normalizeResponsesWSTestMessage(message)
	if err != nil {
		t.Fatalf("normalizeResponsesWSTestMessage() error = %v", err)
	}
	req := create.Request
	if eventID != "evt_2" {
		t.Fatalf("eventID = %q, want evt_2", eventID)
	}
	if req.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("model = %q", req.Model)
	}
	if strings.TrimSpace(string(create.Generate)) != "false" {
		t.Fatalf("generate = %s, want false", create.Generate)
	}
	if req.Stream != nil {
		t.Fatalf("stream = %v, want nil", req.Stream)
	}
	if req.StreamOptions != nil {
		t.Fatalf("stream_options = %#v, want nil", req.StreamOptions)
	}
}

// The HTTP body and the upstream event keep the client's member order and
// number spelling; only the WebSocket members change.
func TestResponsesWSCreateEventKeepsClientBytes(t *testing.T) {
	message := []byte(`{"type":"response.create","model":"m","text":{"format":{"type":"json_schema","name":"r","schema":{"properties":{"b":{},"a":{}}}}},"temperature":1.0,"stream":true,"generate":false}`)

	create, _, err := normalizeResponsesWSTestMessage(message)
	require.NoError(t, err)
	assert.Equal(t, `{"model":"m","text":{"format":{"type":"json_schema","name":"r","schema":{"properties":{"b":{},"a":{}}}}},"temperature":1.0}`, string(create.Body))

	event, err := buildResponsesWSCreateEvent(create.Body, create.Generate, "s1")
	require.NoError(t, err)
	assert.Equal(t, `{"model":"m","text":{"format":{"type":"json_schema","name":"r","schema":{"properties":{"b":{},"a":{}}}}},"temperature":1.0,"type":"response.create","stream_id":"s1","generate":false}`, string(event))

	_, _, err = normalizeResponsesWSTestMessage([]byte(`{"type":"response.create","response":["m"]}`))
	assert.Error(t, err)
}

func TestBuildResponsesWSCreateEventIsFlat(t *testing.T) {
	payload := []byte(`{
		"model": "gpt-5.3-codex-spark",
		"input": "hi",
		"store": false,
		"event_id": "evt_upstream",
		"stream": true,
		"background": true,
		"stream_options": {"include_usage": true}
	}`)

	got, err := buildResponsesWSCreateEvent(payload, common.RawMessage(`false`), "")
	if err != nil {
		t.Fatalf("buildResponsesWSCreateEvent() error = %v", err)
	}
	var data map[string]any
	if err := common.Unmarshal(got, &data); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if data["type"] != responsesWSEventTypeResponseCreate {
		t.Fatalf("type = %#v", data["type"])
	}
	if data["model"] != "gpt-5.3-codex-spark" || data["input"] != "hi" || data["store"] != false {
		t.Fatalf("unexpected flat event fields: %s", got)
	}
	if data["generate"] != false {
		t.Fatalf("generate = %#v, want false", data["generate"])
	}
	for _, key := range []string{"response", "event_id", "stream", "background", "stream_options"} {
		if _, ok := data[key]; ok {
			t.Fatalf("field %q should not be present in upstream event: %s", key, got)
		}
	}
}

func TestHTTPResponsesRequestOmitsWebSocketMetadata(t *testing.T) {
	var req dto.OpenAIResponsesRequest
	require.NoError(t, common.Unmarshal([]byte(`{"model":"gpt-5.3-codex-spark","input":"hi","generate":false,"stream_id":"planner"}`), &req))
	got, err := common.Marshal(req)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, common.Unmarshal(got, &data))
	assert.NotContains(t, data, "stream_id")
	assert.NotContains(t, data, "generate")
}

func TestBuildResponsesWSErrorPayloadIncludesStatus(t *testing.T) {
	payload, err := buildResponsesWSErrorPayload("evt_err", "", types.NewErrorWithStatusCode(
		errors.New("model is required"),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	))
	if err != nil {
		t.Fatalf("buildResponsesWSErrorPayload() error = %v", err)
	}
	var data struct {
		Type    string             `json:"type"`
		Status  int                `json:"status"`
		EventID string             `json:"event_id"`
		Error   *types.OpenAIError `json:"error"`
	}
	if err := common.Unmarshal(payload, &data); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if data.Type != "error" || data.Status != http.StatusBadRequest || data.EventID != "evt_err" {
		t.Fatalf("unexpected error event: %s", payload)
	}
	if data.Error == nil || data.Error.Code != string(types.ErrorCodeInvalidRequest) {
		t.Fatalf("unexpected error body: %#v", data.Error)
	}
}

func TestResponsesWSInvalidRequestErrorUsesBadRequestStatus(t *testing.T) {
	payload, err := buildResponsesWSErrorPayload("", "", newResponsesWSInvalidRequestError(errors.New("bad event")))
	if err != nil {
		t.Fatalf("buildResponsesWSErrorPayload() error = %v", err)
	}
	var data struct {
		Status int `json:"status"`
	}
	if err := common.Unmarshal(payload, &data); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if data.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", data.Status, http.StatusBadRequest)
	}
}

func newTestResponsesWSTarget(t *testing.T) (*websocket.Conn, func()) {
	t.Helper()
	target, _, cleanup := newTestWebSocketPair(t)
	return target, cleanup
}

func newTestWebSocketPair(t *testing.T) (*websocket.Conn, *websocket.Conn, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	serverConnCh := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		serverConnCh <- conn
	}))

	targetURL := "ws" + strings.TrimPrefix(server.URL, "http")
	target, _, err := websocket.DefaultDialer.Dial(targetURL, nil)
	if err != nil {
		server.Close()
		t.Fatalf("dial websocket: %v", err)
	}
	serverConn := <-serverConnCh
	cleanup := func() {
		_ = target.Close()
		_ = serverConn.Close()
		server.Close()
	}
	return target, serverConn, cleanup
}

func TestResponsesWSMessageSizeLimit(t *testing.T) {
	previous := constant.MaxRequestBodyMB
	constant.MaxRequestBodyMB = 1
	t.Cleanup(func() { constant.MaxRequestBodyMB = previous })
	client, server, cleanup := newTestWebSocketPair(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	var admitted atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		ResponsesWebSocketHelper(c, server, func(*http.Request, string, func(*gin.Context) *types.NewAPIError) *types.NewAPIError {
			admitted.Store(true)
			return nil
		})
	}()
	_ = client.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", (1<<20)+1)))
	require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := client.ReadMessage()
	assert.True(t, websocket.IsCloseError(err, websocket.CloseMessageTooBig), "oversized message must be rejected before admission: %v", err)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("websocket request did not exit after oversized message")
	}
	assert.False(t, admitted.Load())
}

func TestResponsesWSShutdownInterruptsBusyWriter(t *testing.T) {
	client, server, cleanupClient := newTestWebSocketPair(t)
	defer cleanupClient()
	target, peer, cleanupTarget := newTestWebSocketPair(t)
	defer cleanupTarget()
	ctx, cancel := context.WithCancel(context.Background())
	s := &responsesWSSession{ctx: ctx, cancel: cancel, client: server, target: target}
	// Blocked network writers own these locks. Shutdown must not wait for
	// them: its close frames are bounded control writes, and closing the
	// sockets is what interrupts those writers.
	s.clientWriteMu.Lock()
	defer s.clientWriteMu.Unlock()
	s.targetWriteMu.Lock()
	defer s.targetWriteMu.Unlock()
	done := make(chan struct{})
	go func() { s.shutdown(websocket.CloseInternalServerErr, "upstream connection closed"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited for a network writer")
	}
	// A dropped socket would read as a synthetic 1006; 1011 is the frame.
	for _, conn := range []*websocket.Conn{client, peer} {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
		_, _, err := conn.ReadMessage()
		assert.True(t, websocket.IsCloseError(err, websocket.CloseInternalServerErr), "want a 1011 close frame, got %v", err)
	}
	assert.Nil(t, s.getTarget())

	// An upstream dial that completes after shutdown is closed with a frame too.
	late, latePeer, cleanupLate := newTestWebSocketPair(t)
	defer cleanupLate()
	assert.False(t, s.setTarget(late))
	require.NoError(t, latePeer.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := latePeer.ReadMessage()
	assert.True(t, websocket.IsCloseError(err, websocket.CloseNormalClosure), "want a 1000 close frame, got %v", err)
}

func TestResponsesWSPassthroughPreservesRawPricingParameters(t *testing.T) {
	create, _, err := normalizeResponsesWSTestMessage([]byte(`{"type":"response.create","generate":false,"response":{"model":"gpt-5.1","input":"hi","vendor":{"tier":"premium"},"stream":true}}`))
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(create.Body)))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, create.Request.Model)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: true})
	info := relaycommon.GenRelayInfoResponses(c, &create.Request)
	payload, apiErr := buildResponsesWSCreatePayload(c, info, create.Request, create.Generate, create.StreamID)
	require.Nil(t, apiErr)
	assert.JSONEq(t, `{"type":"response.create","generate":false,"model":"gpt-5.1","input":"hi","vendor":{"tier":"premium"}}`, string(payload))
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	require.NoError(t, storage.Close())
}

func TestResponsesWSStreamIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, fields, want string
		invalid            bool
	}{
		{name: "default", fields: `"model":"gpt-4o"`},
		{name: "named", fields: `"model":"gpt-4o","stream_id":"planner.A-1_2"`, want: "planner.A-1_2"},
		{name: "wrapped", fields: `"response":{"model":"gpt-4o","stream_id":"wrapped"}`, want: "wrapped"},
		{name: "top-level-wins", fields: `"stream_id":"outer","response":{"model":"gpt-4o","stream_id":"inner"}`, want: "outer"},
		{name: "maximum", fields: `"model":"gpt-4o","stream_id":"` + strings.Repeat("a", 256) + `"`, want: strings.Repeat("a", 256)},
		{name: "too-long", fields: `"stream_id":"` + strings.Repeat("a", 257) + `"`, invalid: true},
		{name: "empty", fields: `"stream_id":""`, invalid: true},
		{name: "null", fields: `"stream_id":null`, invalid: true},
		{name: "number", fields: `"stream_id":123`, invalid: true},
		{name: "unicode", fields: `"stream_id":"计划"`, invalid: true},
		{name: "spaces", fields: `"stream_id":"a b"`, invalid: true},
		{name: "invalid-top-level", fields: `"stream_id":"","response":{"stream_id":"inner"}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create, eventID, err := normalizeResponsesWSTestMessage([]byte(`{"type":"response.create","event_id":"test",` + tc.fields + `}`))
			assert.Equal(t, "test", eventID)
			if tc.invalid {
				require.ErrorContains(t, err, "stream_id")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, create.StreamID)
			assert.NotContains(t, string(create.Body), "stream_id")
			for _, passthrough := range []bool{false, true} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(create.Body)))
				common.SetContextKey(c, constant.ContextKeyOriginalModel, create.Request.Model)
				common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
				common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: passthrough})
				info := relaycommon.GenRelayInfoResponses(c, &create.Request)
				payload, apiErr := buildResponsesWSCreatePayload(c, info, create.Request, create.Generate, create.StreamID)
				require.Nil(t, apiErr)
				var event map[string]any
				require.NoError(t, common.Unmarshal(payload, &event))
				if tc.want == "" {
					assert.NotContains(t, event, "stream_id")
				} else {
					assert.Equal(t, tc.want, event["stream_id"])
				}
				if storage, err := common.GetBodyStorage(c); err == nil {
					require.NoError(t, storage.Close())
				}
			}
		})
	}
}

func TestResponsesWSErrorAttribution(t *testing.T) {
	for _, tc := range []struct {
		name, payload, control            string
		terminal, ambiguous, controlError bool
	}{
		{name: "active-server-failure", payload: `{"status":500,"error":{"type":"server_error"}}`, terminal: true},
		{name: "other-stream", payload: `{"stream_id":"other","status":500}`},
		{name: "previous-response", payload: `{"response_id":"previous","status":500}`},
		{name: "cancel-event-id", payload: `{"event_id":"cancel","status":500}`, control: `{"event_id":"cancel"}`, controlError: true},
		{name: "cancel-target", payload: `{"response_id":"missing","status":400}`, control: `{"response_id":"missing"}`, controlError: true},
		{name: "cancel-code", payload: `{"error":{"type":"invalid_request_error","code":"response_not_found"}}`, control: `{"response_id":"missing"}`, controlError: true},
		{name: "ambiguous-after-cancel", payload: `{"status":500}`, control: `{"response_id":"active"}`, terminal: true, ambiguous: true},
		{name: "explicit-generation-failure", payload: `{"response_id":"active","status":500}`, control: `{"response_id":"active"}`, terminal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var event responsesWSErrorEvent
			require.NoError(t, common.Unmarshal([]byte(tc.payload), &event))
			terminal, ambiguous, controlError := responsesWSErrorEndsRequest(event, "planner", "active", []byte(tc.control))
			assert.Equal(t, tc.terminal, terminal)
			assert.Equal(t, tc.ambiguous, ambiguous)
			assert.Equal(t, tc.controlError, controlError)
		})
	}
}

func TestRealtimeWalletAuthoritativeUsageAndUnknownTail(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, tc := range []struct {
		name                 string
		usage                string
		tail                 bool
		want                 int
		unknown              bool
		overlap, native      bool
		limited, shortWallet bool
	}{
		{"complete", `,"usage":{"total_tokens":300,"input_tokens":300,"input_token_details":{"text_tokens":300}}`, false, 300, false, false, false, false, false},
		{"explicit-zero", `,"usage":{"total_tokens":0,"input_tokens":0}`, false, 0, false, false, false, false, false},
		{"missing-usage", "", false, 0, true, false, false, false, false},
		{"unfinished-tail", `,"usage":{"total_tokens":300,"input_tokens":300,"input_token_details":{"text_tokens":300}}`, true, 300, true, false, false, false, false},
		{"overlapping-responses-and-audio", `,"usage":{"total_tokens":300,"input_tokens":300,"input_token_details":{"text_tokens":300}}`, false, 900, false, true, false, false, false},
		{"native-overlap-cross-currency-off", `,"usage":{"total_tokens":300,"input_tokens":300,"input_token_details":{"text_tokens":300}}`, false, 900, false, true, true, false, false},
		{"key-overrun", `,"usage":{"total_tokens":300,"input_tokens":300,"input_token_details":{"text_tokens":300}}`, false, 300, false, false, false, true, false},
		{"key-overrun-wallet-debt", `,"usage":{"total_tokens":300,"input_tokens":300,"input_token_details":{"text_tokens":300}}`, false, 300, false, false, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupRelayChannelDB(t)
			oldRedis, oldBatch, oldLogs, oldLogDB := common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, model.LOG_DB
			common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, model.LOG_DB = false, false, false, nil
			t.Cleanup(func() {
				common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, model.LOG_DB = oldRedis, oldBatch, oldLogs, oldLogDB
			})
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.CNYWallet{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{}))
			usd := 0
			if tc.native {
				usd = 10000
			}
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "realtime", Status: common.UserStatusEnabled, Quota: usd}).Error)
			cny := 10000
			if tc.shortWallet {
				cny = 700
			}
			require.NoError(t, db.Create(&model.CNYWallet{UserID: 1, Quota: cny, QuotaPerUnit: 500000}).Error)
			require.NoError(t, db.Create(&model.Channel{Id: 1, Name: "test", Key: "test"}).Error)
			policy := model.DefaultWalletExchangePolicy()
			policy.StaticRate, policy.CrossCurrencyEnabled = "7", !tc.native
			require.NoError(t, db.Create(&policy).Error)
			client, gatewayClient, closeClient := newTestWebSocketPair(t)
			defer closeClient()
			gatewayTarget, upstream, closeTarget := newTestWebSocketPair(t)
			defer closeTarget()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
			info := &relaycommon.RelayInfo{RequestId: tc.name, UserId: 1, IsPlayground: true, ForcePreConsume: true, StartTime: time.Now(), OriginModelName: "test", ClientWs: gatewayClient, TargetWs: gatewayTarget,
				PriceData: hosttypes.PriceData{ModelRatio: 1, CompletionRatio: 1, QuotaToPreConsume: 100, GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}}, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1, UpstreamModelName: "test"}}
			if tc.limited {
				token := model.Token{Id: 1, UserId: 1, Key: strings.Repeat("k", 48), Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100}
				require.NoError(t, db.Create(&token).Error)
				info.IsPlayground, info.TokenId, info.TokenKey = false, 1, token.Key
			}
			session, apiErr := service.NewWalletBillingSession(c, info, 100)
			require.Nil(t, apiErr)
			info.Billing = session
			type outcome struct {
				err   *types.NewAPIError
				usage *dto.RealtimeUsage
			}
			result := make(chan outcome, 1)
			go func() { err, usage := openai.OpenaiRealtimeHandler(c, info); result <- outcome{err, usage} }()
			require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)))
			require.NoError(t, upstream.SetReadDeadline(time.Now().Add(5*time.Second)))
			_, _, err := upstream.ReadMessage()
			require.NoError(t, err)
			record, err := model.GetWalletFundingRecord(tc.name)
			require.NoError(t, err)
			assert.True(t, record.UpstreamPending)
			if tc.overlap {
				for _, body := range []string{`{"type":"response.create"}`, `{"type":"input_audio_buffer.append","audio":"AAAA"}`, `{"type":"input_audio_buffer.append","audio":"AAAA"}`} {
					require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(body)))
					_, _, err = upstream.ReadMessage()
					require.NoError(t, err)
				}
				record, err = model.GetWalletFundingRecord(tc.name)
				require.NoError(t, err)
				assert.Equal(t, 300, record.ReservedQuota, "two creates plus one audio batch, not one reservation per chunk")
				require.NoError(t, upstream.WriteMessage(websocket.TextMessage, []byte(`{"type":"input_audio_buffer.committed","item_id":"audio1"}`)))
				for i := range 3 {
					require.NoError(t, upstream.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":"r%d"}}`, i))))
				}
				for i := range 3 {
					require.NoError(t, upstream.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.done","response":{"id":"r%d"`+tc.usage+`}}`, i))))
				}
				require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
				for range 7 {
					_, _, err = client.ReadMessage()
					require.NoError(t, err)
				}
				require.NoError(t, client.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)))
			} else {
				require.NoError(t, upstream.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1"}}`)))
				require.NoError(t, upstream.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.done","response":{"id":"r1"`+tc.usage+`}}`)))
				require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
				_, _, err = client.ReadMessage()
				require.NoError(t, err)
				if !tc.unknown || tc.tail {
					_, _, err = client.ReadMessage()
					require.NoError(t, err)
					if tc.tail {
						require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)))
						_, _, err = upstream.ReadMessage()
						require.NoError(t, err)
						_ = upstream.Close()
					} else {
						require.NoError(t, client.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)))
					}
				}
			}
			var out outcome
			select {
			case out = <-result:
			case <-time.After(5 * time.Second):
				t.Fatal("realtime handler did not finish")
			}
			if tc.unknown {
				require.NotNil(t, out.err)
				require.Nil(t, out.usage)
			} else {
				require.NotNil(t, out.usage)
				service.PostWssConsumeQuota(c, info, "test", out.usage, nil)
			}
			if tc.limited {
				var token model.Token
				require.NoError(t, db.First(&token, 1).Error)
				assert.Equal(t, -200, token.RemainQuota)
				assert.Equal(t, 300, token.UsedQuota)
			}
			info.Billing.Refund(c)
			record, err = model.GetWalletFundingRecord(tc.name)
			require.NoError(t, err)
			if tc.unknown {
				assert.Equal(t, model.CNYBillingReserved, record.Status)
				assert.True(t, record.UpstreamPending)
				assert.GreaterOrEqual(t, record.ReservedQuota, max(100, tc.want))
			} else {
				assert.Equal(t, model.CNYBillingSettled, record.Status)
				assert.Equal(t, tc.want, record.Quota)
				if tc.shortWallet {
					assert.Equal(t, 700, record.AlternateCharged)
					assert.Equal(t, 200, record.DebtQuota)
				} else if tc.native {
					assert.Equal(t, tc.want, record.NativeCharged)
					assert.Zero(t, record.AlternateCharged)
				} else {
					assert.Equal(t, tc.want*7, record.AlternateCharged)
				}
			}
		})
	}
}

func TestResponsesWSWalletFundingAtEachPaidWrite(t *testing.T) {
	metrics := config.GlobalConfig.Get("perf_metrics_setting").(*perf_metrics_setting.PerfMetricsSetting)
	previousMetrics := metrics.Enabled
	metrics.Enabled = false
	t.Cleanup(func() { metrics.Enabled = previousMetrics })
	priorRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o":1}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(priorRatios)) })
	for _, tc := range []struct {
		name, event string
		status      string
		repeats     int
	}{
		{"complete", `{"type":"response.completed","response":{"id":"%s","status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}}`, model.CNYBillingSettled, 2},
		{"reject", `{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"bad input"}}`, model.CNYBillingRefunded, 1},
		{"completed-without-usage", `{"type":"response.completed","response":{"id":"%s","status":"completed"}}`, model.CNYBillingSettled, 2},
		{"failed-without-usage", `{"type":"response.failed","response":{"id":"%s","status":"failed","error":{"type":"server_error","message":"upstream failed"}}}`, model.CNYBillingReserved, 1},
		{"server-error", `{"type":"error","status":500,"error":{"type":"server_error","message":"unknown"}}`, model.CNYBillingReserved, 1},
		{"bare-error", `{"type":"error","error":{"type":"server_error","message":"unknown"}}`, model.CNYBillingReserved, 1},
		{"bare-frame", `{"type":"error","status":500,"message":"upstream rejected"}`, model.CNYBillingReserved, 1},
		{"disconnect", "", model.CNYBillingReserved, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, i18n.Init())
			db := setupRelayChannelDB(t)
			oldRedis, oldBatch, oldLogs, oldLogDB := common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, model.LOG_DB
			common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, model.LOG_DB = false, false, false, db
			t.Cleanup(func() {
				common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, model.LOG_DB = oldRedis, oldBatch, oldLogs, oldLogDB
			})
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.CNYWallet{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{}, &model.Log{}))
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "responses", Status: common.UserStatusEnabled}).Error)
			require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: strings.Repeat("t", 48), Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
			require.NoError(t, db.Create(&model.CNYWallet{UserID: 1, Quota: 1000000, QuotaPerUnit: 500000}).Error)
			policy := model.DefaultWalletExchangePolicy()
			policy.StaticRate, policy.CrossCurrencyEnabled = "7", true
			require.NoError(t, db.Create(&policy).Error)
			observed := make(chan error, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, request, nil)
				if err != nil {
					observed <- err
					return
				}
				defer conn.Close()
				for i := range tc.repeats {
					_, _, err = conn.ReadMessage()
					if err != nil {
						observed <- err
						return
					}
					record, fundingErr := model.GetWalletFundingRecord(fmt.Sprintf("responses-%s-ws-%d", tc.name, i))
					if fundingErr == nil && !record.UpstreamPending {
						fundingErr = fmt.Errorf("paid write was not durably pending")
					}
					observed <- fundingErr
					if tc.event == "" {
						return
					}
					body := tc.event
					if strings.Contains(body, "%s") {
						body = fmt.Sprintf(body, fmt.Sprintf("r%d", i))
					}
					if conn.WriteMessage(websocket.TextMessage, []byte(body)) != nil {
						return
					}
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer upstream.Close()
			channel := &model.Channel{Id: 1, Name: "test", Key: "test", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: "gpt-4o", Group: "default", BaseURL: &upstream.URL}
			channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
			require.NoError(t, db.Create(channel).Error)
			client, gateway, cleanup := newTestWebSocketPair(t)
			defer cleanup()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil).WithContext(ctx)
			c.Set(common.RequestIdKey, "responses-"+tc.name)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_ = ResponsesWebSocketHelper(c, gateway, func(request *http.Request, requestID string, run func(*gin.Context) *types.NewAPIError) *types.NewAPIError {
					call, _ := gin.CreateTestContext(httptest.NewRecorder())
					call.Request = request
					call.Set(common.RequestIdKey, requestID)
					common.SetContextKey(call, constant.ContextKeyUserId, 1)
					common.SetContextKey(call, constant.ContextKeyTokenId, 1)
					common.SetContextKey(call, constant.ContextKeyTokenUnlimited, true)
					common.SetContextKey(call, constant.ContextKeyUserGroup, "default")
					common.SetContextKey(call, constant.ContextKeyTokenGroup, "default")
					common.SetContextKey(call, constant.ContextKeyUsingGroup, "default")
					common.SetContextKey(call, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
					service.GetChannelConstraints(call).AddPin(appdto.ChannelPin{ChannelId: 1, Source: appdto.PinSourceToken, Rank: appdto.PinRankToken, RetryMode: appdto.PinRetrySingleAttempt})
					return run(call)
				})
			}()
			for i := range tc.repeats {
				require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-4o","input":"hi"}`)))
				require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
				_, body, err := client.ReadMessage()
				if tc.event == "" {
					require.True(t, websocket.IsCloseError(err, websocket.CloseInternalServerErr), "disconnect must retain its existing close semantics: %v %s", err, body)
				} else {
					require.NoError(t, err, string(body))
					if tc.name == "bare-frame" {
						var rejection responsesWSErrorEvent
						require.NoError(t, common.Unmarshal(body, &rejection))
						require.NotNil(t, rejection.Error)
						assert.Equal(t, "invalid_request_error", rejection.Error.Type)
						assert.Equal(t, "upstream rejected", rejection.Error.Message)
					} else if tc.status == model.CNYBillingReserved {
						want := tc.event
						if strings.Contains(want, "%s") {
							want = fmt.Sprintf(want, fmt.Sprintf("r%d", i))
						}
						assert.JSONEq(t, want, string(body), "unknown accounting must preserve the upstream terminal event")
					}
				}
				select {
				case err := <-observed:
					require.NoError(t, err)
				case <-time.After(time.Second):
					t.Fatalf("upstream did not receive create: %s", body)
				}
				record, err := model.GetWalletFundingRecord(fmt.Sprintf("responses-%s-ws-%d", tc.name, i))
				require.NoError(t, err)
				assert.Equal(t, tc.status, record.Status, string(body))
				if tc.status == model.CNYBillingReserved {
					assert.True(t, record.UpstreamPending)
				}
			}
			_ = client.Close()
			cancel()
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("responses session did not finish")
			}
		})
	}
}

func TestRealtimeWalletExplicitRejectionAndAudioClear(t *testing.T) {
	require.NoError(t, i18n.Init())
	type event struct {
		client bool
		body   string
	}
	create := event{true, `{"type":"response.create","event_id":"create1"}`}
	reject := event{false, `{"type":"error","error":{"type":"invalid_request_error","event_id":"create1","message":"invalid create"}}`}
	appendAudio := event{true, `{"type":"input_audio_buffer.append","audio":"AAAA"}`}
	clearAudio := event{true, `{"type":"input_audio_buffer.clear"}`}
	cleared := event{false, `{"type":"input_audio_buffer.cleared"}`}
	textInput := event{true, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`}
	created := event{false, `{"type":"response.created","response":{"id":"r1"}}`}
	completed := event{false, `{"type":"response.done","response":{"id":"r1","usage":{"total_tokens":300,"input_tokens":300,"input_token_details":{"text_tokens":300}}}}`}
	for _, tc := range []struct {
		name   string
		events []event
		held   bool
		used   int
	}{
		{"rejected-per-call", []event{create, reject}, false, 0},
		{"server-error-unknown", []event{create, {false, `{"type":"error","error":{"type":"server_error","event_id":"create1","message":"unknown"}}`}}, true, 0},
		{"unmatched-error", []event{create, {false, `{"type":"error","error":{"type":"invalid_request_error","event_id":"other","message":"other event"}}`}}, true, 0},
		{"uncommitted-audio-cleared", []event{appendAudio, clearAudio, cleared}, false, 0},
		{"committed-audio-not-cleared", []event{appendAudio, {true, `{"type":"input_audio_buffer.commit"}`}, clearAudio, cleared}, true, 0},
		{"committed-batch-budget", []event{appendAudio, {true, `{"type":"input_audio_buffer.commit"}`}, appendAudio}, true, 0},
		{"auto-commit-explicit-budget", []event{appendAudio, {false, `{"type":"input_audio_buffer.committed","item_id":"audio1"}`}, create}, true, 0},
		{"auto-response-disabled", []event{{false, `{"type":"session.created","session":{"turn_detection":{"type":"server_vad","create_response":false}}}`}, appendAudio, {false, `{"type":"input_audio_buffer.committed","item_id":"audio1"}`}, create, created, completed}, false, 300},
		{"auto-response-disabled-ga", []event{{false, `{"type":"session.created","session":{"audio":{"input":{"turn_detection":{"type":"server_vad","create_response":false}}}}}`}, appendAudio, {false, `{"type":"input_audio_buffer.committed","item_id":"audio1"}`}, create, created, completed}, false, 300},
		{"two-committed-batches", []event{appendAudio, {true, `{"type":"input_audio_buffer.commit"}`}, appendAudio, {true, `{"type":"input_audio_buffer.commit"}`}, created, completed, {false, `{"type":"response.created","response":{"id":"r2"}}`}, {false, `{"type":"response.done","response":{"id":"r2","usage":{"total_tokens":300,"input_tokens":300,"input_token_details":{"text_tokens":300}}}}`}}, false, 600},
		{"text-input-not-cleared", []event{textInput, appendAudio, clearAudio, cleared}, true, 0},
		{"new-audio-after-clear", []event{appendAudio, clearAudio, appendAudio, cleared}, true, 0},
		{"active-response-not-cleared", []event{create, created, appendAudio, clearAudio, cleared}, true, 0},
		{"rejected-create-keeps-input", []event{textInput, create, reject}, true, 0},
		{"completed-then-rejected", []event{create, created, completed, create, reject}, false, 300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupRelayChannelDB(t)
			oldRedis, oldBatch, oldLogs, oldLogDB := common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, model.LOG_DB
			common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, model.LOG_DB = false, false, false, nil
			t.Cleanup(func() {
				common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, model.LOG_DB = oldRedis, oldBatch, oldLogs, oldLogDB
			})
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.CNYWallet{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{}))
			quota := 10000
			if tc.name == "committed-batch-budget" || tc.name == "auto-commit-explicit-budget" || strings.HasPrefix(tc.name, "auto-response-disabled") {
				quota = 100
			}
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "realtime-cancel", Status: common.UserStatusEnabled, Quota: quota}).Error)
			require.NoError(t, db.Create(&model.Channel{Id: 1, Name: "test", Key: "test"}).Error)
			client, gatewayClient, closeClient := newTestWebSocketPair(t)
			defer closeClient()
			gatewayTarget, upstream, closeTarget := newTestWebSocketPair(t)
			defer closeTarget()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
			info := &relaycommon.RelayInfo{RequestId: tc.name, UserId: 1, IsPlayground: true, ForcePreConsume: true, StartTime: time.Now(), OriginModelName: "test", ClientWs: gatewayClient, TargetWs: gatewayTarget,
				PriceData: hosttypes.PriceData{ModelRatio: 1, CompletionRatio: 1, QuotaToPreConsume: 100, GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}}, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1, UpstreamModelName: "test"}}
			if tc.name == "rejected-per-call" {
				info.PriceData.UsePrice, info.PriceData.ModelPrice = true, 0.0002
			}
			session, apiErr := service.NewWalletBillingSession(c, info, 100)
			require.Nil(t, apiErr)
			info.Billing = session
			type outcome struct {
				err   *types.NewAPIError
				usage *dto.RealtimeUsage
			}
			result := make(chan outcome, 1)
			go func() { err, usage := openai.OpenaiRealtimeHandler(c, info); result <- outcome{err, usage} }()
			for i, frame := range tc.events {
				sender, receiver := upstream, client
				if frame.client {
					sender, receiver = client, upstream
				}
				require.NoError(t, sender.WriteMessage(websocket.TextMessage, []byte(frame.body)))
				if (tc.name == "committed-batch-budget" || tc.name == "auto-commit-explicit-budget") && i == 2 {
					require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
					_, body, err := client.ReadMessage()
					require.NoError(t, err)
					var rejected dto.RealtimeEvent
					require.NoError(t, common.Unmarshal(body, &rejected))
					require.NotNil(t, rejected.Error)
					assert.Equal(t, "insufficient_quota", rejected.Error.Type)
					// The next upstream frame must be this harmless control, proving
					// the unaffordable audio batch never crossed the paid-write boundary.
					frame.body = `{"type":"session.update","session":{}}`
					require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(frame.body)))
				}
				require.NoError(t, receiver.SetReadDeadline(time.Now().Add(5*time.Second)))
				_, got, err := receiver.ReadMessage()
				require.NoError(t, err)
				assert.JSONEq(t, frame.body, string(got))
			}
			require.NoError(t, client.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)))
			var out outcome
			select {
			case out = <-result:
			case <-time.After(5 * time.Second):
				t.Fatal("realtime handler did not finish")
			}
			if tc.held {
				require.NotNil(t, out.err)
				require.Nil(t, out.usage)
			} else if tc.used != 0 {
				require.NotNil(t, out.usage)
				service.PostWssConsumeQuota(c, info, "test", out.usage, nil)
			} else {
				require.Nil(t, out.usage, "a fully rejected or cleared session has no billable response, even for per-call pricing")
			}
			info.Billing.Refund(c)
			record, err := model.GetWalletFundingRecord(tc.name)
			require.NoError(t, err)
			if tc.held {
				assert.Equal(t, model.CNYBillingReserved, record.Status)
				assert.True(t, record.UpstreamPending)
			} else if tc.used != 0 {
				assert.Equal(t, model.CNYBillingSettled, record.Status)
				assert.Equal(t, tc.used, record.Quota)
			} else {
				assert.Equal(t, model.CNYBillingRefunded, record.Status)
				assert.False(t, record.UpstreamPending)
			}
		})
	}
}
