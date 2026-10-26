package service

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

// ResolveAssetActor deliberately does not select a model for management
// actions. A channel is eligible only when at least one of its enabled video
// models is available to this caller. Generation narrows this set further.
func ResolveAssetActor(c *gin.Context) (dto.AssetActor, error) {
	actor, err := AssetActorForUser(c.Request.Context(), c.GetInt("id"), c.GetInt("token_id"))
	if err != nil {
		return actor, err
	}
	pin, pinned, _ := GetChannelConstraints(c).ResolvedPin()
	if pinned {
		if slices.Contains(actor.ChannelIDs, pin.ChannelId) {
			actor.ChannelIDs = []int{pin.ChannelId}
		} else {
			actor.ChannelIDs = nil
		}
	}
	return actor, nil
}

// AssetActorForUser also serves administrative actions on an explicitly
// selected owner. The controller authorizes impersonation before calling it.
func AssetActorForUser(ctx context.Context, userID, tokenID int) (dto.AssetActor, error) {
	actor := dto.AssetActor{UserID: userID, TokenID: tokenID}
	ids, err := authorizedAssetChannels(ctx, userID, tokenID, 0)
	actor.ChannelIDs = ids
	return actor, err
}

// AssetChannelAuthorized rechecks current permissions for durable work without
// retaining a caller's bearer token or treating an old channel list as a grant.
func AssetChannelAuthorized(ctx context.Context, userID, tokenID, channelID int) (bool, error) {
	ids, err := authorizedAssetChannels(ctx, userID, tokenID, channelID)
	return slices.Contains(ids, channelID), err
}

func authorizedAssetChannels(ctx context.Context, userID, tokenID, channelID int) ([]int, error) {
	user, err := model.GetUserById(userID, false)
	if err != nil || user.Status != common.UserStatusEnabled {
		return nil, &AssetError{HTTPStatus: http.StatusForbidden, Code: "AccessDenied", Message: "The asset owner is unavailable."}
	}
	groups := []string{user.Group}
	var modelLimits map[string]bool
	if tokenID > 0 {
		token, tokenErr := model.GetTokenById(tokenID)
		if tokenErr != nil || token.UserId != userID || token.Status != common.TokenStatusEnabled ||
			(token.ExpiredTime != -1 && token.ExpiredTime <= time.Now().Unix()) {
			return nil, &AssetError{HTTPStatus: http.StatusForbidden, Code: "AccessDenied", Message: "The asset API token is unavailable."}
		}
		if token.ModelLimitsEnabled {
			modelLimits = token.GetModelLimitsMap()
			if len(modelLimits) == 0 {
				return nil, nil
			}
		}
		if token.Group == "auto" {
			if token.AutoGroups == "" {
				groups = GetUserAutoGroup(user.Group)
			} else {
				configured, groupErr := token.GetAutoGroups()
				if groupErr != nil {
					return nil, errors.New("invalid token auto groups")
				}
				groups = FilterUserTokenAutoGroups(user.Group, configured)
			}
		} else if token.Group != "" {
			if !IsUserSelectableGroup(user.Group, token.Group) {
				return nil, nil
			}
			groups = []string{token.Group}
		}
	} else {
		groups = nil
		for group := range GetUserUsableGroups(user.Group) {
			if IsUserSelectableGroup(user.Group, group) {
				groups = append(groups, group)
			}
		}
	}
	abilities, err := model.LoadAssetAccessAbilities(ctx, channelID)
	if err != nil {
		return nil, err
	}
	channels := make(map[int]*model.Channel)
	allowed := make(map[int]bool)
	for _, ability := range abilities {
		if allowed[ability.ChannelId] || !slices.Contains(groups, ability.Group) {
			continue
		}
		if modelLimits != nil && !modelLimits[ability.Model] &&
			!modelLimits[ratio_setting.FormatMatchingModelName(ability.Model)] &&
			!modelLimits[ratio_setting.RoutingMatchModelName(ability.Model)] {
			continue
		}
		channel, loaded := channels[ability.ChannelId]
		if !loaded {
			channel, err = model.GetChannelById(ability.ChannelId, true)
			if err != nil {
				return nil, err
			}
			channels[ability.ChannelId] = channel
		}
		if !AssetPluginSupportsChannel(channel, ability.Model) {
			continue
		}
		config, configErr := model.LoadAssetChannelConfig(ctx, channel.Id)
		if configErr != nil {
			return nil, configErr
		}
		if IsAssetChannelEnabled(channel, &config) {
			allowed[channel.Id] = true
		}
	}
	ids := make([]int, 0, len(allowed))
	for id := range allowed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}
