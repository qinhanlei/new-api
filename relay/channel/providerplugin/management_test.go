package providerplugin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// pagedModelsPlugin exchanges its key for a numbered token, signs every
// request with it and reads the vendor's model list page by page.
const pagedModelsPlugin = `
export const meta = {
  apiVersion: 1, key: "mgmt-paged-models", name: "Paged models", version: "1.0.0", author: {name: "Test"},
  relay: [
    {incoming_path: "/v1/chat/completions", upstream_path: "/chat"},
    {incoming_path: "/v1/models", upstream_path: "/models", converter: "plugin"},
  ],
};
let exchanges = 0;
export function exchangeKey(ctx) {
  exchanges++;
  return {token: {value: ctx.apiKey + "-" + exchanges}, expiresAt: Math.floor(Date.now() / 1000) + 3600};
}
export function signRequest(ctx) { return {headers: {"X-Token": ctx.token.value}}; }
export function convertResponse(ctx, res) {
  const page = JSON.parse(res.body);
  const ids = (ctx.state || []).concat(page.items);
  if (page.next) return {next: {path: "/models?page=" + page.next}, state: ids};
  return {body: {object: "list", data: ids.map((id) => ({id: id, object: "model"}))}};
}
`

// A model list fetch follows the plugin's pages, and a 401 exchanges the key
// again once and sends the rejected page again.
func TestFetchModelListPagesAndExchangesAgainOn401(t *testing.T) {
	plugin, err := jsplugin.NewRegistry().Register(pagedModelsPlugin, jsplugin.Options{})
	require.NoError(t, err)
	var mutex sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("X-Token"))
		mutex.Unlock()
		if r.Header.Get("X-Token") == "sk-paged-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"items":["model-b"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":["model-a"],"next":"2"}`))
	}))
	t.Cleanup(upstream.Close)
	info := &relaycommon.RelayInfo{
		RelayFormat:    types.RelayFormatOpenAI,
		RelayMode:      relayconstant.RelayModeUnknown,
		RequestURLPath: dto.AdvancedCustomModelListPath,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          constant.ChannelTypeTaskPlugin,
			ChannelId:            910_001,
			ChannelBaseUrl:       upstream.URL,
			ApiKey:               "sk-paged",
			ChannelOtherSettings: dto.ChannelOtherSettings{AdvancedCustom: plugin.Meta.ChannelRelayConfig(nil)},
		},
	}

	body, err := FetchModelList(context.Background(), plugin, info, ManagementClient{
		Override: func(http.Header, string) error { return nil },
		Send:     http.DefaultClient.Do,
	})

	require.NoError(t, err)
	assert.JSONEq(t, `{"object":"list","data":[{"id":"model-a","object":"model"},{"id":"model-b","object":"model"}]}`, string(body))
	assert.Equal(t, []string{
		"GET /models sk-paged-1",
		"GET /models sk-paged-2",
		"GET /models?page=2 sk-paged-2",
	}, seen)
}

// A plugin that keeps chaining fails the fetch after MaxFollowUps requests.
func TestFetchModelListStopsAtTheFollowUpLimit(t *testing.T) {
	plugin, err := jsplugin.NewRegistry().Register(`
export const meta = {
  apiVersion: 1, key: "mgmt-endless-models", name: "Endless", version: "1.0.0", author: {name: "Test"},
  relay: [{incoming_path: "/v1/models", upstream_path: "/models", converter: "plugin"}],
};
export function convertResponse() { return {next: {path: "/models"}}; }
`, jsplugin.Options{})
	require.NoError(t, err)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	info := &relaycommon.RelayInfo{
		RelayFormat:    types.RelayFormatOpenAI,
		RequestURLPath: dto.AdvancedCustomModelListPath,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          constant.ChannelTypeTaskPlugin,
			ChannelId:            910_002,
			ChannelBaseUrl:       upstream.URL,
			ApiKey:               "sk-endless",
			ChannelOtherSettings: dto.ChannelOtherSettings{AdvancedCustom: plugin.Meta.ChannelRelayConfig(nil)},
		},
	}

	_, err = FetchModelList(context.Background(), plugin, info, ManagementClient{
		Override: func(http.Header, string) error { return nil },
		Send:     http.DefaultClient.Do,
	})

	require.ErrorContains(t, err, "chained more than 32 model list requests")
	assert.Equal(t, int32(MaxFollowUps+1), requests.Load())
}

// A balance request's url must be on the channel base URL host or a host of
// meta.allowedHosts, as the credential goes with it.
func TestQueryBalanceURLNeedsAnAllowedHost(t *testing.T) {
	var vendorRequests atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vendorRequests.Add(1)
		_, _ = w.Write([]byte(`{"usd":7}`))
	}))
	t.Cleanup(vendor.Close)
	base := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(base.Close)
	query := func(t *testing.T, key, allowedHosts string) (jsplugin.BalanceResult, error) {
		plugin, err := jsplugin.NewRegistry().Register(fmt.Sprintf(`
export const meta = {
  apiVersion: 1, key: %q, name: "Balance", version: "1.0.0", author: {name: "Test"}, allowedHosts: %s,
  relay: [{incoming_path: "/v1/chat/completions", upstream_path: "/chat"}],
};
export function buildBalanceRequest() { return {url: %q}; }
export function parseBalance(ctx, res) { return {amount: JSON.parse(res.body).usd, currency: "USD"}; }
`, key, allowedHosts, vendor.URL+"/balance"), jsplugin.Options{})
		require.NoError(t, err)
		info := &relaycommon.RelayInfo{
			RelayFormat: types.RelayFormatOpenAI,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:          constant.ChannelTypeTaskPlugin,
				ChannelId:            910_003,
				ChannelBaseUrl:       base.URL,
				ApiKey:               "sk-balance",
				ChannelOtherSettings: dto.ChannelOtherSettings{AdvancedCustom: plugin.Meta.ChannelRelayConfig(nil)},
			},
		}
		return QueryBalance(context.Background(), plugin, info, ManagementClient{
			Override: func(http.Header, string) error { return nil },
			Send:     http.DefaultClient.Do,
		})
	}

	t.Run("another host", func(t *testing.T) {
		_, err := query(t, "mgmt-balance-elsewhere", "[]")

		require.ErrorContains(t, err, "is not allowed")
		assert.Zero(t, vendorRequests.Load())
	})

	t.Run("an allowed host", func(t *testing.T) {
		balance, err := query(t, "mgmt-balance-allowed", fmt.Sprintf("[%q]", strings.TrimPrefix(vendor.URL, "http://")))

		require.NoError(t, err)
		require.NotNil(t, balance.Amount)
		assert.Equal(t, 7.0, *balance.Amount)
		assert.Equal(t, int32(1), vendorRequests.Load())
	})
}

func TestProviderTransportRetainsWalletHold(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 5
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB, previousKind := model.DB, common.MainDatabaseType()
	previousRedis, previousBatch := common.RedisEnabled, common.BatchUpdateEnabled
	model.DB, common.RedisEnabled, common.BatchUpdateEnabled = db, false, false
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled, common.BatchUpdateEnabled = previousRedis, previousBatch
		common.SetMainDatabaseType(previousKind)
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.CNYWallet{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{}))
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "provider-wallet", Status: common.UserStatusEnabled, Quota: 10000}).Error)
	service.InitHttpClient()
	require.NoError(t, i18n.Init())
	for _, tc := range []struct {
		name            string
		handshakeStatus int
		priorPending    bool
		failHold        bool
		wantPending     bool
	}{
		{name: "http-rejection"},
		{name: "http-followup-rejection", wantPending: true},
		{name: "http-mapped-rejection"},
		{name: "http-mapped-stream-rejection"},
		{name: "http-mapped-stream-followup-rejection", wantPending: true},
		{name: "http-mapped-followup-rejection", wantPending: true},
		{name: "http-mapped-rejected-followup"},
		{name: "http-mapped-prior-pending", priorPending: true, wantPending: true},
		{name: "websocket-initial", wantPending: true},
		{name: "websocket-followup", wantPending: true},
		{name: "websocket-empty-messages", wantPending: true},
		{name: "websocket-hold-write-failure", failHold: true},
		{name: "websocket-redirect", handshakeStatus: http.StatusFound},
		{name: "websocket-rejection", handshakeStatus: http.StatusBadRequest},
		{name: "websocket-timeout", handshakeStatus: http.StatusRequestTimeout, wantPending: true},
		{name: "websocket-server-error", handshakeStatus: http.StatusInternalServerError, wantPending: true},
		{name: "websocket-prior-pending", handshakeStatus: http.StatusBadRequest, priorPending: true, wantPending: true},
		{name: "websocket-cny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := tc.name
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
			info := &relaycommon.RelayInfo{RequestId: transport, UserId: 1, IsPlayground: true, ForcePreConsume: true,
				OriginModelName: "test", RequestURLPath: "/v1/chat/completions", RelayMode: relayconstant.RelayModeChatCompletions,
				PriceData:   hosttypes.PriceData{GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}},
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "test"}}
			session, apiErr := service.NewWalletBillingSession(c, info, 100)
			require.Nil(t, apiErr)
			info.Billing = session
			if tc.priorPending {
				require.NoError(t, service.MarkCNYUpstreamPending(info, true))
			}
			observed := make(chan error, 1)
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if strings.HasPrefix(transport, "http") {
					if strings.HasPrefix(transport, "http-mapped") {
						w.Header().Set("Content-Type", "application/json")
						if r.URL.Path == "/rejected-before-next" {
							w.WriteHeader(http.StatusBadRequest)
							_, _ = w.Write([]byte(`{"retry":true}`))
						} else if r.URL.Path == "/accepted" {
							_, _ = w.Write([]byte(`{"accepted":true}`))
						} else if strings.Contains(transport, "stream") {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = w.Write([]byte(`data: {"code":1302}` + "\n\n"))
						} else {
							_, _ = w.Write([]byte(`{"code":1302}`))
						}
						return
					}
					if r.URL.Path == "/accepted" {
						_, _ = w.Write([]byte(`{}`))
					} else {
						w.WriteHeader(http.StatusBadRequest)
					}
					return
				}
				funding, fundingErr := model.GetWalletFundingRecord(info.RequestId)
				if fundingErr == nil && !funding.UpstreamPending {
					fundingErr = fmt.Errorf("provider handshake arrived before the wallet hold was persisted")
				}
				if fundingErr != nil {
					observed <- fundingErr
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if tc.handshakeStatus != 0 {
					observed <- nil
					w.WriteHeader(tc.handshakeStatus)
					return
				}
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					observed <- err
					return
				}
				defer conn.Close()
				if transport == "websocket-empty-messages" {
					observed <- nil
					return
				}
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				if transport == "websocket-followup" {
					if err = conn.WriteMessage(websocket.TextMessage, []byte("ready")); err != nil {
						observed <- err
						return
					}
				}
				_, _, err = conn.ReadMessage()
				if err == nil {
					var funding *model.WalletFundingRecord
					funding, err = model.GetWalletFundingRecord(info.RequestId)
					if err == nil && !funding.UpstreamPending {
						err = fmt.Errorf("provider frame arrived before the wallet hold was persisted")
					}
				}
				observed <- err
			}))
			defer upstream.Close()
			info.ChannelBaseUrl = upstream.URL
			plugin, err := jsplugin.NewRegistry().Register(`
export const meta = {apiVersion: 1, key: "wallet-transport", name: "Wallet", version: "1.0.0", author: {name: "Test"},
 relay: [{incoming_path: "/v1/chat/completions", upstream_path: "/chat", converter: "plugin"}]};
export function convertRequest() { return {body: {}}; }
export function convertResponse(ctx, response) {
 const body = JSON.parse(response.body);
 if (body.accepted || body.retry) return {next: {path: "/rejected"}};
 if (body.code === 1302) return {error: {status: 429, message: "Rate limited"}};
 return {body: {}};
}
export function convertStreamEvent(ctx, event) {
 if (event && event.data === "ready") return {send: ["submit"]};
 if (event && event.data && JSON.parse(event.data).code === 1302) return {error: {status: 429, message: "Rate limited"}};
 return {};
}
`, jsplugin.Options{})
			require.NoError(t, err)
			info.ChannelOtherSettings.AdvancedCustom = plugin.Meta.ChannelRelayConfig(nil)
			adaptor := &Adaptor{plugin: plugin}
			adaptor.Init(info)
			switch transport {
			case "http-rejection", "http-followup-rejection":
				if transport == "http-followup-rejection" {
					response, err := adaptor.sendStep(c, info, jsplugin.ConvertedRequest{Path: "/accepted"}, []byte(`{}`), "application/json", true)
					require.NoError(t, err)
					require.NoError(t, response.Body.Close())
				}
				response, err := adaptor.sendStep(c, info, jsplugin.ConvertedRequest{Path: "/rejected"}, []byte(`{}`), "application/json", transport == "http-rejection")
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				assert.Equal(t, http.StatusBadRequest, response.StatusCode)
			case "http-mapped-rejection", "http-mapped-followup-rejection", "http-mapped-rejected-followup", "http-mapped-prior-pending", "http-mapped-stream-rejection", "http-mapped-stream-followup-rejection":
				path := "/rejected"
				if transport == "http-mapped-rejected-followup" {
					path = "/rejected-before-next"
				} else if strings.Contains(transport, "followup") {
					path = "/accepted"
				}
				response, err := adaptor.runChain(c, info, jsplugin.ConvertedRequest{Path: path}, []byte(`{}`), "application/json", nil)
				require.NoError(t, err)
				httpResponse, ok := response.(*http.Response)
				require.True(t, ok)
				require.NoError(t, httpResponse.Body.Close())
				assert.Equal(t, http.StatusTooManyRequests, httpResponse.StatusCode)
				if path != "/rejected" {
					assert.Equal(t, int32(2), requests.Load())
				}
			default:
				request := &jsplugin.WebSocketRequest{Path: "/ws?task=generate"}
				if transport == "websocket-initial" || transport == "websocket-cny" {
					request.Messages = []jsplugin.WebSocketMessage{{Text: "submit"}}
				}
				if transport == "websocket-cny" {
					info.FundingCurrency = "CNY"
				}
				if tc.failHold {
					// A missing reservation makes pending persistence fail before
					// transport. Restore the ID afterwards to inspect and refund
					// the real reservation created by this fixture.
					info.RequestId = "missing-reservation"
				}
				_, err := adaptor.openWebSocket(c, info, jsplugin.ConvertedRequest{WebSocket: request})
				info.RequestId = transport
				require.Error(t, err, "disconnect without usage must not settle or refund")
				if tc.failHold {
					var apiErr *types.NewAPIError
					require.ErrorAs(t, err, &apiErr)
					assert.Equal(t, types.ErrorCodeUpdateDataError, apiErr.GetErrorCode())
					require.ErrorIs(t, err, gorm.ErrRecordNotFound)
				}
				if transport == "websocket-cny" || tc.failHold {
					assert.Zero(t, requests.Load(), "local rejection must fail before the handshake")
				} else {
					select {
					case observedErr := <-observed:
						require.NoError(t, observedErr)
					case <-time.After(5 * time.Second):
						t.Fatal("upstream did not observe the handshake")
					}
				}
			}
			assert.Equal(t, tc.wantPending, service.CNYUpstreamPending(info))
			session.Refund(c)
			record, err := model.GetWalletFundingRecord(info.RequestId)
			require.NoError(t, err)
			if tc.wantPending {
				assert.Equal(t, model.CNYBillingReserved, record.Status)
				assert.Equal(t, 100, record.ReservedQuota)
			} else {
				assert.Equal(t, model.CNYBillingRefunded, record.Status)
			}
		})
	}
}
