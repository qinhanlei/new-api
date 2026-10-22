package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/abema/go-mp4"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type memoryAssetStore struct {
	bytes []byte
	key   string
	mime  string
	err   error
}

func TestManagedAssetChannelSelection(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	previousEnabled := common.AssetLibraryEnabled
	common.AssetLibraryEnabled = true
	t.Cleanup(func() { common.AssetLibraryEnabled = previousEnabled })
	const modelName = "asset-routing-model"
	for _, candidate := range []struct {
		id       int
		priority int
		eligible bool
	}{{3101, 100, false}, {3102, 20, true}, {3103, 10, true}} {
		createChannelSelectAutoGroupsChannel(t, db, candidate.id, "default", modelName)
		updates := map[string]any{"priority": candidate.priority}
		if candidate.eligible {
			updates["type"] = constant.ChannelTypeDoubaoVideo
			updates["setting"] = `{"asset_endpoint":"https://assets.example/"}`
		}
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", candidate.id).Updates(updates).Error)
		require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", candidate.id).Update("priority", candidate.priority).Error)
	}
	model.InitChannelCache()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	param := &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: modelName, RequestPath: ctx.Request.URL.Path}
	ordinary, _, selectionErr := SelectChannelForRequest(ctx, modelName, param)
	require.Nil(t, selectionErr)
	require.NotNil(t, ordinary)
	assert.Equal(t, 3101, ordinary.Id, "ordinary requests retain their usual channel priority")

	require.NoError(t, SetTaskAssetIntent(ctx, []any{"na_asset_example"}))
	first, _, selectionErr := SelectChannelForRequest(ctx, modelName, param)
	require.Nil(t, selectionErr)
	require.NotNil(t, first)
	assert.Equal(t, 3102, first.Id)
	param.IncreaseRetry()
	retry, _, selectionErr := SelectChannelForRequest(ctx, modelName, param)
	require.Nil(t, selectionErr)
	require.NotNil(t, retry)
	assert.Equal(t, 3103, retry.Id, "retries must retain the asset-capability constraint")

	GetChannelConstraints(ctx).AddPin(dto.ChannelPin{ChannelId: 3101, Source: dto.PinSourceToken, Rank: dto.PinRankToken})
	pinned, _, selectionErr := SelectChannelForRequest(ctx, modelName, param)
	assert.Nil(t, pinned)
	require.NotNil(t, selectionErr)
	assert.Equal(t, dto.FilterManagedAssets, selectionErr.FilterKind)
}

func (*memoryAssetStore) Backend() string { return model.AssetStorageLocal }

func (s *memoryAssetStore) Put(_ context.Context, key string, reader io.Reader, _ int64, mime string) error {
	s.key, s.mime = key, mime
	s.bytes, _ = io.ReadAll(reader)
	return s.err
}
func (*memoryAssetStore) Presign(context.Context, string) (string, error) {
	return "https://storage.example/source", nil
}
func (*memoryAssetStore) Delete(context.Context, string) error { return nil }

func TestAssetCatalogUpload(t *testing.T) {
	previousEnabled := common.AssetLibraryEnabled
	common.AssetLibraryEnabled = true
	t.Cleanup(func() { common.AssetLibraryEnabled = previousEnabled })
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB, previousType := model.DB, common.MainDatabaseType()
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Asset{}))
	require.NoError(t, db.Create(&model.User{Id: 41, Username: "owner"}).Error)
	store := &memoryAssetStore{}
	var encodedImage bytes.Buffer
	require.NoError(t, png.Encode(&encodedImage, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	content := encodedImage.Bytes()
	asset, err := CreateAsset(context.Background(), 41, "picture", bytes.NewReader(content), store)
	require.NoError(t, err)
	assert.Equal(t, "image", asset.Kind)
	assert.Equal(t, "image/png", asset.MIMEType)
	assert.Equal(t, model.AssetStatusReady, asset.Status)
	stored, err := model.GetAsset(t.Context(), asset.ID)
	require.NoError(t, err)
	assert.Equal(t, model.AssetStorageLocal, stored.StorageBackend)
	assert.Equal(t, 2, asset.Width)
	assert.Equal(t, 3, asset.Height)
	assert.Equal(t, content, store.bytes)
	assert.Equal(t, asset.ObjectKey, store.key)
	assert.Len(t, asset.SHA256, 64)
	encoded, err := common.Marshal(asset)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "object_key")
	assert.NotContains(t, string(encoded), "storage_backend")
	assert.NotContains(t, string(encoded), "user_id")
	_, err = model.GetUserAsset(context.Background(), 42, asset.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	for _, invalid := range []string{"<svg><script>alert(1)</script></svg>", "<html>page</html>", "\x89PNG\r\n\x1a\ntruncated", ""} {
		_, err := CreateAsset(context.Background(), 41, "invalid", strings.NewReader(invalid), store)
		require.ErrorIs(t, err, ErrAssetMediaType)
	}
	// A real PCM payload derives its duration from the stored file, never a
	// client-supplied duration field or filename extension.
	wav := make([]byte, 44+32000)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 8000)
	binary.LittleEndian.PutUint32(wav[28:], 16000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 32000)
	audio, err := CreateAsset(context.Background(), 41, "sound", bytes.NewReader(wav), store)
	require.NoError(t, err)
	assert.Equal(t, "audio", audio.Kind)
	assert.Equal(t, float64(2), audio.DurationSeconds)
	video := assetMP4Fixture(t, 4000)
	clip, err := CreateAsset(context.Background(), 41, "clip", bytes.NewReader(video), store)
	require.NoError(t, err)
	assert.Equal(t, "video", clip.Kind)
	assert.Equal(t, float64(4), clip.DurationSeconds)
	assert.Equal(t, 640, clip.Width)
	assert.Equal(t, 480, clip.Height)
	_, err = CreateAsset(context.Background(), 41, "oversized duration", bytes.NewReader(assetMP4Fixture(t, 3_600_001)), store)
	require.ErrorIs(t, err, ErrAssetMediaType)
	store.err = ErrAssetStorageUnavailable
	_, err = CreateAsset(context.Background(), 41, "failed", bytes.NewReader(content), store)
	require.ErrorIs(t, err, ErrAssetStorageUnavailable)
	var failed model.Asset
	require.NoError(t, db.Where("object_key = ?", store.key).First(&failed).Error)
	assert.Equal(t, model.AssetStatusFailed, failed.Status)
	assert.Positive(t, failed.DeletedAt)
}

func assetMP4Fixture(t *testing.T, duration uint32) []byte {
	t.Helper()
	box := func(kind string, payload []byte) []byte {
		data := make([]byte, 8+len(payload))
		binary.BigEndian.PutUint32(data, uint32(len(data)))
		copy(data[4:], kind)
		copy(data[8:], payload)
		return data
	}
	var movie, track bytes.Buffer
	_, err := mp4.Marshal(&movie, &mp4.Mvhd{Timescale: 1000, DurationV0: duration}, mp4.Context{})
	require.NoError(t, err)
	_, err = mp4.Marshal(&track, &mp4.Tkhd{Width: 640 << 16, Height: 480 << 16}, mp4.Context{})
	require.NoError(t, err)
	file := box("ftyp", []byte("isom\x00\x00\x00\x00mp42"))
	return append(file, box("moov", append(box("mvhd", movie.Bytes()), box("trak", box("tkhd", track.Bytes()))...))...)
}

func TestAssetImportAlwaysRejectsPrivateSources(t *testing.T) {
	settings := system_setting.GetFetchSetting()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	settings.EnableSSRFProtection = false
	settings.AllowPrivateIp = true
	for _, source := range []string{"http://127.0.0.1/source", "http://169.254.169.254/latest/meta-data/", "http://[::1]/source", "http://localhost/source", "file:///etc/passwd", "https://user:secret@example.com/source", "https://example.com:8080/source"} {
		t.Run(source, func(t *testing.T) {
			_, err := ImportAsset(context.Background(), 1, "picture", source, &memoryAssetStore{})
			require.ErrorIs(t, err, ErrAssetSource)
		})
	}
}

func TestAssetS3SigningAndResponseHandling(t *testing.T) {
	var put []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/bucket/assets/na_asset_example", r.URL.Path)
		assert.Contains(t, r.Header.Get("Authorization"), "AWS4-HMAC-SHA256")
		if r.Method == http.MethodPut {
			assert.Equal(t, "*", r.Header.Get("If-None-Match"))
			assert.Equal(t, "image/png", r.Header.Get("Content-Type"))
			put, _ = io.ReadAll(r.Body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	require.NoError(t, err)
	store := &s3AssetStore{endpoint: endpoint, bucket: "bucket", region: "us-east-1", credentials: aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture-secret"}, ttl: 24 * time.Hour, client: server.Client()}
	require.NoError(t, store.Put(context.Background(), "assets/na_asset_example", strings.NewReader("source"), 6, "image/png"))
	assert.Equal(t, []byte("source"), put)
	signed, err := store.Presign(context.Background(), "assets/na_asset_example")
	require.NoError(t, err)
	parsed, err := url.Parse(signed)
	require.NoError(t, err)
	assert.Equal(t, "86400", parsed.Query().Get("X-Amz-Expires"))
	assert.NotEmpty(t, parsed.Query().Get("X-Amz-Signature"))
	assert.NotContains(t, signed, "fixture-secret")
	require.NoError(t, store.Delete(context.Background(), "assets/na_asset_example"))
}

// Storage choice belongs to each immutable source, not the current upload
// default. Provider replicas that are already available need no source store.
func TestAssetStorageRouting(t *testing.T) {
	previousEnabled, previousMaster := common.AssetLibraryEnabled, common.IsMasterNode
	common.AssetLibraryEnabled, common.IsMasterNode = true, true
	t.Cleanup(func() {
		common.AssetLibraryEnabled, common.IsMasterNode = previousEnabled, previousMaster
	})
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB, previousType := model.DB, common.MainDatabaseType()
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.User{}, &model.Asset{}, &model.AssetSpace{}, &model.AssetReplica{}, &model.Task{}, &model.TaskAssetUse{}))
	require.NoError(t, db.Create(&model.User{Id: 41, Username: "owner"}).Error)
	localDir := t.TempDir()
	for key, value := range map[string]string{"ASSET_STORAGE": "local", "ASSET_LOCAL_DIR": localDir, "ASSET_S3_ENDPOINT": "https://objects.example", "ASSET_S3_BUCKET": "bucket", "ASSET_S3_ACCESS_KEY_ID": "fixture", "ASSET_S3_SECRET_ACCESS_KEY": "fixture"} {
		t.Setenv(key, value)
	}
	oldAddress := system_setting.TaskPublicAddress
	system_setting.TaskPublicAddress = "https://assets.example"
	t.Cleanup(func() { system_setting.TaskPublicAddress = oldAddress })
	store, err := GetAssetStore()
	require.NoError(t, err)
	var imageBytes bytes.Buffer
	require.NoError(t, png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	local, err := CreateAsset(t.Context(), 41, "local", &imageBytes, store)
	require.NoError(t, err)
	require.Equal(t, model.AssetStorageLocal, local.StorageBackend)
	legacy := model.Asset{ID: "na_asset_legacy", UserID: 41, Kind: "image", Status: model.AssetStatusReady, ObjectKey: "assets/na_asset_legacy"}
	s3 := model.Asset{ID: "na_asset_s3", UserID: 41, Kind: "image", Status: model.AssetStatusReady, ObjectKey: "assets/na_asset_s3", StorageBackend: model.AssetStorageS3}
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.Create(&s3).Error)
	var sources []string
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		require.NoError(t, common.DecodeJson(r.Body, &request))
		id := request["Id"]
		if r.URL.Query().Get("Action") == "CreateAsset" && request["Name"] == "na_asset_rejected" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ResponseMetadata":{"Error":{"Code":"InvalidParameter","Message":"Rejected"}}}`))
			return
		}
		if r.URL.Query().Get("Action") == "CreateAsset" {
			sources = append(sources, request["URL"])
			id = request["Name"]
		}
		response, err := common.Marshal(map[string]any{"Result": map[string]string{"Id": id, "Status": "Active"}})
		require.NoError(t, err)
		_, err = w.Write(response)
		require.NoError(t, err)
	}))
	defer provider.Close()
	previousHTTP := httpClient
	httpClient = provider.Client()
	t.Cleanup(func() { httpClient = previousHTTP })
	settings, err := common.Marshal(map[string]string{"asset_endpoint": provider.URL})
	require.NoError(t, err)
	channel := model.Channel{Type: constant.ChannelTypeDoubaoVideo, Key: "fixture", Setting: common.GetPointer(string(settings)), Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	space := model.AssetSpace{ID: "aspace_routing", ChannelID: channel.Id, BaseURL: provider.URL, ProjectName: "default", GroupID: "group", GroupState: model.AssetGroupReady}
	require.NoError(t, db.Create(&space).Error)
	submission := &model.TaskSubmission{UserID: 41, ChannelID: channel.Id, SpaceID: space.ID, AssetIDs: []string{local.ID, legacy.ID, s3.ID}}
	resolved, ready, err := ResolveTaskAssets(t.Context(), submission, "worker")
	require.NoError(t, err)
	require.True(t, ready)
	require.Len(t, resolved, 3)
	require.Len(t, sources, 3)
	for i, host := range []string{"assets.example", "objects.example", "objects.example"} {
		parsed, err := url.Parse(sources[i])
		require.NoError(t, err)
		assert.Equal(t, host, parsed.Host)
		assert.NotEmpty(t, parsed.RawQuery)
	}
	rejected := model.Asset{ID: "na_asset_rejected", UserID: 41, Kind: "image", Status: model.AssetStatusReady, ObjectKey: "assets/na_asset_rejected", StorageBackend: model.AssetStorageS3}
	require.NoError(t, db.Create(&rejected).Error)
	failedSubmission := *submission
	failedSubmission.AssetIDs = []string{rejected.ID}
	_, ready, err = ResolveTaskAssets(t.Context(), &failedSubmission, "rejected-worker")
	require.ErrorContains(t, err, "asset import failed")
	assert.False(t, ready)
	var failedReplica model.AssetReplica
	require.NoError(t, db.Where("asset_id = ? AND space_id = ?", rejected.ID, space.ID).First(&failedReplica).Error)
	assert.Equal(t, model.AssetReplicaFailed, failedReplica.Status)
	// Neither cached references nor an existing import's status poll needs the
	// source backend to remain configured.
	t.Setenv("ASSET_S3_ENDPOINT", "")
	t.Setenv("ASSET_STORAGE", "unavailable")
	resolved, ready, err = ResolveTaskAssets(t.Context(), submission, "cached-worker")
	require.NoError(t, err)
	assert.True(t, ready)
	assert.Len(t, resolved, 3)
	pending := model.Asset{ID: "na_asset_pending", UserID: 41, Kind: "image", Status: model.AssetStatusReady}
	require.NoError(t, db.Create(&pending).Error)
	require.NoError(t, db.Create(&model.AssetReplica{AssetID: pending.ID, SpaceID: space.ID, Status: model.AssetReplicaProcessing, RemoteID: "pending-id"}).Error)
	submission.AssetIDs = []string{pending.ID}
	_, ready, err = ResolveTaskAssets(t.Context(), submission, "poll-worker")
	require.NoError(t, err)
	assert.True(t, ready)

	// A misconfigured S3 source cannot block cleanup of local files. A live
	// task still holds its deleted source until terminal completion.
	for _, id := range []string{local.ID, legacy.ID, s3.ID} {
		require.NoError(t, model.DeleteUserAsset(t.Context(), 41, id))
	}
	task := model.Task{TaskID: "task_storage_hold", UserId: 41, Status: model.TaskStatusSubmitted}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Create(&model.TaskAssetUse{TaskID: task.TaskID, AssetID: local.ID}).Error)
	require.NoError(t, db.Model(&model.Asset{}).Where("deleted_at > 0").Update("updated_at", time.Now().Unix()-30).Error)
	require.NoError(t, CleanupDeletedAssets(t.Context()))
	_, err = os.Stat(filepath.Join(localDir, local.ObjectKey))
	require.NoError(t, err)
	for _, id := range []string{legacy.ID, s3.ID} {
		retained, err := model.GetAsset(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, "Asset cleanup failed", retained.Error)
	}
	require.NoError(t, db.Model(&task).Update("status", model.TaskStatusSuccess).Error)
	require.NoError(t, db.Model(&model.Asset{}).Where("deleted_at > 0").Update("updated_at", time.Now().Unix()-30).Error)
	require.NoError(t, CleanupDeletedAssets(t.Context()))
	_, err = model.GetAsset(t.Context(), local.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = os.Stat(filepath.Join(localDir, local.ObjectKey))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = model.GetAsset(t.Context(), legacy.ID)
	require.NoError(t, err)
}

func TestChannelAssetGroupLifecycle(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB, previousType, previousEnabled, previousHTTP := model.DB, common.MainDatabaseType(), common.AssetLibraryEnabled, httpClient
	model.DB, common.AssetLibraryEnabled = db, true
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB, common.AssetLibraryEnabled, httpClient = previousDB, previousEnabled, previousHTTP
		common.SetMainDatabaseType(previousType)
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Asset{}, &model.AssetSpace{}, &model.AssetReplica{}, &model.TaskSubmission{}, &model.Task{}, &model.TaskAssetUse{}))
	for key, value := range map[string]string{"ASSET_STORAGE": "s3", "ASSET_S3_ENDPOINT": "https://objects.example", "ASSET_S3_BUCKET": "test", "ASSET_S3_ACCESS_KEY_ID": "fixture", "ASSET_S3_SECRET_ACCESS_KEY": "fixture"} {
		t.Setenv(key, value)
	}
	asset := model.Asset{ID: "na_asset_shared", UserID: 41, Kind: "image", Status: model.AssetStatusReady, ObjectKey: "assets/shared", StorageBackend: model.AssetStorageS3}
	require.NoError(t, db.Create(&asset).Error)
	groupNames := map[string]string{}
	creates := map[string]int{}
	imports := map[string]int{}
	deleted := map[string]bool{}
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.Trim(r.URL.Path, "/")
		assert.Equal(t, "Bearer key-"+name, r.Header.Get("Authorization"))
		var request map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &request))
		assert.Equal(t, "default", request["ProjectName"])
		var result any
		switch r.URL.Query().Get("Action") {
		case "CreateAssetGroup":
			creates[name]++
			assert.Equal(t, "AIGC", request["GroupType"])
			groupNames[name] = request["Name"].(string)
			if name == "recovery" || name == "missing" {
				w.WriteHeader(http.StatusGatewayTimeout)
				return
			}
			result = map[string]any{"Id": "group-" + name}
		case "ListAssetGroups":
			items := []any{}
			if name != "missing" {
				items = append(items, map[string]any{"Id": "group-" + name, "Name": groupNames[name], "ProjectName": "default", "GroupType": "AIGC"})
			}
			result = map[string]any{"Items": items, "TotalCount": len(items), "PageNumber": 1, "PageSize": 100}
		case "GetAssetGroup":
			result = map[string]any{"Id": "group-" + name, "Name": groupNames[name], "ProjectName": "default", "GroupType": "AIGC"}
		case "DeleteAssetGroup":
			deleted[name] = true
			result = map[string]any{}
		case "CreateAsset":
			imports[name]++
			assert.Equal(t, "group-"+name, request["GroupId"])
			assert.Equal(t, asset.ID, request["Name"])
			assert.Contains(t, request["URL"], "https://objects.example/")
			result = map[string]any{"Id": "asset-" + name, "Status": "Processing"}
		case "GetAsset":
			result = map[string]any{"Id": "asset-" + name, "Status": "Active"}
		default:
			t.Errorf("unexpected action: %s", r.URL.Query().Get("Action"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, err := common.Marshal(map[string]any{"Result": result})
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}))
	defer provider.Close()
	httpClient = provider.Client()
	var spaces []*model.AssetSpace
	var channels []model.Channel
	for _, name := range []string{"first", "second", "third", "recovery", "missing"} {
		settings, err := common.Marshal(map[string]string{"asset_endpoint": provider.URL + "/" + name})
		require.NoError(t, err)
		channel := model.Channel{Name: name, Type: constant.ChannelTypeDoubaoVideo, Key: "key-" + name, Status: common.ChannelStatusEnabled, Setting: common.GetPointer(string(settings))}
		require.NoError(t, db.Create(&channel).Error)
		space, err := model.EnsureChannelAssetSpace(t.Context(), channel.Id)
		require.NoError(t, err)
		submission := &model.TaskSubmission{UserID: 41, ChannelID: channel.Id, SpaceID: space.ID, AssetIDs: []string{asset.ID}}
		_, ready, err := ResolveTaskAssets(t.Context(), submission, "first-worker")
		assert.False(t, ready)
		if name == "recovery" || name == "missing" {
			require.ErrorContains(t, err, "administrator review")
			require.NoError(t, db.First(space, "id = ?", space.ID).Error)
			assert.Equal(t, model.AssetGroupUnknown, space.GroupState)
			require.NoError(t, db.Model(space).Update("next_attempt", 0).Error)
			_, ready, err = ResolveTaskAssets(t.Context(), submission, "recovery-worker")
			assert.False(t, ready)
			assert.Equal(t, 1, creates[name], "a timeout must not replay group creation")
			if name == "missing" {
				require.ErrorContains(t, err, "administrator review")
				assert.Zero(t, imports[name])
				continue
			}
		}
		require.NoError(t, err)
		require.NoError(t, db.Model(&model.AssetReplica{}).Where("space_id = ?", space.ID).Update("next_attempt", 0).Error)
		resolved, ready, err := ResolveTaskAssets(t.Context(), submission, "poll-worker")
		require.NoError(t, err)
		require.True(t, ready)
		assert.Equal(t, "asset://asset-"+name, resolved[asset.ID].(map[string]any)["reference"])
		_, ready, err = ResolveTaskAssets(t.Context(), submission, "cached-worker")
		require.NoError(t, err)
		assert.True(t, ready)
		assert.Equal(t, 1, imports[name])
		assert.Equal(t, 1, creates[name])
		submission.ChannelID = channel.Id + 100
		_, _, err = ResolveTaskAssets(t.Context(), submission, "wrong-channel")
		require.Error(t, err, "a channel must never resolve another channel's replicas")
		spaces, channels = append(spaces, space), append(channels, channel)
	}

	space, channel := spaces[0], channels[0]
	require.Error(t, model.RequestChannelAssetCleanup(t.Context(), channel.Id), "cleanup requires an explicitly disabled channel")
	require.NoError(t, db.Model(&channel).Update("status", common.ChannelStatusManuallyDisabled).Error)
	require.NoError(t, model.RequestChannelAssetCleanup(t.Context(), channel.Id))
	task := model.Task{TaskID: "active-consumer", Status: model.TaskStatusSubmitted}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Create(&model.TaskSubmission{TaskID: task.TaskID, ChannelID: channel.Id, SpaceID: space.ID, Phase: model.SubmissionComplete}).Error)
	require.NoError(t, CleanupChannelAssets(t.Context(), "cleanup-worker"))
	assert.False(t, deleted[channel.Name], "an accepted video retains its material group")
	require.NoError(t, db.Model(&task).Update("status", model.TaskStatusSuccess).Error)
	require.NoError(t, db.Model(space).Update("next_attempt", 0).Error)
	require.NoError(t, CleanupChannelAssets(t.Context(), "cleanup-worker"))
	assert.True(t, deleted[channel.Name])
	_, err = model.GetChannelAssetSpace(t.Context(), channel.Id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = model.GetAsset(t.Context(), asset.ID)
	require.NoError(t, err, "channel cleanup must preserve the original")
	var remaining int64
	require.NoError(t, db.Model(&model.AssetReplica{}).Where("asset_id = ?", asset.ID).Count(&remaining).Error)
	assert.EqualValues(t, 3, remaining, "other channels keep their replicas")
}
