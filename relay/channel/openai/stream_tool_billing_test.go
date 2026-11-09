package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type realtimeRateReservation struct {
	targets []int
	fail    bool
}

func (r *realtimeRateReservation) ReserveBeforeSubmit(quota int) error {
	r.targets = append(r.targets, quota)
	if r.fail {
		return errors.New("insufficient quota")
	}
	return nil
}
func (r *realtimeRateReservation) Reserve(quota int) error { return r.ReserveBeforeSubmit(quota) }
func (r *realtimeRateReservation) GetPreConsumedQuota() int {
	if len(r.targets) == 0 || r.fail {
		return 0
	}
	return r.targets[len(r.targets)-1]
}
func (*realtimeRateReservation) Settle(int) error    { return nil }
func (*realtimeRateReservation) Refund(*gin.Context) {}
func (*realtimeRateReservation) NeedsRefund() bool   { return false }

func TestRealtimeRateReservationsUseCumulativeUsageOnceOnClose(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "upstream closes after two segments"
		if fail {
			name = "reservation failure closes without recounting the segment"
		}
		t.Run(name, func(t *testing.T) {
			upgrader := websocket.Upgrader{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for _, event := range []string{
					`{"type":"response.done","response":{"usage":{"total_tokens":10,"input_tokens":10,"input_token_details":{"text_tokens":10}}}}`,
					`{"type":"response.done","response":{"usage":{"total_tokens":20,"input_tokens":20,"input_token_details":{"text_tokens":20}}}}`,
				} {
					if err := conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
						return
					}
				}
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"), time.Now().Add(time.Second))
			}))
			defer upstream.Close()
			billing := &realtimeRateReservation{fail: fail}
			type outcome struct {
				usage *dto.RealtimeUsage
				err   error
			}
			completed := make(chan outcome, 1)
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				client, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					completed <- outcome{err: err}
					return
				}
				defer client.Close()
				target, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
				if err != nil {
					completed <- outcome{err: err}
					return
				}
				defer target.Close()
				ctx, _ := gin.CreateTestContext(w)
				ctx.Request = r
				info := &relaycommon.RelayInfo{ClientWs: client, TargetWs: target, Billing: billing, OriginModelName: "realtime-rate-test", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "realtime-rate-test"}, PriceData: hosttypes.PriceData{ModelRatio: 1, BillingRate: &hosttypes.BillingRateSnapshot{Rate: 0.6, Source: "user", ModelName: "realtime-rate-test"}}}
				apiErr, usage := OpenaiRealtimeHandler(ctx, info)
				if apiErr != nil {
					completed <- outcome{err: apiErr}
					return
				}
				completed <- outcome{usage: usage}
			}))
			defer gateway.Close()
			client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(gateway.URL, "http"), nil)
			require.NoError(t, err)
			defer client.Close()
			require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
			for {
				if _, _, err := client.ReadMessage(); err != nil {
					break
				}
			}
			result := <-completed
			require.NoError(t, result.err)
			require.NotNil(t, result.usage)
			if fail {
				assert.Equal(t, []int{6}, billing.targets)
				assert.Equal(t, 10, result.usage.TotalTokens)
			} else {
				assert.Equal(t, []int{6, 18}, billing.targets)
				assert.Equal(t, 30, result.usage.TotalTokens)
			}
		})
	}
}

func TestObserveStreamChoicesDedupesFunctionCallNames(t *testing.T) {
	info := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
	seen := make(map[string]struct{})
	var names []string

	chunks := []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"get_time","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{}"}}]}}]}`,
	}
	for _, chunk := range chunks {
		observeStreamChoices(info, chunk, seen, &names)
	}

	require.Len(t, names, 2)
	assert.Equal(t, []string{"get_weather", "get_time"}, names)
	assert.Empty(t, info.StreamStatus.ResponseOutcome(), "tool call deltas carry no finish reason")

	observeStreamChoices(info, `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, seen, &names)
	assert.Equal(t, "completed", info.StreamStatus.ResponseOutcome())
	assert.False(t, info.PerformanceBusinessRejection)

	filtered := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
	observeStreamChoices(filtered, `{"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`, map[string]struct{}{}, &names)
	assert.True(t, filtered.PerformanceBusinessRejection)
}
