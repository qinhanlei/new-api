package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This exercises durable host submission with a real HTTP peer and the engine
// selected by the existing TEST_TASK_DB_DIALECT fixture, never a paid provider.
func TestAssetTaskSubmissionDatabase(t *testing.T) {
	db, dialect := openTaskDialectDatabase(t, &model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Task{}, &model.Log{}, &model.Asset{}, &model.AssetSpace{}, &model.AssetReplica{}, &model.TaskSubmission{}, &model.TaskAssetUse{}, &model.TaskReservation{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{})
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	oldEnabled, oldRedis, oldMemory, oldBatch, oldConsume := common.AssetLibraryEnabled, common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(dialect, dialect)
	common.AssetLibraryEnabled, common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = true, false, false, false, true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldMain, oldLog)
		common.AssetLibraryEnabled, common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = oldEnabled, oldRedis, oldMemory, oldBatch, oldConsume
	})
	for key, value := range map[string]string{"ASSET_STORAGE": "s3", "ASSET_S3_ENDPOINT": "https://objects.example.com", "ASSET_S3_BUCKET": "test", "ASSET_S3_ACCESS_KEY_ID": "test", "ASSET_S3_SECRET_ACCESS_KEY": "test"} {
		t.Setenv(key, value)
	}
	withTieredBillingConfig(t, map[string]string{"asset-model": "tiered_expr"}, map[string]string{"asset-model": `u("units") * 0.01`})
	const source = `
export const meta={apiVersion:1,key:"doubao",name:"Asset test",version:"1.0.0",author:{name:"Test"},models:["asset-model"],fetchMode:"per_task",usageSchema:{units:{type:"number",unit:"count",description:{en:"Task unit price",zh:"任务单价"}}}};
export function buildSubmitRequest(ctx){let id=ctx.requestBody.asset;let ref=ctx.resolvedAssets[id];if(!ref||ref.type!=="image")throw new Error("missing resolved image");return {url:ctx.baseUrl+"/jobs",body:{image:ref.reference,model:ctx.model}};}
export function parseSubmitResponse(ctx,resp){return {taskId:resp.body.id,taskData:resp.body,immediate:resp.body.units?{status:"SUCCESS"}:undefined};}
export function extractUsage(){return {units:1};}
export function extractUsageOnSubmit(ctx,body){return {units:body.units||1};}
export function extractUsageOnComplete(ctx,result,body){return {units:body.units||1};}
export function buildQueryRequest(){return {};}
export function parseTaskResult(){return {};}
`
	previousRegistry := jsplugin.DefaultRegistry
	jsplugin.DefaultRegistry = jsplugin.NewRegistry()
	t.Cleanup(func() { jsplugin.DefaultRegistry = previousRegistry })
	plugin, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(plugin.Meta.Key) })
	for index, tc := range []struct {
		name      string
		code      int
		status    model.TaskStatus
		charged   bool
		extra     bool
		lostLease bool
	}{
		{"accepted", 200, model.TaskStatusSubmitted, true, false, false},
		{"known rejection", 400, model.TaskStatusFailure, false, false, false},
		{"uncertain acceptance", 500, model.TaskStatusSubmitUnknown, true, false, false},
		{"billing recovery", 200, model.TaskStatusSubmitting, true, true, false},
		{"expired owner", 200, model.TaskStatusSubmitUnknown, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			var submittedID string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request map[string]any
				if err := common.DecodeJson(r.Body, &request); err != nil {
					t.Error(err)
				}
				assert.Equal(t, "asset://provider-specific", request["image"])
				if tc.lostLease {
					assert.NoError(t, db.Model(&model.TaskSubmission{}).Where("task_id = ?", submittedID).Updates(map[string]any{"phase": model.SubmissionUnknown, "lease_owner": "", "lease_until": 0}).Error)
					assert.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", submittedID).Update("status", model.TaskStatusSubmitUnknown).Error)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				if tc.extra {
					_, _ = w.Write([]byte(`{"id":"provider-job","units":2}`))
				} else {
					_, _ = w.Write([]byte(`{"id":"provider-job"}`))
				}
			}))
			defer server.Close()
			initial := int(common.QuotaPerUnit)
			if tc.extra {
				initial = common.QuotaRound(common.QuotaPerUnit * 0.01)
			}
			user := model.User{Username: fmt.Sprintf("asset-user-%d", index), AffCode: fmt.Sprintf("asset-aff-%d", index), Status: common.UserStatusEnabled, Group: "default", Quota: initial, Setting: `{"billing_preference":"wallet_only"}`}
			require.NoError(t, db.Create(&user).Error)
			token := model.Token{UserId: user.Id, Key: common.GetRandomString(32), Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: initial, Group: "default"}
			require.NoError(t, db.Create(&token).Error)
			settings := `{"task_plugin_key":"doubao","asset_endpoint":"https://provider.example.com"}`
			channel := model.Channel{Name: "test", Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, BaseURL: &server.URL, Key: "test", Models: "asset-model", Group: "default", Setting: &settings}
			require.NoError(t, db.Create(&channel).Error)
			space := model.AssetSpace{ID: fmt.Sprintf("aspace_test_%d", index), ChannelID: channel.Id, BaseURL: "https://provider.example.com", ProjectName: "default", GroupID: "group", GroupState: model.AssetGroupReady}
			require.NoError(t, db.Create(&space).Error)
			require.NoError(t, db.Create(&model.Ability{ChannelId: channel.Id, Group: "default", Model: "asset-model", Enabled: true}).Error)
			asset := model.Asset{ID: fmt.Sprintf("na_asset_fixture_%d", index), UserID: user.Id, Kind: "image", Status: model.AssetStatusReady, ObjectKey: "private-object"}
			require.NoError(t, db.Create(&asset).Error)
			c := taskSubmissionTestContext()
			c.Request = httptest.NewRequest("POST", "/v1/videos", strings.NewReader(`{}`))
			c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{Plugin: plugin})
			c.Set(jsplugin.ContextKeyProtocolRequest, jsplugin.ProtocolRequestContext{Protocol: "openai_responses", RouteRequestContext: jsplugin.RouteRequestContext{Body: map[string]any{"kind": "json", "value": map[string]any{"background": true}}}})
			c.Set("task_request", map[string]any{"model": "asset-model", "asset": asset.ID})
			require.NoError(t, service.SetTaskAssetIntent(c, []any{asset.ID}))
			info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, OriginModelName: "asset-model", UsingGroup: "default", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, UpstreamModelName: "asset-model"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{Action: "image_to_video"}}
			taskErr := service.PrepareTaskAssets(c, info)
			require.NotNil(t, taskErr)
			require.Equal(t, "asset_task_queued", taskErr.Code)
			var submission model.TaskSubmission
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&submission).Error)
			submittedID = submission.TaskID
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, initial, user.Quota, "asset preparation must not charge")
			require.NoError(t, db.Create(&model.AssetReplica{AssetID: asset.ID, SpaceID: space.ID, Status: model.AssetReplicaReady, RemoteID: "private-management-id", GenerationRef: "asset://provider-specific"}).Error)
			// Deletion forbids new consumers but must not invalidate an accepted job.
			require.NoError(t, model.DeleteUserAsset(t.Context(), user.Id, asset.ID))
			used, err := model.AssetHasConsumers(t.Context(), asset.ID)
			require.NoError(t, err)
			assert.True(t, used)
			if index == 0 {
				for i := range 5 {
					blockedAsset := model.Asset{ID: fmt.Sprintf("na_asset_wait_%d", i), UserID: user.Id, Kind: "image", Status: model.AssetStatusReady}
					require.NoError(t, db.Create(&blockedAsset).Error)
					require.NoError(t, db.Create(&model.AssetReplica{AssetID: blockedAsset.ID, SpaceID: space.ID, Status: model.AssetReplicaProcessing, LeaseUntil: time.Now().Unix() + 3600}).Error)
					blocked := submission
					blocked.TaskID = model.GenerateTaskID()
					blocked.AssetIDs = []string{blockedAsset.ID}
					blocked.CreatedAt = time.Now().Unix() - 60 + int64(i)
					blocked.RequestBody = model.LongText(fmt.Sprintf(`{"model":"asset-model","asset":%q}`, blockedAsset.ID))
					require.NoError(t, model.CreateTaskSubmission(t.Context(), &model.Task{TaskID: blocked.TaskID, UserId: user.Id, ChannelId: channel.Id, Status: model.TaskStatusWaitingAssets}, &blocked))
				}
				require.NoError(t, runAssetTasks(t.Context(), "scheduler"))
				assert.Zero(t, calls.Load(), "first pass may use its batch on older imports")
				require.NoError(t, runAssetTasks(t.Context(), "scheduler"))
			} else {
				claimed, ok, err := model.ClaimTaskSubmission(t.Context(), submission.TaskID, "test-worker", time.Now().Unix())
				require.NoError(t, err)
				require.True(t, ok)
				_, ok, err = model.ClaimTaskSubmission(t.Context(), submission.TaskID, "competing-worker", time.Now().Unix())
				require.NoError(t, err)
				assert.False(t, ok)
				processErr := processAssetTask(t.Context(), claimed)
				if tc.extra {
					require.ErrorIs(t, processErr, model.ErrTaskReservationQuota)
				} else if tc.lostLease {
					require.ErrorContains(t, processErr, "submission lease lost")
					require.Error(t, failAssetSubmission(t.Context(), claimed, "late failure"))
					require.Error(t, markAssetSubmissionUnknown(t.Context(), claimed))
				} else {
					require.NoError(t, processErr)
				}
			}
			task, exists, err := model.GetByTaskId(user.Id, submission.TaskID)
			require.NoError(t, err)
			require.True(t, exists)
			assert.Equal(t, tc.status, task.Status, task.FailReason)
			assert.True(t, task.PrivateData.ResponsesBackground, "worker must preserve the durable client protocol flags")
			assert.EqualValues(t, 1, calls.Load())
			require.NoError(t, db.First(&user, user.Id).Error)
			if tc.charged {
				assert.Less(t, user.Quota, initial)
			} else {
				assert.Equal(t, initial, user.Quota)
			}
			recovered, ok, err := model.ClaimTaskSubmission(t.Context(), submission.TaskID, "restarted-worker", time.Now().Unix()+300)
			require.NoError(t, err)
			if tc.extra {
				require.True(t, ok)
				assert.Equal(t, model.SubmissionAccepted, recovered.Phase)
				require.NoError(t, db.Model(&user).Update("quota", initial).Error)
				require.NoError(t, db.Model(&token).Update("remain_quota", initial).Error)
				require.NoError(t, processAssetTask(t.Context(), recovered))
				require.NoError(t, db.First(task, task.ID).Error)
				assert.EqualValues(t, model.TaskStatusSuccess, task.Status)
				assert.Equal(t, initial*2, task.Quota)
				assert.EqualValues(t, 1, calls.Load(), "billing recovery must never resend an accepted request")
			} else {
				assert.False(t, ok)
			}
			if tc.code == 200 && !tc.lostLease {
				assert.Equal(t, "provider-job", task.PrivateData.UpstreamTaskID)
				var logs []model.Log
				require.NoError(t, db.Where("user_id = ? AND type = ?", user.Id, model.LogTypeConsume).Find(&logs).Error)
				require.Len(t, logs, 1, "first submission and recovered settlement must each produce one consumption record")
				assert.Equal(t, task.Quota, logs[0].Quota)
				assert.Contains(t, logs[0].Other, task.TaskID)
				assert.Equal(t, "asset-model", logs[0].ModelName)
				_, claimedAgain, err := model.ClaimTaskSubmission(t.Context(), submission.TaskID, "after-settlement", time.Now().Unix()+600)
				require.NoError(t, err)
				assert.False(t, claimedAgain)
			}
			if tc.code == 500 {
				assert.Empty(t, task.PrivateData.UpstreamTaskID)
				require.NotNil(t, task.PrivateData.BillingContext)
				recorder := httptest.NewRecorder()
				admin, _ := gin.CreateTestContext(recorder)
				admin.Set("id", 99)
				admin.Params = gin.Params{{Key: "task_id", Value: task.TaskID}}
				admin.Request = httptest.NewRequest("POST", "/api/asset-reconciliation/tasks/"+task.TaskID, strings.NewReader(`{"confirmed_absent":true,"note":"Confirmed absent in provider console"}`))
				admin.Request.Header.Set("Content-Type", "application/json")
				ReconcileAssetTask(admin)
				require.Equal(t, 200, recorder.Code, recorder.Body.String())
				resolved, ok, err := model.ClaimTaskSubmission(t.Context(), task.TaskID, "reconcile-worker", time.Now().Unix())
				require.NoError(t, err)
				require.True(t, ok)
				require.NoError(t, processAssetTask(t.Context(), resolved))
				require.NoError(t, db.First(&user, user.Id).Error)
				assert.Equal(t, initial, user.Quota)
				assert.EqualValues(t, 1, calls.Load())
			}
		})
	}
}

func TestAssetArkRequestAndReceipt(t *testing.T) {
	engine := gin.New()
	engine.POST("/api/v3/contents/generations/tasks", middleware.PrepareArkVideo(), func(c *gin.Context) {
		defer common.CleanupBodyStorage(c)
		var body map[string]any
		require.NoError(t, common.UnmarshalBodyReusable(c, &body))
		metadata := body["metadata"].(map[string]any)
		assert.Equal(t, false, metadata["watermark"])
		assert.Equal(t, float64(0), metadata["seed"])
		assert.Contains(t, fmt.Sprint(metadata["content"]), "asset://na_asset_fixture")
		assert.NotContains(t, metadata, "model")
		key := "doubao"
		source, err := plugins.Source(key)
		require.NoError(t, err)
		plugin, err := jsplugin.CompilePlugin(source, jsplugin.Options{})
		require.NoError(t, err)
		intentValue, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": "doubao-seedance-2-5-260628", "body": map[string]any{"kind": "json", "value": body}})
		require.NoError(t, err, key)
		intent := intentValue.(map[string]any)
		assert.Equal(t, []any{"na_asset_fixture"}, intent["assetRefs"], key)
		descriptor, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"model": "doubao-seedance-2-5-260628", "upstreamModel": "doubao-seedance-2-5-260628", "baseUrl": "https://provider.example.com", "apiKey": "test", "requestBody": intent["requestBody"], "managedAssets": true, "resolvedAssets": map[string]any{"na_asset_fixture": map[string]any{"type": "image", "reference": "asset://remote-image"}}})
		require.NoError(t, err, key)
		encoded, err := common.Marshal(descriptor)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), "asset://remote-image", key)
		assert.NotContains(t, string(encoded), "asset://na_asset_", key)
		c.JSON(200, arkTaskView(&model.Task{TaskID: "task_public", Status: model.TaskStatusWaitingAssets}))
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"asset-model","content":[{"type":"text","text":"dance"},{"type":"image_url","image_url":{"url":"asset://na_asset_fixture"},"role":"reference_image"}],"duration":4,"watermark":false,"seed":0}`))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	assert.Equal(t, 200, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"phase":"waiting_assets"`)
}
