package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelAssetCredentialRotation(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	originalDB, previousType := model.DB, common.MainDatabaseType()
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = originalDB
		common.SetMainDatabaseType(previousType)
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.AssetSpace{}, &model.AssetReplica{}))
	groupName := "na_space_test"
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GetAssetGroup", r.URL.Query().Get("Action"))
		assert.Equal(t, "Bearer new-key", r.Header.Get("Authorization"))
		body, err := common.Marshal(map[string]any{"Result": map[string]string{"Id": "group-1", "Name": groupName, "ProjectName": "default", "GroupType": "AIGC"}})
		require.NoError(t, err)
		_, err = w.Write(body)
		require.NoError(t, err)
	}))
	defer provider.Close()
	channel := model.Channel{Type: constant.ChannelTypeDoubaoVideo, Name: "assets", Key: "old-key", BaseURL: common.GetPointer(provider.URL), HeaderOverride: common.GetPointer(`{"Authorization":"Bearer old-key"}`)}
	channel.SetSetting(dto.ChannelSettings{AssetEndpoint: provider.URL, TLSInsecureSkipVerify: true})
	require.NoError(t, channel.Insert())
	space := model.AssetSpace{ID: groupName, ChannelID: channel.Id, BaseURL: provider.URL, ProjectName: "default", GroupID: "group-1", GroupState: model.AssetGroupReady}
	require.NoError(t, database.Create(&space).Error)
	for _, header := range []string{`{"Authorization":"Bearer different-account"}`, ""} {
		update := model.Channel{Id: channel.Id, HeaderOverride: &header}
		require.ErrorIs(t, ValidateChannelAssetChange(t.Context(), &channel, &update), model.ErrChannelAssetScopeInUse)
	}
	for _, header := range []*string{nil, common.GetPointer(`{ "Authorization": "Bearer old-key" }`)} {
		update := model.Channel{Id: channel.Id, HeaderOverride: header}
		require.NoError(t, ValidateChannelAssetChange(t.Context(), &channel, &update))
	}
	withoutHeader := channel
	withoutHeader.HeaderOverride = nil
	update := model.Channel{Id: channel.Id, HeaderOverride: common.GetPointer("")}
	require.NoError(t, ValidateChannelAssetChange(t.Context(), &withoutHeader, &update))
	patch := model.Channel{Id: channel.Id, Key: "new-key"}
	groupName = "unrelated-group"
	require.ErrorIs(t, ValidateChannelAssetChange(t.Context(), &channel, &patch), model.ErrChannelAssetRotation)
	groupName = space.ID
	require.NoError(t, ValidateChannelAssetChange(t.Context(), &channel, &patch))
	require.NoError(t, database.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("header_override", `{"Authorization":"Bearer different-account"}`).Error)
	require.ErrorIs(t, patch.Update(), model.ErrChannelAssetRotation, "a concurrent header change invalidates credential verification")
	require.NoError(t, database.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("header_override", channel.HeaderOverride).Error)
	// A different scope appearing after verification cannot use its proof.
	require.NoError(t, database.Model(&space).Update("group_id", "group-2").Error)
	require.ErrorIs(t, patch.Update(), model.ErrChannelAssetRotation)
	require.NoError(t, database.Model(&space).Update("group_id", "group-1").Error)
	require.NoError(t, ValidateChannelAssetChange(t.Context(), &channel, &patch))
	require.NoError(t, patch.Update())
	stored, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "new-key", stored.Key)
}
