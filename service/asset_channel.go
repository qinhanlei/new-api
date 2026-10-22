package service

import (
	"context"
	"errors"
	"reflect"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/assetprovider"
	"gorm.io/gorm"
)

func ChannelAssetProviderClient(channel *model.Channel, space *model.AssetSpace) (assetprovider.Client, error) {
	config, err := channel.AssetConfiguration()
	if err != nil {
		return nil, err
	}
	if space != nil && (space.ChannelID != channel.Id || space.BaseURL != config.BaseURL || space.ProjectName != config.ProjectName) {
		return nil, model.ErrChannelAssetScopeInUse
	}
	settings := channel.GetSetting()
	client, err := GetHttpClientWithProxySettings(settings.Proxy, settings)
	if err != nil {
		return nil, err
	}
	providerConfig := assetprovider.Config{BaseURL: config.BaseURL, ProjectName: config.ProjectName, AuthMode: config.AuthMode, APIKey: config.APIKey, SecretKey: config.SecretKey}
	if space != nil {
		providerConfig.GroupID = space.GroupID
	}
	return assetprovider.New(providerConfig, client)
}

// ValidateChannelAssetChange performs the remote read before the database
// transaction; the model verifies the exact snapshot again under its row lock.
func ValidateChannelAssetChange(ctx context.Context, original, update *model.Channel) error {
	candidate := update.AssetUpdateCandidate(original)
	if err := candidate.ValidateAssetSettings(); err != nil {
		return err
	}
	space, err := model.GetChannelAssetSpace(ctx, original.Id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	config, err := candidate.AssetConfiguration()
	if err != nil || config.BaseURL != space.BaseURL || config.ProjectName != space.ProjectName || candidate.Type != original.Type || candidate.GetBaseURL() != original.GetBaseURL() || !reflect.DeepEqual(candidate.GetHeaderOverride(), original.GetHeaderOverride()) {
		return model.ErrChannelAssetScopeInUse
	}
	if config.AuthMode == "aksk" && candidate.Key != original.Key {
		return model.ErrChannelAssetScopeInUse
	}
	oldConfig, err := original.AssetConfiguration()
	if err != nil {
		return model.ErrChannelAssetScopeInUse
	}
	if config.AuthMode == oldConfig.AuthMode && config.APIKey == oldConfig.APIKey && config.SecretKey == oldConfig.SecretKey {
		return nil
	}
	if space.GroupState != model.AssetGroupReady || space.GroupID == "" {
		return model.ErrChannelAssetRotation
	}
	client, err := ChannelAssetProviderClient(candidate, space)
	if err != nil {
		return model.ErrChannelAssetRotation
	}
	group, err := client.GetGroup(ctx, space.GroupID)
	if err != nil || group.ID != space.GroupID || group.Name != space.ID || group.ProjectName != space.ProjectName || group.Type != "AIGC" {
		return model.ErrChannelAssetRotation
	}
	update.ConfirmAssetCredentialRotation(original, space)
	return nil
}
