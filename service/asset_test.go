package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func assetTestDatabase(t *testing.T) {
	t.Helper()
	dialect := os.Getenv("TEST_ASSET_DIALECT")
	var driver gorm.Dialector
	dbType := common.DatabaseTypeSQLite
	switch dialect {
	case "", "sqlite":
		driver = sqlite.Open(t.TempDir() + "/assets.db")
	case "mysql":
		require.NotEmpty(t, os.Getenv("TEST_MYSQL_DSN"))
		driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
		dbType = common.DatabaseTypeMySQL
	case "postgres":
		require.NotEmpty(t, os.Getenv("TEST_POSTGRES_DSN"))
		driver = postgres.Open(os.Getenv("TEST_POSTGRES_DSN"))
		dbType = common.DatabaseTypePostgreSQL
	default:
		t.Fatalf("unsupported asset test dialect %q", dialect)
	}
	database, err := gorm.Open(driver, &gorm.Config{Logger: logger.Discard, NamingStrategy: schema.NamingStrategy{TablePrefix: "at_" + common.GetUUID()[:8] + "_"}})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	previousDB, previousType := model.DB, common.MainDatabaseType()
	model.DB = database
	common.SetMainDatabaseType(dbType)
	tables := append(model.AssetModels(), &model.Option{}, &model.Channel{}, &model.Task{}, &model.Ability{}, &model.User{}, &model.Token{})
	for range 2 {
		require.NoError(t, database.AutoMigrate(tables...))
	}
	t.Cleanup(func() {
		require.NoError(t, database.Migrator().DropTable(tables...))
		require.NoError(t, sqlDB.Close())
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
	config := system_setting.AssetStorageConfig{AssetStorageFields: dto.AssetStorageFields{Enabled: true, Endpoint: "https://storage.example", Region: "cn-beijing", Bucket: "assets", AccessKey: "storage-access", ForcePathStyle: true}, SecretKey: "storage-secret"}
	require.NoError(t, model.SaveAssetStorageConfig(t.Context(), config))
	for _, id := range []int{1, 2} {
		channel := model.Channel{Id: id, Name: fmt.Sprintf("channel-%d", id), Key: "channel-key", Type: 54, Status: common.ChannelStatusEnabled}
		require.NoError(t, database.Create(&channel).Error)
		config := system_setting.AssetChannelConfig{AssetChannelFields: dto.AssetChannelFields{Enabled: true, Endpoint: "https://upstream.example", ProjectName: "actual-project", AuthType: "bearer"}, ChannelScope: model.AssetChannelScope(&channel)}
		require.NoError(t, model.SaveAssetChannelConfig(t.Context(), id, config))
	}
	previousHTTP, previousProtected := httpClient, ssrfProtectedHTTPClient
	httpClient = &http.Client{Timeout: time.Second}
	ssrfProtectedHTTPClient = httpClient
	t.Cleanup(func() { httpClient = previousHTTP; ssrfProtectedHTTPClient = previousProtected })
}

func assetTestGroup(t *testing.T, userID int, name string) model.AssetGroup {
	t.Helper()
	group := model.AssetGroup{PublicID: model.NewAssetGroupID(), UserID: userID, ProjectName: "default", Name: name, GroupType: "AIGC"}
	require.NoError(t, model.CreateAssetGroup(t.Context(), &group))
	return group
}

func assetTestAsset(t *testing.T, group model.AssetGroup) model.Asset {
	t.Helper()
	asset := model.Asset{PublicID: model.NewAssetID(), UserID: group.UserID, ProjectName: group.ProjectName, GroupID: group.ID, GroupPublicID: group.PublicID, Name: "test", AssetType: "Image", Status: model.AssetProcessing, ObjectKey: "new-api-assets/test-" + common.GetUUID(), ObjectState: model.AssetReady}
	require.NoError(t, model.ReserveAsset(t.Context(), &asset, 1))
	return asset
}

func TestAssetStorageEndpointConfiguration(t *testing.T) {
	assetTestDatabase(t)
	for _, tc := range []struct {
		name     string
		endpoint string
		want     string
	}{
		{"cloud without scheme", " tos-s3-cn-beijing.volces.com/ ", "https://tos-s3-cn-beijing.volces.com"},
		{"explicit HTTP", "http://127.0.0.1:9000/", "http://127.0.0.1:9000"},
		{"private host without scheme", "minio.internal:9000", "https://minio.internal:9000"},
		{"HTTPS", "https://s3.example.com/", "https://s3.example.com"},
		{"unsupported scheme", "ftp://s3.example.com", ""},
		{"credentials in URL", "https://user:secret@s3.example.com", ""},
		{"bucket in path", "https://s3.example.com/assets", ""},
		{"query", "https://s3.example.com?region=cn-beijing", ""},
		{"empty query", "https://s3.example.com?", ""},
		{"fragment", "https://s3.example.com#bucket", ""},
		{"missing host", "http://", ""},
		{"incomplete HTTP scheme", "http:", ""},
		{"incomplete HTTPS scheme", "https:", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := model.LoadAssetStorageConfig(t.Context())
			require.NoError(t, err)
			input := dto.AssetStorageUpdate{AssetStorageFields: before.AssetStorageFields}
			input.Endpoint = tc.endpoint
			input.Provider = "minio"
			view, err := UpdateAssetStorageConfig(t.Context(), input)
			if tc.want == "" {
				var failure *AssetError
				require.ErrorAs(t, err, &failure)
				assert.Equal(t, "InvalidParameter.Endpoint", failure.Code)
				stored, loadErr := model.LoadAssetStorageConfig(t.Context())
				require.NoError(t, loadErr)
				assert.Equal(t, before, stored)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, view.Endpoint)
			assert.Equal(t, before.Revision+1, view.Revision)
			stored, err := model.LoadAssetStorageConfig(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tc.want, stored.Endpoint)
			assert.Equal(t, "minio", stored.Provider)
			assert.Equal(t, before.SecretKey, stored.SecretKey)
			assert.Equal(t, before.CursorSecret, stored.CursorSecret)
		})
	}
}

func TestAssetActionsPaginationAndOwnership(t *testing.T) {
	assetTestDatabase(t)
	actor := dto.AssetActor{UserID: 1, ChannelIDs: []int{1, 2}}
	for _, name := range []string{"first", "second", "third"} {
		assetTestGroup(t, 1, name)
	}
	other := assetTestGroup(t, 2, "private")
	result, err := ExecuteAssetAction(t.Context(), actor, "ListAssetGroups", []byte(`{"Filter":{"GroupType":"AIGC"},"MaxResults":2}`))
	require.NoError(t, err)
	page := result.(map[string]any)
	assert.Len(t, page["Items"], 2)
	assert.NotContains(t, page, "TotalCount")
	next, ok := page["NextToken"].(string)
	require.True(t, ok)
	request, _ := common.Marshal(map[string]any{"Filter": map[string]any{"GroupType": "AIGC"}, "MaxResults": 2, "NextToken": next})
	result, err = ExecuteAssetAction(t.Context(), actor, "ListAssetGroups", request)
	require.NoError(t, err)
	assert.Len(t, result.(map[string]any)["Items"], 1)
	assert.NotContains(t, result, "NextToken")
	_, err = ExecuteAssetAction(t.Context(), dto.AssetActor{UserID: 2}, "ListAssetGroups", request)
	var failure *AssetError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, "InvalidParameter.NextToken", failure.Code)
	request, _ = common.Marshal(map[string]any{"Id": other.PublicID})
	_, err = ExecuteAssetAction(t.Context(), actor, "GetAssetGroup", request)
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, http.StatusNotFound, failure.HTTPStatus)
	for _, body := range []string{`{"Filter":{"GroupType":"AIGC"},"PageNumber":1}`, `{"Filter":{"GroupType":"AIGC"},"PageNumber":1,"PageSize":10,"NextToken":""}`, `{"Filter":{"GroupType":"LivenessFace"}}`, `{"Filter":{"GroupType":"AIGC"},"NextToken":""}`} {
		_, err = ExecuteAssetAction(t.Context(), actor, "ListAssetGroups", []byte(body))
		require.ErrorAs(t, err, &failure)
		assert.Equal(t, http.StatusBadRequest, failure.HTTPStatus)
	}
	result, err = ExecuteAssetAction(t.Context(), actor, "ListAssetGroups", []byte(`{"Filter":{"GroupType":"AIGC"},"PageNumber":1,"PageSize":10}`))
	require.NoError(t, err)
	assert.Equal(t, int64(3), result.(map[string]any)["TotalCount"])
	admin := dto.AssetActor{UserID: 1, AllUsers: true}
	result, err = ExecuteAssetAction(t.Context(), admin, "ListAssetGroups", []byte(`{"Filter":{"GroupType":"AIGC"},"PageNumber":1,"PageSize":10}`))
	require.NoError(t, err)
	assert.Equal(t, int64(4), result.(map[string]any)["TotalCount"])
	_, err = ExecuteAssetAction(t.Context(), admin, "DeleteAssetGroup", request)
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, http.StatusForbidden, failure.HTTPStatus)
	created, err := ExecuteAssetAction(t.Context(), actor, "CreateAssetGroup", []byte(`{"Name":"default group type"}`))
	require.NoError(t, err)
	defaultGroup, err := model.GetAssetGroup(t.Context(), actor.UserID, created.(map[string]string)["Id"], "default")
	require.NoError(t, err)
	assert.Equal(t, "AIGC", defaultGroup.GroupType)
}

func TestAssetReadinessLeasesAndDeletionHolds(t *testing.T) {
	assetTestDatabase(t)
	ctx := t.Context()
	group := assetTestGroup(t, 1, "group")
	asset := assetTestAsset(t, group)
	actor := dto.AssetActor{UserID: 1, ChannelIDs: []int{1, 2}}
	_, err := PrepareAssetRouting(ctx, actor, []string{asset.PublicID}, 1)
	var failure *AssetError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, "AssetNotReady", failure.Code)
	var outcomes [2][]model.AssetReplica
	var claimErrors [2]error
	start := make(chan struct{})
	var contenders sync.WaitGroup
	for i := range 2 {
		contenders.Go(func() { <-start; outcomes[i], claimErrors[i] = model.ClaimAssetReplicas(ctx, time.Now().Unix(), 2) })
	}
	close(start)
	contenders.Wait()
	for _, claimErr := range claimErrors {
		require.NoError(t, claimErr)
	}
	leases := append(outcomes[0], outcomes[1]...)
	require.Len(t, leases, 1)
	second, err := model.ClaimAssetReplicas(ctx, time.Now().Unix(), 2)
	require.NoError(t, err)
	assert.Empty(t, second)
	replica := leases[0]
	stale := replica
	stale.LeaseToken = "stale"
	stale.Status = model.AssetActive
	stale.UpstreamID = "wrong"
	stale.State = model.AssetReady
	assert.ErrorIs(t, model.FinishAssetReplica(ctx, &stale, 0), model.ErrAssetConflict)
	before, err := model.GetAsset(ctx, 1, asset.PublicID, "default")
	require.NoError(t, err)
	assert.Equal(t, model.AssetProcessing, before.Status)
	replica.Status = model.AssetActive
	replica.State = model.AssetReady
	replica.UpstreamID = "upstream-one"
	replica.ModerationStrategy = "Default"
	require.NoError(t, model.FinishAssetReplica(ctx, &replica, 0))
	ready, err := PrepareAssetRouting(ctx, actor, []string{asset.PublicID}, 2)
	require.NoError(t, err)
	assert.Equal(t, []int{1}, ready)
	stored, err := model.AssetReplicas(ctx, []int64{asset.ID})
	require.NoError(t, err)
	require.Len(t, stored, 2)
	// The second channel is queued once, even when generation is retried.
	_, err = PrepareAssetRouting(ctx, actor, []string{asset.PublicID}, 2)
	require.NoError(t, err)
	stored, err = model.AssetReplicas(ctx, []int64{asset.ID})
	require.NoError(t, err)
	assert.Len(t, stored, 2)
	otherActor := dto.AssetActor{UserID: 2, ChannelIDs: actor.ChannelIDs}
	_, err = PrepareAssetRouting(ctx, otherActor, []string{asset.PublicID}, 1)
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, http.StatusNotFound, failure.HTTPStatus)
	refs, err := ResolveAssetReferences(ctx, actor, 1, []string{asset.PublicID}, "task-public")
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "upstream-one", refs[0].UpstreamID)
	require.NoError(t, model.CommitAssetUses(ctx, "task-public"))
	require.NoError(t, model.DeleteAsset(ctx, 1, asset.PublicID, "default"))
	cleanup, err := model.ClaimAssetWork(ctx, time.Now().Unix(), 10)
	require.NoError(t, err)
	assert.Empty(t, cleanup)
	_, err = ResolveAssetReferences(ctx, actor, 1, []string{asset.PublicID}, "task-new")
	require.Error(t, err)
	require.NoError(t, model.ReleaseAssetUses(ctx, "task-public"))
	cleanup, err = model.ClaimAssetWork(ctx, time.Now().Unix(), 10)
	require.NoError(t, err)
	require.Len(t, cleanup, 1)
	assert.ErrorIs(t, model.GuardAssetChannelMutation(model.DB, nil, []int{1}), model.ErrAssetInUse)
	config, err := model.LoadAssetStorageConfig(ctx)
	require.NoError(t, err)
	config.Bucket = "different"
	assert.ErrorIs(t, model.SaveAssetStorageConfig(ctx, config), model.ErrAssetInUse)
	config.Bucket = "assets"
	config.Enabled = false
	require.NoError(t, model.SaveAssetStorageConfig(ctx, config))
	_, err = PrepareAssetRouting(ctx, actor, []string{asset.PublicID}, 1)
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, "AssetStorageDisabled", failure.Code)
}

func TestAssetWorkRejectsStaleCandidates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deleted bool
		finish  func(*testing.T, *model.Asset, int64)
		reclaim bool
	}{
		{
			name: "source completed",
			finish: func(t *testing.T, asset *model.Asset, _ int64) {
				asset.ObjectState = model.AssetReady
				asset.ContentType = "image/png"
				asset.Size = 128
				asset.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("completed source")))
				require.NoError(t, model.FinishAssetSource(t.Context(), asset, "", "", 0))
			},
		},
		{
			name: "source failed",
			finish: func(t *testing.T, asset *model.Asset, _ int64) {
				require.NoError(t, model.FinishAssetSource(t.Context(), asset, "InvalidSource", "Source is invalid", 0))
			},
		},
		{
			name: "source retry postponed",
			finish: func(t *testing.T, asset *model.Asset, now int64) {
				require.NoError(t, model.FinishAssetSource(t.Context(), asset, "SourceUnavailable", "Source is unavailable", now+60))
			},
			reclaim: true,
		},
		{
			name:    "cleanup completed",
			deleted: true,
			finish: func(t *testing.T, asset *model.Asset, _ int64) {
				require.NoError(t, model.FinishAssetCleanup(t.Context(), asset, true))
			},
		},
		{
			name:    "cleanup retry postponed",
			deleted: true,
			finish: func(t *testing.T, asset *model.Asset, _ int64) {
				require.NoError(t, model.FinishAssetCleanup(t.Context(), asset, false))
			},
			reclaim: true,
		},
		{name: "lease replaced", reclaim: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assetTestDatabase(t)
			ctx := t.Context()
			group := assetTestGroup(t, 1, "group")
			asset := assetTestAsset(t, group)
			require.NoError(t, model.DB.Model(&model.Asset{}).Where("id = ?", asset.ID).Updates(map[string]any{
				"object_state": model.AssetPending,
				"source_url":   "https://source.example/image.png",
			}).Error)
			if tc.deleted {
				require.NoError(t, model.DeleteAsset(ctx, asset.UserID, asset.PublicID, asset.ProjectName))
			}
			now := time.Now().Unix()
			var expected model.Asset
			interleaved := false
			const callback = "test:finish-before-asset-claim"
			// Let another worker claim and finish after the old candidate was read,
			// without relying on goroutine scheduling or sleeps.
			require.NoError(t, model.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				rows, ok := tx.Statement.Dest.(*[]model.Asset)
				if interleaved || !ok || len(*rows) != 1 || (*rows)[0].ID != asset.ID {
					return
				}
				interleaved = true
				claimed, err := model.ClaimAssetWork(ctx, now, 1)
				require.NoError(t, err)
				require.Len(t, claimed, 1)
				if tc.finish != nil {
					tc.finish(t, &claimed[0], now)
				}
				require.NoError(t, model.DB.First(&expected, asset.ID).Error)
			}))
			t.Cleanup(func() { require.NoError(t, model.DB.Callback().Query().Remove(callback)) })

			claimed, err := model.ClaimAssetWork(ctx, now, 1)
			require.NoError(t, err)
			require.True(t, interleaved)
			assert.Empty(t, claimed, "an old candidate must not be claimed after another worker changes it")
			var current model.Asset
			require.NoError(t, model.DB.First(&current, asset.ID).Error)
			assert.Equal(t, expected, current, "the stale claim must preserve the other worker's result and lease")
			if tc.reclaim {
				claimed, err = model.ClaimAssetWork(ctx, max(current.NextAttemptAt, current.LeaseExpiresAt), 1)
				require.NoError(t, err)
				require.Len(t, claimed, 1, "a fresh candidate must remain claimable when due")
				assert.Equal(t, current.ID, claimed[0].ID)
				assert.Equal(t, current.Attempts, claimed[0].Attempts)
				assert.NotEmpty(t, claimed[0].LeaseToken)
				assert.NotEqual(t, current.LeaseToken, claimed[0].LeaseToken)
			}
		})
	}
}

func TestAssetWorkReclaimsExpiredUpload(t *testing.T) {
	assetTestDatabase(t)
	ctx := t.Context()
	group := assetTestGroup(t, 1, "group")
	asset := assetTestAsset(t, group)
	now := time.Now().Unix()
	require.NoError(t, model.DB.Model(&model.Asset{}).Where("id = ?", asset.ID).Updates(map[string]any{
		"object_state":     "uploading",
		"lease_token":      "interrupted-upload",
		"lease_expires_at": now,
	}).Error)
	require.NoError(t, model.DB.First(&asset, asset.ID).Error)
	claimed, err := model.ClaimAssetWork(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, asset.ID, claimed[0].ID)
	assert.NotEmpty(t, claimed[0].LeaseToken)
	assert.NotEqual(t, asset.LeaseToken, claimed[0].LeaseToken)
	assert.Greater(t, claimed[0].LeaseExpiresAt, now)
	asset.ObjectState = model.AssetReady
	assert.ErrorIs(t, model.FinishAssetSource(ctx, &asset, "", "", 0), model.ErrAssetConflict)
}

func TestAssetReplicaWorkRejectsStaleCandidates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      string
		status     string
		upstreamID string
		retryDelay int64
		keepLease  bool
	}{
		{name: "created pending moderation", state: model.AssetReady, status: model.AssetProcessing, upstreamID: "remote-asset", retryDelay: 60},
		{name: "moderation completed", state: model.AssetReady, status: model.AssetActive, upstreamID: "remote-asset"},
		{name: "creation failed", state: model.AssetPending, status: model.AssetFailed},
		{name: "retry postponed", state: model.AssetPending, status: model.AssetProcessing, retryDelay: 60},
		{name: "retry still due", state: model.AssetPending, status: model.AssetProcessing},
		{name: "expired lease replaced", keepLease: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assetTestDatabase(t)
			ctx := t.Context()
			asset := assetTestAsset(t, assetTestGroup(t, 1, "group"))
			require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Update("next_attempt_at", 0).Error)
			now := time.Now().Unix()
			var expected, previousLease model.AssetReplica
			interleaved := false
			const callback = "test:finish-before-replica-claim"
			require.NoError(t, model.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				rows, ok := tx.Statement.Dest.(*[]model.AssetReplica)
				if interleaved || !ok || len(*rows) != 1 || (*rows)[0].AssetID != asset.ID {
					return
				}
				interleaved = true
				claimAt := now
				if tc.keepLease {
					claimAt -= 120
				}
				claimed, err := model.ClaimAssetReplicas(ctx, claimAt, 1)
				require.NoError(t, err)
				require.Len(t, claimed, 1)
				previousLease = claimed[0]
				if !tc.keepLease {
					claimed[0].State = tc.state
					claimed[0].Status = tc.status
					claimed[0].UpstreamID = tc.upstreamID
					retryAt := int64(0)
					if tc.retryDelay > 0 {
						retryAt = now + tc.retryDelay
					}
					require.NoError(t, model.FinishAssetReplica(ctx, &claimed[0], retryAt))
				}
				expected, err = model.GetAssetReplica(ctx, asset.ID, 1)
				require.NoError(t, err)
			}))
			t.Cleanup(func() { require.NoError(t, model.DB.Callback().Query().Remove(callback)) })

			claimed, err := model.ClaimAssetReplicas(ctx, now, 1)
			require.NoError(t, err)
			require.True(t, interleaved)
			assert.Empty(t, claimed)
			current, err := model.GetAssetReplica(ctx, asset.ID, 1)
			require.NoError(t, err)
			assert.Equal(t, expected, current, "the stale candidate must preserve the current upstream identity and retry state")
			if current.Status == model.AssetProcessing {
				claimed, err = model.ClaimAssetReplicas(ctx, max(now, current.NextAttemptAt, current.LeaseExpiresAt), 1)
				require.NoError(t, err)
				require.Len(t, claimed, 1, "fresh due work must remain claimable, including an expired lease")
				assert.Equal(t, expected.UpstreamID, claimed[0].UpstreamID)
				assert.NotEqual(t, previousLease.LeaseToken, claimed[0].LeaseToken)
				before, err := model.GetAssetReplica(ctx, asset.ID, 1)
				require.NoError(t, err)
				assert.ErrorIs(t, model.FinishAssetReplica(ctx, &previousLease, 0), model.ErrAssetConflict)
				after, err := model.GetAssetReplica(ctx, asset.ID, 1)
				require.NoError(t, err)
				assert.Equal(t, before, after, "an expired worker cannot replace the new lease's result")
			}
		})
	}
}

func TestAssetGroupCleanupRejectsStaleCandidates(t *testing.T) {
	for _, name := range []string{"cleanup completed", "cleanup postponed", "expired lease replaced"} {
		t.Run(name, func(t *testing.T) {
			assetTestDatabase(t)
			ctx := t.Context()
			group := assetTestGroup(t, 1, "group")
			asset := assetTestAsset(t, group)
			require.NoError(t, model.DeleteAssetGroup(ctx, 1, group.PublicID, "default"))
			require.NoError(t, model.DB.Model(&model.Asset{}).Where("id = ?", asset.ID).Update("object_state", model.AssetDeleted).Error)
			now := time.Now().Unix()
			require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).
				Updates(map[string]any{"state": model.AssetReady, "upstream_id": "remote-group", "updated_at": now - 150}).Error)
			var expected, previousLease model.AssetGroupReplica
			interleaved := false
			const callback = "test:finish-before-group-cleanup-claim"
			require.NoError(t, model.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				rows, ok := tx.Statement.Dest.(*[]model.AssetGroupReplica)
				if interleaved || !ok || len(*rows) != 1 || (*rows)[0].GroupID != group.ID {
					return
				}
				interleaved = true
				claimAt := now
				if name == "expired lease replaced" {
					claimAt -= 90
				}
				claimed, err := model.ClaimDeletedAssetGroups(ctx, claimAt, 1)
				require.NoError(t, err)
				require.Len(t, claimed, 1)
				previousLease = claimed[0]
				if name != "expired lease replaced" {
					if name == "cleanup completed" {
						claimed[0].State = model.AssetDeleted
					}
					require.NoError(t, model.FinishAssetGroupReplica(ctx, &claimed[0]))
				}
				expected, err = model.GetAssetGroupReplica(ctx, group.ID, 1)
				require.NoError(t, err)
			}))
			t.Cleanup(func() { require.NoError(t, model.DB.Callback().Query().Remove(callback)) })

			claimed, err := model.ClaimDeletedAssetGroups(ctx, now, 1)
			require.NoError(t, err)
			require.True(t, interleaved)
			assert.Empty(t, claimed)
			current, err := model.GetAssetGroupReplica(ctx, group.ID, 1)
			require.NoError(t, err)
			assert.Equal(t, expected, current, "the stale candidate must not repeat deletion or skip the retry delay")
			if current.State != model.AssetDeleted {
				claimed, err = model.ClaimDeletedAssetGroups(ctx, max(now, current.UpdatedAt+30, current.LeaseExpiresAt), 1)
				require.NoError(t, err)
				require.Len(t, claimed, 1)
				assert.NotEqual(t, previousLease.LeaseToken, claimed[0].LeaseToken)
				claimed[0].State = model.AssetDeleted
				require.NoError(t, model.FinishAssetGroupReplica(ctx, &claimed[0]))
				before, err := model.GetAssetGroupReplica(ctx, group.ID, 1)
				require.NoError(t, err)
				assert.ErrorIs(t, model.FinishAssetGroupReplica(ctx, &previousLease), model.ErrAssetConflict)
				after, err := model.GetAssetGroupReplica(ctx, group.ID, 1)
				require.NoError(t, err)
				assert.Equal(t, before, after, "an expired worker cannot resurrect a deleted group")
			}
		})
	}
}

func TestAssetFailedReplicaAndConfigurationContracts(t *testing.T) {
	assetTestDatabase(t)
	ctx := t.Context()
	group := assetTestGroup(t, 1, "group")
	asset := assetTestAsset(t, group)
	replicas, err := model.ClaimAssetReplicas(ctx, time.Now().Unix(), 1)
	require.NoError(t, err)
	require.Len(t, replicas, 1)
	replica := replicas[0]
	replica.Status = model.AssetFailed
	replica.State = model.AssetReady
	replica.UpstreamID = "remote"
	replica.ErrorCode = "ContentRestricted"
	replica.ErrorMessage = "Rejected by upstream"
	require.NoError(t, model.FinishAssetReplica(ctx, &replica, 0))
	body, _ := common.Marshal(map[string]string{"Id": asset.PublicID})
	result, err := ExecuteAssetAction(ctx, dto.AssetActor{UserID: 1}, "GetAsset", body)
	require.NoError(t, err)
	view := result.(dto.AssetView)
	assert.Equal(t, model.AssetFailed, view.Status)
	require.NotNil(t, view.Error)
	assert.Equal(t, "ContentRestricted", view.Error.Code)
	_, err = PrepareAssetRouting(ctx, dto.AssetActor{UserID: 1, ChannelIDs: []int{1}}, []string{asset.PublicID}, 1)
	var failure *AssetError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, http.StatusUnprocessableEntity, failure.HTTPStatus)
	config, err := model.LoadAssetStorageConfig(ctx)
	require.NoError(t, err)
	old := config
	require.NoError(t, model.SaveAssetStorageConfig(ctx, config))
	assert.ErrorIs(t, model.SaveAssetStorageConfig(ctx, old), model.ErrAssetConflict)
	viewConfig, err := GetAssetStorageConfig(ctx)
	require.NoError(t, err)
	encoded, err := common.Marshal(viewConfig)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "storage-secret")
	assert.NotContains(t, string(encoded), "cursor_secret")
	assert.Error(t, model.UpdateOption(system_setting.AssetStorageOption, "{}"))
	assert.Error(t, model.UpdateOptionsBulk(map[string]string{model.AssetChannelOptionKey(1): "{}"}))
	channel, err := model.GetChannelById(1, true)
	require.NoError(t, err)
	channel.Key = "other-account"
	assert.ErrorIs(t, model.DB.Transaction(func(tx *gorm.DB) error { return model.GuardAssetChannelMutation(tx, channel, nil) }), model.ErrAssetInUse)
	channel.Key = ""
	assert.ErrorIs(t, channel.Save(), model.ErrAssetInUse)
	assert.ErrorIs(t, channel.Delete(), model.ErrAssetInUse)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 2).Update("status", common.ChannelStatusManuallyDisabled).Error)
	deleted, err := model.DeleteDisabledChannel()
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	removedConfig, err := model.LoadAssetChannelConfig(ctx, 2)
	require.NoError(t, err)
	assert.False(t, removedConfig.Enabled)
	assert.Empty(t, removedConfig.SecretKey)
}

func TestAssetChannelSaveValidatesUpstream(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		code    string
		message string
	}{
		{"HTML endpoint", http.StatusMethodNotAllowed, "<html>Method not allowed</html>", "UpstreamHTTP405", "The upstream material API returned HTTP 405 (POST not allowed). Check the complete material API address and path."},
		{"missing endpoint", http.StatusNotFound, "<html>Not found</html>", "UpstreamHTTP404", "The upstream material API returned HTTP 404 (not found). Check the complete material API address and path."},
		{"missing project", http.StatusBadRequest, `{"error":"项目不存在"}`, "UpstreamError", "项目不存在"},
		{"structured endpoint error", http.StatusMethodNotAllowed, `{"error":{"code":"ActionNotAllowed","message":"Action is disabled for this project"}}`, "ActionNotAllowed", "Action is disabled for this project"},
		{"official project error", http.StatusBadRequest, `{"ResponseMetadata":{"Error":{"Code":"InvalidParameter.ProjectName","Message":"Project does not exist"}}}`, "InvalidParameter.ProjectName", "Project does not exist"},
		{"missing list", http.StatusOK, `{"Result":{}}`, "UpstreamInvalidResponse", "The upstream material list was invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assetTestDatabase(t)
			before, err := model.LoadAssetChannelConfig(t.Context(), 1)
			require.NoError(t, err)
			calls := 0
			httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
				calls++
				assert.Equal(t, "ListAssetGroups", request.URL.Query().Get("Action"))
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: request}, nil
			})
			_, err = UpdateAssetChannelConfig(t.Context(), 1, dto.AssetChannelUpdate{AssetChannelFields: before.AssetChannelFields})
			var failure *AssetError
			require.ErrorAs(t, err, &failure)
			if tc.status >= 400 {
				assert.Equal(t, tc.status, failure.HTTPStatus)
			}
			assert.Equal(t, tc.code, failure.Code)
			assert.Equal(t, tc.message, failure.Message)
			stored, err := model.LoadAssetChannelConfig(t.Context(), 1)
			require.NoError(t, err)
			assert.Equal(t, before, stored)
			assert.Equal(t, 1, calls)
			input := dto.AssetChannelUpdate{AssetChannelFields: before.AssetChannelFields}
			input.Enabled = false
			_, err = UpdateAssetChannelConfig(t.Context(), 1, input)
			require.NoError(t, err)
			assert.Equal(t, 1, calls, "disabling must not depend on upstream availability")
		})
	}
}

func TestAssetChannelReenableUsesPageNumbers(t *testing.T) {
	assetTestDatabase(t)
	config, err := model.LoadAssetChannelConfig(t.Context(), 1)
	require.NoError(t, err)
	calls := 0
	httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, "ListAssetGroups", request.URL.Query().Get("Action"))
		var payload map[string]any
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		assert.Equal(t, "actual-project", payload["ProjectName"])
		assert.Equal(t, map[string]any{"GroupType": "AIGC"}, payload["Filter"])
		assert.NotContains(t, payload, "MaxResults")
		assert.NotContains(t, payload, "NextToken")
		status, body := http.StatusOK, `{"Result":{"Items":[],"TotalCount":0,"PageNumber":1,"PageSize":1}}`
		if payload["PageNumber"] != float64(1) || payload["PageSize"] != float64(1) {
			status, body = http.StatusBadRequest, `{"error":{"message":"PageNumber is required and must be >= 1"}}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	input := dto.AssetChannelUpdate{AssetChannelFields: config.AssetChannelFields}
	input.Enabled = false
	disabled, err := UpdateAssetChannelConfig(t.Context(), 1, input)
	require.NoError(t, err)
	assert.Zero(t, calls)
	input.AssetChannelFields = disabled.AssetChannelFields
	input.Enabled = true
	saved, err := UpdateAssetChannelConfig(t.Context(), 1, input)
	require.NoError(t, err)
	assert.True(t, saved.Enabled)
	assert.Equal(t, 1, calls)
}

func TestAssetUpstreamCanonicalEndpoint(t *testing.T) {
	assetTestDatabase(t)
	for _, tc := range []struct {
		name         string
		path         string
		status       int
		location     string
		authType     string
		action       string
		failNext     bool
		redirectNext bool
		wantCode     string
		calls        int
	}{
		{name: "unchanged endpoint", path: "/ark", status: 200, calls: 1},
		{name: "trailing slash", path: "/ark/", status: 200, calls: 1},
		{name: "301 preserves HTTPS", path: "/ark", status: 301, location: "http://upstream.example/ark/?%s", calls: 2},
		{name: "302 preserves POST", path: "/ark", status: 302, location: "/ark/?%s", calls: 2},
		{name: "307 relative path", path: "/ark", status: 307, location: "ark/?%s", calls: 2},
		{name: "308 signed create", path: "/ark", status: 308, location: "https://upstream.example/ark/?%s", authType: "aksk", action: "CreateAssetGroup", calls: 2},
		{name: "escaped path", path: "/ark%20api", status: 301, location: "/ark%%20api/?%s", calls: 2},
		{name: "foreign host", path: "/ark", status: 301, location: "https://other.example/ark/?%s", wantCode: "UpstreamInvalidResponse", calls: 1},
		{name: "different port", path: "/ark", status: 301, location: "https://upstream.example:8443/ark/?%s", wantCode: "UpstreamInvalidResponse", calls: 1},
		{name: "other path", path: "/ark", status: 301, location: "/other/?%s", wantCode: "UpstreamInvalidResponse", calls: 1},
		{name: "changed query", path: "/ark", status: 301, location: "/ark/?Action=DeleteAsset&original=%s", wantCode: "UpstreamInvalidResponse", calls: 1},
		{name: "userinfo", path: "/ark", status: 301, location: "https://user:password@upstream.example/ark/?%s", wantCode: "UpstreamInvalidResponse", calls: 1},
		{name: "fragment", path: "/ark", status: 301, location: "/ark/?%s#fragment", wantCode: "UpstreamInvalidResponse", calls: 1},
		{name: "303 not replayed", path: "/ark", status: 303, location: "/ark/?%s", wantCode: "UpstreamInvalidResponse", calls: 1},
		{name: "second redirect stops", path: "/ark", status: 301, location: "/ark/?%s", redirectNext: true, wantCode: "UpstreamInvalidResponse", calls: 2},
		{name: "server error not replayed", path: "/ark", status: 503, location: "/ark/?%s", action: "CreateAssetGroup", wantCode: "UpstreamResultUnknown", calls: 1},
		{name: "create failure remains unknown", path: "/ark", status: 301, location: "/ark/?%s", action: "CreateAssetGroup", failNext: true, wantCode: "UpstreamResultUnknown", calls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := system_setting.AssetChannelConfig{AssetChannelFields: dto.AssetChannelFields{Endpoint: "https://upstream.example" + tc.path, ProjectName: "actual-project", AuthType: tc.authType, AccessKey: "test-access"}, SecretKey: "test-secret"}
			action := tc.action
			if action == "" {
				action = "GetAsset"
			}
			calls := 0
			var signatures []string
			httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
				calls++
				assert.Equal(t, "https", request.URL.Scheme)
				assert.Equal(t, "upstream.example", request.URL.Host)
				assert.Equal(t, http.MethodPost, request.Method)
				assert.Equal(t, action, request.URL.Query().Get("Action"))
				assert.Equal(t, AssetAPIVersion, request.URL.Query().Get("Version"))
				var payload map[string]any
				require.NoError(t, common.DecodeJson(request.Body, &payload))
				assert.Equal(t, map[string]any{"Id": "remote-asset", "ProjectName": "actual-project"}, payload)
				if tc.authType == "aksk" {
					assert.Contains(t, request.Header.Get("Authorization"), "HMAC-SHA256 ")
					signatures = append(signatures, request.Header.Get("Authorization"))
				} else {
					assert.Equal(t, "Bearer test-key", request.Header.Get("Authorization"))
				}
				if calls > 1 {
					assert.Equal(t, tc.path+"/", request.URL.EscapedPath())
					if tc.failNext {
						return nil, io.ErrUnexpectedEOF
					}
				}
				status, body, header := http.StatusOK, `{"Result":{"Id":"remote-asset"}}`, http.Header{}
				if calls == 1 && tc.status != http.StatusOK {
					status, body = tc.status, "<html>upstream redirect or error</html>"
					header.Set("Location", fmt.Sprintf(tc.location, request.URL.RawQuery))
				} else if tc.redirectNext {
					status, body = http.StatusMovedPermanently, "<html>redirect again</html>"
					header.Set("Location", request.URL.Path+"/?"+request.URL.RawQuery)
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			var result struct {
				ID string `json:"Id"`
			}
			err := callAssetUpstream(t.Context(), &model.Channel{Key: "test-key"}, config, action, map[string]any{"Id": "remote-asset"}, &result)
			if tc.wantCode == "" {
				require.NoError(t, err)
				assert.Equal(t, "remote-asset", result.ID)
			} else {
				var failure *AssetError
				require.ErrorAs(t, err, &failure)
				assert.Equal(t, tc.wantCode, failure.Code)
			}
			assert.Equal(t, tc.calls, calls)
			if tc.authType == "aksk" {
				require.Len(t, signatures, 2)
				assert.NotEqual(t, signatures[0], signatures[1], "the canonical path must be signed again")
			}
		})
	}
}

func TestAssetChannelRepairsFailedEndpointPath(t *testing.T) {
	assetTestDatabase(t)
	ctx := t.Context()
	group := assetTestGroup(t, 1, "group")
	asset := assetTestAsset(t, group)
	require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Updates(map[string]any{"status": model.AssetFailed, "attempts": 5}).Error)
	require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).Updates(map[string]any{"state": model.AssetUnknown, "create_started_at": time.Now().Add(-6 * time.Minute).Unix()}).Error)
	before, err := model.LoadAssetChannelConfig(ctx, 1)
	require.NoError(t, err)
	httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "/ark", request.URL.Path)
		assert.Equal(t, "ListAssetGroups", request.URL.Query().Get("Action"))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"Result":{"Items":[]}}`)), Request: request}, nil
	})
	input := dto.AssetChannelUpdate{AssetChannelFields: before.AssetChannelFields}
	input.Endpoint += "/ark"
	_, err = UpdateAssetChannelConfig(ctx, 1, input)
	require.NoError(t, err)
	groups, err := model.AssetChannelGroupReplicas(ctx, 1)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, model.AssetUnknown, groups[0].State, "saving must not discard uncertain upstream creation")
	replicas, err := model.AssetReplicas(ctx, []int64{asset.ID})
	require.NoError(t, err)
	require.Len(t, replicas, 1)
	assert.Equal(t, model.AssetFailed, replicas[0].Status)
}

func TestAssetEndpointRepairPreservesResourceBoundaries(t *testing.T) {
	for _, change := range []string{"host", "project", "known group", "known material", "group lease", "processing material", "deleted group"} {
		t.Run(change, func(t *testing.T) {
			assetTestDatabase(t)
			ctx := t.Context()
			group := assetTestGroup(t, 1, "protected")
			asset := assetTestAsset(t, group)
			require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Update("status", model.AssetFailed).Error)
			before, err := model.LoadAssetChannelConfig(ctx, 1)
			require.NoError(t, err)
			input := dto.AssetChannelUpdate{AssetChannelFields: before.AssetChannelFields}
			input.Endpoint += "/ark"
			switch change {
			case "host":
				input.Endpoint = "https://different.example/ark"
			case "project":
				input.ProjectName = "different-project"
			case "known group":
				require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).Update("upstream_id", "existing-group").Error)
			case "known material":
				require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Update("upstream_id", "existing-material").Error)
			case "group lease":
				_, claimed, err := model.ClaimAssetGroupReplica(ctx, group.ID, 1, time.Now().Unix())
				require.NoError(t, err)
				require.True(t, claimed)
			case "processing material":
				require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Update("status", model.AssetProcessing).Error)
			case "deleted group":
				require.NoError(t, model.DeleteAssetGroup(ctx, 1, group.PublicID, "default"))
			}
			httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"Result":{"Items":[]}}`)), Request: request}, nil
			})
			_, err = UpdateAssetChannelConfig(ctx, 1, input)
			assert.ErrorIs(t, err, model.ErrAssetInUse)
			stored, err := model.LoadAssetChannelConfig(ctx, 1)
			require.NoError(t, err)
			assert.Equal(t, before, stored)
		})
	}
}

func assetRetryFixture(t *testing.T) (model.AssetGroup, model.Asset) {
	t.Helper()
	assetTestDatabase(t)
	previousRegistry := jsplugin.DefaultRegistry
	jsplugin.DefaultRegistry = jsplugin.NewRegistry()
	t.Cleanup(func() { _ = jsplugin.DefaultRegistry.Unregister("doubao"); jsplugin.DefaultRegistry = previousRegistry })
	source, err := os.ReadFile("../plugins/tasks/doubao/plugin.js")
	require.NoError(t, err)
	_, err = jsplugin.DefaultRegistry.Register(string(source), jsplugin.Options{})
	require.NoError(t, err)
	require.NoError(t, model.DB.Create(&model.User{Id: 1, Username: "asset-owner", Group: "default", Status: common.UserStatusEnabled}).Error)
	require.NoError(t, model.DB.Create(&model.Token{Id: 1, UserId: 1, Key: "asset-token", Status: common.TokenStatusEnabled, ExpiredTime: -1}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{ChannelId: 1, Model: "doubao-seedance-2-0-260128", Group: "default", Enabled: true}).Error)
	group := assetTestGroup(t, 1, "retry")
	asset := assetTestAsset(t, group)
	require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Updates(map[string]any{"status": model.AssetFailed, "token_id": 1, "attempts": 5, "error_code": "UpstreamInvalidResponse", "error_message": "The upstream material response was not valid JSON"}).Error)
	return group, asset
}

func TestAssetReplicaRetryPreservesExistingReferences(t *testing.T) {
	for _, tc := range []struct {
		name         string
		groupFound   bool
		assetFound   bool
		zeroPageSize bool
	}{
		{name: "no existing resources"},
		{name: "existing resources", groupFound: true, assetFound: true},
		{name: "empty group page", zeroPageSize: true},
		{name: "empty asset page", groupFound: true, zeroPageSize: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group, asset := assetRetryFixture(t)
			ctx := t.Context()
			if tc.groupFound {
				config, err := model.LoadAssetChannelConfig(ctx, 1)
				require.NoError(t, err)
				config.SizeLimitsMB.Image = 1
				require.NoError(t, model.SaveAssetChannelConfig(ctx, 1, config))
				require.NoError(t, model.DB.Model(&model.Asset{}).Where("id = ?", asset.ID).Update("size", 2_000_000).Error)
			}
			require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).Updates(map[string]any{"state": model.AssetUnknown, "create_started_at": time.Now().Add(-6 * time.Minute).Unix(), "error_code": "UpstreamInvalidResponse"}).Error)
			if tc.groupFound {
				require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Updates(map[string]any{"state": model.AssetUnknown, "create_started_at": time.Now().Add(-6 * time.Minute).Unix()}).Error)
			}
			require.NoError(t, model.EnsureAssetReplicas(ctx, []model.Asset{asset}, 2, 1))
			require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ? AND channel_id = ?", asset.ID, 2).Updates(map[string]any{"state": model.AssetReady, "status": model.AssetActive, "upstream_id": "other-channel-material"}).Error)
			other, err := model.GetAssetReplica(ctx, asset.ID, 2)
			require.NoError(t, err)
			var actions []string
			httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
				action := request.URL.Query().Get("Action")
				actions = append(actions, action)
				var payload map[string]any
				require.NoError(t, common.DecodeJson(request.Body, &payload))
				assert.Equal(t, "actual-project", payload["ProjectName"])
				body := `{"Result":{"Items":[]}}`
				if tc.zeroPageSize {
					body = `{"Result":{"Items":[],"TotalCount":0,"PageNumber":1,"PageSize":0}}`
				}
				switch action {
				case "ListAssetGroups":
					if tc.groupFound {
						body = fmt.Sprintf(`{"Result":{"Items":[{"Id":"remote-group","Name":%q,"GroupType":"AIGC"}]}}`, group.PublicID)
					}
				case "ListAssets":
					if tc.assetFound {
						body = fmt.Sprintf(`{"Result":{"Items":[{"Id":"unnamed-material","Name":"","GroupId":"remote-group","AssetType":"Image"},{"Id":"remote-material","Name":%q,"GroupId":"remote-group","AssetType":"Image"}]}}`, asset.PublicID)
					}
				default:
					require.FailNow(t, "recovery must only query existing resources", "got %s", action)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			require.NoError(t, RetryAssetReplica(ctx, 1, asset.PublicID, 1))
			assert.ErrorIs(t, RetryAssetReplica(ctx, 1, asset.PublicID, 1), model.ErrAssetConflict)
			replica, err := model.GetAssetReplica(ctx, asset.ID, 1)
			require.NoError(t, err)
			assert.Equal(t, model.AssetProcessing, replica.Status)
			assert.Equal(t, 1, replica.TokenID)
			assert.Zero(t, replica.Attempts)
			assert.Empty(t, replica.ErrorCode)
			assert.Empty(t, replica.ErrorMessage)
			assert.Positive(t, replica.NextAttemptAt)
			groupReplica, err := model.GetAssetGroupReplica(ctx, group.ID, 1)
			require.NoError(t, err)
			if tc.assetFound {
				assert.Equal(t, "remote-material", replica.UpstreamID)
				assert.Equal(t, model.AssetReady, replica.State)
			} else {
				assert.Empty(t, replica.UpstreamID)
				assert.Equal(t, model.AssetPending, replica.State)
			}
			if tc.groupFound {
				assert.Equal(t, "remote-group", groupReplica.UpstreamID)
				assert.Equal(t, model.AssetReady, groupReplica.State)
				assert.Equal(t, []string{"ListAssetGroups", "ListAssets"}, actions)
			} else {
				assert.Empty(t, groupReplica.UpstreamID)
				assert.Equal(t, model.AssetPending, groupReplica.State)
				assert.Equal(t, []string{"ListAssetGroups"}, actions)
			}
			stored, err := model.GetAsset(ctx, 1, asset.PublicID, "default")
			require.NoError(t, err)
			assert.Equal(t, asset.ObjectKey, stored.ObjectKey)
			assert.Equal(t, model.AssetReady, stored.ObjectState)
			assert.Equal(t, model.AssetActive, stored.Status)
			preserved, err := model.GetAssetReplica(ctx, asset.ID, 2)
			require.NoError(t, err)
			assert.Equal(t, other, preserved)
			if tc.groupFound && !tc.assetFound {
				leases, err := model.ClaimAssetReplicas(ctx, time.Now().Unix(), 1)
				require.NoError(t, err)
				require.Len(t, leases, 1)
				require.NoError(t, processAssetReplica(ctx, &leases[0]))
				failed, err := model.GetAssetReplica(ctx, asset.ID, 1)
				require.NoError(t, err)
				assert.Equal(t, model.AssetFailed, failed.Status)
				assert.Equal(t, "FileSizeTooLarge", failed.ErrorCode)
				assert.Zero(t, failed.NextAttemptAt)
				assert.Equal(t, []string{"ListAssetGroups", "ListAssets"}, actions)
			}
		})
	}
}

func TestAssetReplicaRetryRejectsUncertainLists(t *testing.T) {
	for _, assetList := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			body string
		}{
			{"missing items", `{"Result":{}}`},
			{"null items", `{"Result":{"Items":null}}`},
			{"missing name", `{"Result":{"Items":[{"Id":"one","GroupType":"AIGC","GroupId":"remote-group","AssetType":"Image"}]}}`},
			{"empty identifier", `{"Result":{"Items":[{"Id":"","Name":%q,"GroupType":"AIGC","GroupId":"remote-group","AssetType":"Image"}]}}`},
			{"duplicate match", `{"Result":{"Items":[{"Id":"one","Name":%[1]q,"GroupType":"AIGC","GroupId":"remote-group","AssetType":"Image"},{"Id":"two","Name":%[1]q,"GroupType":"AIGC","GroupId":"remote-group","AssetType":"Image"}]}}`},
			{"incomplete pages", `{"Result":{"Items":[],"NextToken":"unending"}}`},
			{"incomplete numbered page", `{"Result":{"Items":[],"TotalCount":101}}`},
			{"unexpected page", `{"Result":{"Items":[],"TotalCount":0,"PageNumber":2}}`},
			{"unexpected page size", `{"Result":{"Items":[],"TotalCount":0,"PageNumber":1,"PageSize":10}}`},
			{"zero size missing items", `{"Result":{"TotalCount":0,"PageNumber":1,"PageSize":0}}`},
			{"zero size null items", `{"Result":{"Items":null,"TotalCount":0,"PageNumber":1,"PageSize":0}}`},
			{"zero size missing total", `{"Result":{"Items":[],"PageNumber":1,"PageSize":0}}`},
			{"zero size positive total", `{"Result":{"Items":[],"TotalCount":1,"PageNumber":1,"PageSize":0}}`},
			{"zero size nonempty items", `{"Result":{"Items":[{"Id":"one","Name":%q,"GroupType":"AIGC","GroupId":"remote-group","AssetType":"Image"}],"TotalCount":1,"PageNumber":1,"PageSize":0}}`},
			{"zero size missing page", `{"Result":{"Items":[],"TotalCount":0,"PageSize":0}}`},
			{"zero size unexpected page", `{"Result":{"Items":[],"TotalCount":0,"PageNumber":2,"PageSize":0}}`},
			{"zero size next page", `{"Result":{"Items":[],"TotalCount":0,"PageNumber":1,"PageSize":0,"NextToken":"unending"}}`},
		} {
			t.Run(fmt.Sprintf("asset=%t/%s", assetList, tc.name), func(t *testing.T) {
				group, asset := assetRetryFixture(t)
				require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).Updates(map[string]any{"state": model.AssetUnknown, "create_started_at": time.Now().Add(-6 * time.Minute).Unix()}).Error)
				action, name := "ListAssetGroups", group.PublicID
				if assetList {
					action, name = "ListAssets", asset.PublicID
					require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).Updates(map[string]any{"state": model.AssetReady, "upstream_id": "remote-group"}).Error)
					require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Updates(map[string]any{"state": model.AssetUnknown, "create_started_at": time.Now().Add(-6 * time.Minute).Unix()}).Error)
				}
				before, err := model.GetAssetReplica(t.Context(), asset.ID, 1)
				require.NoError(t, err)
				beforeGroup, err := model.GetAssetGroupReplica(t.Context(), group.ID, 1)
				require.NoError(t, err)
				httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
					assert.Equal(t, action, request.URL.Query().Get("Action"))
					body := tc.body
					if strings.Contains(body, "%") {
						body = fmt.Sprintf(body, name)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
				})
				require.Error(t, RetryAssetReplica(t.Context(), 1, asset.PublicID, 1))
				after, err := model.GetAssetReplica(t.Context(), asset.ID, 1)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				afterGroup, err := model.GetAssetGroupReplica(t.Context(), group.ID, 1)
				require.NoError(t, err)
				assert.Equal(t, beforeGroup, afterGroup)
			})
		}
	}
}

func TestAssetUpstreamLookupUsesPageNumbers(t *testing.T) {
	assetTestDatabase(t)
	config, err := model.LoadAssetChannelConfig(t.Context(), 1)
	require.NoError(t, err)
	channel, err := model.GetChannelById(1, true)
	require.NoError(t, err)
	for _, action := range []string{"ListAssetGroups", "ListAssets"} {
		for _, tc := range []struct {
			name      string
			total     int
			duplicate bool
			wantID    string
			wantError bool
			calls     int
		}{
			{name: "match on second page", total: 101, wantID: "remote-match", calls: 2},
			{name: "full final page", total: 100, calls: 1},
			{name: "duplicate across pages", total: 101, duplicate: true, wantError: true, calls: 2},
			{name: "scan limit", total: 2001, wantError: true, calls: 20},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				calls := 0
				httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
					calls++
					assert.Equal(t, action, request.URL.Query().Get("Action"))
					var payload map[string]any
					require.NoError(t, common.DecodeJson(request.Body, &payload))
					assert.Equal(t, float64(calls), payload["PageNumber"])
					assert.Equal(t, float64(100), payload["PageSize"])
					assert.NotContains(t, payload, "MaxResults")
					assert.NotContains(t, payload, "NextToken")
					items := []map[string]any{}
					for i := range min(100, tc.total-(calls-1)*100) {
						id, name := fmt.Sprintf("other-%d-%d", calls, i), "unrelated"
						if tc.total == 101 && (calls == 2 || tc.duplicate && i == 0) {
							id, name = "remote-match", "managed-name"
						}
						items = append(items, map[string]any{"Id": id, "Name": name, "GroupType": "AIGC", "GroupId": "remote-group", "AssetType": "Image"})
					}
					body, err := common.Marshal(map[string]any{"Result": map[string]any{"Items": items, "TotalCount": tc.total, "PageNumber": calls, "PageSize": 100}})
					require.NoError(t, err)
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
				})
				var id string
				var err error
				if action == "ListAssetGroups" {
					id, err = findUpstreamAssetGroup(t.Context(), channel, config, "managed-name")
				} else {
					id, err = findUpstreamAsset(t.Context(), channel, config, &model.Asset{PublicID: "managed-name", AssetType: "Image"}, "remote-group")
				}
				if tc.wantError {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				assert.Equal(t, tc.wantID, id)
				assert.Equal(t, tc.calls, calls)
			})
		}
	}
}

func TestAssetReplicaRetryRejectsStateChanges(t *testing.T) {
	for _, change := range []string{"group lease", "configuration", "deletion", "disabled owner"} {
		t.Run(change, func(t *testing.T) {
			group, asset := assetRetryFixture(t)
			require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).Updates(map[string]any{"state": model.AssetUnknown, "create_started_at": time.Now().Add(-6 * time.Minute).Unix()}).Error)
			httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
				switch change {
				case "group lease":
					_, claimed, err := model.ClaimAssetGroupReplica(t.Context(), group.ID, 1, time.Now().Unix())
					require.NoError(t, err)
					require.True(t, claimed)
				case "configuration":
					config, err := model.LoadAssetChannelConfig(t.Context(), 1)
					require.NoError(t, err)
					require.NoError(t, model.SaveAssetChannelConfig(t.Context(), 1, config))
				case "deletion":
					require.NoError(t, model.DeleteAsset(t.Context(), 1, asset.PublicID, "default"))
				case "disabled owner":
					require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", 1).Update("status", common.UserStatusDisabled).Error)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"Result":{"Items":[]}}`)), Request: request}, nil
			})
			assert.ErrorIs(t, RetryAssetReplica(t.Context(), 1, asset.PublicID, 1), model.ErrAssetConflict)
			replica, err := model.GetAssetReplica(t.Context(), asset.ID, 1)
			require.NoError(t, err)
			assert.Equal(t, model.AssetFailed, replica.Status)
			assert.Equal(t, 5, replica.Attempts)
		})
	}
}

func TestAssetReplicaRetryFencesStaleGroupClaims(t *testing.T) {
	group, asset := assetRetryFixture(t)
	ctx := t.Context()
	require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).Updates(map[string]any{"state": model.AssetUnknown, "create_started_at": time.Now().Add(-6 * time.Minute).Unix()}).Error)
	httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"Result":{"Items":[]}}`)), Request: request}, nil
	})
	// Complete recovery after a worker reads the old group but before it tries
	// to claim that group, without timing-dependent goroutines or sleeps.
	recovered := false
	const callback = "test:retry-before-group-claim"
	require.NoError(t, model.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*model.AssetGroupReplica)
		if recovered || !ok || row.GroupID != group.ID {
			return
		}
		recovered = true
		require.NoError(t, RetryAssetReplica(ctx, 1, asset.PublicID, 1))
	}))
	t.Cleanup(func() { require.NoError(t, model.DB.Callback().Query().Remove(callback)) })
	_, claimed, err := model.ClaimAssetGroupReplica(ctx, group.ID, 1, time.Now().Unix())
	require.NoError(t, err)
	require.True(t, recovered)
	assert.False(t, claimed, "a worker must not claim the recovered group using its old unknown state")
	current, err := model.GetAssetGroupReplica(ctx, group.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, model.AssetPending, current.State)
	assert.Empty(t, current.LeaseToken)
}

func TestAssetReplicaRetryPreservesTokenAndConsistencyLimits(t *testing.T) {
	group, asset := assetRetryFixture(t)
	ctx := t.Context()
	_, err := model.GetAssetReplica(ctx, asset.ID, 1)
	require.NoError(t, err)
	httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
		t.Errorf("blocked recovery must not access upstream")
		return nil, fmt.Errorf("unexpected upstream request")
	})
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", 1).Update("status", common.TokenStatusDisabled).Error)
	var failure *AssetError
	require.ErrorAs(t, RetryAssetReplica(ctx, 1, asset.PublicID, 1), &failure)
	assert.Equal(t, http.StatusForbidden, failure.HTTPStatus)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", 1).Update("status", common.TokenStatusEnabled).Error)
	require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).Updates(map[string]any{"state": model.AssetUnknown, "create_started_at": time.Now().Unix()}).Error)
	require.ErrorAs(t, RetryAssetReplica(ctx, 1, asset.PublicID, 1), &failure)
	assert.Equal(t, "UpstreamResultUnknown", failure.Code)
	assert.Equal(t, http.StatusConflict, failure.HTTPStatus)
}

func TestAssetOptionCollationAliasesCannotOverwriteSecrets(t *testing.T) {
	assetTestDatabase(t)
	aliases := map[string]string{
		"ＡｒｋＡｓｓｅｔＳｔｏｒａｇｅＳｅｃｒｅｔ":                            system_setting.AssetStorageOption,
		strings.ToLower(system_setting.AssetStorageOption): system_setting.AssetStorageOption,
		system_setting.AssetStorageOption + " ":            system_setting.AssetStorageOption,
		"ÁrkAssetStorageSecret":                            system_setting.AssetStorageOption,
		"ArkAssetChánnel.1.Secret":                         model.AssetChannelOptionKey(1),
	}
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		// Exercise real case/accent-insensitive, PAD SPACE key comparison. The
		// table belongs exclusively to this test's random namespace.
		table := model.DB.NamingStrategy.TableName("Option")
		require.NoError(t, model.DB.Exec("ALTER TABLE `"+table+"` MODIFY `key` varchar(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL").Error)
		aliases["ArkAssetStorageSecret\u200d"] = system_setting.AssetStorageOption
	}
	for alias, canonical := range aliases {
		t.Run(alias, func(t *testing.T) {
			var before model.Option
			require.NoError(t, model.DB.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: canonical}).First(&before).Error)
			if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
				var matched model.Option
				require.NoError(t, model.DB.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: alias}).First(&matched).Error)
				assert.Equal(t, canonical, matched.Key)
			}
			assert.Error(t, model.UpdateOption(alias, `{"enabled":false}`))
			assert.Error(t, model.UpdateOptionsBulk(map[string]string{alias: `{"enabled":false}`}))
			assert.Error(t, model.UpdateOptionsBulk(map[string]string{alias: `{"enabled":false}`, "ServerAddress": "https://example.com"}))
			var after model.Option
			require.NoError(t, model.DB.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: canonical}).First(&after).Error)
			assert.Equal(t, before, after)
		})
	}
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		// UCA 5.2 also equates stroked o with o. NFKD does not, so this case
		// exercises protection using the key column's actual collation.
		table := model.DB.NamingStrategy.TableName("Option")
		require.NoError(t, model.DB.Exec("ALTER TABLE `"+table+"` MODIFY `key` varchar(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_520_ci NOT NULL").Error)
		alias := "ArkAssetStørageSecret"
		var before model.Option
		require.NoError(t, model.DB.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: alias}).First(&before).Error)
		assert.Equal(t, system_setting.AssetStorageOption, before.Key)
		assert.Error(t, model.UpdateOption(alias, "{}"))
		assert.Error(t, model.UpdateOptionsBulk(map[string]string{alias: "{}", "ServerAddress": "https://example.com"}))
		var after model.Option
		require.NoError(t, model.DB.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: alias}).First(&after).Error)
		assert.Equal(t, before, after)
		for _, first := range []struct{ alias, canonical string }{
			{"ArkAssetStørageSecret", system_setting.AssetStorageOption},
			{"ArkAssetChanneł.1.Secret", model.AssetChannelOptionKey(1)},
			{"ArkAssetChanneł．1．Secret", model.AssetChannelOptionKey(1)},
			{"ArkAssetChan\u20ddnel.1.Secret", model.AssetChannelOptionKey(1)},
		} {
			require.NoError(t, model.DB.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: first.canonical}).Delete(&model.Option{}).Error)
			assert.Error(t, model.UpdateOption(first.alias, "{}"))
			assert.Error(t, model.UpdateOptionsBulk(map[string]string{first.alias: "{}", "ServerAddress": "https://example.com"}))
			var count int64
			require.NoError(t, model.DB.Model(&model.Option{}).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: first.canonical}).Count(&count).Error)
			assert.Zero(t, count)
		}
	}
}

func TestAssetExpiredUsesSkipActiveHeadOfBatch(t *testing.T) {
	assetTestDatabase(t)
	tasks := make([]model.Task, 0, 202)
	uses := make([]model.AssetUse, 0, 204)
	for i := range 200 {
		id := fmt.Sprintf("active-%d", i)
		tasks = append(tasks, model.Task{TaskID: id, Status: model.TaskStatusInProgress})
		uses = append(uses, model.AssetUse{AssetID: 1, TaskID: id, CreatedAt: 1})
	}
	for _, status := range []model.TaskStatus{model.TaskStatusSuccess, model.TaskStatusFailure} {
		id := string(status)
		tasks = append(tasks, model.Task{TaskID: id, Status: status})
		uses = append(uses, model.AssetUse{AssetID: 1, TaskID: id, CreatedAt: 1})
	}
	uses = append(uses, model.AssetUse{AssetID: 1, TaskID: "old-orphan", CreatedAt: 1}, model.AssetUse{AssetID: 1, TaskID: "recent-orphan", CreatedAt: time.Now().Unix()})
	require.NoError(t, model.DB.Create(&tasks).Error)
	require.NoError(t, model.DB.Omit("task_id").Create(&model.Task{Status: model.TaskStatusInProgress}).Error)
	require.NoError(t, model.DB.Create(&uses).Error)
	require.NoError(t, model.ExpiredAssetUses(t.Context(), time.Now().Add(-73*time.Hour).Unix()))
	var remaining []model.AssetUse
	require.NoError(t, model.DB.Order("id").Find(&remaining).Error)
	require.Len(t, remaining, 201)
	assert.Equal(t, "active-0", remaining[0].TaskID)
	assert.Equal(t, "recent-orphan", remaining[200].TaskID)
}

func TestAssetUnknownCreateReconcilesBeforeFailing(t *testing.T) {
	for _, tc := range []struct{ unknownGroup, expired, lowerGroupLimit bool }{
		{false, false, false}, {true, false, false}, {false, true, false}, {true, true, false},
		{true, false, true},
	} {
		unknownGroup := tc.unknownGroup
		t.Run(fmt.Sprintf("group=%t/expired=%t/lower_group_limit=%t", unknownGroup, tc.expired, tc.lowerGroupLimit), func(t *testing.T) {
			assetTestDatabase(t)
			previousRegistry := jsplugin.DefaultRegistry
			jsplugin.DefaultRegistry = jsplugin.NewRegistry()
			t.Cleanup(func() { _ = jsplugin.DefaultRegistry.Unregister("doubao"); jsplugin.DefaultRegistry = previousRegistry })
			source, err := os.ReadFile("../plugins/tasks/doubao/plugin.js")
			require.NoError(t, err)
			_, err = jsplugin.DefaultRegistry.Register(string(source), jsplugin.Options{})
			require.NoError(t, err)
			require.NoError(t, model.DB.Create(&model.User{Id: 1, Username: "asset-owner", Group: "default", Status: common.UserStatusEnabled}).Error)
			require.NoError(t, model.DB.Create(&model.Token{Id: 1, UserId: 1, Key: "asset-token", Status: common.TokenStatusEnabled, ExpiredTime: -1}).Error)
			require.NoError(t, model.DB.Create(&model.Ability{ChannelId: 1, Model: "doubao-seedance-2-0-260128", Group: "default", Enabled: true}).Error)
			var mu sync.Mutex
			counts := map[string]int{}
			name := ""
			groupName := ""
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				action := r.URL.Query().Get("Action")
				counts[action]++
				var payload map[string]any
				require.NoError(t, common.DecodeJson(r.Body, &payload))
				result := map[string]any{}
				switch action {
				case "CreateAssetGroup":
					groupName = payload["Name"].(string)
					if unknownGroup {
						w.WriteHeader(http.StatusBadGateway)
						_, _ = io.WriteString(w, `{"ResponseMetadata":{"Error":{"Code":"InternalError","Message":"Try later"}}}`)
						return
					}
					result["Id"] = "remote-group"
				case "ListAssetGroups":
					result["Items"] = []any{}
					if counts[action] > 1 {
						result["Items"] = []any{map[string]any{"Id": "remote-group", "Name": groupName, "GroupType": "AIGC"}}
					}
				case "CreateAsset":
					name = payload["Name"].(string)
					if !unknownGroup {
						w.WriteHeader(http.StatusBadGateway)
						_, _ = io.WriteString(w, `{"ResponseMetadata":{"Error":{"Code":"InternalError","Message":"Try later"}}}`)
						return
					}
					result["Id"] = "remote-asset"
				case "ListAssets":
					result["Items"] = []any{}
					if counts[action] > 1 {
						result["Items"] = []any{map[string]any{"Id": "remote-asset", "Name": name, "GroupId": "remote-group", "AssetType": "Image"}}
					}
				case "GetAsset":
					result = map[string]any{"Id": "remote-asset", "Status": "Active"}
				default:
					t.Errorf("unexpected action %s", action)
				}
				if items, ok := result["Items"].([]any); ok && len(items) == 0 {
					result["TotalCount"] = 0
					result["PageNumber"] = 1
					result["PageSize"] = 0
				}
				body, err := common.Marshal(map[string]any{"Result": result})
				require.NoError(t, err)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			httpClient = server.Client()
			config, err := model.LoadAssetChannelConfig(t.Context(), 1)
			require.NoError(t, err)
			config.Endpoint = server.URL
			require.NoError(t, model.SaveAssetChannelConfig(t.Context(), 1, config))
			group := assetTestGroup(t, 1, "group")
			asset := assetTestAsset(t, group)
			require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Update("token_id", 1).Error)
			steps := 3
			if tc.expired {
				steps = 2
			}
			for step := range steps {
				if (!unknownGroup || tc.lowerGroupLimit) && step == 1 {
					// A lower limit must not prevent recovery of an already-created resource.
					config, err := model.LoadAssetChannelConfig(t.Context(), 1)
					require.NoError(t, err)
					config.SizeLimitsMB.Image = 1
					require.NoError(t, model.SaveAssetChannelConfig(t.Context(), 1, config))
					require.NoError(t, model.DB.Model(&model.Asset{}).Where("id = ?", asset.ID).Update("size", 2_000_000).Error)
				}
				if tc.expired && step == 1 {
					if unknownGroup {
						require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).Update("create_started_at", time.Now().Add(-6*time.Minute).Unix()).Error)
					} else {
						require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Update("create_started_at", time.Now().Add(-6*time.Minute).Unix()).Error)
					}
				}
				require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Update("next_attempt_at", 0).Error)
				leases, err := model.ClaimAssetReplicas(t.Context(), time.Now().Unix(), 1)
				require.NoError(t, err)
				require.Len(t, leases, 1)
				if step == 1 {
					leases[0].Attempts = 9
				}
				require.NoError(t, processAssetReplica(t.Context(), &leases[0]))
				stored, err := model.GetAssetByID(t.Context(), asset.ID)
				require.NoError(t, err)
				if tc.expired && step == 1 {
					assert.Equal(t, model.AssetFailed, stored.Status)
				} else if step < 2 {
					assert.Equal(t, model.AssetProcessing, stored.Status)
				} else if tc.lowerGroupLimit {
					assert.Equal(t, model.AssetFailed, stored.Status)
					assert.Equal(t, "FileSizeTooLarge", stored.ErrorCode)
				} else {
					assert.Equal(t, model.AssetActive, stored.Status)
				}
			}
			mu.Lock()
			assert.Equal(t, 1, counts["CreateAssetGroup"])
			if unknownGroup && (tc.expired || tc.lowerGroupLimit) {
				assert.Zero(t, counts["CreateAsset"])
			} else {
				assert.Equal(t, 1, counts["CreateAsset"])
			}
			mu.Unlock()
			if tc.lowerGroupLimit {
				groupReplica, err := model.GetAssetGroupReplica(t.Context(), group.ID, 1)
				require.NoError(t, err)
				assert.Equal(t, model.AssetReady, groupReplica.State)
				assert.Equal(t, "remote-group", groupReplica.UpstreamID)
				replica, err := model.GetAssetReplica(t.Context(), asset.ID, 1)
				require.NoError(t, err)
				assert.Equal(t, model.AssetFailed, replica.Status)
				assert.Equal(t, "FileSizeTooLarge", replica.ErrorCode)
				assert.Zero(t, replica.NextAttemptAt)
			}
		})
	}
}

type assetRoundTripper func(*http.Request) (*http.Response, error)

func (transport assetRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func assetTestMediaFile(t *testing.T, assetType string, size int64) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "material-*")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	header := map[string]string{
		"Image": "\x89PNG\r\n\x1a\n",
		"Video": "\x00\x00\x00\x18ftypqt  ",
		"Audio": "ID3\x03\x00\x00\x00\x00\x00\x00",
	}[assetType]
	_, err = file.WriteString(header)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(size))
	_, err = file.Seek(0, io.SeekStart)
	require.NoError(t, err)
	return file
}

func TestAssetDefaultSizeLimitsUploadAndURL(t *testing.T) {
	assetTestDatabase(t)
	previousFetch := *system_setting.GetFetchSetting()
	*system_setting.GetFetchSetting() = system_setting.FetchSetting{EnableSSRFProtection: true, AllowedPorts: []string{"443"}}
	t.Cleanup(func() { *system_setting.GetFetchSetting() = previousFetch })
	for _, source := range []string{"upload", "URL"} {
		for _, tc := range []struct {
			name      string
			assetType string
			size      int64
			allowed   bool
			other     dto.AssetSizeLimits
		}{
			{name: "image below limit", assetType: "Image", size: 30_000_000 - 1, allowed: true},
			{name: "image at limit", assetType: "Image", size: 30_000_000},
			{name: "video at limit", assetType: "Video", size: 200_000_000, allowed: true},
			{name: "video over limit", assetType: "Video", size: 200_000_000 + 1},
			{name: "audio at limit", assetType: "Audio", size: 15_000_000, allowed: true},
			{name: "audio over limit", assetType: "Audio", size: 15_000_000 + 1},
			{name: "another channel constrains all users", assetType: "Image", size: 2_000_000, other: dto.AssetSizeLimits{Image: 1}},
			{name: "empty material", assetType: "Image"},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				config, err := model.LoadAssetChannelConfig(t.Context(), 2)
				require.NoError(t, err)
				config.SizeLimitsMB = tc.other
				require.NoError(t, model.SaveAssetChannelConfig(t.Context(), 2, config))
				file := assetTestMediaFile(t, tc.assetType, tc.size)
				spoolDirectory := t.TempDir()
				t.Setenv("TMPDIR", spoolDirectory)
				puts := 0
				uploadedDigest := ""
				httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
					if request.Method == http.MethodGet {
						assert.Equal(t, "https://8.8.8.8/material", request.URL.String())
						// Incorrect source metadata must not bypass actual-byte admission.
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(file), ContentLength: 1, Request: request}, nil
					}
					puts++
					assert.Equal(t, http.MethodPut, request.Method)
					sum := sha256.New()
					n, err := io.Copy(sum, request.Body)
					require.NoError(t, err)
					assert.Equal(t, tc.size, n)
					uploadedDigest = fmt.Sprintf("%x", sum.Sum(nil))
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
				})
				group := assetTestGroup(t, 1, "sized-material")
				actor := dto.AssetActor{UserID: 1, ChannelIDs: []int{1}}
				input := dto.AssetUpload{GroupID: group.PublicID, AssetType: tc.assetType, Size: 1}
				var asset *model.Asset
				if source == "upload" {
					_, err = CreateUploadedAsset(t.Context(), actor, input, file)
					var stored model.Asset
					require.NoError(t, model.DB.Where("group_id = ?", group.ID).First(&stored).Error)
					asset = &stored
				} else {
					asset, err = reserveAsset(t.Context(), actor, input, "https://8.8.8.8/material")
					require.NoError(t, err)
					err = fetchAssetContent(t.Context(), asset)
				}
				remaining, listErr := os.ReadDir(spoolDirectory)
				require.NoError(t, listErr)
				assert.Empty(t, remaining, "successful and rejected operations must remove temporary source files")
				if !tc.allowed {
					var failure *AssetError
					require.ErrorAs(t, err, &failure)
					assert.Equal(t, "FileSizeTooLarge", failure.Code)
					assert.Zero(t, puts)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, tc.size, asset.Size)
				assert.Equal(t, model.AssetReady, asset.ObjectState)
				assert.Equal(t, uploadedDigest, asset.SHA256)
				assert.Equal(t, 1, puts)
			})
		}
	}
}

func TestAssetChannelSizeLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  dto.AssetSizeLimits
		second dto.AssetSizeLimits
		omit   string
		want   dto.AssetSizeLimits
	}{
		{name: "official defaults", want: dto.AssetSizeLimits{Image: 30, Video: 200, Audio: 15}},
		{name: "minimum per type", first: dto.AssetSizeLimits{Image: 25, Video: 100}, second: dto.AssetSizeLimits{Video: 50, Audio: 10}, want: dto.AssetSizeLimits{Image: 25, Video: 50, Audio: 10}},
		{name: "all channels allow larger files", first: dto.AssetSizeLimits{Image: 50, Video: 300, Audio: 25}, second: dto.AssetSizeLimits{Image: 40, Video: 400, Audio: 20}, want: dto.AssetSizeLimits{Image: 40, Video: 300, Audio: 20}},
		{name: "disabled material config", second: dto.AssetSizeLimits{Image: 1, Video: 1, Audio: 1}, omit: "config", want: dto.AssetSizeLimits{Image: 30, Video: 200, Audio: 15}},
		{name: "disabled channel", second: dto.AssetSizeLimits{Image: 1, Video: 1, Audio: 1}, omit: "channel", want: dto.AssetSizeLimits{Image: 30, Video: 200, Audio: 15}},
		{name: "unconfigured channel", second: dto.AssetSizeLimits{Image: 1, Video: 1, Audio: 1}, omit: "missing", want: dto.AssetSizeLimits{Image: 30, Video: 200, Audio: 15}},
		{name: "changed channel account", second: dto.AssetSizeLimits{Image: 1, Video: 1, Audio: 1}, omit: "scope", want: dto.AssetSizeLimits{Image: 30, Video: 200, Audio: 15}},
		{name: "no enabled channel", first: dto.AssetSizeLimits{Image: 1, Video: 1, Audio: 1}, omit: "all", want: dto.AssetSizeLimits{Image: 30, Video: 200, Audio: 15}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assetTestDatabase(t)
			for i, limits := range []dto.AssetSizeLimits{tc.first, tc.second} {
				config, err := model.LoadAssetChannelConfig(t.Context(), i+1)
				require.NoError(t, err)
				config.SizeLimitsMB = limits
				if (tc.omit == "config" && i == 1) || tc.omit == "all" {
					config.Enabled = false
				}
				require.NoError(t, model.SaveAssetChannelConfig(t.Context(), i+1, config))
			}
			switch tc.omit {
			case "channel":
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 2).Update("status", common.ChannelStatusManuallyDisabled).Error)
			case "missing":
				require.NoError(t, model.DB.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: model.AssetChannelOptionKey(2)}).Delete(&model.Option{}).Error)
			case "scope":
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 2).Update("key", "other-account-key").Error)
			}
			limits, err := GetAssetSizeLimits(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tc.want, limits)
		})
	}
}

func TestAssetChannelSizeLimitConfiguration(t *testing.T) {
	assetTestDatabase(t)
	checks := 0
	httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
		checks++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"Result":{"Items":[]}}`)), Request: request}, nil
	})
	for _, tc := range []struct {
		name    string
		limits  dto.AssetSizeLimits
		invalid bool
	}{
		{name: "custom limits", limits: dto.AssetSizeLimits{Image: 20, Video: 50, Audio: 10}},
		{name: "reset to defaults"},
		{name: "negative", limits: dto.AssetSizeLimits{Audio: -1}, invalid: true},
		{name: "numeric precision overflow", limits: dto.AssetSizeLimits{Video: 9_007_199_254}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := model.LoadAssetChannelConfig(t.Context(), 1)
			require.NoError(t, err)
			input := dto.AssetChannelUpdate{AssetChannelFields: before.AssetChannelFields}
			input.SizeLimitsMB = tc.limits
			previousChecks := checks
			view, err := UpdateAssetChannelConfig(t.Context(), 1, input)
			if tc.invalid {
				require.Error(t, err)
				assert.Equal(t, previousChecks, checks)
				stored, loadErr := model.LoadAssetChannelConfig(t.Context(), 1)
				require.NoError(t, loadErr)
				assert.Equal(t, before, stored)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.limits, view.SizeLimitsMB)
			stored, err := model.LoadAssetChannelConfig(t.Context(), 1)
			require.NoError(t, err)
			assert.Equal(t, tc.limits, stored.SizeLimitsMB)
			assert.Equal(t, before.SecretKey, stored.SecretKey)
		})
	}
	// Unreadable active configuration must not silently relax admission limits.
	require.NoError(t, model.DB.Model(&model.Option{}).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: model.AssetChannelOptionKey(1)}).Update("value", "{").Error)
	_, err := GetAssetSizeLimits(t.Context())
	require.Error(t, err)
}

func TestAssetReplicaSizeLimitKeepsOtherReadyCopies(t *testing.T) {
	group, asset := assetRetryFixture(t)
	ctx := t.Context()
	asset.Size = 2_000_000
	require.NoError(t, model.DB.Model(&asset).Update("size", asset.Size).Error)
	require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).Updates(map[string]any{"state": model.AssetReady, "upstream_id": "remote-group"}).Error)
	require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Update("status", model.AssetProcessing).Error)
	require.NoError(t, model.EnsureAssetReplicas(ctx, []model.Asset{asset}, 2, 1))
	require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ? AND channel_id = ?", asset.ID, 2).Updates(map[string]any{"state": model.AssetReady, "status": model.AssetActive, "upstream_id": "other-material"}).Error)
	other, err := model.GetAssetReplica(ctx, asset.ID, 2)
	require.NoError(t, err)
	config, err := model.LoadAssetChannelConfig(ctx, 1)
	require.NoError(t, err)
	config.SizeLimitsMB.Image = 1
	require.NoError(t, model.SaveAssetChannelConfig(ctx, 1, config))
	var actions []string
	httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
		action := request.URL.Query().Get("Action")
		actions = append(actions, action)
		body := `{"Result":{"Id":"new-material"}}`
		if action == "GetAsset" {
			body = `{"Result":{"Id":"new-material","Status":"Active"}}`
		} else {
			assert.Equal(t, "CreateAsset", action)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	leases, err := model.ClaimAssetReplicas(ctx, time.Now().Unix(), 1)
	require.NoError(t, err)
	require.Len(t, leases, 1)
	require.NoError(t, processAssetReplica(ctx, &leases[0]))
	failed, err := model.GetAssetReplica(ctx, asset.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, model.AssetFailed, failed.Status)
	assert.Equal(t, "FileSizeTooLarge", failed.ErrorCode)
	assert.Zero(t, failed.NextAttemptAt)
	assert.Empty(t, actions)
	views, err := model.GetAssetReplicaViews(ctx, 1, asset.PublicID)
	require.NoError(t, err)
	require.Len(t, views, 2)
	for _, view := range views {
		if view.ChannelID == 1 {
			require.NotNil(t, view.FileSizeBytes)
			require.NotNil(t, view.CurrentLimitBytes)
			assert.Equal(t, asset.Size, *view.FileSizeBytes)
			assert.Equal(t, int64(999_999), *view.CurrentLimitBytes)
		} else {
			assert.Nil(t, view.FileSizeBytes)
			assert.Nil(t, view.CurrentLimitBytes)
		}
	}
	require.NoError(t, RetryAssetReplica(ctx, 1, asset.PublicID, 1))
	leases, err = model.ClaimAssetReplicas(ctx, time.Now().Unix(), 1)
	require.NoError(t, err)
	require.Len(t, leases, 1)
	require.NoError(t, processAssetReplica(ctx, &leases[0]))
	failed, err = model.GetAssetReplica(ctx, asset.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, model.AssetFailed, failed.Status)
	assert.Equal(t, "FileSizeTooLarge", failed.ErrorCode)
	assert.Zero(t, failed.NextAttemptAt)
	assert.Empty(t, actions)
	stored, err := model.GetAssetByID(ctx, asset.ID)
	require.NoError(t, err)
	assert.Equal(t, model.AssetReady, stored.ObjectState)
	assert.Equal(t, asset.ObjectKey, stored.ObjectKey)
	ready, err := PrepareAssetRouting(ctx, dto.AssetActor{UserID: 1, ChannelIDs: []int{1, 2}}, []string{asset.PublicID}, 1)
	require.NoError(t, err)
	assert.Equal(t, []int{2}, ready)
	preserved, err := model.GetAssetReplica(ctx, asset.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, other, preserved)

	config, err = model.LoadAssetChannelConfig(ctx, 1)
	require.NoError(t, err)
	config.SizeLimitsMB.Image = 3
	require.NoError(t, model.SaveAssetChannelConfig(ctx, 1, config))
	require.NoError(t, RetryAssetReplica(ctx, 1, asset.PublicID, 1))
	leases, err = model.ClaimAssetReplicas(ctx, time.Now().Unix(), 1)
	require.NoError(t, err)
	require.Len(t, leases, 1)
	require.NoError(t, processAssetReplica(ctx, &leases[0]))
	assert.Equal(t, []string{"CreateAsset", "GetAsset"}, actions)
	config, err = model.LoadAssetChannelConfig(ctx, 1)
	require.NoError(t, err)
	config.SizeLimitsMB.Image = 1
	require.NoError(t, model.SaveAssetChannelConfig(ctx, 1, config))
	ready, err = PrepareAssetRouting(ctx, dto.AssetActor{UserID: 1, ChannelIDs: []int{1, 2}}, []string{asset.PublicID}, 1)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, ready)
}

func TestAssetUploadDeadlineFinalizesWithoutCallerContext(t *testing.T) {
	for _, staleLease := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%t", staleLease), func(t *testing.T) {
			assetTestDatabase(t)
			group := assetTestGroup(t, 1, "upload")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			httpClient.Transport = assetRoundTripper(func(request *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := request.Context().Deadline()
				assert.True(t, ok)
				assert.Less(t, time.Until(deadline), 3*time.Minute)
				if staleLease {
					require.NoError(t, model.DB.Model(&model.Asset{}).Where("group_id = ?", group.ID).Update("lease_token", "new-owner").Error)
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
				}
				cancel()
				return nil, context.Canceled
			})
			image := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
			_, err := CreateUploadedAsset(ctx, dto.AssetActor{UserID: 1, ChannelIDs: []int{1}}, dto.AssetUpload{GroupID: group.PublicID, AssetType: "Image"}, bytes.NewReader(image))
			require.Error(t, err)
			assert.Equal(t, 1, calls)
			var stored model.Asset
			require.NoError(t, model.DB.Where("group_id = ?", group.ID).First(&stored).Error)
			if staleLease {
				assert.ErrorIs(t, err, model.ErrAssetConflict)
				assert.Equal(t, "new-owner", stored.LeaseToken)
				assert.Equal(t, "uploading", stored.ObjectState)
				assert.Zero(t, stored.DeletedAt)
			} else {
				assert.Equal(t, model.AssetFailed, stored.Status)
				assert.Empty(t, stored.LeaseToken)
				assert.Zero(t, stored.LeaseExpiresAt)
				assert.Positive(t, stored.DeletedAt)
			}
		})
	}
}

func TestAssetS3AndArkTransportContracts(t *testing.T) {
	assetTestDatabase(t)
	var mu sync.Mutex
	objects := map[string][]byte{}
	var upstreamRequests []map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/ark" {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, AssetAPIVersion, r.URL.Query().Get("Version"))
			if strings.HasPrefix(r.Header.Get("Authorization"), "HMAC-SHA256 ") {
				assert.Contains(t, r.Header.Get("Authorization"), "/cn-beijing/ark/request")
				assert.NotEmpty(t, r.Header.Get("X-Date"))
				assert.NotEmpty(t, r.Header.Get("X-Content-Sha256"))
			} else {
				assert.Equal(t, "Bearer channel-key", r.Header.Get("Authorization"))
			}
			var payload map[string]any
			require.NoError(t, common.DecodeJson(r.Body, &payload))
			upstreamRequests = append(upstreamRequests, payload)
			switch r.URL.Query().Get("Action") {
			case "CreateAssetGroup":
				_, _ = io.WriteString(w, `{"ResponseMetadata":{},"Result":{"Id":"remote-group"}}`)
			case "CreateAsset":
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"ResponseMetadata":{"Error":{"Code":"InternalError","Message":"secret channel-key https://storage.example/?signature=usable"}}}`)
			case "ListAssets":
				_, _ = io.WriteString(w, `{"ResponseMetadata":{},"Result":{"Items":[{"Id":"remote-asset","Name":"asset-na-unknown","AssetType":"Image","GroupId":"remote-group"}]}}`)
			case "ListAssetGroups":
				_, _ = io.WriteString(w, `{"ResponseMetadata":{},"Result":{"Items":[]}}`)
			case "GetAsset":
				_, _ = io.WriteString(w, `{"ResponseMetadata":{},"Result":{"Id":"remote-asset","Status":"Failed","Error":{"Code":"ContentRestricted","Message":"Rejected"}}}`)
			default:
				t.Errorf("unexpected action %s", r.URL.Query().Get("Action"))
			}
			return
		}
		assert.True(t, strings.HasPrefix(r.URL.Path, "/assets/new-api-assets/"))
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			objects[r.URL.Path] = data
		case http.MethodGet:
			assert.NotEmpty(t, r.URL.Query().Get("X-Amz-Signature"))
			data, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(data)
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected S3 method %s", r.Method)
		}
	}))
	defer server.Close()
	httpClient = server.Client()
	ssrfProtectedHTTPClient = httpClient
	config, err := model.LoadAssetStorageConfig(t.Context())
	require.NoError(t, err)
	config.Endpoint = server.URL
	require.NoError(t, model.SaveAssetStorageConfig(t.Context(), config))
	require.NoError(t, testAssetStorage(t.Context(), config))
	assert.Empty(t, objects)
	upstream, err := model.LoadAssetChannelConfig(t.Context(), 1)
	require.NoError(t, err)
	upstream.Endpoint = server.URL + "/ark"
	require.NoError(t, model.SaveAssetChannelConfig(t.Context(), 1, upstream))
	group := assetTestGroup(t, 1, "group")
	image := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	result, err := CreateUploadedAsset(t.Context(), dto.AssetActor{UserID: 1, ChannelIDs: []int{1}}, dto.AssetUpload{GroupID: group.PublicID, AssetType: "Image", ContentType: "image/png", Size: 1}, bytes.NewReader(image))
	require.NoError(t, err)
	asset, err := model.GetAsset(t.Context(), 1, result.(map[string]string)["Id"], "default")
	require.NoError(t, err)
	assert.Equal(t, int64(len(image)), asset.Size)
	assert.Equal(t, model.AssetReady, asset.ObjectState)
	assert.Equal(t, model.AssetProcessing, asset.Status)
	_, err = CreateUploadedAsset(t.Context(), dto.AssetActor{UserID: 1, ChannelIDs: []int{1}}, dto.AssetUpload{GroupID: group.PublicID, AssetType: "Image", ContentType: "image/png"}, strings.NewReader("<script>invalid</script>"))
	require.Error(t, err)
	channel, err := model.GetChannelById(1, true)
	require.NoError(t, err)
	var created struct {
		ID string `json:"Id"`
	}
	require.NoError(t, callAssetUpstream(t.Context(), channel, upstream, "CreateAssetGroup", map[string]any{"ProjectName": "logical-project", "Name": "gateway", "GroupType": "AIGC"}, &created))
	assert.Equal(t, "remote-group", created.ID)
	assert.Equal(t, "actual-project", upstreamRequests[0]["ProjectName"])
	err = callAssetUpstream(t.Context(), channel, upstream, "CreateAsset", map[string]any{"GroupId": created.ID}, &created)
	var failure *AssetError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, "UpstreamResultUnknown", failure.Code)
	assert.NotContains(t, failure.Message, channel.Key)
	assert.NotContains(t, failure.Message, "signature=")
	unknown := &model.Asset{PublicID: "asset-na-unknown", AssetType: "Image"}
	id, err := findUpstreamAsset(t.Context(), channel, upstream, unknown, "remote-group")
	require.NoError(t, err)
	assert.Equal(t, "remote-asset", id)
	var failed dto.AssetView
	require.NoError(t, callAssetUpstream(t.Context(), channel, upstream, "GetAsset", map[string]any{"Id": id}, &failed))
	assert.Equal(t, model.AssetFailed, failed.Status)
	require.NotNil(t, failed.Error)
	signed := upstream
	signed.AuthType = "aksk"
	signed.AccessKey = "official-access"
	signed.SecretKey = "official-secret"
	require.NoError(t, callAssetUpstream(t.Context(), channel, signed, "GetAsset", map[string]any{"Id": id}, &failed))
	// An uncertain create is never repeated. Deletion waits for consistency,
	// then absence from the complete exact-name lookup permits cleanup.
	require.NoError(t, model.DB.Model(&model.AssetGroupReplica{}).Where("group_id = ?", group.ID).Updates(map[string]any{"state": model.AssetReady, "upstream_id": "remote-group"}).Error)
	require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Updates(map[string]any{"state": model.AssetUnknown, "create_started_at": time.Now().Unix()}).Error)
	require.Error(t, cleanupAsset(t.Context(), asset))
	assert.NotEmpty(t, objects)
	require.NoError(t, model.DB.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Update("create_started_at", time.Now().Add(-6*time.Minute).Unix()).Error)
	require.NoError(t, cleanupAsset(context.Background(), asset))
	assert.Empty(t, objects)
	replicas, err := model.AssetReplicas(t.Context(), []int64{asset.ID})
	require.NoError(t, err)
	require.Len(t, replicas, 1)
	assert.Equal(t, model.AssetDeleted, replicas[0].State)
	unknownGroup := &model.AssetGroupReplica{GroupID: group.ID, ChannelID: 1, State: model.AssetUnknown, CreateStartedAt: time.Now().Add(-6 * time.Minute).Unix()}
	require.NoError(t, cleanupAssetGroup(t.Context(), unknownGroup))
}
