package system_setting

import "github.com/QuantumNous/new-api/dto"

const AssetStorageOption = "ArkAssetStorageSecret"
const AssetChannelOptionPrefix = "ArkAssetChannel."

// These records are stored only through the asset configuration service. Public
// configuration responses use separate DTOs so secrets cannot be serialized.
type AssetStorageConfig struct {
	dto.AssetStorageFields
	SecretKey    string `json:"secret_key"`
	CursorSecret string `json:"cursor_secret"`
}

type AssetChannelConfig struct {
	dto.AssetChannelFields
	SecretKey    string `json:"secret_key"`
	ChannelScope string `json:"channel_scope"`
}
