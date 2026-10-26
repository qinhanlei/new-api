package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

const assetCreateConsistencyWindow = 5 * time.Minute

// StartAssetWorker runs on every node. Each operation owns a fenced database
// lease; network calls never hold database transactions or require local files.
func StartAssetWorker(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := runAssetWork(ctx); err != nil && !errors.Is(err, context.Canceled) {
			common.SysError(common.LogText("Material background work failed"))
		}
	}
}

func runAssetWork(ctx context.Context) error {
	now := time.Now().Unix()
	if err := model.ExpiredAssetUses(ctx, now-int64((73*time.Hour).Seconds())); err != nil {
		return err
	}
	assets, err := model.ClaimAssetWork(ctx, now, 1)
	if err != nil {
		return err
	}
	for i := range assets {
		asset := &assets[i]
		if asset.DeletedAt > 0 {
			operation, cancel := context.WithTimeout(ctx, 2*time.Minute)
			done := cleanupAsset(operation, asset) == nil
			cancel()
			if err = model.FinishAssetCleanup(ctx, asset, done); err != nil {
				return err
			}
			continue
		}
		if asset.SourceURL == "" {
			err = assetError(http.StatusBadRequest, "UploadInterrupted", "The upload did not complete; upload the material again")
		} else {
			actor, actorErr := AssetActorForUser(ctx, asset.UserID, asset.TokenID)
			if actorErr != nil {
				err = actorErr
			} else if len(actor.ChannelIDs) == 0 {
				err = assetError(http.StatusForbidden, "AccessDenied", "No material channel is available to the owner")
			} else {
				operation, cancel := context.WithTimeout(ctx, 2*time.Minute)
				err = fetchAssetContent(operation, asset)
				cancel()
			}
		}
		code, message, retryable := assetFailure(err)
		retryAt := int64(0)
		if err == nil {
			code = ""
			message = ""
		} else if retryable && asset.Attempts < 4 {
			retryAt = time.Now().Add(time.Duration(10*(asset.Attempts+1)) * time.Second).Unix()
		}
		if err = model.FinishAssetSource(ctx, asset, code, message, retryAt); err != nil {
			return err
		}
	}
	replicas, err := model.ClaimAssetReplicas(ctx, time.Now().Unix(), 1)
	if err != nil {
		return err
	}
	for i := range replicas {
		operation, cancel := context.WithTimeout(ctx, 90*time.Second)
		err = processAssetReplica(operation, &replicas[i])
		cancel()
		if err != nil {
			return err
		}
	}
	groups, err := model.ClaimDeletedAssetGroups(ctx, time.Now().Unix(), 1)
	if err != nil {
		return err
	}
	for i := range groups {
		replica := &groups[i]
		operation, cancel := context.WithTimeout(ctx, time.Minute)
		if cleanupAssetGroup(operation, replica) == nil {
			replica.State = model.AssetDeleted
		}
		cancel()
		if err = model.FinishAssetGroupReplica(ctx, replica); err != nil {
			return err
		}
	}
	return nil
}

func processAssetReplica(ctx context.Context, replica *model.AssetReplica) error {
	asset, err := model.GetAssetByID(ctx, replica.AssetID)
	if err != nil {
		return err
	}
	channel, err := model.GetChannelById(replica.ChannelID, true)
	if err != nil {
		return finishAssetReplicaError(ctx, replica, assetError(http.StatusForbidden, "ChannelUnavailable", "The material channel is unavailable"))
	}
	config, err := model.LoadAssetChannelConfig(ctx, replica.ChannelID)
	if err != nil {
		return err
	}
	if !IsAssetChannelEnabled(channel, &config) {
		return finishAssetReplicaError(ctx, replica, assetError(http.StatusForbidden, "ChannelUnavailable", "The material channel is disabled or its account changed"))
	}
	if replica.UpstreamID == "" {
		allowed, accessErr := AssetChannelAuthorized(ctx, asset.UserID, replica.TokenID, replica.ChannelID)
		if accessErr != nil {
			return finishAssetReplicaError(ctx, replica, accessErr)
		}
		if !allowed {
			return finishAssetReplicaError(ctx, replica, assetError(http.StatusForbidden, "AccessDenied", "The owner can no longer use this material channel"))
		}
		groupID, groupErr := ensureUpstreamAssetGroup(ctx, asset, channel, config)
		if groupErr != nil {
			return finishAssetReplicaError(ctx, replica, groupErr)
		}
		if groupID == "" {
			replica.Attempts = -1
			return model.FinishAssetReplica(ctx, replica, time.Now().Add(5*time.Second).Unix())
		}
		if replica.State == model.AssetCreating || replica.State == model.AssetUnknown {
			replica.State = model.AssetUnknown
			id, reconcileErr := findUpstreamAsset(ctx, channel, config, asset, groupID)
			if reconcileErr != nil {
				return finishAssetReplicaError(ctx, replica, reconcileErr)
			}
			if id == "" {
				replica.State = model.AssetUnknown
				return finishAssetReplicaError(ctx, replica, assetError(http.StatusConflict, "UpstreamResultUnknown", "The upstream create result is unknown; verify the material on this channel before retrying"))
			}
			replica.UpstreamID = id
			replica.State = model.AssetReady
		} else {
			sourceURL, urlErr := signedAssetURL(ctx, asset)
			if urlErr != nil {
				return finishAssetReplicaError(ctx, replica, urlErr)
			}
			if err = model.MarkAssetReplicaCreating(ctx, replica); err != nil {
				return err
			}
			var created struct {
				ID string `json:"Id"`
			}
			err = callAssetUpstream(ctx, channel, config, "CreateAsset", map[string]any{"GroupId": groupID, "Name": asset.PublicID, "AssetType": asset.AssetType, "URL": sourceURL}, &created)
			if err != nil {
				var failure *AssetError
				if errors.As(err, &failure) && failure.Code != "UpstreamResultUnknown" && failure.HTTPStatus >= 400 && failure.HTTPStatus < 500 {
					replica.State = model.AssetPending
				} else {
					replica.State = model.AssetUnknown
				}
				return finishAssetReplicaError(ctx, replica, err)
			}
			if created.ID == "" || len(created.ID) > 191 {
				replica.State = model.AssetUnknown
				return finishAssetReplicaError(ctx, replica, assetError(http.StatusBadGateway, "UpstreamResultUnknown", "The upstream create response did not identify the material"))
			}
			replica.UpstreamID = created.ID
			replica.State = model.AssetReady
		}
	}
	var result struct {
		ID         string `json:"Id"`
		Status     string `json:"Status"`
		Moderation struct {
			Strategy string `json:"Strategy"`
		} `json:"Moderation"`
		Error *dto.AssetFailure `json:"Error"`
	}
	err = callAssetUpstream(ctx, channel, config, "GetAsset", map[string]any{"Id": replica.UpstreamID}, &result)
	if err != nil {
		return finishAssetReplicaError(ctx, replica, err)
	}
	if result.ID != replica.UpstreamID || (result.Status != model.AssetActive && result.Status != model.AssetProcessing && result.Status != model.AssetFailed) {
		return finishAssetReplicaError(ctx, replica, assetError(http.StatusBadGateway, "UpstreamInvalidResponse", "The upstream material state was invalid"))
	}
	replica.Status = result.Status
	if result.Moderation.Strategy == "Default" || result.Moderation.Strategy == "Skip" {
		replica.ModerationStrategy = result.Moderation.Strategy
	}
	replica.ErrorCode = ""
	replica.ErrorMessage = ""
	replica.Attempts = -1
	if result.Error != nil {
		replica.ErrorCode = safeAssetMessage(result.Error.Code, channel.Key, config.SecretKey)
		if len(replica.ErrorCode) > 128 {
			replica.ErrorCode = "UpstreamError"
		}
		replica.ErrorMessage = safeAssetMessage(result.Error.Message, channel.Key, config.SecretKey)
	}
	if replica.Status == model.AssetFailed && replica.ErrorCode == "" {
		replica.ErrorCode = "UpstreamModerationFailed"
		replica.ErrorMessage = "The upstream material processing failed"
	}
	if replica.Status != model.AssetFailed {
		replica.ErrorCode = ""
		replica.ErrorMessage = ""
	}
	return model.FinishAssetReplica(ctx, replica, time.Now().Add(10*time.Second).Unix())
}

func finishAssetReplicaError(ctx context.Context, replica *model.AssetReplica, err error) error {
	code, message, retryable := assetFailure(err)
	replica.ErrorCode = code
	replica.ErrorMessage = message
	retryAt := int64(0)
	if replica.State == model.AssetUnknown && code != "AccessDenied" && code != "ChannelUnavailable" {
		if time.Now().Before(time.Unix(replica.CreateStartedAt, 0).Add(assetCreateConsistencyWindow)) {
			replica.Status = model.AssetProcessing
			return model.FinishAssetReplica(ctx, replica, time.Now().Add(10*time.Second).Unix())
		}
		replica.Status = model.AssetFailed
		return model.FinishAssetReplica(ctx, replica, 0)
	}
	if retryable && replica.Attempts < 4 {
		retryAt = time.Now().Add(time.Duration(10*(replica.Attempts+1)) * time.Second).Unix()
	} else {
		replica.Status = model.AssetFailed
	}
	return model.FinishAssetReplica(ctx, replica, retryAt)
}

func ensureUpstreamAssetGroup(ctx context.Context, asset *model.Asset, channel *model.Channel, config system_setting.AssetChannelConfig) (string, error) {
	replica, claimed, err := model.ClaimAssetGroupReplica(ctx, asset.GroupID, channel.Id, time.Now().Unix())
	if err != nil {
		return "", err
	}
	if replica.State == model.AssetReady {
		return replica.UpstreamID, nil
	}
	if replica.State == model.AssetFailed {
		return "", assetError(http.StatusUnprocessableEntity, replica.ErrorCode, replica.ErrorMessage)
	}
	if !claimed {
		return "", nil
	}
	group, err := model.GetAssetGroupByID(ctx, asset.GroupID)
	if err != nil {
		return "", err
	}
	if replica.State == model.AssetCreating || replica.State == model.AssetUnknown {
		replica.State = model.AssetUnknown
		replica.UpstreamID, err = findUpstreamAssetGroup(ctx, channel, config, group.PublicID)
		if err == nil && replica.UpstreamID == "" {
			err = assetError(http.StatusConflict, "UpstreamResultUnknown", "The upstream group create result is unknown; inspect this channel before retrying")
		}
	} else {
		if err = model.MarkAssetGroupCreating(ctx, replica); err != nil {
			return "", err
		}
		var result struct {
			ID string `json:"Id"`
		}
		err = callAssetUpstream(ctx, channel, config, "CreateAssetGroup", map[string]any{"Name": group.PublicID, "Description": "New API managed material group", "GroupType": "AIGC"}, &result)
		replica.UpstreamID = result.ID
		if err == nil && (result.ID == "" || len(result.ID) > 191) {
			replica.UpstreamID = ""
			err = assetError(http.StatusBadGateway, "UpstreamResultUnknown", "The upstream group create response did not contain an identifier")
		}
	}
	if err == nil {
		replica.State = model.AssetReady
		replica.ErrorCode = ""
		replica.ErrorMessage = ""
	} else {
		code, message, retryable := assetFailure(err)
		replica.ErrorCode = code
		replica.ErrorMessage = message
		if code == "UpstreamResultUnknown" || replica.State == model.AssetUnknown {
			replica.State = model.AssetUnknown
		} else if retryable {
			replica.State = model.AssetPending
		} else {
			replica.State = model.AssetFailed
		}
	}
	if saveErr := model.FinishAssetGroupReplica(ctx, replica); saveErr != nil {
		return "", saveErr
	}
	if replica.State == model.AssetUnknown && time.Now().Before(time.Unix(replica.CreateStartedAt, 0).Add(assetCreateConsistencyWindow)) {
		return "", nil
	}
	return replica.UpstreamID, err
}

// The gateway's random public ID is the upstream name, independent of display
// names. Reconciliation accepts exactly one exact match and never re-creates a
// resource after an uncertain response.
func findUpstreamAssetGroup(ctx context.Context, channel *model.Channel, config system_setting.AssetChannelConfig, name string) (string, error) {
	type groupPage struct {
		Items []struct {
			ID        string `json:"Id"`
			Name      string `json:"Name"`
			GroupType string `json:"GroupType"`
		} `json:"Items"`
		NextToken string `json:"NextToken"`
	}
	id := ""
	payload := map[string]any{"Filter": map[string]any{"GroupType": "AIGC", "Name": name}, "MaxResults": 100}
	for range 20 {
		var result groupPage
		if err := callAssetUpstream(ctx, channel, config, "ListAssetGroups", payload, &result); err != nil {
			return "", err
		}
		for _, item := range result.Items {
			if item.Name != name || item.GroupType != "AIGC" {
				continue
			}
			if id != "" || len(item.ID) > 191 {
				return "", assetError(http.StatusConflict, "UpstreamResultUnknown", "The upstream group result is ambiguous")
			}
			id = item.ID
		}
		if result.NextToken == "" {
			return id, nil
		}
		payload["NextToken"] = result.NextToken
	}
	return "", assetError(http.StatusConflict, "UpstreamResultUnknown", "The upstream group result is ambiguous")
}

func findUpstreamAsset(ctx context.Context, channel *model.Channel, config system_setting.AssetChannelConfig, asset *model.Asset, groupID string) (string, error) {
	type assetPage struct {
		Items []struct {
			ID        string `json:"Id"`
			Name      string `json:"Name"`
			AssetType string `json:"AssetType"`
			GroupID   string `json:"GroupId"`
		} `json:"Items"`
		NextToken string `json:"NextToken"`
	}
	id := ""
	payload := map[string]any{"Filter": map[string]any{"GroupType": "AIGC", "GroupIds": []string{groupID}, "Name": asset.PublicID}, "MaxResults": 100}
	for range 20 {
		var result assetPage
		if err := callAssetUpstream(ctx, channel, config, "ListAssets", payload, &result); err != nil {
			return "", err
		}
		for _, item := range result.Items {
			if item.Name != asset.PublicID || item.GroupID != groupID || item.AssetType != asset.AssetType {
				continue
			}
			if id != "" || len(item.ID) > 191 {
				return "", assetError(http.StatusConflict, "UpstreamResultUnknown", "The upstream material result is ambiguous")
			}
			id = item.ID
		}
		if result.NextToken == "" {
			return id, nil
		}
		payload["NextToken"] = result.NextToken
	}
	return "", assetError(http.StatusConflict, "UpstreamResultUnknown", "The upstream material result is ambiguous")
}

func cleanupAsset(ctx context.Context, asset *model.Asset) error {
	replicas, err := model.AssetReplicas(ctx, []int64{asset.ID})
	if err != nil {
		return err
	}
	for _, replica := range replicas {
		if replica.State == model.AssetDeleted {
			continue
		}
		if replica.UpstreamID != "" || replica.State == model.AssetCreating || replica.State == model.AssetUnknown {
			channel, loadErr := model.GetChannelById(replica.ChannelID, true)
			if loadErr != nil {
				return loadErr
			}
			config, loadErr := model.LoadAssetChannelConfig(ctx, replica.ChannelID)
			if loadErr != nil {
				return loadErr
			}
			if replica.UpstreamID == "" {
				if time.Now().Before(time.Unix(replica.CreateStartedAt, 0).Add(assetCreateConsistencyWindow)) {
					return errors.New("waiting for upstream creation to settle")
				}
				groups, groupErr := model.AssetChannelGroupReplicas(ctx, channel.Id)
				if groupErr != nil {
					return groupErr
				}
				groupID := ""
				for _, group := range groups {
					if group.GroupID == asset.GroupID {
						groupID = group.UpstreamID
						break
					}
				}
				if groupID == "" {
					return errors.New("upstream material group requires reconciliation")
				}
				replica.UpstreamID, err = findUpstreamAsset(ctx, channel, config, asset, groupID)
				if err != nil {
					return err
				}
				if replica.UpstreamID == "" {
					if err = model.MarkAssetReplicaDeleted(ctx, replica.ID); err != nil {
						return err
					}
					continue
				}
			}
			err = callAssetUpstream(ctx, channel, config, "DeleteAsset", map[string]any{"Id": replica.UpstreamID}, nil)
			if err != nil && !assetUpstreamNotFound(err) {
				return err
			}
		}
		if err = model.MarkAssetReplicaDeleted(ctx, replica.ID); err != nil {
			return err
		}
	}
	return deleteAssetObject(ctx, asset)
}

func cleanupAssetGroup(ctx context.Context, replica *model.AssetGroupReplica) error {
	if replica.UpstreamID == "" && replica.State != model.AssetCreating && replica.State != model.AssetUnknown {
		return nil
	}
	channel, err := model.GetChannelById(replica.ChannelID, true)
	if err != nil {
		return err
	}
	config, err := model.LoadAssetChannelConfig(ctx, replica.ChannelID)
	if err != nil {
		return err
	}
	if replica.UpstreamID == "" {
		if time.Now().Before(time.Unix(replica.CreateStartedAt, 0).Add(assetCreateConsistencyWindow)) {
			return errors.New("waiting for upstream group creation to settle")
		}
		group, loadErr := model.GetAssetGroupByID(ctx, replica.GroupID)
		if loadErr != nil {
			return loadErr
		}
		replica.UpstreamID, err = findUpstreamAssetGroup(ctx, channel, config, group.PublicID)
		if err != nil {
			return err
		}
		if replica.UpstreamID == "" {
			return nil
		}
	}
	err = callAssetUpstream(ctx, channel, config, "DeleteAssetGroup", map[string]any{"Id": replica.UpstreamID}, nil)
	if assetUpstreamNotFound(err) {
		return nil
	}
	return err
}

func assetUpstreamNotFound(err error) bool {
	var failure *AssetError
	return errors.As(err, &failure) && (failure.HTTPStatus == http.StatusNotFound || strings.HasPrefix(failure.Code, "ResourceNotFound"))
}
