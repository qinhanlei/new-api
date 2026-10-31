package service

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// AssetPluginSupportsChannel uses the installed plugin's video bindings, not
// vendor model-name prefixes. Model aliases retain their declared capability.
func AssetPluginSupportsChannel(channel *model.Channel, modelName string) bool {
	if channel == nil || channel.ChannelInfo.IsMultiKey {
		return false
	}
	generation := jsplugin.DefaultRegistry.Generation()
	if generation == nil {
		return false
	}
	declared := modelName
	if canonical, ok := generation.CanonicalModel(modelName); ok {
		declared = canonical
	} else if alias, ok := model.ResolveTaskModelAlias(generation, modelName); ok {
		declared = alias.Declared
	}
	for _, binding := range generation.LookupEndpointCandidates(http.MethodPost, "/v1/videos", declared) {
		if !binding.Plugin.Meta.SupportsAssetReferences() {
			continue
		}
		if channel.Type == constant.ChannelTypeTaskPlugin || channel.Type == constant.ChannelTypeNewAPI {
			if channel.GetSetting().BindsTaskPlugin(binding.Plugin.Meta.Key) {
				return true
			}
		} else if owner, ok := generation.GetByChannelType(channel.Type); ok && owner == binding.Plugin {
			return true
		}
	}
	return false
}

// TaskAssetReferences contains only validated gateway identifiers. Candidate
// decoders and final submission compare their references with this snapshot.
func TaskAssetReferences(c *gin.Context) []string {
	value, _ := c.Get(jsplugin.ContextKeyAssetReferences)
	ids, _ := value.([]string)
	return ids
}

func selectAssetChannel(c *gin.Context, modelName string, retry *RetryParam) (*model.Channel, string, *ChannelSelectError) {
	actor, err := ResolveAssetActor(c)
	if err != nil {
		return nil, "", assetChannelError(c, err)
	}
	constraints := GetChannelConstraints(c)
	originalFilters := constraints.Filters
	groups := []string{retry.TokenGroup}
	if retry.TokenGroup == "auto" {
		groups = GetRequestAutoGroups(c, common.GetContextKeyString(c, constant.ContextKeyUserGroup))
	}
	eligible := make([]int, 0, len(actor.ChannelIDs))
	for _, id := range actor.ChannelIDs {
		if !model.IsChannelEnabledForAnyGroupModel(groups, modelName, id) {
			continue
		}
		channel, err := model.CacheGetChannel(id)
		if err != nil {
			continue
		}
		if matches, _ := model.ChannelSatisfiesFilters(channel, modelName, originalFilters); matches {
			eligible = append(eligible, id)
		}
	}
	actor.ChannelIDs = eligible
	constraints.Filters = append(slices.Clone(originalFilters), dto.ChannelFilter{Kind: dto.FilterAssetChannels, AssetChannelIDs: actor.ChannelIDs})
	defer func() { constraints.Filters = originalFilters }()
	// Finding the usual preference must not advance auto-group or affinity retry state
	// twice when another channel is the first one with ready replicas.
	retryBefore := *retry
	retryBefore.Retry = common.GetPointer(retry.GetRetry())
	selectionState := make(map[string]any)
	for _, key := range []string{string(constant.ContextKeyAutoGroup), string(constant.ContextKeyAutoGroupIndex), string(constant.ContextKeyAutoGroupRetryIndex), ginKeyChannelAffinityLogInfo, ginKeyChannelAffinitySkipRetry, ginKeyChannelAffinityRetryOffset} {
		selectionState[key], _ = c.Get(key)
	}
	preferred, group, selectErr := selectChannelForRequest(c, modelName, retry)
	if selectErr != nil {
		return nil, group, selectErr
	}
	ready, err := PrepareAssetRouting(c.Request.Context(), actor, TaskAssetReferences(c), preferred.Id)
	if err != nil {
		return nil, group, assetChannelError(c, err)
	}
	if slices.Contains(ready, preferred.Id) {
		return preferred, group, nil
	}
	*retry = retryBefore
	for key, value := range selectionState {
		c.Set(key, value)
	}
	constraints.Filters = append(constraints.Filters, dto.ChannelFilter{Kind: dto.FilterAssetChannels, AssetChannelIDs: ready})
	channel, group, selectErr := selectChannelForRequest(c, modelName, retry)
	if selectErr != nil {
		if !selectErr.NoAvailableChannel && selectErr.FilterKind != dto.FilterAssetChannels {
			return nil, group, selectErr
		}
		c.Header("Retry-After", "5")
		return nil, group, &ChannelSelectError{StatusCode: http.StatusConflict, Code: "AssetNotReady", Message: i18n.TSource(c, "Materials are being prepared for an eligible channel; retry later")}
	}
	return channel, group, nil
}

func assetChannelError(c *gin.Context, err error) *ChannelSelectError {
	if errors.Is(err, model.ErrAssetGroupUnavailable) {
		err = assetError(http.StatusConflict, "AssetGroupUnavailable", "The material group is being deleted")
	} else if errors.Is(err, model.ErrAssetChannelConflict) {
		err = assetError(http.StatusBadRequest, "AssetChannelConflict", "Referenced portraits must share one verification channel")
	}
	var assetErr *AssetError
	if errors.As(err, &assetErr) {
		if assetErr.Retryable {
			c.Header("Retry-After", "5")
		}
		return &ChannelSelectError{StatusCode: assetErr.HTTPStatus, Code: types.ErrorCode(assetErr.Code), Message: i18n.TSource(c, assetErr.Message)}
	}
	return &ChannelSelectError{StatusCode: http.StatusInternalServerError, Code: "AssetUnavailable", Message: i18n.TSource(c, "Asset preparation failed")}
}

// ResolveTaskAssets runs for every submit attempt, before plugin request
// building and quota reservation. The backend atomically protects these
// references from deletion until the task's submission outcome is known.
func ResolveTaskAssets(ctx context.Context, c *gin.Context, channelID int, ids []string, taskID string) ([]dto.ResolvedAssetReference, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	actor, err := ResolveAssetActor(c)
	if err != nil {
		return nil, err
	}
	return ResolveAssetReferences(ctx, actor, channelID, ids, taskID)
}
