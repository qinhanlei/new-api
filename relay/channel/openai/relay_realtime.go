package openai

import (
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type realtimeFrame struct {
	client bool
	body   []byte
	err    error
}

type realtimePendingCreate struct {
	eventID      string
	inputBatches int
}

func readRealtimeFrames(conn *websocket.Conn, client bool, frames chan<- realtimeFrame, done <-chan struct{}) {
	for {
		_, body, err := conn.ReadMessage()
		select {
		case frames <- realtimeFrame{client: client, body: body, err: err}:
		case <-done:
			return
		}
		if err != nil {
			return
		}
	}
}

// A single owner processes both directions, so usage and submission state cannot
// race. Reader goroutines only transfer frames and never mutate billing state.
func OpenaiRealtimeHandler(c *gin.Context, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.RealtimeUsage) {
	if info == nil || info.ClientWs == nil || info.TargetWs == nil {
		return types.NewError(fmt.Errorf("invalid websocket connection"), types.ErrorCodeBadResponse), nil
	}
	info.IsStream = true
	frames, done := make(chan realtimeFrame, 2), make(chan struct{})
	defer close(done)
	defer info.ClientWs.Close()
	defer info.TargetWs.Close()
	go readRealtimeFrames(info.ClientWs, true, frames, done)
	go readRealtimeFrames(info.TargetWs, false, frames, done)
	total := &dto.RealtimeUsage{}
	active := make(map[string]bool)
	completed := make(map[string]bool)
	var waiting []realtimePendingCreate
	// Committed audio and conversation items remain owed even if a later
	// response.create is rejected. Only uncommitted audio can be cleared.
	pendingInputBatches := 0
	pendingAutoBatches := 0
	autoResponse := true
	bufferedAudio := false
	var audioVersion uint64
	var pendingClears []uint64
	clientCommits := 0
	inFlight, hasUsage, untrackedResponse := false, false, false
	var relayErr error
loop:
	for {
		select {
		case <-c.Request.Context().Done():
			relayErr = c.Request.Context().Err()
			break loop
		case frame := <-frames:
			if frame.err != nil {
				if !websocket.IsCloseError(frame.err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					relayErr = frame.err
				}
				break loop
			}
			var event dto.RealtimeEvent
			if err := common.Unmarshal(frame.body, &event); err != nil {
				relayErr = err
				break loop
			}
			if frame.client {
				paid := event.Type == dto.RealtimeEventTypeResponseCreate || event.Type == dto.RealtimeEventInputAudioBufferAppend || event.Type == dto.RealtimeEventTypeConversationCreate
				if paid {
					// Audio chunks share one input batch. response.create cannot
					// consume uncommitted audio, so that buffer keeps its own hold.
					pendingTurns := len(active) + len(waiting) + pendingInputBatches + pendingAutoBatches
					if bufferedAudio {
						pendingTurns++
					}
					reserveInput := false
					switch event.Type {
					case dto.RealtimeEventTypeResponseCreate:
						// One explicit response consumes all committed conversation inputs.
						pendingTurns += 1 - pendingInputBatches
						reserveInput = true
					case dto.RealtimeEventInputAudioBufferAppend:
						reserveInput = !bufferedAudio
						if reserveInput {
							pendingTurns++
						}
					default:
						reserveInput = pendingInputBatches == 0
						if reserveInput {
							pendingTurns++
						}
					}
					if reserveInput {
						if err := service.ReserveRealtimeTurn(info, total, pendingTurns); err != nil {
							if err = helper.WssObject(c, info.ClientWs, &dto.RealtimeEvent{Type: "error", EventId: event.EventId, Error: &types.OpenAIError{Type: "insufficient_quota", Code: "insufficient_quota", Message: err.Error()}}); err != nil {
								relayErr = err
								break loop
							}
							continue
						}
					}
					if err := service.MarkCNYUpstreamPending(info, true); err != nil {
						relayErr = err
						break loop
					}
					inFlight = true
					switch event.Type {
					case dto.RealtimeEventTypeResponseCreate:
						waiting = append(waiting, realtimePendingCreate{eventID: event.EventId, inputBatches: pendingInputBatches})
						pendingInputBatches = 0
					case dto.RealtimeEventInputAudioBufferAppend:
						audioVersion++
						bufferedAudio = true
					default:
						pendingInputBatches = max(1, pendingInputBatches)
					}
				}
				switch event.Type {
				case "input_audio_buffer.commit":
					clientCommits++
					if bufferedAudio {
						pendingInputBatches++
						bufferedAudio = false
						audioVersion++
					}
				case "input_audio_buffer.clear":
					pendingClears = append(pendingClears, audioVersion)
				}

				if event.Type == dto.RealtimeEventTypeSessionUpdate && event.Session != nil && event.Session.Tools != nil {
					info.RealtimeTools = event.Session.Tools
				}
				if err := helper.WssString(c, info.TargetWs, string(frame.body)); err != nil {
					relayErr = err
					break loop
				}
				continue
			}
			info.SetFirstResponseTime()
			var envelope struct {
				Response *struct {
					ID string `json:"id"`
				} `json:"response"`
				Error *struct {
					Type    string `json:"type"`
					EventID string `json:"event_id"`
				} `json:"error"`
				Session *struct {
					Audio *struct {
						Input *struct {
							TurnDetection any `json:"turn_detection"`
						} `json:"input"`
					} `json:"audio"`
				} `json:"session"`
			}
			if err := common.Unmarshal(frame.body, &envelope); err != nil {
				relayErr = err
				break loop
			}
			responseID := ""
			if envelope.Response != nil {
				responseID = envelope.Response.ID
			}
			if event.Type == dto.RealtimeEventTypeResponseDone && responseID != "" && completed[responseID] {
				continue
			}
			if strings.HasPrefix(event.Type, "response.") {
				inFlight = true
				if event.Type != "response.created" && event.Type != dto.RealtimeEventTypeResponseDone && len(active) == 0 {
					untrackedResponse = true
				}
				if err := service.MarkCNYUpstreamPending(info, true); err != nil {
					relayErr = err
					break loop
				}
			}
			switch event.Type {
			case "error":
				if envelope.Error != nil && envelope.Error.Type == "invalid_request_error" && envelope.Error.EventID != "" {
					for i, pending := range waiting {
						if pending.eventID == envelope.Error.EventID {
							pendingInputBatches += pending.inputBatches
							waiting = append(waiting[:i], waiting[i+1:]...)
							inFlight = len(active) != 0 || len(waiting) != 0 || pendingInputBatches != 0 || pendingAutoBatches != 0 || bufferedAudio || untrackedResponse
							break
						}
					}
				}
			case "input_audio_buffer.committed":
				if clientCommits > 0 {
					clientCommits--
				} else if bufferedAudio {
					if autoResponse {
						pendingAutoBatches++
					} else {
						pendingInputBatches++
					}
					bufferedAudio = false
					audioVersion++
				}
			case "input_audio_buffer.cleared":
				if len(pendingClears) != 0 {
					if pendingClears[0] == audioVersion {
						bufferedAudio = false
						inFlight = len(active) != 0 || len(waiting) != 0 || pendingInputBatches != 0 || pendingAutoBatches != 0 || untrackedResponse
					}
					pendingClears = pendingClears[1:]
				}
			case "response.created":
				if active[responseID] {
					break
				}
				if pendingAutoBatches > 0 {
					pendingAutoBatches--
				} else if len(waiting) > 0 {
					waiting = waiting[1:]
				} else {
					pendingInputBatches = max(0, pendingInputBatches-1)
				}
				active[responseID] = true
				inFlight = true
				if err := service.MarkCNYUpstreamPending(info, true); err != nil {
					relayErr = err
					break loop
				}
			case dto.RealtimeEventTypeResponseDone:
				if responseID != "" && completed[responseID] {
					continue
				}
				if event.Response == nil || event.Response.Usage == nil {
					relayErr = fmt.Errorf("%s", i18n.T(c, i18n.MsgBillingUpstreamUsageUnknown))
					break loop
				}
				if err := addRealtimeUsage(total, event.Response.Usage); err != nil {
					relayErr = fmt.Errorf("%s", i18n.T(c, i18n.MsgBillingRealtimeUsageInvalid))
					break loop
				}
				if err := service.PreWssConsumeQuota(c, info, total); err != nil {
					relayErr = err
					break loop
				}
				hasUsage = true
				if !active[responseID] {
					if pendingAutoBatches > 0 {
						pendingAutoBatches--
					} else if len(waiting) > 0 {
						waiting = waiting[1:]
					} else {
						pendingInputBatches = max(0, pendingInputBatches-1)
					}
				}
				delete(active, responseID)
				if responseID != "" {
					completed[responseID] = true
				}
				inFlight = len(active) != 0 || len(waiting) != 0 || pendingInputBatches != 0 || pendingAutoBatches != 0 || bufferedAudio || untrackedResponse
			case dto.RealtimeEventTypeSessionUpdated, dto.RealtimeEventTypeSessionCreated:
				if event.Session != nil {
					turnDetection := event.Session.TurnDetection
					if envelope.Session != nil && envelope.Session.Audio != nil && envelope.Session.Audio.Input != nil {
						turnDetection = envelope.Session.Audio.Input.TurnDetection
					}
					autoResponse = turnDetection != nil
					if detection, ok := turnDetection.(map[string]any); ok {
						if enabled, ok := detection["create_response"].(bool); ok {
							autoResponse = enabled
						}
					}
					info.InputAudioFormat = common.GetStringIfEmpty(event.Session.InputAudioFormat, info.InputAudioFormat)
					info.OutputAudioFormat = common.GetStringIfEmpty(event.Session.OutputAudioFormat, info.OutputAudioFormat)
				}
			}
			if !inFlight && service.CNYUpstreamPending(info) {
				if err := service.MarkCNYUpstreamPending(info, false); err != nil {
					relayErr = err
					inFlight = true
					break loop
				}
			}
			if err := helper.WssString(c, info.ClientWs, string(frame.body)); err != nil {
				relayErr = err
				break loop
			}
		}
	}
	if inFlight {
		if relayErr == nil {
			relayErr = fmt.Errorf("%s", i18n.T(c, i18n.MsgBillingUpstreamUsageUnknown))
		}
		return types.NewError(relayErr, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry()), nil
	}
	var apiErr *types.NewAPIError
	if relayErr != nil {
		apiErr = types.NewError(relayErr, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
	}
	if !hasUsage {
		return apiErr, nil
	}
	return apiErr, total
}

func addRealtimeUsage(total, usage *dto.RealtimeUsage) error {
	for _, field := range []struct {
		total *int
		value int
	}{
		{&total.TotalTokens, usage.TotalTokens}, {&total.InputTokens, usage.InputTokens}, {&total.OutputTokens, usage.OutputTokens},
		{&total.InputTokenDetails.CachedTokens, usage.InputTokenDetails.CachedTokens}, {&total.InputTokenDetails.TextTokens, usage.InputTokenDetails.TextTokens},
		{&total.InputTokenDetails.AudioTokens, usage.InputTokenDetails.AudioTokens}, {&total.OutputTokenDetails.TextTokens, usage.OutputTokenDetails.TextTokens},
		{&total.OutputTokenDetails.AudioTokens, usage.OutputTokenDetails.AudioTokens},
	} {
		if field.value < 0 || *field.total > math.MaxInt-field.value {
			return fmt.Errorf("invalid realtime usage")
		}
		*field.total += field.value
	}
	return nil
}
