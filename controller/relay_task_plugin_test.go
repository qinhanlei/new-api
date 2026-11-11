package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type taskSubmissionTestBilling struct {
	events     *[]string
	settleErr  error
	reserveErr error
	onSettle   func()
	refunds    int
}

func (b *taskSubmissionTestBilling) Settle(int) error {
	*b.events = append(*b.events, "settle")
	if b.onSettle != nil {
		b.onSettle()
	}
	return b.settleErr
}

func (b *taskSubmissionTestBilling) Refund(*gin.Context) {
	*b.events = append(*b.events, "refund")
	b.refunds++
}

func (b *taskSubmissionTestBilling) NeedsRefund() bool        { return b.refunds == 0 }
func (b *taskSubmissionTestBilling) GetPreConsumedQuota() int { return 0 }
func (b *taskSubmissionTestBilling) Reserve(int) error {
	*b.events = append(*b.events, "reserve")
	return b.reserveErr
}

func (b *taskSubmissionTestBilling) ReserveBeforeSubmit(quota int) error { return b.Reserve(quota) }

type assetSubmissionTestAdaptor struct {
	channel.TaskAdaptor
	url string
}

func (a *assetSubmissionTestAdaptor) BuildRequestURL(*relaycommon.RelayInfo) (string, error) {
	return a.url, nil
}

func (*assetSubmissionTestAdaptor) BuildRequestHeader(*gin.Context, *http.Request, *relaycommon.RelayInfo) error {
	return nil
}

func (*assetSubmissionTestAdaptor) SubmissionStarted(c *gin.Context) {
	c.Set(pluginruntime.ContextKeyAssetSubmissionStarted, true)
}

func TestPresentTaskSubmissionUsesNativePresenterAfterPersistence(t *testing.T) {
	plugin, err := pluginruntime.CompilePlugin(`
export const meta = {apiVersion:1,key:"presenter-test",name:"Presenter",version:"1.0.0",author:{name:"Test"},models:["model"],fetchMode:"per_task",routes:[{method:"POST",path:"/vendor/jobs",type:"submit",decode:"decode",render:"created"}]};
export const native = {decode:function(ctx){return {kind:"submit",model:"model",requestBody:ctx.body.value};},created:function(ctx,task){return {data:{task_id:task.task_id},upstream:task.data};}};
export function buildSubmitRequest(){return {}} export function parseSubmitResponse(){return {taskId:"upstream"}} export function buildQueryRequest(){return {}} export function parseTaskResult(){return {status:"SUCCESS"}}
`, pluginruntime.Options{})
	require.NoError(t, err)
	priceData := types.PriceData{}
	priceData.AddOtherRatio("seconds", 5)
	task := &model.Task{TaskID: "task_public", SubmitTime: 123}
	task.SetData(map[string]any{"task_id": "upstream_private"})
	outcome := &taskSubmissionOutcome{
		Result:    &relay.TaskSubmitResult{},
		Task:      task,
		RelayInfo: &relaycommon.RelayInfo{PriceData: priceData},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/vendor/jobs", strings.NewReader(`{"model":"model"}`))
	c.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{Plugin: plugin, Route: plugin.Meta.Routes[0]})
	c.Set(pluginruntime.ContextKeyRouteRequest, pluginruntime.RouteRequestContext{Path: "/vendor/jobs", Method: http.MethodPost, Body: map[string]any{"kind": "json", "value": map[string]any{"model": "model"}}})

	presentTaskSubmission(c, outcome)

	assert.JSONEq(t, `{
		"data":{"task_id":"task_public"},
		"upstream":{"task_id":"upstream_private"}
	}`, recorder.Body.String())
	assert.JSONEq(t, `{"seconds":5}`, recorder.Header().Get("X-New-Api-Other-Ratios"))
}

// The submit presenter may answer with the vendor's status; a native route that
// already relayed the vendor's event stream gets nothing more.
func TestPresentTaskSubmissionHonorsReplyWrapperAndRelayedStreams(t *testing.T) {
	plugin, err := pluginruntime.CompilePlugin(`
export const meta = {apiVersion:1,key:"accepted-test",name:"Accepted",version:"1.0.0",author:{name:"Test"},models:["model"],fetchMode:"per_task",routes:[{method:"POST",path:"/vendor/jobs",type:"submit",decode:"decode",render:"created"}]};
export const native = {decode:function(ctx){return {kind:"submit",model:"model",requestBody:ctx.body.value};},created:function(ctx,task){return {__status:202,__body:{id:task.task_id}};}};
export function buildSubmitRequest(){return {}} export function parseSubmitResponse(){return {taskId:"upstream"}} export function buildQueryRequest(){return {}} export function parseTaskResult(){return {status:"SUCCESS"}}
`, pluginruntime.Options{})
	require.NoError(t, err)
	present := func(recorder *httptest.ResponseRecorder, written bool) {
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/vendor/jobs", strings.NewReader(`{"model":"model"}`))
		c.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{Plugin: plugin, Route: plugin.Meta.Routes[0]})
		c.Set(pluginruntime.ContextKeyRouteRequest, pluginruntime.RouteRequestContext{Path: "/vendor/jobs", Method: http.MethodPost, Body: map[string]any{"kind": "json", "value": map[string]any{"model": "model"}}})
		if written {
			_, _ = c.Writer.WriteString("data: {}\n\n")
		}
		presentTaskSubmission(c, &taskSubmissionOutcome{Result: &relay.TaskSubmitResult{}, Task: &model.Task{TaskID: "task_public"}, RelayInfo: &relaycommon.RelayInfo{}})
	}

	accepted := httptest.NewRecorder()
	present(accepted, false)
	assert.Equal(t, http.StatusAccepted, accepted.Code)
	assert.JSONEq(t, `{"id":"task_public"}`, accepted.Body.String())

	streamed := httptest.NewRecorder()
	present(streamed, true)
	assert.Equal(t, "data: {}\n\n", streamed.Body.String())
}

func TestPresentTaskSubmissionFallbackUsesPersistedPublicID(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	outcome := &taskSubmissionOutcome{
		Result:    &relay.TaskSubmitResult{},
		Task:      &model.Task{TaskID: "task_persisted", SubmitTime: 456},
		RelayInfo: &relaycommon.RelayInfo{OriginModelName: "video-model"},
	}

	presentTaskSubmission(c, outcome)

	assert.JSONEq(t, `{
		"id":"task_persisted",
		"task_id":"task_persisted",
		"status":"queued",
		"model":"video-model",
		"created_at":456
	}`, recorder.Body.String())
}

func TestPresentTaskSubmissionUsesHostOpenAIVideoCreateReceipt(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{
		Protocol:  "openai_video",
		Operation: pluginruntime.HostProtocolOperation{Name: "create"},
	})
	task := &model.Task{
		TaskID:     "task_public",
		Status:     model.TaskStatusSubmitted,
		Progress:   "0%",
		CreatedAt:  456,
		Properties: model.Properties{OriginModelName: "video-model"},
	}
	outcome := &taskSubmissionOutcome{Result: &relay.TaskSubmitResult{}, Task: task, RelayInfo: &relaycommon.RelayInfo{}}

	presentTaskSubmission(c, outcome)

	assert.JSONEq(t, `{"id":"task_public","object":"video","model":"video-model","status":"queued","progress":0,"created_at":456}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "task_id")
}

func TestExecuteTaskSubmissionRefundsWhenInsertFails(t *testing.T) {
	events := make([]string, 0, 3)
	database := setupTaskSubmissionDatabase(t, false, &events)
	_ = database
	billing := &taskSubmissionTestBilling{events: &events}
	c := taskSubmissionTestContext()
	info := taskSubmissionRelayInfo(billing)

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		return &relay.TaskSubmitResult{
			UpstreamTaskID: "upstream_private",
			Platform:       constant.TaskPlatform("plugin"),
		}, nil
	})

	assert.Nil(t, outcome)
	require.NotNil(t, taskErr)
	assert.Equal(t, "task_insert_failed", taskErr.Code)
	assert.Equal(t, []string{"reserve", "insert", "refund"}, events)
	assert.Equal(t, 1, billing.refunds)
	assert.False(t, c.Writer.Written())
}

func TestAssetReferencesFollowTaskSubmissionDurability(t *testing.T) {
	for _, tc := range []struct {
		name     string
		started  bool
		success  bool
		wantUses int64
	}{
		{name: "rejected before transport", wantUses: 0},
		{name: "upstream result unknown", started: true, wantUses: 1},
		{name: "persisted task", started: true, success: true, wantUses: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, dialect := openTaskDialectDatabase(t, &model.Task{}, &model.Asset{}, &model.AssetUse{}, &model.User{}, &model.Channel{})
			previousDB, previousType := model.DB, common.MainDatabaseType()
			previousLogConsume := common.LogConsumeEnabled
			model.DB = database
			common.SetMainDatabaseType(dialect)
			common.LogConsumeEnabled = false
			t.Cleanup(func() {
				model.DB = previousDB
				common.SetMainDatabaseType(previousType)
				common.LogConsumeEnabled = previousLogConsume
			})
			asset := model.Asset{PublicID: "asset-na-durable", UserID: 1, Status: model.AssetActive}
			require.NoError(t, database.Create(&asset).Error)
			require.NoError(t, database.Create(&model.AssetUse{AssetID: asset.ID, TaskID: "task_public", ChannelID: 1, CreatedAt: time.Now().Unix()}).Error)
			events := []string{}
			billing := &taskSubmissionTestBilling{events: &events}
			c := taskSubmissionTestContext()
			c.Set(pluginruntime.ContextKeyAssetReferences, []string{asset.PublicID})
			info := taskSubmissionRelayInfo(billing)
			outcome, taskErr := executeTaskSubmissionWith(c, info, func(c *gin.Context, _ *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
				c.Set(pluginruntime.ContextKeyAssetSubmissionStarted, tc.started)
				if !tc.success {
					return nil, service.TaskErrorWrapperLocal(errors.New("material submission rejected"), "AssetNotReady", http.StatusConflict)
				}
				return &relay.TaskSubmitResult{UpstreamTaskID: "upstream-task", Platform: constant.TaskPlatform("doubao")}, nil
			})
			if tc.success {
				require.Nil(t, taskErr)
				require.NotNil(t, outcome)
			} else {
				require.NotNil(t, taskErr)
				assert.Nil(t, outcome)
				assert.NotContains(t, events, "reserve")
			}
			var count int64
			require.NoError(t, database.Model(&model.AssetUse{}).Count(&count).Error)
			assert.Equal(t, tc.wantUses, count)
			if tc.success {
				var use model.AssetUse
				require.NoError(t, database.First(&use).Error)
				assert.True(t, use.Committed)
				require.NoError(t, database.First(&asset, asset.ID).Error)
				assert.Positive(t, asset.LastInferenceAt)
			}
		})
	}
}

func TestExecuteTaskSubmissionSettlementFailureStaysDurableAndWritesNothing(t *testing.T) {
	events := make([]string, 0, 3)
	database := setupTaskSubmissionDatabase(t, true, &events)
	billing := &taskSubmissionTestBilling{events: &events, settleErr: errors.New("settlement failed")}
	c := taskSubmissionTestContext()
	info := taskSubmissionRelayInfo(billing)

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		return &relay.TaskSubmitResult{
			UpstreamTaskID: "upstream_private",
			Platform:       constant.TaskPlatform("plugin"),
		}, nil
	})

	assert.Nil(t, outcome)
	require.NotNil(t, taskErr)
	assert.Equal(t, "task_billing_settlement_failed", taskErr.Code)
	assert.Equal(t, []string{"reserve", "insert", "settle"}, events)
	assert.Zero(t, billing.refunds)
	var count int64
	require.NoError(t, database.Model(&model.Task{}).Where("task_id = ?", "task_public").Count(&count).Error)
	assert.Equal(t, int64(1), count)
	assert.False(t, c.Writer.Written())
}

func TestExecuteTaskSubmissionPersistsPinnedPluginProvenance(t *testing.T) {
	events := make([]string, 0, 3)
	database := setupTaskSubmissionDatabase(t, true, &events)
	previousLogConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = false
	t.Cleanup(func() { common.LogConsumeEnabled = previousLogConsumeEnabled })

	c := taskSubmissionTestContext()
	c.Set(common.RequestIdKey, "request-public")
	c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{
		Generation: &pluginruntime.RoutingGeneration{Number: 42},
		Plugin: &pluginruntime.LoadedPlugin{Meta: pluginruntime.Meta{
			Key:        "document-parser",
			Name:       "Document Parser",
			Version:    "1.2.3",
			APIVersion: 1,
			Author: pluginruntime.AuthorMeta{
				Name: "Community Author",
				URL:  "https://plugins.example/author",
			},
		}},
	})
	billing := &taskSubmissionTestBilling{events: &events}
	info := taskSubmissionRelayInfo(billing)

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		return &relay.TaskSubmitResult{
			UpstreamTaskID: "upstream-private",
			Platform:       constant.TaskPlatform("document-parser"),
		}, nil
	})

	require.Nil(t, taskErr)
	require.NotNil(t, outcome)
	require.NotNil(t, outcome.Task.PrivateData.Execution)
	require.NotNil(t, outcome.Task.PrivateData.Execution.TaskPlugin)
	assert.Equal(t, "request-public", outcome.Task.PrivateData.Execution.RequestID)
	assert.Equal(t, "/plugin/submit", outcome.Task.PrivateData.Execution.RequestPath)
	assert.Equal(t, "1.2.3", outcome.Task.PrivateData.Execution.TaskPlugin.Version)
	assert.Equal(t, uint64(42), outcome.Task.PrivateData.Execution.TaskPlugin.Generation)
	require.NotNil(t, outcome.Task.PrivateData.Execution.TaskPlugin.Author)
	assert.Equal(t, "Community Author", outcome.Task.PrivateData.Execution.TaskPlugin.Author.Name)
	assert.Equal(t, "https://plugins.example/author", outcome.Task.PrivateData.Execution.TaskPlugin.Author.URL)

	var stored model.Task
	require.NoError(t, database.Where("task_id = ?", "task_public").First(&stored).Error)
	require.NotNil(t, stored.PrivateData.Execution)
	require.NotNil(t, stored.PrivateData.Execution.TaskPlugin)
	assert.Equal(t, "document-parser", stored.PrivateData.Execution.TaskPlugin.Key)
	require.NotNil(t, stored.PrivateData.Execution.TaskPlugin.Author)
	assert.Equal(t, "Community Author", stored.PrivateData.Execution.TaskPlugin.Author.Name)
	assert.Equal(t, "upstream-private", stored.PrivateData.UpstreamTaskID)
}

// A submit route declaring retainResult: false persists the task row for
// billing but never writes the upstream snapshot for an immediate terminal
// result, while the in-memory task still carries it for the presenter. An
// asynchronous result on the same route is retained because polling and
// retrieval depend on it.
func TestExecuteTaskSubmissionHonorsRouteRetainResult(t *testing.T) {
	retainFalse := false
	for _, tc := range []struct {
		name          string
		immediate     *relaycommon.TaskInfo
		wantDiscarded bool
	}{
		{"immediate success is discarded", &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, Progress: "100%"}, true},
		{"immediate failure is discarded", &relaycommon.TaskInfo{Status: model.TaskStatusFailure, Reason: "rejected"}, true},
		{"asynchronous result is retained", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := make([]string, 0, 3)
			database, _ := openTaskDialectDatabase(t, &model.Task{}, &model.User{}, &model.Channel{})
			previousDB := model.DB
			model.DB = database
			t.Cleanup(func() { model.DB = previousDB })
			previousLogConsumeEnabled := common.LogConsumeEnabled
			common.LogConsumeEnabled = false
			t.Cleanup(func() { common.LogConsumeEnabled = previousLogConsumeEnabled })

			c := taskSubmissionTestContext()
			c.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{
				Plugin: &pluginruntime.LoadedPlugin{Meta: pluginruntime.Meta{Key: "sync-images"}},
				Route:  pluginruntime.Route{Method: http.MethodPost, Path: "/sync/images", Type: pluginruntime.RouteTypeSubmit, RetainResult: &retainFalse},
			})
			info := taskSubmissionRelayInfo(&taskSubmissionTestBilling{events: &events})
			upstream := []byte(`{"data":[{"url":"https://cdn.example/a.png"}]}`)

			outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
				return &relay.TaskSubmitResult{
					UpstreamTaskID: "task_public",
					Platform:       constant.TaskPlatform("sync-images"),
					TaskData:       upstream,
					Immediate:      tc.immediate,
				}, nil
			})
			require.Nil(t, taskErr)
			require.NotNil(t, outcome)
			assert.JSONEq(t, string(upstream), string(outcome.Task.Data), "presenter keeps the in-memory snapshot")
			assert.Equal(t, tc.wantDiscarded, outcome.Task.PrivateData.ResultDiscarded)
			assert.Equal(t, !tc.wantDiscarded, outcome.Task.ResultRetrievable())

			var stored model.Task
			require.NoError(t, database.Where("task_id = ?", "task_public").First(&stored).Error)
			assert.Equal(t, tc.wantDiscarded, stored.PrivateData.ResultDiscarded)
			var nullCount int64
			require.NoError(t, database.Model(&model.Task{}).Where("task_id = ? AND data IS NULL", "task_public").Count(&nullCount).Error)
			if tc.wantDiscarded {
				assert.Equal(t, int64(1), nullCount, "snapshot column must stay NULL")
				artifacts, err := projectTaskArtifacts(&stored)
				require.NoError(t, err)
				assert.Empty(t, artifacts)
			} else {
				assert.Equal(t, int64(0), nullCount)
				assert.JSONEq(t, string(upstream), string(stored.Data))
			}
			listed := model.TaskGetAllUserTask(1, 0, 10, model.SyncTaskQueryParams{})
			require.Len(t, listed, 1)
			assert.Empty(t, listed[0].Data, "task lists never select the snapshot column")
		})
	}
}

func TestExecuteTaskSubmissionRefundsCancellationBeforeDurableBarrier(t *testing.T) {
	events := make([]string, 0, 2)
	setupTaskSubmissionDatabase(t, true, &events)
	billing := &taskSubmissionTestBilling{events: &events}
	c := taskSubmissionTestContext()
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	info := taskSubmissionRelayInfo(billing)

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		cancel()
		return &relay.TaskSubmitResult{
			UpstreamTaskID: "upstream_private",
			Platform:       constant.TaskPlatform("plugin"),
		}, nil
	})

	assert.Nil(t, outcome)
	require.NotNil(t, taskErr)
	assert.Equal(t, "request_cancelled", taskErr.Code)
	assert.Equal(t, []string{"refund"}, events)
	assert.Equal(t, 1, billing.refunds)
	assert.False(t, c.Writer.Written())
}

func TestExecuteTaskSubmissionDisconnectBeforeUpstreamAcceptanceSkipsSubmitAndRefunds(t *testing.T) {
	events := make([]string, 0, 1)
	setupTaskSubmissionDatabase(t, true, &events)
	billing := &taskSubmissionTestBilling{events: &events}
	c := taskSubmissionTestContext()
	requestContext, cancel := context.WithCancel(c.Request.Context())
	cancel()
	c.Request = c.Request.WithContext(requestContext)
	info := taskSubmissionRelayInfo(billing)
	submitted := false

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		submitted = true
		return nil, nil
	})

	assert.Nil(t, outcome)
	require.NotNil(t, taskErr)
	assert.Equal(t, "request_cancelled", taskErr.Code)
	assert.False(t, submitted)
	assert.Equal(t, []string{"refund"}, events)
	assert.Equal(t, 1, billing.refunds)
	assert.False(t, c.Writer.Written())
}

func TestExecuteTaskSubmissionCallerCancellationDuringSubmitRefundsBeforeDurableBarrier(t *testing.T) {
	events := make([]string, 0, 1)
	setupTaskSubmissionDatabase(t, true, &events)
	billing := &taskSubmissionTestBilling{events: &events}
	c := taskSubmissionTestContext()
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	info := taskSubmissionRelayInfo(billing)
	submitStarted := make(chan struct{})
	done := make(chan struct{})
	var outcome *taskSubmissionOutcome
	var taskErr *dto.TaskError

	go func() {
		defer close(done)
		outcome, taskErr = executeTaskSubmissionWith(c, info, func(c *gin.Context, _ *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
			close(submitStarted)
			<-c.Request.Context().Done()
			return nil, service.TaskErrorWrapperLocal(c.Request.Context().Err(), "do_request_failed", http.StatusInternalServerError)
		})
	}()
	select {
	case <-submitStarted:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "submission did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "submission did not stop after disconnect")
	}

	assert.Nil(t, outcome)
	require.NotNil(t, taskErr)
	assert.Equal(t, "request_cancelled", taskErr.Code)
	assert.Equal(t, []string{"refund"}, events)
	assert.Equal(t, 1, billing.refunds)
	assert.False(t, c.Writer.Written())
}

func TestExecuteTaskSubmissionDisconnectAfterDurableInsertDoesNotRefund(t *testing.T) {
	events := make([]string, 0, 3)
	database := setupTaskSubmissionDatabase(t, true, &events)
	previousLogConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = false
	t.Cleanup(func() { common.LogConsumeEnabled = previousLogConsumeEnabled })
	c := taskSubmissionTestContext()
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	billing := &taskSubmissionTestBilling{
		events:   &events,
		onSettle: cancel,
	}
	info := taskSubmissionRelayInfo(billing)

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		return &relay.TaskSubmitResult{
			UpstreamTaskID: "upstream_private",
			Platform:       constant.TaskPlatform("plugin"),
		}, nil
	})

	require.Nil(t, taskErr)
	require.NotNil(t, outcome)
	assert.Equal(t, "task_public", outcome.Task.TaskID)
	assert.Equal(t, []string{"reserve", "insert", "settle"}, events)
	assert.Zero(t, billing.refunds)
	var count int64
	require.NoError(t, database.Model(&model.Task{}).Where("task_id = ?", "task_public").Count(&count).Error)
	assert.Equal(t, int64(1), count)
	assert.False(t, c.Writer.Written())
}

// setupTaskSubmissionDatabase opens the dialect selected by
// TEST_TASK_DB_DIALECT (SQLite in memory by default) and records every task
// INSERT in events so tests can assert the reserve → insert → settle order.
// Without migrate the task table does not exist and inserts fail.
func setupTaskSubmissionDatabase(t *testing.T, migrate bool, events *[]string) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	var models []any
	if migrate {
		models = append(models, &model.Task{})
	}
	database, _ := openTaskDialectDatabase(t, models...)
	require.NoError(t, database.Callback().Create().Before("gorm:create").Register("test:task-submit-order", func(*gorm.DB) {
		*events = append(*events, "insert")
	}))
	model.DB = database
	t.Cleanup(func() { model.DB = previousDB })
	return database
}

func taskSubmissionTestContext() *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/plugin/submit", strings.NewReader(`{}`))
	return c
}

func taskSubmissionRelayInfo(billing relaycommon.BillingSettler) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserId:          1,
		UsingGroup:      "default",
		OriginModelName: "plugin-model",
		Billing:         billing,
		TaskRelayInfo: &relaycommon.TaskRelayInfo{
			PublicTaskID:  "task_public",
			LockedChannel: &model.Channel{Id: 1, Type: constant.ChannelTypeTaskPlugin, Name: "plugin"},
		},
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1, ChannelType: constant.ChannelTypeTaskPlugin},
	}
}

// Uses the real submit adaptor, expression evaluator, BillingSession, task row
// and consume log. Set TEST_TASK_DB_DIALECT plus TEST_MYSQL_DSN or
// TEST_POSTGRES_DSN to exercise the same contract on an external test database.
// Unique table prefixes keep the fixture isolated from all existing tables.
// openTaskDialectDatabase opens the engine selected by TEST_TASK_DB_DIALECT
// (default SQLite in memory; MySQL and PostgreSQL through TEST_MYSQL_DSN and
// TEST_POSTGRES_DSN) with a unique table prefix, migrates the given models and
// drops them on cleanup. It logs the engine version so database verification
// runs leave a record.
func openTaskDialectDatabase(t *testing.T, models ...any) (*gorm.DB, common.DatabaseType) {
	t.Helper()
	models = append(models, &model.BillingRateRule{})
	dialect := common.DatabaseType(os.Getenv("TEST_TASK_DB_DIALECT"))
	var driver gorm.Dialector
	switch dialect {
	case "", common.DatabaseTypeSQLite:
		dialect = common.DatabaseTypeSQLite
		driver = sqlite.Open(":memory:")
	case common.DatabaseTypeMySQL:
		require.NotEmpty(t, os.Getenv("TEST_MYSQL_DSN"))
		driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
	case common.DatabaseTypePostgreSQL:
		require.NotEmpty(t, os.Getenv("TEST_POSTGRES_DSN"))
		driver = postgres.New(postgres.Config{DSN: os.Getenv("TEST_POSTGRES_DSN"), PreferSimpleProtocol: true})
	default:
		t.Fatalf("unsupported test dialect %q", dialect)
	}
	db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: fmt.Sprintf("tsubmit_%d_", time.Now().UnixNano())}})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(models...))
	t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(models...)) })
	var version string
	if dialect == common.DatabaseTypeSQLite {
		require.NoError(t, db.Raw("SELECT sqlite_version()").Scan(&version).Error)
	} else {
		require.NoError(t, db.Raw("SELECT version()").Scan(&version).Error)
	}
	t.Logf("database: %s %s", dialect, version)
	return db, dialect
}

const immediateTaskSettlementPlugin = `
export const meta={apiVersion:1,key:"generic-settlement",name:"Generic settlement",version:"1.0.0",author:{name:"Test"},models:["document-model"],fetchMode:"per_task",submitResponseTypes:["json","sse"],usageSchema:{units:{type:"number",unit:"count",description:"Document generation unit price"}}};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/compile",body:ctx.requestBody,responseType:ctx.requestBody.responseType || "json"};}
export function parseSubmitEvent(ctx,event){const body=JSON.parse(event.data);return {state:body,done:body.complete === true};}
export function parseSubmitResponse(ctx,resp){return {taskId:"vendor-job",taskData:resp.body,immediate:{status:resp.body.status,reason:"provider rejected job"}};}
export function extractUsage(){return {units:4};}
export function extractUsageOnComplete(ctx,result,body){if(body.hookError)throw new Error("completion usage unavailable");return body.usage;}
export function parseTaskResult(){throw new Error("completed submissions must not poll");}
export function buildQueryRequest(){throw new Error("completed submissions must not poll");}
`

func TestImmediateTaskSettlementDatabase(t *testing.T) {
	db, dialect := openTaskDialectDatabase(t, &model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}, &model.Log{}, &model.CNYWallet{}, &model.CNYTokenBudget{}, &model.CNYBillingRecord{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{})
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	oldRedis, oldMemory, oldBatch, oldConsume, oldExport := common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, common.DataExportEnabled
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(dialect, dialect)
	common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, common.DataExportEnabled = false, false, false, true, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldMain, oldLog)
		common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, common.DataExportEnabled = oldRedis, oldMemory, oldBatch, oldConsume, oldExport
	})

	const expression = `u("units") == 7 ? tier("missing", u("missing") * 1.0) : u("units") == 8 ? tier("invalid", -1.0) : u("units") > 3 ? tier("bulk", u("units") * 0.01) : tier("small", u("units") * 0.01)`
	withTieredBillingConfig(t, map[string]string{"document-model": "tiered_expr"}, map[string]string{"document-model": expression})
	plugin, err := pluginruntime.CompilePlugin(immediateTaskSettlementPlugin, pluginruntime.Options{})
	require.NoError(t, err)
	for index, tc := range []struct {
		name, status string
		actual       any
		count        float64
		stream       bool
	}{
		{"partial", "SUCCESS", 2, 2, false}, {"zero", "SUCCESS", 0, 0, false}, {"larger", "SUCCESS", 6, 6, false},
		{"invalid usage", "SUCCESS", -1, 4, false}, {"expression failure", "SUCCESS", 7, 4, false}, {"negative result", "SUCCESS", 8, 4, false}, {"missing usage", "SUCCESS", nil, 4, false}, {"failed", "FAILURE", 9, 0, false},
		{"hook failure", "SUCCESS", 1, 4, false}, {"SSE ended early", "SUCCESS", 1, 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				body := map[string]any{"status": tc.status}
				if tc.actual != nil {
					body["usage"] = map[string]any{"units": tc.actual}
				}
				body["hookError"] = tc.name == "hook failure"
				encoded, err := common.Marshal(body)
				if err != nil {
					panic(err)
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
				} else {
					_, _ = w.Write(encoded)
				}
			}))
			defer server.Close()
			initial := int(20 * common.QuotaPerUnit)
			user := model.User{Username: fmt.Sprintf("task_user_%d", index), AffCode: fmt.Sprintf("task_aff_%d", index), Quota: initial, Status: common.UserStatusEnabled}
			require.NoError(t, db.Create(&user).Error)
			ch := model.Channel{Name: "test provider", Type: constant.ChannelTypeTaskPlugin}
			require.NoError(t, db.Create(&ch).Error)
			c := taskSubmissionTestContext()
			c.Set("group", "default")
			c.Set("username", user.Username)
			c.Set("task_request", map[string]any{"model": "document-model"})
			if tc.stream {
				c.Set("task_request", map[string]any{"model": "document-model", "responseType": "sse"})
				c.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{Plugin: plugin, Route: pluginruntime.Route{Method: http.MethodPost, Path: "/compile", Type: pluginruntime.RouteTypeSubmit}})
			}
			c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Plugin: plugin})
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "document-model")
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
			common.SetContextKey(c, constant.ContextKeyChannelId, ch.Id)
			common.SetContextKey(c, constant.ContextKeyChannelType, ch.Type)
			info := taskSubmissionRelayInfo(nil)
			info.UserId = user.Id
			info.OriginModelName = "document-model"
			info.UserGroup = "default"
			info.UsingGroup = "default"
			info.IsPlayground = true
			info.UserSetting.BillingPreference = "wallet_only"
			info.PublicTaskID = model.GenerateTaskID()
			info.LockedChannel = &ch
			outcome, taskErr := executeTaskSubmission(c, info)
			require.Nil(t, taskErr)
			require.NotNil(t, outcome)
			want := common.QuotaRound(tc.count * 0.01 * common.QuotaPerUnit)
			assert.Equal(t, want, outcome.Result.Quota)
			assert.Equal(t, want, info.PriceData.Quota)
			var stored model.Task
			require.NoError(t, db.Where("task_id = ?", info.PublicTaskID).First(&stored).Error)
			assert.Equal(t, want, stored.Quota)
			pending := tc.name == "invalid usage" || tc.name == "expression failure" || tc.name == "negative result" || tc.name == "hook failure" || tc.stream
			if pending {
				assert.Equal(t, model.TaskStatus(model.TaskStatusAwaitingReview), stored.Status)
				assert.Zero(t, stored.FinishTime)
			} else {
				assert.Equal(t, model.TaskStatus(tc.status), stored.Status)
				assert.Positive(t, stored.FinishTime)
			}
			assert.Equal(t, float64(4), info.TieredBillingSnapshot.EstimatedQuotaBeforeGroup/(0.01*common.QuotaPerUnit))
			if tc.status == "SUCCESS" {
				assert.Equal(t, tc.count, stored.PrivateData.BillingContext.TieredSnapshot.UsageFacts["units"])
			}
			var updated model.User
			require.NoError(t, db.First(&updated, user.Id).Error)
			assert.Equal(t, initial-want, updated.Quota)
			assert.Equal(t, 1, updated.RequestCount)
			record, err := model.GetWalletFundingRecord(info.RequestId)
			require.NoError(t, err)
			assert.Equal(t, stored.TaskID, record.TaskID)
			assert.Equal(t, info.RequestId, stored.PrivateData.Execution.RequestID)
			var logs []model.Log
			require.NoError(t, db.Where("user_id = ?", user.Id).Find(&logs).Error)
			if pending {
				assert.Equal(t, model.CNYBillingSubmitted, record.Status)
				assert.Zero(t, updated.UsedQuota)
				assert.Empty(t, logs)
				info.Billing.Refund(c)
				require.NoError(t, db.First(&updated, user.Id).Error)
				assert.Equal(t, initial-want, updated.Quota)
				return
			}
			assert.Equal(t, want, updated.UsedQuota)
			require.Len(t, logs, 1)
			assert.Equal(t, want, logs[0].Quota)
			var other map[string]any
			require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
			assert.True(t, stored.PrivateData.DeliveredInline)
			assert.Equal(t, true, other["task_sync"])
			if tc.status == "SUCCESS" {
				assert.Equal(t, tc.count, other["usage_facts"].(map[string]any)["units"])
			}
			assert.False(t, c.Writer.Written(), "presentation must follow persistence and settlement")
			require.NoError(t, info.Billing.Settle(want))
			info.Billing.Refund(c)
			require.NoError(t, db.First(&updated, user.Id).Error)
			assert.Equal(t, initial-want, updated.Quota, "terminal settlement is idempotent")
		})
	}
}

func TestCNYPluginUnknownCompletionKeepsReservation(t *testing.T) {
	const expression = `v2:CNY:u("units") * 0.01`
	withTieredBillingConfig(t, map[string]string{"document-model": "tiered_expr"}, map[string]string{"document-model": expression})
	plugin, err := pluginruntime.CompilePlugin(immediateTaskSettlementPlugin, pluginruntime.Options{})
	require.NoError(t, err)
	oldRedis, oldMemory, oldBatch, oldConsume, oldExport := common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, common.DataExportEnabled
	common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, common.DataExportEnabled = false, false, false, true, false
	t.Cleanup(func() {
		common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, common.DataExportEnabled = oldRedis, oldMemory, oldBatch, oldConsume, oldExport
	})
	for _, tc := range []struct {
		name, responseType, contentType, body string
	}{
		{"completion hook error", "json", "application/json", `{"status":"SUCCESS","hookError":true,"usage":{"units":1}}`},
		{"SSE EOF before completion", "sse", "text/event-stream", "data: {\"status\":\"SUCCESS\",\"usage\":{\"units\":1}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, dialect := openTaskDialectDatabase(t, &model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}, &model.Log{}, &model.CNYWallet{}, &model.CNYTokenBudget{}, &model.CNYBillingRecord{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{})
			oldDB, oldLogDB := model.DB, model.LOG_DB
			oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(dialect, dialect)
			t.Cleanup(func() {
				model.DB, model.LOG_DB = oldDB, oldLogDB
				common.SetDatabaseTypes(oldMain, oldLog)
			})
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "cny-plugin", Status: common.UserStatusEnabled, Quota: 123456, UsedQuota: 123}).Error)
			require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: strings.Repeat("d", 48), Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 654321, UsedQuota: 456}).Error)
			ch := model.Channel{Id: 1, Name: "CNY plugin", Type: constant.ChannelTypeTaskPlugin, UsedQuota: 789}
			require.NoError(t, db.Create(&ch).Error)
			_, err := model.AdjustCNYWallet(1, 99, common.RoleRootUser, 100000, "initial", "test")
			require.NoError(t, err)
			_, err = model.UpdateCNYTokenBudget(1, 1, model.CNYTokenBudgetChange{Enabled: true, RemainQuota: 100000})
			require.NoError(t, err)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/compile", strings.NewReader(`{}`))
			c.Set("group", "default")
			c.Set("username", "cny-plugin")
			c.Set("task_request", map[string]any{"model": "document-model", "responseType": tc.responseType})
			c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Plugin: plugin})
			if tc.responseType == "sse" {
				c.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{Plugin: plugin, Route: pluginruntime.Route{Method: http.MethodPost, Path: "/compile", Type: pluginruntime.RouteTypeSubmit}})
			}
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "document-model")
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
			common.SetContextKey(c, constant.ContextKeyChannelId, ch.Id)
			common.SetContextKey(c, constant.ContextKeyChannelType, ch.Type)
			info := taskSubmissionRelayInfo(nil)
			info.TokenId, info.RequestId = 1, "cny-plugin-request"
			info.OriginModelName, info.UserGroup = "document-model", "default"
			info.UserSetting.BillingPreference = "wallet_only"
			info.LockedChannel = &ch

			outcome, taskErr := executeTaskSubmission(c, info)

			require.Nil(t, taskErr)
			require.NotNil(t, outcome)
			var task model.Task
			require.NoError(t, db.Where("task_id = ?", info.PublicTaskID).First(&task).Error)
			assert.Equal(t, model.TaskStatus(model.TaskStatusAwaitingReview), task.Status)
			assert.Zero(t, task.FinishTime)
			assert.NotEmpty(t, task.FailReason)
			assert.Equal(t, 20000, task.Quota)
			require.NotNil(t, task.PrivateData.BillingContext)
			require.NotNil(t, task.PrivateData.BillingContext.TieredSnapshot)
			assert.Equal(t, billingexpr.CurrencyCNY, task.PrivateData.BillingContext.TieredSnapshot.BillingCurrency())
			assert.Equal(t, float64(4), task.PrivateData.BillingContext.TieredSnapshot.UsageFacts["units"])
			record, err := model.GetCNYBillingRecord(info.RequestId)
			require.NoError(t, err)
			assert.Equal(t, model.CNYBillingSubmitted, record.Status)
			assert.Equal(t, task.TaskID, record.TaskID)
			assert.Equal(t, 20000, record.ReservedQuota)
			assert.Zero(t, record.Quota)
			assert.Zero(t, record.RefundQuota)
			wallet, err := model.GetCNYWallet(1)
			require.NoError(t, err)
			assert.Equal(t, 80000, wallet.Quota)
			assert.Equal(t, 20000, wallet.ReservedQuota)
			assert.Zero(t, wallet.UsedQuota)
			budget, err := model.GetCNYTokenBudget(1, 1)
			require.NoError(t, err)
			assert.Equal(t, 80000, budget.RemainQuota)
			assert.Zero(t, budget.UsedQuota)
			var user model.User
			require.NoError(t, db.First(&user, 1).Error)
			assert.Equal(t, 123456, user.Quota)
			assert.Equal(t, 123, user.UsedQuota)
			var token model.Token
			require.NoError(t, db.First(&token, 1).Error)
			assert.Equal(t, 654321, token.RemainQuota)
			assert.Equal(t, 456, token.UsedQuota)
			require.NoError(t, db.First(&ch, 1).Error)
			assert.Equal(t, int64(789), ch.UsedQuota)
			var logCount int64
			require.NoError(t, db.Model(&model.Log{}).Count(&logCount).Error)
			assert.Zero(t, logCount, "CNY requests must not write USD consume logs")
			if tc.responseType == "sse" {
				assert.Equal(t, tc.body, recorder.Body.String(), "native clients still receive the provider's partial stream")
			} else {
				assert.False(t, c.Writer.Written())
			}
		})
	}
}

func TestAcceptedSubmitStreamNeverRetries(t *testing.T) {
	c := taskSubmissionTestContext()
	assert.Equal(t, service.PolicyDecision{Action: "stop", Reason: "task_accepted", Source: "system"}, decideTaskRetry(c, &dto.TaskError{StatusCode: 502, LocalError: true, NoRetry: true}, 3))
}

// Local task rejections carry a message but no cause; the response and the
// decision record must still be produced.
func TestRespondTaskSubmissionErrorWithoutCause(t *testing.T) {
	previousErrorLog := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = false
	t.Cleanup(func() { constant.ErrorLogEnabled = previousErrorLog })
	c := taskSubmissionTestContext()
	taskErr := &dto.TaskError{Code: "get_channel_failed", Message: "no channel", StatusCode: http.StatusServiceUnavailable, LocalError: true}
	require.NotPanics(t, func() { respondTaskSubmissionError(c, taskErr) })
	assert.Equal(t, http.StatusServiceUnavailable, c.Writer.Status())
	events := service.RequestPolicy(c).Events()
	require.Len(t, events, 1)
	assert.Equal(t, service.PolicyDecision{Action: "stop", Reason: "request_failed", Source: "system"}, events[0].Decision)
	assert.Equal(t, http.StatusServiceUnavailable, events[0].Status)
	assert.Equal(t, service.PolicyDecision{Action: "stop", Reason: "local_rejection", Source: "system"}, decideTaskRetry(c, &dto.TaskError{StatusCode: http.StatusForbidden, LocalError: true, Message: "billing"}, 2), "local errors stop once the status rules do not force a retry")
}

func TestAssetUnknownSubmissionStopsRetryWithoutChangingKnownRejections(t *testing.T) {
	for _, tc := range []struct {
		name    string
		assets  bool
		started bool
		err     *dto.TaskError
		want    string
	}{
		{"transport", true, true, &dto.TaskError{StatusCode: 500, Code: "do_request_failed"}, "stop"},
		{"unreadable accepted response", true, true, &dto.TaskError{StatusCode: 502, Code: "parse_response_failed"}, "stop"},
		{"local response parsing", true, true, &dto.TaskError{StatusCode: 400, LocalError: true}, "stop"},
		{"explicit rate limit", true, true, &dto.TaskError{StatusCode: 429}, "retry"},
		{"before transport", true, false, &dto.TaskError{StatusCode: 502}, "retry"},
		{"unrelated task", false, true, &dto.TaskError{StatusCode: 502}, "retry"},
		{"success", true, true, nil, "stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := taskSubmissionTestContext()
			if tc.assets {
				c.Set(pluginruntime.ContextKeyAssetReferences, []string{"asset-na-example"})
			}
			c.Set(pluginruntime.ContextKeyAssetSubmissionStarted, tc.started)
			assert.Equal(t, tc.want, decideTaskRetry(c, tc.err, 2).Action)
		})
	}
}

func TestExecuteTaskSubmissionRefundsWhenFinalReserveFails(t *testing.T) {
	events := []string{}
	setupTaskSubmissionDatabase(t, true, &events)
	billing := &taskSubmissionTestBilling{events: &events, reserveErr: errors.New("insufficient funds")}
	c := taskSubmissionTestContext()
	info := taskSubmissionRelayInfo(billing)
	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		return &relay.TaskSubmitResult{Platform: "plugin", Quota: 600, Immediate: &relaycommon.TaskInfo{Status: "SUCCESS"}}, nil
	})
	require.Nil(t, outcome)
	require.NotNil(t, taskErr)
	assert.Equal(t, http.StatusForbidden, taskErr.StatusCode)
	assert.Equal(t, []string{"reserve", "refund"}, events)
	assert.Equal(t, 1, billing.refunds)
	assert.False(t, c.Writer.Written())
}

func TestCNYTaskSubmissionKeepsReservationUntilTerminal(t *testing.T) {
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })
	for _, state := range []string{"async", "success", "failure", "unknown", "insert failure", "local rejection"} {
		t.Run(state, func(t *testing.T) {
			db, dialect := openTaskDialectDatabase(t, &model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}, &model.Asset{}, &model.AssetUse{}, &model.CNYWallet{}, &model.CNYTokenBudget{}, &model.CNYBillingRecord{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{})
			previousDB, previousLogDB := model.DB, model.LOG_DB
			previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
			previousRetry := common.RetryTimes
			model.DB, model.LOG_DB = db, nil
			common.SetDatabaseTypes(dialect, dialect)
			common.RetryTimes = 3
			t.Cleanup(func() {
				model.DB, model.LOG_DB = previousDB, previousLogDB
				common.SetDatabaseTypes(previousMain, previousLog)
				common.RetryTimes = previousRetry
			})
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "submit-cny", Status: common.UserStatusEnabled, Quota: 123456}).Error)
			require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: strings.Repeat("b", 48), Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 0}).Error)
			_, err := model.AdjustCNYWallet(1, 99, common.RoleRootUser, 100000, "initial", "test")
			require.NoError(t, err)
			_, err = model.UpdateCNYTokenBudget(1, 1, model.CNYTokenBudgetChange{Enabled: true, RemainQuota: 100000})
			require.NoError(t, err)
			c := taskSubmissionTestContext()
			info := taskSubmissionRelayInfo(nil)
			info.TokenId, info.RequestId = 1, "request-native"
			info.PriceData = types.PriceData{Currency: "CNY", Quota: 10000, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}}
			expr := `v2:CNY:u("tokens") * 10 / 1000000`
			info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{Currency: "CNY", BillingMode: "tiered_expr", ExprString: expr, ExprHash: billingexpr.ExprHashString(expr), GroupRatio: 1, QuotaPerUnit: 500000, TaskUsageBilling: true, UsageFacts: map[string]any{"tokens": float64(2000)}}
			asset := model.Asset{PublicID: "asset-na-cny", UserID: 1, Status: model.AssetActive}
			require.NoError(t, db.Create(&asset).Error)
			require.NoError(t, db.Create(&model.AssetUse{AssetID: asset.ID, TaskID: info.PublicTaskID, ChannelID: 1, CreatedAt: time.Now().Unix()}).Error)
			c.Set(pluginruntime.ContextKeyAssetReferences, []string{asset.PublicID})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if state == "unknown" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if assert.NoError(t, err) {
						_ = conn.Close()
					}
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(upstream.Close)
			adaptor := &assetSubmissionTestAdaptor{url: upstream.URL}
			attempts := 0
			outcome, taskErr := executeTaskSubmissionWith(c, info, func(c *gin.Context, info *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
				attempts++
				require.Nil(t, service.PreConsumeBilling(c, 10000, info))
				if state == "local rejection" {
					info.ChannelSetting.Proxy = "://invalid"
				}
				response, err := channel.DoTaskApiRequest(adaptor, c, info, strings.NewReader(`{}`))
				if err != nil {
					return nil, service.TaskErrorWrapperLocal(err, "do_request_failed", http.StatusBadGateway)
				}
				require.NoError(t, response.Body.Close())
				result := &relay.TaskSubmitResult{UpstreamTaskID: "upstream-id", Platform: constant.TaskPlatform("doubao"), Quota: 10000}
				if state == "success" {
					result.Immediate = &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}
					result.Quota = 6000
				}
				if state == "failure" {
					result.Immediate = &relaycommon.TaskInfo{Status: model.TaskStatusFailure, Reason: "vendor rejected"}
					result.Quota = 0
				}
				if state == "insert failure" {
					require.NoError(t, db.Migrator().DropTable(&model.Task{}))
				}
				return result, nil
			})
			wantAttempts := 1
			if state == "local rejection" {
				wantAttempts += common.RetryTimes
			}
			assert.Equal(t, wantAttempts, attempts)
			record, err := model.GetCNYBillingRecord("request-native")
			require.NoError(t, err)
			wallet, err := model.GetCNYWallet(1)
			require.NoError(t, err)
			switch state {
			case "async":
				require.Nil(t, taskErr)
				require.NotNil(t, outcome)
				assert.Equal(t, model.CNYBillingSubmitted, record.Status)
				assert.Zero(t, wallet.UsedQuota)
				assert.Equal(t, 10000, wallet.ReservedQuota)
			case "success":
				require.Nil(t, taskErr)
				assert.Equal(t, model.CNYBillingSettled, record.Status)
				assert.Equal(t, 6000, wallet.UsedQuota)
				assert.Zero(t, wallet.ReservedQuota)
			case "failure":
				require.Nil(t, taskErr)
				assert.Equal(t, model.CNYBillingRefunded, record.Status)
				assert.Equal(t, 100000, wallet.Quota)
			case "local rejection":
				require.NotNil(t, taskErr)
				assert.Equal(t, model.CNYBillingRefunded, record.Status)
				assert.Equal(t, 100000, wallet.Quota)
				assert.False(t, c.GetBool(pluginruntime.ContextKeyAssetSubmissionStarted))
			default:
				require.NotNil(t, taskErr)
				assert.Equal(t, model.CNYBillingReserved, record.Status)
				assert.Equal(t, 90000, wallet.Quota)
				assert.Zero(t, wallet.UsedQuota)
			}
			var uses []model.AssetUse
			require.NoError(t, db.Find(&uses).Error)
			if state == "local rejection" {
				assert.Empty(t, uses)
			} else {
				require.Len(t, uses, 1)
				assert.Equal(t, taskErr == nil, uses[0].Committed)
			}
			var user model.User
			require.NoError(t, db.First(&user, 1).Error)
			assert.Equal(t, 123456, user.Quota)
			assert.Zero(t, user.UsedQuota)
		})
	}
}

func TestCNYTextAdmissionBeforeChannelMetadata(t *testing.T) {
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })
	require.NoError(t, i18n.Init())
	db, dialect := openTaskDialectDatabase(t, &model.User{}, &model.Token{}, &model.CNYWallet{}, &model.CNYTokenBudget{}, &model.CNYBillingRecord{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{})
	previousDB := model.DB
	mainType, logType := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB = db
	common.SetDatabaseTypes(dialect, dialect)
	t.Cleanup(func() { model.DB = previousDB; common.SetDatabaseTypes(mainType, logType) })
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "native-admit", Status: common.UserStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: strings.Repeat("c", 48), Status: common.TokenStatusEnabled, ExpiredTime: -1}).Error)
	_, err := model.AdjustCNYWallet(1, 99, common.RoleRootUser, 100000, "initial", "test")
	require.NoError(t, err)
	_, err = model.UpdateCNYTokenBudget(1, 1, model.CNYTokenBudgetChange{Enabled: true, RemainQuota: 100000})
	require.NoError(t, err)
	withTieredBillingConfig(t, map[string]string{"native-text": "tiered_expr"}, map[string]string{"native-text": "v2:CNY:p * 2 + c * 8"})
	c := taskSubmissionTestContext()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-text","messages":[{"role":"user","content":"hello"}]}`))
	common.SetContextKey(c, constant.ContextKeyUserId, 1)
	common.SetContextKey(c, constant.ContextKeyTokenId, 1)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "native-text")
	c.Set("channel_id", 7)
	info, err := relaycommon.GenRelayInfo(c, relaytypes.RelayFormatOpenAI, &relaydto.GeneralOpenAIRequest{Model: "native-text"}, nil)
	require.NoError(t, err)
	require.Nil(t, info.ChannelMeta)
	require.Nil(t, relay.PrepareRequestBilling(c, info))
	require.Nil(t, info.ChannelMeta, "admission must preserve initial routing semantics")
	record, err := model.GetCNYBillingRecord(info.RequestId)
	require.NoError(t, err)
	assert.Equal(t, 7, record.ChannelID)
	c.Set("channel_id", 8)
	require.Nil(t, service.PrepareBillingForSelectedGroup(c, info))
	record, err = model.GetCNYBillingRecord(info.RequestId)
	require.NoError(t, err)
	assert.Equal(t, 8, record.ChannelID)
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelSetting: relaydto.ChannelSettings{Proxy: "://invalid"}}
	request := httptest.NewRequest(http.MethodPost, "http://upstream.invalid", strings.NewReader(`{}`))
	_, err = channel.DoRequest(c, request, info)
	require.Error(t, err)
	assert.False(t, service.CNYUpstreamPending(info), "a local proxy error has not submitted paid work")
	assert.True(t, info.Billing.NeedsRefund())
	info.Billing.Refund(c)
	record, err = model.GetCNYBillingRecord(info.RequestId)
	require.NoError(t, err)
	assert.Equal(t, model.CNYBillingRefunded, record.Status)

	// Admission through a CNY budget must not unlock legacy USD-only routes.
	c.Set("token_usd_exhausted", true)
	for _, handler := range []func(*gin.Context, *relaycommon.RelayInfo) *dto.MidjourneyResponse{relay.RelaySwapFace, relay.RelayMidjourneySubmit} {
		response := handler(c, &relaycommon.RelayInfo{})
		require.NotNil(t, response)
		assert.Equal(t, i18n.T(c, i18n.MsgBillingUSDTokenExhausted), response.Description)
	}
}

func TestUSDWalletTaskSubmissionKeepsReservationUntilTerminal(t *testing.T) {
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })
	for _, state := range []string{"async", "success", "failure", "unknown", "insert failure", "cancel after acceptance"} {
		t.Run(state, func(t *testing.T) {
			db, dialect := openTaskDialectDatabase(t, &model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}, &model.Log{}, &model.CNYWallet{}, &model.CNYTokenBudget{}, &model.CNYBillingRecord{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{})
			previousDB, previousLogDB := model.DB, model.LOG_DB
			previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
			previousRetry, previousBatch, previousMemory := common.RetryTimes, common.BatchUpdateEnabled, common.MemoryCacheEnabled
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(dialect, dialect)
			common.RetryTimes, common.BatchUpdateEnabled, common.MemoryCacheEnabled = 3, true, false
			t.Cleanup(func() {
				model.DB, model.LOG_DB = previousDB, previousLogDB
				common.SetDatabaseTypes(previousMain, previousLog)
				common.RetryTimes, common.BatchUpdateEnabled, common.MemoryCacheEnabled = previousRetry, previousBatch, previousMemory
			})
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "submit-usd", Status: common.UserStatusEnabled, Quota: 100000}).Error)
			require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: strings.Repeat("u", 48), Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100000}).Error)
			require.NoError(t, db.Create(&model.Channel{Id: 1, Name: "wallet"}).Error)
			c := taskSubmissionTestContext()
			ctx, cancel := context.WithCancel(c.Request.Context())
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
			info := taskSubmissionRelayInfo(nil)
			info.TokenId, info.RequestId, info.ForcePreConsume = 1, "request-wallet", true
			info.TokenKey = strings.Repeat("u", 48)
			info.UserSetting.BillingPreference = "wallet_only"
			info.PriceData = types.PriceData{Currency: "USD", Quota: 10000, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}}
			attempts := 0
			outcome, taskErr := executeTaskSubmissionWith(c, info, func(c *gin.Context, info *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
				attempts++
				require.Nil(t, service.PreConsumeBilling(c, 10000, info))
				require.NoError(t, service.MarkCNYUpstreamPending(info, true))
				if state == "unknown" {
					return nil, service.TaskErrorWrapper(errors.New("transport lost after submit"), "do_request_failed", http.StatusBadGateway)
				}
				result := &relay.TaskSubmitResult{UpstreamTaskID: "upstream-id", Platform: constant.TaskPlatform("doubao"), Quota: 12000}
				if state == "success" {
					result.Immediate = &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}
					result.Quota = 6000
				}
				if state == "failure" {
					result.Immediate = &relaycommon.TaskInfo{Status: model.TaskStatusFailure, Reason: "vendor rejected"}
					result.Quota = 0
				}
				if state == "insert failure" {
					require.NoError(t, db.Migrator().DropTable(&model.Task{}))
				}
				if state == "cancel after acceptance" {
					cancel()
				}
				return result, nil
			})
			assert.Equal(t, 1, attempts)
			record, err := model.GetWalletFundingRecord("request-wallet")
			require.NoError(t, err)
			var user model.User
			require.NoError(t, db.First(&user, 1).Error)
			var token model.Token
			require.NoError(t, db.First(&token, 1).Error)
			switch state {
			case "async":
				require.Nil(t, taskErr)
				require.NotNil(t, outcome)
				assert.Equal(t, model.CNYBillingSubmitted, record.Status)
				assert.Equal(t, 10000, record.ReservedQuota)
				assert.Equal(t, 12000, outcome.Task.Quota, "submit estimate differs from actual reserved funds")
				assert.Equal(t, 90000, user.Quota)
				assert.Equal(t, 1, user.RequestCount)
				assert.Zero(t, user.UsedQuota)
			case "success":
				require.Nil(t, taskErr)
				assert.Equal(t, model.CNYBillingSettled, record.Status)
				assert.Equal(t, 94000, user.Quota)
				assert.Equal(t, 6000, user.UsedQuota)
				assert.Equal(t, 1, user.RequestCount)
			case "failure":
				require.Nil(t, taskErr)
				assert.Equal(t, model.CNYBillingRefunded, record.Status)
				assert.Equal(t, 100000, user.Quota)
				assert.Zero(t, user.UsedQuota)
				assert.Equal(t, 1, user.RequestCount)
			default:
				require.NotNil(t, taskErr)
				assert.Equal(t, model.CNYBillingReserved, record.Status)
				assert.True(t, record.UpstreamPending)
				assert.Equal(t, 90000, user.Quota)
				assert.Zero(t, user.UsedQuota)
				assert.Zero(t, user.RequestCount)
			}
			assert.Equal(t, user.Quota, token.RemainQuota)
			info.Billing.Refund(c)
			require.NoError(t, db.First(&token, 1).Error)
			assert.Equal(t, user.Quota, token.RemainQuota, "accepted work is never refunded by the outer request")
		})
	}
}

func TestMidjourneyWalletPollRequiresConfirmedFailure(t *testing.T) {
	for _, status := range []string{"PROCESSING", "SUCCESS", "FAILURE"} {
		t.Run(status, func(t *testing.T) {
			db, dialect := openTaskDialectDatabase(t, &model.Midjourney{}, &model.User{}, &model.Token{}, &model.Channel{}, &model.CNYWallet{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{}, &model.Log{})
			oldDB, oldLogDB := model.DB, model.LOG_DB
			oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
			oldRedis, oldMemory, oldBatch, oldLogs := common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(dialect, dialect)
			common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false, false, false
			t.Cleanup(func() {
				model.DB, model.LOG_DB = oldDB, oldLogDB
				common.SetDatabaseTypes(oldMain, oldLog)
				common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = oldRedis, oldMemory, oldBatch, oldLogs
			})
			service.InitHttpClient()
			submitted := time.Now().Add(-2 * time.Hour).UnixMilli()
			progress := "100%"
			if status == "PROCESSING" {
				progress = "50%"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := common.Marshal([]dto.MidjourneyDto{{MjId: "task", Status: status, Progress: progress, SubmitTime: submitted, FailReason: "provider status detail"}})
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				_, _ = w.Write(body)
			}))
			defer upstream.Close()
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "mj", Status: common.UserStatusEnabled}).Error)
			require.NoError(t, db.Create(&model.CNYWallet{UserID: 1, Quota: 10000, QuotaPerUnit: 500000}).Error)
			require.NoError(t, db.Create(&model.Channel{Id: 1, Name: "mj", Key: "test", BaseURL: &upstream.URL}).Error)
			policy := model.DefaultWalletExchangePolicy()
			policy.StaticRate, policy.CrossCurrencyEnabled = "7", true
			require.NoError(t, db.Create(&policy).Error)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/mj/submit", nil)
			info := &relaycommon.RelayInfo{RequestId: "poll-" + status, UserId: 1, IsPlayground: true, StartTime: time.Now(), ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1}}
			require.NoError(t, service.ReserveMidjourneyWallet(c, info, 1000))
			require.NoError(t, service.MarkCNYUpstreamPending(info, true))
			task := &model.Midjourney{UserId: 1, ChannelId: 1, MjId: "task", Status: "SUBMITTED", Progress: "0%", SubmitTime: submitted}
			prepared, err := service.PrepareMidjourneyTaskBilling(info, task, 1000, true)
			require.NoError(t, err)
			created, err := service.PersistMidjourneyTaskBilling(info, task, prepared)
			require.NoError(t, err)
			require.True(t, created)
			for range 2 {
				runMidjourneyTaskUpdateOnce(context.Background(), nil)
			}
			var persisted model.Midjourney
			require.NoError(t, db.First(&persisted, task.Id).Error)
			assert.Equal(t, status, persisted.Status, "elapsed local time cannot replace the provider's status")
			record, err := model.GetWalletFundingRecord(info.RequestId)
			require.NoError(t, err)
			var wallet model.CNYWallet
			require.NoError(t, db.Where("user_id = ?", 1).First(&wallet).Error)
			if status == "FAILURE" {
				assert.Equal(t, model.CNYBillingRefunded, record.Status)
				assert.Equal(t, 10000, wallet.Quota)
				assert.Zero(t, persisted.Quota)
			} else {
				assert.Equal(t, model.CNYBillingSettled, record.Status)
				assert.Equal(t, 3000, wallet.Quota)
				assert.Equal(t, 1000, persisted.Quota)
			}
		})
	}
}

func TestCNYBillingRatesAdmissionAndSettlement(t *testing.T) {
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })
	for _, tc := range []struct {
		name       string
		group      float64
		userRate   *string
		wantRate   float64
		wantSource string
	}{
		{"model replaces group", 2, nil, 0.8, "model"},
		{"user replaces model", 2, common.GetPointer("0.5"), 0.5, "user"},
		{"paid override of free group", 0, common.GetPointer("0.5"), 0.5, "user"},
		{"free override of paid group", 2, common.GetPointer("0"), 0, "user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, dialect := openTaskDialectDatabase(t, &model.User{}, &model.Token{}, &model.Channel{}, &model.CNYWallet{}, &model.CNYTokenBudget{}, &model.CNYBillingRecord{}, &model.WalletFundingRecord{}, &model.WalletExchangePolicy{}, &model.ExchangeRateObservation{})
			previousDB, previousLogs, previousEnabled := model.DB, model.LOG_DB, common.LogConsumeEnabled
			mainType, logType := common.MainDatabaseType(), common.LogDatabaseType()
			previousConfig := config.GlobalConfig.ExportAllConfigs()
			previousFX, previousCount := operation_setting.USDExchangeRate, constant.CountToken
			model.DB, model.LOG_DB, common.LogConsumeEnabled = db, nil, false
			common.SetDatabaseTypes(dialect, dialect)
			constant.CountToken = false
			t.Cleanup(func() {
				model.DB, model.LOG_DB, common.LogConsumeEnabled = previousDB, previousLogs, previousEnabled
				common.SetDatabaseTypes(mainType, logType)
				require.NoError(t, config.GlobalConfig.LoadFromDB(previousConfig))
				operation_setting.USDExchangeRate, constant.CountToken = previousFX, previousCount
			})
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"billing_setting.billing_mode":                `{"native-rate":"tiered_expr"}`,
				"billing_setting.billing_expr":                `{"native-rate":"v2:CNY:2000 + p * 2 + c * 8"}`,
				"group_ratio_setting.group_ratio":             fmt.Sprintf(`{"default":%g}`, tc.group),
				"quota_setting.pre_consume_multiplier":        "1",
				"quota_setting.enable_free_model_pre_consume": "false",
			}))
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "native-rates", Status: common.UserStatusEnabled, Quota: 123456}).Error)
			require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: strings.Repeat("d", 48), Status: common.TokenStatusEnabled, ExpiredTime: -1}).Error)
			require.NoError(t, db.Create(&model.Channel{Id: 1, Name: "native-rates", Key: "test"}).Error)
			_, err := model.AdjustCNYWallet(1, 99, common.RoleRootUser, 100000, "initial", "test")
			require.NoError(t, err)
			_, err = model.UpdateCNYTokenBudget(1, 1, model.CNYTokenBudgetChange{Enabled: true, RemainQuota: 100000})
			require.NoError(t, err)
			_, _, err = model.UpdateBillingRateRules(0, 99, []model.BillingRateChange{{ModelName: "native-rate", Rate: common.GetPointer("0.8")}})
			require.NoError(t, err)
			version := int64(0)
			if tc.userRate != nil {
				_, _, err = model.UpdateBillingRateRules(1, 99, []model.BillingRateChange{{ModelName: "native-rate", Rate: tc.userRate}})
				require.NoError(t, err)
				version = 1
			}
			c := taskSubmissionTestContext()
			c.Set("token_usd_exhausted", true)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
			info := &relaycommon.RelayInfo{
				UserId: 1, TokenId: 1, RequestId: "native-rates", OriginModelName: "native-rate", UserGroup: "default", UsingGroup: "default",
				StartTime: time.Now(), RelayMode: relayconstant.RelayModeChatCompletions,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1}, BillingRequestInput: &billingexpr.RequestInput{},
			}
			// This entry point resolves DB rules through ModelPriceHelper before
			// reserving the native wallet, including explicitly free overrides.
			require.Nil(t, relay.PrepareRequestBilling(c, info))
			require.NotNil(t, info.Billing)
			rate := info.PriceData.BillingRate
			require.NotNil(t, rate)
			assert.Equal(t, tc.wantSource, rate.Source)
			assert.Equal(t, tc.wantRate, rate.Rate)
			assert.Equal(t, billingexpr.CurrencyCNY, info.PriceData.Currency)
			record, err := model.GetCNYBillingRecord(info.RequestId)
			require.NoError(t, err)
			assert.Equal(t, common.QuotaRound(1000*tc.wantRate), record.ReservedQuota)
			_, _, err = model.UpdateBillingRateRules(1, 99, []model.BillingRateChange{{ModelName: "native-rate", Rate: common.GetPointer("0.9"), ExpectedVersion: version}})
			require.NoError(t, err)
			operation_setting.USDExchangeRate = 100
			info.PriceData.SetGroupRatioInfo(types.GroupRatioInfo{GroupRatio: 3})
			require.Nil(t, service.PrepareBillingForSelectedGroup(c, info))
			assert.Equal(t, rate, info.TieredBillingSnapshot.BillingRate)
			service.PostTextConsumeQuota(c, info, &relaydto.Usage{PromptTokens: 2000, CompletionTokens: 100, TotalTokens: 2100}, nil)
			record, err = model.GetCNYBillingRecord(info.RequestId)
			require.NoError(t, err)
			wantQuota := common.QuotaRound(3400 * tc.wantRate)
			assert.Equal(t, model.CNYBillingSettled, record.Status)
			assert.Equal(t, billingexpr.CurrencyCNY, record.Currency)
			assert.Equal(t, wantQuota, record.Quota)
			var storedSnapshot billingexpr.BillingSnapshot
			require.NoError(t, common.UnmarshalJsonStr(record.Snapshot, &storedSnapshot))
			assert.Equal(t, rate, storedSnapshot.BillingRate)
			assert.Equal(t, billingexpr.CurrencyCNY, storedSnapshot.BillingCurrency())
			var other struct {
				BillingRate *types.BillingRateSnapshot `json:"billing_rate"`
			}
			require.NoError(t, common.UnmarshalJsonStr(record.Other, &other))
			assert.Equal(t, rate, other.BillingRate)
			wallet, err := model.GetCNYWallet(1)
			require.NoError(t, err)
			assert.Equal(t, 100000-wantQuota, wallet.Quota)
			assert.Equal(t, wantQuota, wallet.UsedQuota)
			assert.Zero(t, wallet.ReservedQuota)
			budget, err := model.GetCNYTokenBudget(1, 1)
			require.NoError(t, err)
			assert.Equal(t, 100000-wantQuota, budget.RemainQuota)
			var user model.User
			var token model.Token
			require.NoError(t, db.First(&user, 1).Error)
			require.NoError(t, db.First(&token, 1).Error)
			assert.Equal(t, 123456, user.Quota)
			assert.Zero(t, user.UsedQuota)
			assert.Zero(t, token.UsedQuota)
		})
	}
}
