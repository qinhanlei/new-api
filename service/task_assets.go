package service

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/assetprovider"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

const ContextTaskAssetRefs = "task_asset_refs"
const ContextResolvedAssets = "task_resolved_assets"
const ContextTaskSubmission = "task_asset_submission"
const ContextQueuedAssetTask = "task_asset_queued"

// SetTaskAssetIntent accepts only host IDs. A plugin declares references, while
// ownership and the actual stored media type remain host decisions.
func SetTaskAssetIntent(c *gin.Context, value any) error {
	c.Set(ContextTaskAssetRefs, []string{})
	ids, err := ParseTaskAssetReferences(value)
	if err != nil {
		return err
	}
	c.Set(ContextTaskAssetRefs, ids)
	if len(ids) > 0 {
		if !common.AssetLibraryEnabled {
			return errors.New(i18n.T(c, i18n.MsgAssetDisabled))
		}
		// Both native and shared-protocol decoders run before distribution. Keep
		// the same constraint for affinity, pinned channels and every retry.
		GetChannelConstraints(c).AddFilter(dto.ChannelFilter{Kind: dto.FilterManagedAssets})
	}
	return nil
}

// ParseTaskAssetReferences validates the same host-owned identifiers before
// shared endpoint selection and after the selected channel's final decoder.
func ParseTaskAssetReferences(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok || len(items) > 32 {
		return nil, errors.New("assetRefs must contain at most 32 asset IDs")
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		id, ok := item.(string)
		if !ok || !strings.HasPrefix(id, "na_asset_") || len(id) > 64 || strings.ContainsAny(id, "/\\: \t\n") {
			return nil, errors.New("invalid managed asset reference")
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// PrepareTaskAssets runs after the final channel's decoder and before any build
// hook or quota reservation. The worker supplies a resolved immutable snapshot.
func PrepareTaskAssets(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	if _, resumed := c.Get(ContextTaskSubmission); resumed {
		return nil
	}
	ids := c.GetStringSlice(ContextTaskAssetRefs)
	if len(ids) == 0 {
		return nil
	}
	bad := func(key string) *dto.TaskError {
		return TaskErrorWrapperLocal(errors.New(i18n.T(c, key)), "invalid_asset_reference", http.StatusBadRequest)
	}
	if !common.AssetLibraryEnabled {
		return bad(i18n.MsgAssetDisabled)
	}
	space, err := model.EnsureChannelAssetSpace(c.Request.Context(), info.ChannelId)
	if err != nil {
		return bad(i18n.MsgAssetBinding)
	}
	pinValue, _ := c.Get(jsplugin.ContextKeyPinnedPlugin)
	pin, ok := pinValue.(jsplugin.PinnedPlugin)
	if !ok || pin.Plugin == nil {
		return bad(i18n.MsgAssetPlugin)
	}
	request, _ := c.Get("task_request")
	body, err := common.Marshal(request)
	if err != nil || len(body) > 1<<20 || strings.Contains(string(body), "request_file:") {
		return bad(i18n.MsgAssetJSON)
	}
	for _, id := range ids {
		asset, err := model.GetUserAsset(c.Request.Context(), info.UserId, id)
		if err != nil || asset.Status != model.AssetStatusReady || !slices.Contains([]string{"image", "video", "audio"}, asset.Kind) {
			return bad(i18n.MsgAssetChannelMedia)
		}
	}
	now := time.Now().Unix()
	task := model.InitTask(constant.TaskPlatform(pin.Plugin.Meta.Key), info)
	task.Status = model.TaskStatusWaitingAssets
	task.Action = info.Action
	task.CreatedAt, task.UpdatedAt = now, now
	task.PrivateData.Execution = TaskExecutionSnapshotFromContext(c)
	task.PrivateData.TokenId = info.TokenId
	task.PrivateData.DurableBilling = true
	task.PrivateData.Key = ""
	if value, exists := c.Get(jsplugin.ContextKeyProtocolRequest); exists {
		if protocol, ok := value.(jsplugin.ProtocolRequestContext); ok && protocol.Protocol == "openai_responses" {
			if body, ok := protocol.Body.(map[string]any); ok {
				if request, ok := body["value"].(map[string]any); ok {
					task.PrivateData.ResponsesBackground, _ = request["background"].(bool)
				}
			}
		}
	}
	submission := &model.TaskSubmission{TaskID: task.TaskID, UserID: info.UserId, TokenID: info.TokenId, ChannelID: info.ChannelId, SpaceID: space.ID,
		PluginKey: pin.Plugin.Meta.Key, PluginHash: pin.Plugin.SourceHash, Model: info.OriginModelName,
		UpstreamModel: info.UpstreamModelName, Action: info.Action, Group: info.UsingGroup, RequestPath: c.Request.URL.Path,
		RequestBody: model.LongText(body), AssetIDs: ids, Phase: model.SubmissionWaiting, NextAttempt: now, CreatedAt: now, UpdatedAt: now}
	if info.TaskRelayInfo != nil {
		for _, origin := range info.OriginTasks {
			submission.OriginTaskIDs = append(submission.OriginTaskIDs, origin.TaskID)
		}
	}
	if err := model.CreateTaskSubmission(c.Request.Context(), task, submission); err != nil {
		return bad(i18n.MsgAssetUnavailable)
	}
	c.Set(ContextQueuedAssetTask, task)
	return TaskErrorWrapperLocal(errors.New("waiting for assets"), "asset_task_queued", http.StatusAccepted)
}

func AssetProviderClient(ctx context.Context, space *model.AssetSpace) (assetprovider.Client, error) {
	channel, err := model.GetChannelById(space.ChannelID, true)
	if err != nil {
		return nil, errors.New("asset channel is unavailable")
	}
	return ChannelAssetProviderClient(channel, space)
}

// EnsureAssetGroup creates one channel-owned AIGC group. A crash or ambiguous
// POST is recovered by its stable name, never by blindly replaying creation.
func EnsureAssetGroup(ctx context.Context, space *model.AssetSpace, owner string) (bool, error) {
	if space.GroupState == "ready" && space.GroupID != "" {
		return true, nil
	}
	if space.GroupState == "cleaning" {
		return false, errors.New("asset channel cleanup is pending")
	}
	now := time.Now().Unix()
	db := model.DB.WithContext(ctx)
	claimed := db.Model(&model.AssetSpace{}).
		Where("id = ? AND group_state IN ? AND lease_until <= ? AND next_attempt <= ?", space.ID, []string{"pending", "unknown", "failed"}, now, now).
		Updates(map[string]any{"lease_owner": owner, "lease_until": now + 90, "updated_at": now})
	if claimed.Error != nil || claimed.RowsAffected == 0 {
		return false, claimed.Error
	}
	if err := db.Where("id = ? AND lease_owner = ?", space.ID, owner).First(space).Error; err != nil {
		return false, err
	}
	client, err := AssetProviderClient(ctx, space)
	var group assetprovider.Group
	state := space.GroupState
	if err == nil && state == "unknown" {
		var found bool
		group, found, err = client.FindGroup(ctx, space.ID)
		if err == nil && !found {
			err = errors.New("Asset group creation needs administrator review")
		}
	} else if err == nil {
		// Commit the uncertain boundary before any upstream side effect.
		result := db.Model(&model.AssetSpace{}).Where("id = ? AND lease_owner = ? AND lease_until > ?", space.ID, owner, now).
			Update("group_state", "unknown")
		if result.Error != nil {
			return false, result.Error
		}
		if result.RowsAffected != 1 {
			return false, errors.New("asset group lease lost")
		}
		state = "unknown"
		group, err = client.CreateGroup(ctx, space.ID)
		var providerErr *assetprovider.Error
		if errors.As(err, &providerErr) && !providerErr.Uncertain {
			state = "failed"
		}
	}
	message := ""
	if err == nil {
		state, space.GroupID = "ready", group.ID
	} else {
		message = "Asset group preparation failed"
		if state == "unknown" {
			message = "Asset group creation needs administrator review"
		}
	}
	result := db.Model(&model.AssetSpace{}).Where("id = ? AND lease_owner = ?", space.ID, owner).
		Updates(map[string]any{"group_id": space.GroupID, "group_state": state, "error": message,
			"lease_owner": "", "lease_until": 0, "next_attempt": now + 30, "updated_at": now})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, errors.New("asset group lease lost")
	}
	space.GroupState, space.Error = state, message
	if err != nil {
		return false, errors.New(message)
	}
	return true, nil
}

// ResolveTaskAssets performs one bounded sync pass. Pending imports are polled
// on a later scheduler pass, so no HTTP request waits for an upstream audit.
func ResolveTaskAssets(ctx context.Context, submission *model.TaskSubmission, owner string) (map[string]any, bool, error) {
	space, err := model.GetAssetSpace(ctx, submission.SpaceID)
	if err != nil || space.ChannelID != submission.ChannelID || space.GroupState == "cleaning" {
		return nil, false, errors.New("asset channel is unavailable")
	}
	ready, err := EnsureAssetGroup(ctx, space, owner)
	if err != nil || !ready {
		return nil, false, err
	}
	client, err := AssetProviderClient(ctx, space)
	if err != nil {
		return nil, false, err
	}
	resolved := make(map[string]any, len(submission.AssetIDs))
	for _, id := range submission.AssetIDs {
		asset, err := model.GetAsset(ctx, id)
		if err != nil || asset.UserID != submission.UserID {
			return nil, false, errors.New("asset source is unavailable")
		}
		now := time.Now().Unix()
		replica, claimed, err := model.ClaimAssetReplica(ctx, id, space.ID, owner, now, now+60)
		if err != nil {
			return nil, false, err
		}
		if replica.Status == model.AssetReplicaFailed || replica.Status == model.AssetReplicaUnknown {
			if replica.Status == model.AssetReplicaUnknown && replica.LeaseUntil > now {
				return nil, false, nil
			}
			return nil, false, errors.New("asset import failed or its result needs administrator review")
		}
		if replica.Status == model.AssetReplicaReady {
			resolved[id] = map[string]any{"reference": replica.GenerationRef, "type": asset.Kind, "mimeType": asset.MIMEType, "size": asset.Size, "width": asset.Width, "height": asset.Height, "durationSeconds": asset.DurationSeconds}
			continue
		}
		if !claimed {
			return nil, false, nil
		}
		if replica.Status != model.AssetReplicaReady {
			var result assetprovider.Result
			if replica.RemoteID == "" {
				// Persist the uncertain boundary before creating an upstream asset.
				replica.Status = model.AssetReplicaUnknown
				store, storeErr := GetAssetStoreForBackend(asset.StorageBackend)
				if storeErr != nil {
					return nil, false, storeErr
				}
				url, signErr := store.Presign(ctx, asset.ObjectKey)
				if signErr != nil {
					return nil, false, signErr
				}
				if err := model.MarkAssetReplicaCreating(ctx, id, space.ID, owner); err != nil {
					return nil, false, err
				}
				result, err = client.Create(ctx, assetprovider.Source{Name: asset.ID, Kind: asset.Kind, MIMEType: asset.MIMEType, URL: url})
			} else {
				result, err = client.Get(ctx, replica.RemoteID)
			}
			if err != nil {
				var providerErr *assetprovider.Error
				if errors.As(err, &providerErr) && !providerErr.Uncertain && replica.RemoteID == "" {
					replica.Status = model.AssetReplicaFailed
				}
				replica.Error = "Asset provider request failed"
			} else {
				replica.RemoteID, replica.GenerationRef = result.ID, result.Reference
				replica.Status = result.Status
				if result.Status == "pending" {
					replica.Status = model.AssetReplicaProcessing
				}
				replica.Error = result.Error
			}
		}
		replica.NextAttempt, replica.LeaseUntil = now+15, 0
		if err := model.SaveAssetReplica(ctx, replica, owner); err != nil {
			return nil, false, err
		}
		if replica.Status == model.AssetReplicaFailed || replica.Status == model.AssetReplicaUnknown {
			return nil, false, errors.New("asset import failed or its result needs administrator review")
		}
		if replica.Status != model.AssetReplicaReady {
			return nil, false, nil
		}
		resolved[id] = map[string]any{"reference": replica.GenerationRef, "type": asset.Kind, "mimeType": asset.MIMEType, "size": asset.Size, "width": asset.Width, "height": asset.Height, "durationSeconds": asset.DurationSeconds}
	}
	return resolved, true, nil
}
