package model

import (
	"crypto/sha256"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

const OfficialAssetEndpoint = "https://ark.cn-beijing.volcengineapi.com/"

var (
	ErrChannelAssetsUnavailable = common.NewMessage("Channel asset management is not configured")
	ErrChannelAssetScopeInUse   = common.NewMessage("Clean up channel assets before changing its account, endpoint, or project")
	ErrChannelAssetRotation     = common.NewMessage("New credentials must retain access to the channel asset group")
	ErrChannelAssetCleaning     = common.NewMessage("Channel assets are being cleaned up")
)

type ChannelAssetCredentials struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

type ChannelAssetConfig struct {
	BaseURL, ProjectName, AuthMode, APIKey, SecretKey string
}

// MarshalJSON never exposes management credentials, including responses from
// less common channel endpoints. Internal channel caching does not use JSON.
func (channel Channel) MarshalJSON() ([]byte, error) {
	type channelJSON Channel
	return common.Marshal(struct {
		channelJSON
		AssetCredentials           *ChannelAssetCredentials `json:"asset_credentials,omitempty"`
		AssetCredentialsConfigured bool                     `json:"asset_credentials_configured"`
	}{channelJSON: channelJSON(channel), AssetCredentialsConfigured: channel.AssetCredentials != nil && channel.AssetCredentials.AccessKey != "" && channel.AssetCredentials.SecretKey != ""})
}

func (channel *Channel) assetSettings() (dto.ChannelSettings, error) {
	var settings dto.ChannelSettings
	if channel.Setting != nil && *channel.Setting != "" {
		if err := common.UnmarshalJsonStr(*channel.Setting, &settings); err != nil {
			return settings, ErrChannelAssetsUnavailable
		}
	}
	return settings, nil
}

func (channel *Channel) SupportsAssets() bool {
	_, err := channel.AssetConfiguration()
	return err == nil
}

func (channel *Channel) AssetConfiguration() (ChannelAssetConfig, error) {
	settings, err := channel.assetSettings()
	if err != nil {
		return ChannelAssetConfig{}, err
	}
	config := ChannelAssetConfig{BaseURL: strings.TrimSpace(settings.AssetEndpoint), ProjectName: strings.TrimSpace(settings.AssetProject), AuthMode: "bearer", APIKey: channel.Key}
	if err := channel.ValidateAssetSettings(); err != nil {
		return config, err
	}
	if channel.Type != constant.ChannelTypeDoubaoVideo && !((channel.Type == constant.ChannelTypeTaskPlugin || channel.Type == constant.ChannelTypeNewAPI) && settings.BindsTaskPlugin("doubao")) {
		return config, ErrChannelAssetsUnavailable
	}
	if channel.ChannelInfo.IsMultiKey || strings.ContainsAny(channel.Key, "\r\n") {
		return config, ErrChannelAssetsUnavailable
	}
	if config.ProjectName == "" {
		config.ProjectName = "default"
	}
	if config.BaseURL == "" {
		base := strings.TrimRight(channel.GetBaseURL(), "/")
		if base != "https://ark.cn-beijing.volces.com" && base != "https://ark.cn-beijing.volces.com/api/v3" && !(base == "" && channel.Type != constant.ChannelTypeNewAPI) {
			return config, ErrChannelAssetsUnavailable
		}
		config.BaseURL = OfficialAssetEndpoint
	}
	if channel.AssetCredentials != nil && channel.AssetCredentials.AccessKey != "" && channel.AssetCredentials.SecretKey != "" {
		config.AuthMode = "aksk"
		config.APIKey, config.SecretKey = channel.AssetCredentials.AccessKey, channel.AssetCredentials.SecretKey
	}
	if config.APIKey == "" || (strings.TrimRight(config.BaseURL, "/") == strings.TrimRight(OfficialAssetEndpoint, "/") && config.AuthMode != "aksk") {
		return config, ErrChannelAssetsUnavailable
	}
	return config, nil
}

func (channel *Channel) ValidateAssetSettings() error {
	settings, err := channel.assetSettings()
	if err != nil {
		return err
	}
	endpoint := strings.TrimSpace(settings.AssetEndpoint)
	if endpoint != "" {
		target, err := url.Parse(endpoint)
		if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.RawQuery != "" || target.ForceQuery || target.Fragment != "" || len(endpoint) > 512 || strings.ContainsAny(endpoint, "\\\r\n\t ") {
			return common.NewMessage("Asset management endpoint must be an HTTPS URL without credentials, query, or fragment")
		}
	}
	if len(settings.AssetProject) > 255 || strings.ContainsAny(settings.AssetProject, "\x00\r\n") {
		return common.NewMessage("Invalid asset project")
	}
	if credentials := channel.AssetCredentials; credentials != nil {
		if (credentials.AccessKey == "") != (credentials.SecretKey == "") || len(credentials.AccessKey) > 4096 || len(credentials.SecretKey) > 4096 || strings.ContainsAny(credentials.AccessKey+credentials.SecretKey, "\x00\r\n") {
			return common.NewMessage("Asset management requires both an access key and a secret key")
		}
	}
	if endpoint == "" && settings.AssetProject == "" && (channel.AssetCredentials == nil || channel.AssetCredentials.AccessKey == "") {
		return nil
	}
	if channel.ChannelInfo.IsMultiKey {
		return common.NewMessage("Asset management requires a single channel credential")
	}
	if channel.Type != constant.ChannelTypeDoubaoVideo && !((channel.Type == constant.ChannelTypeTaskPlugin || channel.Type == constant.ChannelTypeNewAPI) && settings.BindsTaskPlugin("doubao")) {
		return common.NewMessage("Asset management requires a Doubao task channel")
	}
	return nil
}

// AssetUpdateCandidate mirrors the nonzero-field semantics of Channel.Update.
func (channel *Channel) AssetUpdateCandidate(original *Channel) *Channel {
	merged := *original
	if channel.Type != 0 {
		merged.Type = channel.Type
	}
	if channel.Key != "" {
		merged.Key = channel.Key
	}
	if channel.BaseURL != nil {
		merged.BaseURL = channel.BaseURL
	}
	if channel.Setting != nil {
		merged.Setting = channel.Setting
	}
	if channel.HeaderOverride != nil {
		merged.HeaderOverride = channel.HeaderOverride
	}
	if channel.AssetCredentials != nil {
		merged.AssetCredentials = channel.AssetCredentials
	}
	merged.ChannelInfo = channel.ChannelInfo
	return &merged
}

// ConfirmAssetCredentialRotation binds a successful upstream read to the exact
// old/new credentials and scope. The transaction rejects a concurrent change.
func (channel *Channel) ConfirmAssetCredentialRotation(original *Channel, space *AssetSpace) {
	channel.assetCredentialValidation = channel.assetRotationFingerprint(original, space)
}

func (channel *Channel) assetRotationFingerprint(original *Channel, space *AssetSpace) [32]byte {
	updated := channel.AssetUpdateCandidate(original)
	data, _ := common.Marshal([]any{original.Id, original.Key, original.AssetCredentials, original.GetBaseURL(), original.Setting, original.GetHeaderOverride(), updated.Key, updated.AssetCredentials, updated.GetBaseURL(), updated.Setting, updated.GetHeaderOverride(), space.ID, space.GroupID, space.GroupState})
	return sha256.Sum256(data)
}

func (channel *Channel) guardAssetUpdate(tx *gorm.DB, original *Channel, replace bool) error {
	updated := channel.AssetUpdateCandidate(original)
	if replace {
		updated = channel
	}
	if err := updated.ValidateAssetSettings(); err != nil {
		return err
	}
	var space AssetSpace
	err := lockForUpdate(tx).Where("channel_id = ?", channel.Id).First(&space).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	config, configErr := updated.AssetConfiguration()
	if configErr != nil || config.BaseURL != space.BaseURL || config.ProjectName != space.ProjectName || updated.GetBaseURL() != original.GetBaseURL() || updated.Type != original.Type || !reflect.DeepEqual(updated.GetHeaderOverride(), original.GetHeaderOverride()) {
		return ErrChannelAssetScopeInUse
	}
	// An AK/SK group read cannot establish the account/project of a different
	// inference API key. Reusing the scope after that change would be a guess.
	if config.AuthMode == "aksk" && updated.Key != original.Key {
		return ErrChannelAssetScopeInUse
	}
	oldConfig, err := original.AssetConfiguration()
	if err != nil {
		return ErrChannelAssetScopeInUse
	}
	if config.AuthMode == oldConfig.AuthMode && config.APIKey == oldConfig.APIKey && config.SecretKey == oldConfig.SecretKey {
		return nil
	}
	if space.GroupState != AssetGroupReady || space.GroupID == "" || space.LeaseUntil > time.Now().Unix() || channel.assetCredentialValidation != channel.assetRotationFingerprint(original, &space) {
		return ErrChannelAssetRotation
	}
	var count int64
	if err := tx.Model(&AssetReplica{}).Where("space_id = ? AND lease_until > ?", space.ID, time.Now().Unix()).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrChannelAssetRotation
	}
	return nil
}

func guardChannelAssetDeletion(tx *gorm.DB, ids []int) error {
	var channels []Channel
	if err := lockForUpdate(tx).Select("id").Where("id IN ?", ids).Order("id").Find(&channels).Error; err != nil {
		return err
	}
	var count int64
	if err := tx.Model(&AssetSpace{}).Where("channel_id IN ?", ids).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrChannelAssetScopeInUse
	}
	return nil
}

func guardChannelAssetEnable(tx *gorm.DB, ids []int) error {
	var channels []Channel
	if err := lockForUpdate(tx).Select("id").Where("id IN ?", ids).Order("id").Find(&channels).Error; err != nil {
		return err
	}
	var count int64
	if err := tx.Model(&AssetSpace{}).Where("channel_id IN ? AND group_state = ?", ids, AssetGroupCleaning).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrChannelAssetCleaning
	}
	return nil
}
