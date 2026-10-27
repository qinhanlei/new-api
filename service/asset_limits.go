package service

import (
	"context"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
)

// Keep byte conversion and multipart overhead within JavaScript's exact integer
// range as well as int64. This is a representation bound, not a storage quota.
const maxAssetSizeMB int64 = (1<<53 - 1 - (1 << 20)) / 1_000_000

// GetAssetSizeLimits applies one upload policy across all enabled material
// channels. Ownership and channel permissions are enforced independently.
func GetAssetSizeLimits(ctx context.Context) (dto.AssetSizeLimits, error) {
	var limits dto.AssetSizeLimits
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return limits, err
	}
	found := false
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled {
			continue
		}
		config, err := model.LoadAssetChannelConfig(ctx, channel.Id)
		if err != nil {
			return limits, err
		}
		if !IsAssetChannelEnabled(channel, &config) {
			continue
		}
		if err = validateAssetSizeLimits(config.SizeLimitsMB); err != nil {
			return limits, err
		}
		next := config.SizeLimitsMB.WithDefaults()
		if !found {
			limits, found = next, true
			continue
		}
		limits.Image = min(limits.Image, next.Image)
		limits.Video = min(limits.Video, next.Video)
		limits.Audio = min(limits.Audio, next.Audio)
	}
	return limits.WithDefaults(), nil
}

func validateAssetSizeLimits(limits dto.AssetSizeLimits) error {
	for _, value := range []int64{limits.Image, limits.Video, limits.Audio} {
		if value < 0 || value > maxAssetSizeMB {
			return assetError(http.StatusBadRequest, "InvalidParameter.SizeLimits", "Material size limits must be non-negative whole numbers within the supported range")
		}
	}
	return nil
}

func validateAssetSize(size int64, assetType string, limits dto.AssetSizeLimits) error {
	if size > limits.ByteLimit(assetType) {
		return assetError(http.StatusBadRequest, "FileSizeTooLarge", "Material exceeds the file size limit")
	}
	return nil
}
