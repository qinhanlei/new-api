package model

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestAssetDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "asset_catalog_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousType := DB, common.MainDatabaseType()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			t.Cleanup(func() {
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				require.NoError(t, db.Migrator().DropTable(&AssetReplica{}, &AssetSpace{}, &Asset{}, &Ability{}, &Channel{}, &User{}))
				require.NoError(t, sqlDB.Close())
			})
			// Existing released users must survive addition and repeated migration of
			// the three new tables. Their schema is deliberately left unchanged.
			require.NoError(t, db.Migrator().CreateTable(&User{}))
			require.NoError(t, db.Create(&User{Id: 7001, Username: "asset-owner"}).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&Asset{}, &AssetSpace{}, &AssetReplica{}, &Channel{}, &Ability{}))
			}
			var user User
			require.NoError(t, db.First(&user, 7001).Error)
			assert.Equal(t, "asset-owner", user.Username)
			ctx := context.Background()
			asset := &Asset{ID: "na_asset_matrix", UserID: 7001, Name: "example", Kind: "image", Size: 20, Status: AssetStatusReady, StorageBackend: AssetStorageLocal}
			require.NoError(t, ReserveAsset(ctx, asset, 1, 100))
			persisted, err := GetAsset(ctx, asset.ID)
			require.NoError(t, err)
			assert.Equal(t, AssetStorageLocal, persisted.StorageBackend)
			_, err = GetUserAsset(ctx, 7002, asset.ID)
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			require.ErrorIs(t, RenameUserAsset(ctx, 7002, asset.ID, "stolen"), gorm.ErrRecordNotFound)
			require.ErrorIs(t, DeleteUserAsset(ctx, 7002, asset.ID), gorm.ErrRecordNotFound)
			require.ErrorIs(t, ReserveAsset(ctx, &Asset{ID: "na_asset_second", UserID: 7001, Size: 10}, 1, 100), ErrAssetLimit)
			require.ErrorIs(t, ReserveAsset(ctx, &Asset{ID: "na_asset_second", UserID: 7001, Size: 90}, 10, 100), ErrAssetLimit)
			uploading := Asset{ID: "na_asset_uploading", UserID: 7001, Status: AssetStatusUploading}
			require.NoError(t, db.Create(&uploading).Error)
			require.ErrorIs(t, DeleteUserAsset(ctx, 7001, uploading.ID), ErrAssetUploading)
			channel := Channel{Type: constant.ChannelTypeDoubaoVideo, Name: "asset channel", Key: "inference-key", Status: common.ChannelStatusEnabled, AssetCredentials: &ChannelAssetCredentials{AccessKey: "access-key", SecretKey: "secret-key"}, HeaderOverride: common.GetPointer(`{"Authorization":"Bearer inference-key"}`), Tag: common.GetPointer("asset-mixed")}
			require.NoError(t, db.Create(&channel).Error)
			space, err := EnsureChannelAssetSpace(ctx, channel.Id)
			require.NoError(t, err)
			reused, err := EnsureChannelAssetSpace(ctx, channel.Id)
			require.NoError(t, err)
			assert.Equal(t, space.ID, reused.ID)
			require.NoError(t, db.AutoMigrate(&Asset{}, &AssetSpace{}, &AssetReplica{}))
			stored, err := GetAssetSpace(ctx, space.ID)
			require.NoError(t, err)
			assert.Equal(t, channel.Id, stored.ChannelID)
			assert.Equal(t, OfficialAssetEndpoint, stored.BaseURL)
			require.Error(t, db.Create(&AssetSpace{ID: "duplicate-scope", ChannelID: channel.Id}).Error)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return LockChannelAssetScope(tx, channel.Id, space.ID) }))
			patch := Channel{Id: channel.Id}
			patch.SetSetting(dto.ChannelSettings{AssetProject: "different-project"})
			require.ErrorIs(t, patch.Update(), ErrChannelAssetScopeInUse)
			videoKeyChange := Channel{Id: channel.Id, Key: "unverified-inference-account"}
			require.ErrorIs(t, videoKeyChange.Update(), ErrChannelAssetScopeInUse)
			for _, header := range []string{`{"Authorization":"Bearer different-account"}`, ""} {
				headerChange := Channel{Id: channel.Id, HeaderOverride: &header}
				require.ErrorIs(t, headerChange.Update(), ErrChannelAssetScopeInUse)
			}
			for _, header := range []*string{nil, common.GetPointer(`{ "Authorization": "Bearer inference-key" }`)} {
				unchangedHeader := Channel{Id: channel.Id, HeaderOverride: header}
				require.NoError(t, unchangedHeader.Update())
				assert.Equal(t, channel.GetHeaderOverride(), unchangedHeader.GetHeaderOverride())
			}
			plain := Channel{Type: constant.ChannelTypeOpenAI, Name: "plain channel", Key: "plain-key", Tag: channel.Tag}
			require.NoError(t, db.Create(&plain).Error)
			require.ErrorIs(t, EditChannelByTag(channel.GetTag(), common.GetPointer("moved"), nil, common.GetPointer("new-model"), nil, nil, nil, nil, common.GetPointer(`{"Authorization":"Bearer different-account"}`)), ErrChannelAssetScopeInUse)
			for _, original := range []*Channel{&channel, &plain} {
				var persisted Channel
				require.NoError(t, db.First(&persisted, original.Id).Error)
				assert.Equal(t, original.GetTag(), persisted.GetTag())
				assert.Equal(t, original.Models, persisted.Models)
				assert.Equal(t, original.GetHeaderOverride(), persisted.GetHeaderOverride(), "a rejected mixed batch must not mutate any channel")
			}
			for _, remove := range []func() error{
				channel.Delete,
				func() error { _, err := BatchDeleteChannels([]int{channel.Id}); return err },
				func() error { _, err := DeleteChannelByStatus(int64(common.ChannelStatusEnabled)); return err },
			} {
				require.ErrorIs(t, remove(), ErrChannelAssetScopeInUse)
			}
			require.NoError(t, db.Model(space).Updates(map[string]any{"group_id": "group-1", "group_state": AssetGroupReady}).Error)
			space.GroupID, space.GroupState = "group-1", AssetGroupReady
			rotation := Channel{Id: channel.Id, AssetCredentials: &ChannelAssetCredentials{AccessKey: "rotated-ak", SecretKey: "rotated-sk"}}
			require.ErrorIs(t, rotation.Update(), ErrChannelAssetRotation)
			rotation.ConfirmAssetCredentialRotation(&channel, space)
			require.NoError(t, rotation.Update())
			configured, err := GetChannelById(channel.Id, false)
			require.NoError(t, err)
			payload, err := common.Marshal(configured)
			require.NoError(t, err)
			assert.Contains(t, string(payload), `"asset_credentials_configured":true`)
			assert.NotContains(t, string(payload), "rotated-ak")
			assert.NotContains(t, string(payload), "rotated-sk")
			assert.Equal(t, "rotated-sk", configured.AssetCredentials.SecretKey)
			bound := Channel{Type: constant.ChannelTypeTaskPlugin, Name: "bound asset channel", Key: "inference-key", AssetCredentials: channel.AssetCredentials}
			bound.SetSetting(dto.ChannelSettings{TaskPluginKey: "doubao"})
			require.NoError(t, db.Create(&bound).Error)
			_, err = EnsureChannelAssetSpace(ctx, bound.Id)
			require.NoError(t, err)
			emptyHeader := Channel{Id: bound.Id, HeaderOverride: common.GetPointer("")}
			require.NoError(t, emptyHeader.Update(), "nil and empty header overrides are equivalent")
			changed, err := UnbindTaskPlugin(bound.Id, "doubao")
			require.Error(t, err)
			assert.False(t, changed)
			require.NoError(t, db.First(&bound, bound.Id).Error)
			assert.True(t, bound.GetSetting().BindsTaskPlugin("doubao"))
			replica, claimed, err := ClaimAssetReplica(ctx, asset.ID, space.ID, "worker-a", 100, 200)
			require.NoError(t, err)
			require.True(t, claimed)
			_, claimed, err = ClaimAssetReplica(ctx, asset.ID, space.ID, "worker-b", 101, 201)
			require.NoError(t, err)
			assert.False(t, claimed)
			require.Error(t, SaveAssetReplica(ctx, replica, "worker-b"))
			replica.Status, replica.RemoteID, replica.GenerationRef = AssetReplicaReady, "management-id", "asset://generation-id"
			require.NoError(t, SaveAssetReplica(ctx, replica, "worker-a"))
			_, claimed, err = ClaimAssetReplica(ctx, asset.ID, space.ID, "worker-c", 300, 400)
			require.NoError(t, err)
			assert.False(t, claimed)
			require.Error(t, db.Create(&AssetReplica{AssetID: asset.ID, SpaceID: space.ID}).Error, "duplicate mapping must be rejected")
			require.Error(t, RequestChannelAssetCleanup(ctx, channel.Id), "enabled channels must not enter cleanup")
			require.NoError(t, db.Model(&channel).Update("status", common.ChannelStatusManuallyDisabled).Error)
			require.NoError(t, RequestChannelAssetCleanup(ctx, channel.Id))
			require.Error(t, db.Transaction(func(tx *gorm.DB) error { return LockChannelAssetScope(tx, channel.Id, space.ID) }))
			channel.Status = common.ChannelStatusEnabled
			require.ErrorIs(t, channel.saveStatusState(), ErrChannelAssetCleaning)
			_, err = DeleteDisabledChannel()
			require.ErrorIs(t, err, ErrChannelAssetScopeInUse)
			now := time.Now().Unix()
			uncertain, claimed, err := ClaimAssetReplica(ctx, asset.ID, "aspace_uncertain", "worker-d", now, now+60)
			require.NoError(t, err)
			require.True(t, claimed)
			require.NoError(t, MarkAssetReplicaCreating(ctx, asset.ID, uncertain.SpaceID, "worker-d"))
			_, claimed, err = ClaimAssetReplica(ctx, asset.ID, uncertain.SpaceID, "worker-e", now+61, now+120)
			require.NoError(t, err)
			assert.False(t, claimed, "an ambiguous import must not be replayed after its lease expires")
			require.NoError(t, DeleteUserAsset(ctx, 7001, asset.ID))
			_, err = GetUserAsset(ctx, 7001, asset.ID)
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			retained, err := GetAsset(ctx, asset.ID)
			require.NoError(t, err)
			assert.Equal(t, AssetStatusDeleted, retained.Status)
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
		})
	}
}

func TestChannelAssetConfiguration(t *testing.T) {
	channel := Channel{Type: constant.ChannelTypeDoubaoVideo, Key: "inference-key"}
	assert.False(t, channel.SupportsAssets(), "official management requires AK/SK")
	channel.AssetCredentials = &ChannelAssetCredentials{AccessKey: "access-key", SecretKey: "secret-key"}
	config, err := channel.AssetConfiguration()
	require.NoError(t, err)
	assert.Equal(t, OfficialAssetEndpoint, config.BaseURL)
	assert.Equal(t, "default", config.ProjectName)
	assert.Equal(t, "aksk", config.AuthMode)
	channel.BaseURL = common.GetPointer("https://upstream.example")
	assert.False(t, channel.SupportsAssets(), "custom gateways must declare their management endpoint")
	channel.SetSetting(dto.ChannelSettings{AssetEndpoint: "https://upstream.example/openapi/"})
	channel.AssetCredentials = nil
	config, err = channel.AssetConfiguration()
	require.NoError(t, err)
	assert.Equal(t, "bearer", config.AuthMode)
	assert.Equal(t, channel.Key, config.APIKey)
	channel.ChannelInfo.IsMultiKey = true
	assert.False(t, channel.SupportsAssets())
	channel.ChannelInfo.IsMultiKey = false
	channel.Type = constant.ChannelTypeNewAPI
	assert.False(t, channel.SupportsAssets())
	channel.SetSetting(dto.ChannelSettings{AssetEndpoint: "https://upstream.example/openapi/", TaskExtendPluginKeys: []string{"doubao"}})
	assert.True(t, channel.SupportsAssets())
	channel.Setting = common.GetPointer(`{"asset_endpoint":"https://upstream.example/?secret=bad"}`)
	assert.False(t, channel.SupportsAssets())
}
