package service

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func localAssetTestEnvironment(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"ASSET_STORAGE", "ASSET_S3_ENDPOINT", "ASSET_S3_BUCKET", "ASSET_S3_REGION", "ASSET_S3_ACCESS_KEY_ID", "ASSET_S3_SECRET_ACCESS_KEY", "ASSET_S3_SESSION_TOKEN", "ASSET_S3_URL_TTL_SECONDS"} {
		t.Setenv(name, "")
	}
	directory := filepath.Join(t.TempDir(), "sources")
	t.Setenv("ASSET_LOCAL_DIR", directory)
	previousEnabled, previousAddress, previousPublic := common.AssetLibraryEnabled, system_setting.ServerAddress, system_setting.TaskPublicAddress
	previousMaster := common.IsMasterNode
	common.AssetLibraryEnabled = true
	common.IsMasterNode = true
	system_setting.ServerAddress, system_setting.TaskPublicAddress = "https://gateway.example", ""
	t.Cleanup(func() {
		common.AssetLibraryEnabled, system_setting.ServerAddress, system_setting.TaskPublicAddress = previousEnabled, previousAddress, previousPublic
		common.IsMasterNode = previousMaster
	})
	return directory
}

func TestAssetStorageStartup(t *testing.T) {
	for _, tc := range []struct {
		name       string
		backend    string
		master     bool
		s3         bool
		invalidS3  bool
		libraryOff bool
		wantError  bool
		wantLocal  bool
	}{
		{name: "master default backend", master: true, wantLocal: true},
		{name: "slave default backend"},
		{name: "slave explicit local", backend: "local"},
		{name: "unknown backend is still rejected", backend: "invalid", wantError: true},
		{name: "master s3", backend: "s3", master: true, s3: true},
		{name: "slave s3", backend: "s3", s3: true},
		{name: "legacy s3", s3: true},
		{name: "incomplete s3 is still rejected", backend: "s3", wantError: true},
		{name: "legacy incomplete s3 is still rejected", s3: true, invalidS3: true, wantError: true},
		{name: "master local overrides legacy s3", backend: "local", master: true, s3: true, invalidS3: true, wantLocal: true},
		{name: "slave local overrides legacy s3", backend: "local", s3: true, invalidS3: true},
		{name: "disabled library has no storage requirement", backend: "invalid", master: true, libraryOff: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := localAssetTestEnvironment(t)
			common.IsMasterNode = tc.master
			common.AssetLibraryEnabled = !tc.libraryOff
			t.Setenv("ASSET_STORAGE", tc.backend)
			if tc.s3 {
				t.Setenv("ASSET_S3_ENDPOINT", "https://objects.example")
				t.Setenv("ASSET_S3_BUCKET", "sources")
				t.Setenv("ASSET_S3_ACCESS_KEY_ID", "fixture")
				t.Setenv("ASSET_S3_SECRET_ACCESS_KEY", "fixture-secret")
				if tc.invalidS3 {
					t.Setenv("ASSET_S3_SECRET_ACCESS_KEY", "")
				}
			}
			err := InitAssetStorage()
			if tc.wantError {
				require.ErrorIs(t, err, ErrAssetStorageUnavailable)
			} else {
				require.NoError(t, err)
			}
			if tc.wantLocal {
				key, err := os.ReadFile(filepath.Join(directory, localAssetSigningKey))
				require.NoError(t, err)
				assert.Len(t, key, 32)
			} else {
				_, err := os.Stat(directory)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			if !tc.master {
				_, err := GetAssetStoreForBackend(model.AssetStorageLocal)
				require.ErrorIs(t, err, ErrAssetStorageUnavailable)
			}
			if tc.s3 && !tc.invalidS3 && tc.backend != "local" {
				store, err := GetAssetStore()
				require.NoError(t, err)
				assert.Equal(t, model.AssetStorageS3, store.Backend())
				_, err = store.Presign(t.Context(), "assets/na_asset_fixture")
				require.NoError(t, err, "node role must not restrict S3 access")
			}
		})
	}
}

func TestAssetStorageSelection(t *testing.T) {
	directory := localAssetTestEnvironment(t)
	// Even a plausible anonymous capability cannot initialize local storage in
	// an S3-only deployment or regenerate a missing key.
	id := NewAssetID("na_asset_")
	expires := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	_, _, err := OpenLocalAssetContent(t.Context(), id, expires, strings.Repeat("A", 43))
	require.ErrorIs(t, err, ErrAssetStorageUnavailable)
	_, err = os.Stat(directory)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.MkdirAll(directory, 0700))
	_, _, err = OpenLocalAssetContent(t.Context(), id, expires, strings.Repeat("A", 43))
	require.ErrorIs(t, err, ErrAssetStorageUnavailable)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	assert.Empty(t, entries)
	store, err := GetAssetStore()
	require.NoError(t, err)
	assert.Equal(t, model.AssetStorageLocal, store.Backend())
	// A legacy S3-only deployment cannot silently change its write location.
	t.Setenv("ASSET_S3_ENDPOINT", "https://storage.example")
	_, err = GetAssetStore()
	require.ErrorIs(t, err, ErrAssetStorageUnavailable)
	t.Setenv("ASSET_S3_BUCKET", "sources")
	t.Setenv("ASSET_S3_ACCESS_KEY_ID", "fixture")
	t.Setenv("ASSET_S3_SECRET_ACCESS_KEY", "fixture-secret")
	store, err = GetAssetStore()
	require.NoError(t, err)
	assert.Equal(t, model.AssetStorageS3, store.Backend())
	t.Setenv("ASSET_STORAGE", "local")
	store, err = GetAssetStore()
	require.NoError(t, err)
	assert.Equal(t, model.AssetStorageLocal, store.Backend())
	legacy, err := GetAssetStoreForBackend("")
	require.NoError(t, err)
	assert.Equal(t, model.AssetStorageS3, legacy.Backend())
	t.Setenv("ASSET_STORAGE", "invalid")
	_, err = GetAssetStore()
	require.ErrorIs(t, err, ErrAssetStorageUnavailable)
	t.Setenv("ASSET_STORAGE", "s3")
	t.Setenv("ASSET_S3_SECRET_ACCESS_KEY", "")
	_, err = GetAssetStore()
	require.ErrorIs(t, err, ErrAssetStorageUnavailable)
	common.AssetLibraryEnabled = false
	_, err = GetAssetStoreForBackend(model.AssetStorageLocal)
	require.ErrorIs(t, err, ErrAssetStorageUnavailable)
}

func TestLocalAssetImmutableStorage(t *testing.T) {
	directory := localAssetTestEnvironment(t)
	store, err := GetAssetStore()
	require.NoError(t, err)
	key := "assets/" + NewAssetID("na_asset_")
	require.NoError(t, store.Put(t.Context(), key, strings.NewReader("source"), 6, "image/png"))
	require.Error(t, store.Put(t.Context(), key, strings.NewReader("replace"), 7, "image/png"))
	content, err := os.ReadFile(filepath.Join(directory, key))
	require.NoError(t, err)
	assert.Equal(t, "source", string(content))
	for _, key := range []string{"../outside", "/absolute", "assets/../.signing-key", "assets/.signing-key"} {
		require.Error(t, store.Put(t.Context(), key, strings.NewReader("source"), 6, "image/png"))
		require.Error(t, store.Delete(t.Context(), key))
		_, err := store.Presign(t.Context(), key)
		require.Error(t, err)
	}
	for _, size := range []int64{2, 20} {
		require.Error(t, store.Put(t.Context(), "assets/"+NewAssetID("na_asset_"), strings.NewReader("source"), size, "image/png"))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Error(t, store.Put(ctx, "assets/"+NewAssetID("na_asset_"), strings.NewReader("source"), 6, "image/png"))
	require.NoError(t, store.Delete(t.Context(), key))
	require.NoError(t, store.Delete(t.Context(), key))
	_, err = os.Stat(filepath.Join(directory, key))
	require.ErrorIs(t, err, os.ErrNotExist)
	// A missing/corrupt key must not be replaced while existing store state remains.
	require.NoError(t, os.WriteFile(filepath.Join(directory, ".signing-key"), []byte("damaged"), 0600))
	_, err = GetAssetStore()
	require.ErrorIs(t, err, ErrAssetStorageUnavailable)
	require.NoError(t, os.Remove(filepath.Join(directory, ".signing-key")))
	_, err = GetAssetStore()
	require.ErrorIs(t, err, ErrAssetStorageUnavailable)
}

func TestLocalAssetDownloadLifecycle(t *testing.T) {
	directory := localAssetTestEnvironment(t)
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
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Asset{}, &model.Task{}, &model.TaskAssetUse{}))
	require.NoError(t, db.Create(&model.User{Id: 41, Username: "owner"}).Error)

	// Concurrent initialization must preserve the same signing key.
	stores, failures := make([]AssetStore, 2), make([]error, 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := range 2 {
		group.Go(func() { <-start; stores[i], failures[i] = GetAssetStore() })
	}
	close(start)
	group.Wait()
	for _, err := range failures {
		require.NoError(t, err)
	}
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	asset, err := CreateAsset(t.Context(), 41, "source", bytes.NewReader(encoded.Bytes()), stores[0])
	require.NoError(t, err)
	for _, store := range stores {
		signed, err := store.Presign(t.Context(), asset.ObjectKey)
		require.NoError(t, err)
		parsed, err := url.Parse(signed)
		require.NoError(t, err)
		_, file, err := OpenLocalAssetContent(t.Context(), asset.ID, parsed.Query().Get("expires"), parsed.Query().Get("access"))
		require.NoError(t, err)
		content, err := io.ReadAll(file)
		require.NoError(t, err)
		require.NoError(t, file.Close())
		assert.Equal(t, encoded.Bytes(), content)
	}
	signed, err := stores[0].Presign(t.Context(), asset.ObjectKey)
	require.NoError(t, err)
	parsed, err := url.Parse(signed)
	require.NoError(t, err)
	expires, access := parsed.Query().Get("expires"), parsed.Query().Get("access")
	// Slaves cannot serve even a valid source URL with the original key and file.
	common.IsMasterNode = false
	_, _, err = OpenLocalAssetContent(t.Context(), asset.ID, expires, access)
	require.ErrorIs(t, err, ErrAssetStorageUnavailable)
	common.IsMasterNode = true
	// Reopening storage (or changing the write default) must preserve existing links.
	t.Setenv("ASSET_STORAGE", "s3")
	_, file, err := OpenLocalAssetContent(t.Context(), asset.ID, expires, access)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	for _, tc := range []struct{ id, expires, access string }{
		{asset.ID, "1", access}, {asset.ID, "0" + expires, access},
		{asset.ID, expires, strings.Repeat("A", 43)}, {NewAssetID("na_asset_"), expires, access},
	} {
		// Invalid capabilities must be rejected before any database operation.
		model.DB = nil
		_, _, err := OpenLocalAssetContent(t.Context(), tc.id, tc.expires, tc.access)
		model.DB = db
		require.ErrorIs(t, err, ErrAssetAccessInvalid)
	}
	for _, status := range []string{model.AssetStatusUploading, model.AssetStatusFailed} {
		require.NoError(t, db.Model(asset).Update("status", status).Error)
		_, _, err := OpenLocalAssetContent(t.Context(), asset.ID, expires, access)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	}
	require.NoError(t, db.Model(asset).Update("status", model.AssetStatusReady).Error)
	require.NoError(t, db.Create(&model.Task{TaskID: "consumer", Status: model.TaskStatusWaitingAssets}).Error)
	require.NoError(t, db.Create(&model.TaskAssetUse{TaskID: "consumer", AssetID: asset.ID}).Error)
	require.NoError(t, model.DeleteUserAsset(t.Context(), 41, asset.ID))
	_, file, err = OpenLocalAssetContent(t.Context(), asset.ID, expires, access)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	_, err = model.GetUserAsset(t.Context(), 41, asset.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "consumer").Update("status", model.TaskStatusSuccess).Error)
	_, _, err = OpenLocalAssetContent(t.Context(), asset.ID, expires, access)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	// Even a valid capability cannot escape the configured root through a symlink.
	require.NoError(t, db.Model(asset).Updates(map[string]any{"status": model.AssetStatusReady, "deleted_at": 0}).Error)
	require.NoError(t, os.Remove(filepath.Join(directory, asset.ObjectKey)))
	outside := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.WriteFile(outside, encoded.Bytes(), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(directory, asset.ObjectKey)))
	_, _, err = OpenLocalAssetContent(t.Context(), asset.ID, expires, access)
	require.ErrorIs(t, err, ErrAssetStorageUnavailable)
}

func TestLocalAssetPublicAddress(t *testing.T) {
	localAssetTestEnvironment(t)
	store, err := GetAssetStore()
	require.NoError(t, err)
	key := "assets/" + NewAssetID("na_asset_")
	for _, address := range []string{"http://localhost:3000", "https://localhost", "https://127.0.0.1", "https://10.0.0.1", "https://user:secret@gateway.example", "https://gateway.example?extra=1"} {
		system_setting.ServerAddress = address
		_, err := store.Presign(t.Context(), key)
		require.ErrorIs(t, err, ErrAssetStorageUnavailable)
	}
	system_setting.TaskPublicAddress = "https://downloads.example/prefix%2Fpart"
	signed, err := store.Presign(t.Context(), key)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(signed, "https://downloads.example/prefix%2Fpart/v1/assets/"))
}
