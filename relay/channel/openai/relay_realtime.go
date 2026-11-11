package openai

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func OpenaiRealtimeHandler(c *gin.Context, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.RealtimeUsage) {
	if info == nil || info.ClientWs == nil || info.TargetWs == nil {
		return types.NewError(fmt.Errorf("invalid websocket connection"), types.ErrorCodeBadResponse), nil
	}

	info.IsStream = true
	clientConn := info.ClientWs
	targetConn := info.TargetWs

	clientClosed := make(chan struct{})
	targetClosed := make(chan struct{})
	sendChan := make(chan []byte, 100)
	receiveChan := make(chan []byte, 100)
	errChan := make(chan error, 2)

	usage := &dto.RealtimeUsage{}
	localUsage := &dto.RealtimeUsage{}
	sumUsage := &dto.RealtimeUsage{}
	var usageMu sync.Mutex
	var readers sync.WaitGroup
	readers.Add(2)

	gopool.Go(func() {
		defer readers.Done()
		defer func() {
			if r := recover(); r != nil {
				errChan <- fmt.Errorf("panic in client reader: %v", r)
			}
		}()
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := clientConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- fmt.Errorf("error reading from client: %v", err)
					}
					close(clientClosed)
					return
				}

				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- fmt.Errorf("error unmarshalling message: %v", err)
					return
				}

				err = func() error {
					usageMu.Lock()
					defer usageMu.Unlock()
					if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdate {
						if realtimeEvent.Session != nil {
							if realtimeEvent.Session.Tools != nil {
								info.RealtimeTools = realtimeEvent.Session.Tools
							}
						}
					}

					textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
					if err != nil {
						return fmt.Errorf("error counting text token: %v", err)
					}
					logger.LogInfo(c, common.LogText("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
					localUsage.TotalTokens += textToken + audioToken
					localUsage.InputTokens += textToken + audioToken
					localUsage.InputTokenDetails.TextTokens += textToken
					localUsage.InputTokenDetails.AudioTokens += audioToken

					return nil
				}()
				if err != nil {
					errChan <- err
					return
				}

				err = helper.WssString(c, targetConn, string(message))
				if err != nil {
					errChan <- fmt.Errorf("error writing to target: %v", err)
					return
				}

				select {
				case sendChan <- message:
				default:
				}
			}
		}
	})

	gopool.Go(func() {
		defer readers.Done()
		defer func() {
			if r := recover(); r != nil {
				errChan <- fmt.Errorf("panic in target reader: %v", r)
			}
		}()
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := targetConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- fmt.Errorf("error reading from target: %v", err)
					}
					close(targetClosed)
					return
				}
				info.SetFirstResponseTime()
				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- fmt.Errorf("error unmarshalling message: %v", err)
					return
				}

				err = func() error {
					usageMu.Lock()
					defer usageMu.Unlock()
					if realtimeEvent.Type == dto.RealtimeEventTypeResponseDone {
						realtimeUsage := realtimeEvent.Response.Usage
						if realtimeUsage != nil {
							usage.TotalTokens += realtimeUsage.TotalTokens
							usage.InputTokens += realtimeUsage.InputTokens
							usage.OutputTokens += realtimeUsage.OutputTokens
							usage.InputTokenDetails.AudioTokens += realtimeUsage.InputTokenDetails.AudioTokens
							usage.InputTokenDetails.CachedTokens += realtimeUsage.InputTokenDetails.CachedTokens
							usage.InputTokenDetails.TextTokens += realtimeUsage.InputTokenDetails.TextTokens
							usage.OutputTokenDetails.AudioTokens += realtimeUsage.OutputTokenDetails.AudioTokens
							usage.OutputTokenDetails.TextTokens += realtimeUsage.OutputTokenDetails.TextTokens
							// Upstream usage replaces the local estimate even when the
							// reservation fails and closes the session.
							localUsage = &dto.RealtimeUsage{}
							err := preConsumeUsage(c, info, usage, sumUsage)
							if err != nil {
								return fmt.Errorf("error consume usage: %v", err)
							}
							// 本次计费完成，清除
							usage = &dto.RealtimeUsage{}

							localUsage = &dto.RealtimeUsage{}
						} else {
							textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
							if err != nil {
								return fmt.Errorf("error counting text token: %v", err)
							}
							logger.LogInfo(c, common.LogText("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
							localUsage.TotalTokens += textToken + audioToken
							info.IsFirstRequest = false
							localUsage.InputTokens += textToken + audioToken
							localUsage.InputTokenDetails.TextTokens += textToken
							localUsage.InputTokenDetails.AudioTokens += audioToken
							err = preConsumeUsage(c, info, localUsage, sumUsage)
							if err != nil {
								return fmt.Errorf("error consume usage: %v", err)
							}
							// 本次计费完成，清除
							localUsage = &dto.RealtimeUsage{}
							// print now usage
						}
						logger.LogInfo(c, common.LogText("realtime streaming sumUsage: %v", sumUsage))
						logger.LogInfo(c, common.LogText("realtime streaming localUsage: %v", localUsage))
						logger.LogInfo(c, common.LogText("realtime streaming localUsage: %v", localUsage))

					} else if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdated || realtimeEvent.Type == dto.RealtimeEventTypeSessionCreated {
						realtimeSession := realtimeEvent.Session
						if realtimeSession != nil {
							// update audio format
							info.InputAudioFormat = common.GetStringIfEmpty(realtimeSession.InputAudioFormat, info.InputAudioFormat)
							info.OutputAudioFormat = common.GetStringIfEmpty(realtimeSession.OutputAudioFormat, info.OutputAudioFormat)
						}
					} else {
						textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
						if err != nil {
							return fmt.Errorf("error counting text token: %v", err)
						}
						logger.LogInfo(c, common.LogText("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
						localUsage.TotalTokens += textToken + audioToken
						localUsage.OutputTokens += textToken + audioToken
						localUsage.OutputTokenDetails.TextTokens += textToken
						localUsage.OutputTokenDetails.AudioTokens += audioToken
					}
					return nil
				}()
				if err != nil {
					errChan <- err
					return
				}

				err = helper.WssString(c, clientConn, string(message))
				if err != nil {
					errChan <- fmt.Errorf("error writing to client: %v", err)
					return
				}

				select {
				case receiveChan <- message:
				default:
				}
			}
		}
	})

	select {
	case <-clientClosed:
	case <-targetClosed:
	case err := <-errChan:
		//return service.OpenAIErrorWrapper(err, "realtime_error", http.StatusInternalServerError), nil
		logger.LogError(c, common.LogText("realtime error: %s", err.Error()))
	case <-c.Done():
	}

	// Stop both producers before the final reservation and settlement read
	// their totals. Closing the sockets unblocks their pending ReadMessage.
	_ = clientConn.Close()
	_ = targetConn.Close()
	readers.Wait()

	if usage.TotalTokens != 0 {
		_ = preConsumeUsage(c, info, usage, sumUsage)
	}

	if localUsage.TotalTokens != 0 {
		_ = preConsumeUsage(c, info, localUsage, sumUsage)
	}

	// check usage total tokens, if 0, use local usage

	return nil, sumUsage
}

func preConsumeUsage(ctx *gin.Context, info *relaycommon.RelayInfo, usage *dto.RealtimeUsage, totalUsage *dto.RealtimeUsage) error {
	if usage == nil || totalUsage == nil {
		return fmt.Errorf("invalid usage pointer")
	}

	totalUsage.TotalTokens += usage.TotalTokens
	totalUsage.InputTokens += usage.InputTokens
	totalUsage.OutputTokens += usage.OutputTokens
	totalUsage.InputTokenDetails.CachedTokens += usage.InputTokenDetails.CachedTokens
	totalUsage.InputTokenDetails.TextTokens += usage.InputTokenDetails.TextTokens
	totalUsage.InputTokenDetails.AudioTokens += usage.InputTokenDetails.AudioTokens
	totalUsage.OutputTokenDetails.TextTokens += usage.OutputTokenDetails.TextTokens
	totalUsage.OutputTokenDetails.AudioTokens += usage.OutputTokenDetails.AudioTokens
	// The segment is now included in the total, even if reserving fails. Do
	// not add it a second time when the session closes after that failure.
	*usage = dto.RealtimeUsage{}
	err := service.PreWssConsumeQuota(ctx, info, totalUsage)
	return err
}
