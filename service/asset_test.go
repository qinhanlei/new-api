package service

import (
	"bytes"
	"context"
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
	for _, tc := range []struct{ unknownGroup, expired bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		unknownGroup := tc.unknownGroup
		t.Run(fmt.Sprintf("group=%t/expired=%t", unknownGroup, tc.expired), func(t *testing.T) {
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
				} else {
					assert.Equal(t, model.AssetActive, stored.Status)
				}
			}
			mu.Lock()
			assert.Equal(t, 1, counts["CreateAssetGroup"])
			if tc.expired && unknownGroup {
				assert.Zero(t, counts["CreateAsset"])
			} else {
				assert.Equal(t, 1, counts["CreateAsset"])
			}
			mu.Unlock()
		})
	}
}

type assetRoundTripper func(*http.Request) (*http.Response, error)

func (transport assetRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
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
