package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

type assetActionRequest struct {
	ID          string  `json:"Id"`
	ProjectName string  `json:"ProjectName"`
	Name        *string `json:"Name"`
	Description *string `json:"Description"`
	GroupType   string  `json:"GroupType"`
	GroupID     string  `json:"GroupId"`
	URL         string  `json:"URL"`
	AssetType   string  `json:"AssetType"`
	BytedToken  string  `json:"BytedToken"`
	CallbackURL string  `json:"CallbackURL"`
	Moderation  *struct {
		Strategy string `json:"Strategy"`
	} `json:"Moderation"`
	Filter     *assetFilter `json:"Filter"`
	PageNumber *int         `json:"PageNumber"`
	PageSize   *int         `json:"PageSize"`
	MaxResults *int         `json:"MaxResults"`
	NextToken  *string      `json:"NextToken"`
	SortBy     string       `json:"SortBy"`
	SortOrder  string       `json:"SortOrder"`
}

type assetFilter struct {
	GroupType string   `json:"GroupType"`
	GroupIDs  []string `json:"GroupIds"`
	Name      string   `json:"Name"`
	Statuses  []string `json:"Statuses"`
}

type assetCursor struct {
	Fingerprint string `json:"f"`
	ID          int64  `json:"i"`
	Timestamp   int64  `json:"t"`
	GroupID     string `json:"g,omitempty"`
}

func normalizeAssetProject(project string) (string, error) {
	if project == "" {
		return "default", nil
	}
	if utf8.RuneCountInString(project) > 64 || strings.ContainsAny(project, "\r\n\t") {
		return "", assetError(http.StatusBadRequest, "InvalidParameter.ProjectName", "Invalid project name")
	}
	return project, nil
}

// ExecuteAssetAction returns the official Result payload. Authentication and
// the ResponseMetadata envelope belong to the HTTP controller.
func ExecuteAssetAction(ctx context.Context, actor dto.AssetActor, action string, raw []byte) (any, error) {
	if actor.UserID <= 0 {
		return nil, assetError(http.StatusUnauthorized, "Unauthorized", "Authentication is required")
	}
	if actor.AllUsers && action != "ListAssetGroups" && action != "ListAssets" {
		return nil, assetError(http.StatusForbidden, "AccessDenied", "Cross-user access is only available for administrative lists")
	}
	var request assetActionRequest
	if err := common.Unmarshal(raw, &request); err != nil {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter", "The request body must be a JSON object")
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter", "The request body must be a JSON object")
	}
	var err error
	request.ProjectName, err = normalizeAssetProject(request.ProjectName)
	if err != nil {
		return nil, err
	}
	if request.Name != nil && (utf8.RuneCountInString(*request.Name) > 64 || strings.TrimSpace(*request.Name) == "") {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter.Name", "Name must contain 1 to 64 characters")
	}
	if request.Description != nil && utf8.RuneCountInString(*request.Description) > 300 {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter.Description", "Description must not exceed 300 characters")
	}
	var result any
	switch action {
	case "CreateVisualValidateSession":
		if request.CallbackURL != "" {
			return nil, assetError(http.StatusBadRequest, "InvalidParameter.CallbackURL", "The gateway manages the verification callback URL")
		}
		var input dto.AssetValidationCreate
		if err = common.Unmarshal(raw, &input); err != nil {
			return nil, assetError(http.StatusBadRequest, "InvalidParameter", "The request body must be a JSON object")
		}
		return CreateAssetValidationSession(ctx, actor, input)
	case "GetVisualValidateResult":
		return GetAssetValidationResult(ctx, actor, request.BytedToken, request.ProjectName)
	case "CreateAssetGroup":
		if request.GroupType == "" {
			request.GroupType = "AIGC"
		}
		if request.GroupType != "AIGC" {
			return nil, assetError(http.StatusBadRequest, "InvalidParameter.GroupType", "Only AIGC material groups are supported")
		}
		if request.Name == nil {
			return nil, assetError(http.StatusBadRequest, "MissingParameter.Name", "Name is required")
		}
		if _, err = assetCreationChannel(ctx, actor, 0); err != nil {
			return nil, err
		}
		group := model.AssetGroup{PublicID: model.NewAssetGroupID(), UserID: actor.UserID, ProjectName: request.ProjectName, Name: *request.Name, GroupType: "AIGC"}
		if request.Description != nil {
			group.Description = *request.Description
		}
		err = model.CreateAssetGroup(ctx, &group)
		result = map[string]string{"Id": group.PublicID}
	case "GetAssetGroup":
		var group *model.AssetGroup
		group, err = model.GetAssetGroup(ctx, actor.UserID, request.ID, request.ProjectName)
		if err == nil {
			result = assetGroupView(*group, false)
		}
	case "UpdateAssetGroup":
		if request.Name == nil && request.Description == nil {
			return nil, assetError(http.StatusBadRequest, "MissingParameter", "Name or Description is required")
		}
		err = model.UpdateAssetGroup(ctx, actor.UserID, request.ID, request.ProjectName, request.Name, request.Description)
		result = map[string]string{"Id": request.ID}
	case "DeleteAssetGroup":
		err = model.DeleteAssetGroup(ctx, actor.UserID, request.ID, request.ProjectName)
		result = map[string]any{}
		if err == nil {
			// Human groups stay visible until upstream deletion is confirmed.
			group, loadErr := model.GetAssetGroup(ctx, actor.UserID, request.ID, request.ProjectName)
			if loadErr == nil && group.GroupType == model.AssetGroupLivenessFace {
				result = assetGroupView(*group, false)
			} else if loadErr != nil && !errors.Is(loadErr, gorm.ErrRecordNotFound) {
				err = loadErr
			}
		}
	case "CancelAssetGroupDeletion":
		err = model.CancelAssetGroupDeletion(ctx, actor.UserID, request.ID, request.ProjectName)
		result = map[string]string{"Id": request.ID}
	case "CreateAsset":
		if request.Moderation != nil && request.Moderation.Strategy != "Default" {
			return nil, assetError(http.StatusBadRequest, "InvalidParameter.Moderation", "Only the default moderation strategy is supported")
		}
		if request.URL == "" {
			return nil, assetError(http.StatusBadRequest, "MissingParameter.URL", "URL is required")
		}
		if err = ValidateSSRFProtectedFetchURL(request.URL); err != nil {
			return nil, assetError(http.StatusBadRequest, "InvalidParameter.URL", "Material URL is not allowed")
		}
		name := ""
		if request.Name != nil {
			name = *request.Name
		}
		var asset *model.Asset
		asset, err = reserveAsset(ctx, actor, dto.AssetUpload{GroupID: request.GroupID, ProjectName: request.ProjectName, Name: name, AssetType: request.AssetType}, request.URL)
		if err == nil {
			result = map[string]string{"Id": asset.PublicID}
		}
	case "GetAsset":
		var asset *model.Asset
		asset, err = model.GetAsset(ctx, actor.UserID, request.ID, request.ProjectName)
		if err == nil {
			result, err = assetView(ctx, *asset, false)
		}
	case "UpdateAsset":
		if request.Name == nil {
			return nil, assetError(http.StatusBadRequest, "MissingParameter.Name", "Name is required")
		}
		err = model.UpdateAssetName(ctx, actor.UserID, request.ID, request.ProjectName, *request.Name)
		result = map[string]string{"Id": request.ID}
	case "DeleteAsset":
		err = model.DeleteAsset(ctx, actor.UserID, request.ID, request.ProjectName)
		result = map[string]any{}
	case "ListAssetGroups", "ListAssets":
		return listAssets(ctx, actor, action, request)
	default:
		return nil, assetError(http.StatusBadRequest, "InvalidAction", "Unsupported material action")
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, assetError(http.StatusNotFound, "ResourceNotFound", "The material resource was not found")
	}
	if errors.Is(err, model.ErrAssetStorageDisabled) {
		return nil, assetError(http.StatusServiceUnavailable, "AssetStorageDisabled", "Material storage is not enabled")
	}
	return result, err
}

type AssetDeletion struct {
	ID    string
	Error error
}

// BatchDeleteAssets validates the entire batch before changing anything. Each
// asset keeps the single-delete transaction and its ownership/lifecycle checks.
func BatchDeleteAssets(ctx context.Context, actor dto.AssetActor, request dto.AssetBatchDeleteRequest) ([]AssetDeletion, error) {
	if actor.UserID <= 0 {
		return nil, assetError(http.StatusUnauthorized, "Unauthorized", "Authentication is required")
	}
	if actor.AllUsers {
		return nil, assetError(http.StatusForbidden, "AccessDenied", "Cross-user access is only available for administrative lists")
	}
	project, err := normalizeAssetProject(request.ProjectName)
	if err != nil {
		return nil, err
	}
	if len(request.IDs) == 0 || len(request.IDs) > 100 {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter.Ids", "Select between 1 and 100 assets")
	}
	ids := make([]string, 0, len(request.IDs))
	for _, id := range request.IDs {
		suffix, ok := strings.CutPrefix(id, model.AssetIDPrefix)
		if !ok || suffix == "" || len(id) > 64 || strings.ContainsFunc(suffix, func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_')
		}) {
			return nil, assetError(http.StatusBadRequest, "InvalidParameter.Ids", "Invalid asset ID")
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	results := make([]AssetDeletion, 0, len(ids))
	for _, id := range ids {
		// Deletes share the storage lock, so parallel execution adds contention
		// without improving progress. Partial success must remain observable.
		err := model.DeleteAsset(ctx, actor.UserID, id, project)
		results = append(results, AssetDeletion{ID: id, Error: err})
	}
	return results, nil
}

func assetCreationChannel(ctx context.Context, actor dto.AssetActor, preferred int) (int, error) {
	storage, err := model.LoadAssetStorageConfig(ctx)
	if err != nil {
		return 0, err
	}
	if !storage.Enabled {
		return 0, assetError(http.StatusServiceUnavailable, "AssetStorageDisabled", "Material storage is not enabled")
	}
	if len(actor.ChannelIDs) == 0 {
		return 0, assetError(http.StatusForbidden, "NoAvailableChannel", "No material channel is available to this caller")
	}
	if preferred != 0 && slices.Contains(actor.ChannelIDs, preferred) {
		return preferred, nil
	}
	var candidates []*model.Channel
	var highest int64
	for _, id := range actor.ChannelIDs {
		channel, loadErr := model.GetChannelById(id, true)
		if loadErr != nil {
			return 0, loadErr
		}
		priority := channel.GetPriority()
		if len(candidates) == 0 || priority > highest {
			candidates = nil
			highest = priority
		}
		if priority == highest {
			candidates = append(candidates, channel)
		}
	}
	weight := 0
	for _, channel := range candidates {
		weight += max(1, channel.GetWeight())
	}
	choice := rand.IntN(weight)
	for _, channel := range candidates {
		choice -= max(1, channel.GetWeight())
		if choice < 0 {
			return channel.Id, nil
		}
	}
	return 0, assetError(http.StatusServiceUnavailable, "NoAvailableChannel", "No material channel is available")
}

func reserveAsset(ctx context.Context, actor dto.AssetActor, input dto.AssetUpload, sourceURL string) (*model.Asset, error) {
	if input.AssetType != "Image" && input.AssetType != "Video" && input.AssetType != "Audio" {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter.AssetType", "AssetType must be Image, Video or Audio")
	}
	project, err := normalizeAssetProject(input.ProjectName)
	if err != nil {
		return nil, err
	}
	group, err := model.GetAssetGroup(ctx, actor.UserID, input.GroupID, project)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, assetError(http.StatusNotFound, "ResourceNotFound", "The material group was not found")
	}
	if err != nil {
		return nil, err
	}
	if !group.Usable() {
		return nil, assetError(http.StatusConflict, "AssetGroupUnavailable", "The material group is being deleted")
	}
	if group.GroupType == model.AssetGroupLivenessFace && !slices.Contains(actor.ChannelIDs, group.InitialChannelID) {
		return nil, assetError(http.StatusForbidden, "AssetChannelUnavailable", "The portrait verification channel is unavailable")
	}
	channelID, err := assetCreationChannel(ctx, actor, group.InitialChannelID)
	if err != nil {
		return nil, err
	}
	id := model.NewAssetID()
	name := input.Name
	if name == "" {
		name = id
	}
	if utf8.RuneCountInString(name) > 64 {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter.Name", "Name must not exceed 64 characters")
	}
	asset := &model.Asset{PublicID: id, UserID: actor.UserID, TokenID: actor.TokenID, ProjectName: project, GroupID: group.ID, GroupPublicID: group.PublicID, Name: name, AssetType: input.AssetType, Status: model.AssetProcessing, SourceURL: sourceURL, ObjectKey: "new-api-assets/" + id, ObjectState: model.AssetPending, NextAttemptAt: time.Now().Unix()}
	if sourceURL == "" {
		asset.ObjectState = "uploading"
		asset.LeaseToken = common.GetUUID()
		asset.LeaseExpiresAt = time.Now().Add(5 * time.Minute).Unix()
	}
	if err = model.ReserveAsset(ctx, asset, channelID); err != nil {
		return nil, err
	}
	return asset, nil
}

func CreateUploadedAsset(ctx context.Context, actor dto.AssetActor, input dto.AssetUpload, reader io.Reader) (any, error) {
	if actor.UserID <= 0 || actor.AllUsers {
		return nil, assetError(http.StatusForbidden, "AccessDenied", "An individual material owner is required")
	}
	asset, err := reserveAsset(ctx, actor, input, "")
	if err != nil {
		return nil, err
	}
	operation, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err = storeAssetContent(operation, asset, reader)
	cancel()
	// Finalize the durable reservation even when the caller disconnects. This
	// bounded write cannot keep an upload lease alive indefinitely.
	finalize, finish := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer finish()
	if err != nil {
		code, message, _ := assetFailure(err)
		if finishErr := model.FinishAssetSource(finalize, asset, code, message, 0); finishErr != nil {
			return nil, finishErr
		}
		_ = model.DeleteAsset(finalize, actor.UserID, asset.PublicID, asset.ProjectName)
		return nil, err
	}
	if err = model.FinishAssetSource(finalize, asset, "", "", 0); err != nil {
		return nil, err
	}
	return map[string]string{"Id": asset.PublicID}, nil
}

func assetGroupView(group model.AssetGroup, allUsers bool) dto.AssetGroupView {
	view := dto.AssetGroupView{ID: group.PublicID, Name: group.Name, Description: group.Description, GroupType: group.GroupType, ProjectName: group.ProjectName, CreateTime: time.Unix(group.CreatedAt, 0).UTC().Format(time.RFC3339), UpdateTime: time.Unix(group.UpdatedAt, 0).UTC().Format(time.RFC3339)}
	if allUsers {
		view.UserID = group.UserID
	}
	if group.GroupType == model.AssetGroupLivenessFace {
		view.Status = group.Lifecycle
		if view.Status == "" {
			view.Status = model.AssetGroupActive
		}
		view.ChannelID = group.InitialChannelID
		if group.ErrorCode != "" {
			view.Error = &dto.AssetFailure{Code: group.ErrorCode, Message: group.ErrorMessage}
		}
	}
	return view
}

func assetView(ctx context.Context, asset model.Asset, allUsers bool) (dto.AssetView, error) {
	view := dto.AssetView{ID: asset.PublicID, Name: asset.Name, GroupID: asset.GroupPublicID, AssetType: asset.AssetType, ProjectName: asset.ProjectName, Status: asset.Status, CreateTime: time.Unix(asset.CreatedAt, 0).UTC().Format(time.RFC3339), UpdateTime: time.Unix(asset.UpdatedAt, 0).UTC().Format(time.RFC3339)}
	if allUsers {
		view.UserID = asset.UserID
	}
	if asset.ModerationStrategy != "" {
		view.Moderation = map[string]string{"Strategy": asset.ModerationStrategy}
	}
	if asset.ErrorCode != "" {
		view.Error = &dto.AssetFailure{Code: asset.ErrorCode, Message: asset.ErrorMessage}
	}
	if asset.LastInferenceAt > 0 {
		view.LastInferenceTime = time.Unix(asset.LastInferenceAt, 0).UTC().Format(time.RFC3339)
	}
	var err error
	view.URL, err = signedAssetURL(ctx, &asset)
	return view, err
}

func listAssets(ctx context.Context, actor dto.AssetActor, action string, request assetActionRequest) (any, error) {
	if request.Filter == nil || (request.Filter.GroupType != model.AssetGroupAIGC && request.Filter.GroupType != model.AssetGroupLivenessFace) {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter.Filter", "Filter.GroupType must be AIGC or LivenessFace")
	}
	if len(request.Filter.GroupIDs) > 100 || utf8.RuneCountInString(request.Filter.Name) > 64 {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter.Filter", "The material filter is too large")
	}
	for _, status := range request.Filter.Statuses {
		if status != model.AssetActive && status != model.AssetProcessing && status != model.AssetFailed {
			return nil, assetError(http.StatusBadRequest, "InvalidParameter.Statuses", "Unsupported material status")
		}
	}
	if request.SortBy == "" {
		request.SortBy = "CreateTime"
	}
	if request.SortOrder == "" {
		request.SortOrder = "Desc"
	}
	if request.SortBy != "CreateTime" && request.SortBy != "UpdateTime" && !(action == "ListAssets" && request.SortBy == "GroupId") {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter.SortBy", "Unsupported material sort field")
	}
	if request.SortOrder != "Asc" && request.SortOrder != "Desc" {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter.SortOrder", "SortOrder must be Asc or Desc")
	}
	q := model.AssetListQuery{UserID: actor.UserID, AllUsers: actor.AllUsers, ProjectName: request.ProjectName, GroupType: request.Filter.GroupType, GroupIDs: request.Filter.GroupIDs, Name: request.Filter.Name, Statuses: request.Filter.Statuses, SortBy: request.SortBy, Descending: request.SortOrder == "Desc", Limit: 10}
	paged := request.PageNumber != nil || request.PageSize != nil
	if paged {
		if request.PageNumber == nil || request.PageSize == nil || request.MaxResults != nil || request.NextToken != nil || *request.PageNumber < 1 || *request.PageSize < 1 || *request.PageSize > 100 || *request.PageNumber > 20000 / *request.PageSize {
			return nil, assetError(http.StatusBadRequest, "InvalidParameter.Pagination", "Use PageNumber and PageSize together, without cursor parameters, within 20000 results")
		}
		q.Limit = *request.PageSize
		q.Offset = (*request.PageNumber - 1) * q.Limit
	} else if request.MaxResults != nil {
		if *request.MaxResults < 1 || *request.MaxResults > 100 {
			return nil, assetError(http.StatusBadRequest, "InvalidParameter.MaxResults", "MaxResults must be between 1 and 100")
		}
		q.Limit = *request.MaxResults
	}
	// Bind cursors to identity and every query dimension. A token from another
	// user, project, or filter is invalid even if its position is otherwise valid.
	fingerprintData, _ := common.Marshal([]any{actor.UserID, actor.AllUsers, action, request.ProjectName, request.Filter, request.SortBy, request.SortOrder})
	fingerprintHash := sha256.Sum256(fingerprintData)
	fingerprint := hex.EncodeToString(fingerprintHash[:])
	secret, err := model.AssetCursorSecret(ctx)
	if err != nil {
		return nil, err
	}
	if request.NextToken != nil {
		payload, signature, ok := strings.Cut(*request.NextToken, ".")
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(payload)
		sig, sigErr := base64.RawURLEncoding.DecodeString(signature)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(decoded)
		var cursor assetCursor
		if !ok || decodeErr != nil || sigErr != nil || !hmac.Equal(sig, mac.Sum(nil)) || common.Unmarshal(decoded, &cursor) != nil || cursor.Fingerprint != fingerprint || cursor.ID <= 0 {
			return nil, assetError(http.StatusBadRequest, "InvalidParameter.NextToken", "NextToken is invalid for this query")
		}
		q.AfterID = cursor.ID
		q.AfterValue = cursor.Timestamp
		if q.SortBy == "GroupId" {
			q.AfterValue = cursor.GroupID
		}
	}
	limit := q.Limit
	if !paged {
		q.Limit++
	}
	var total int64
	items := []any{}
	var last assetCursor
	more := false
	if action == "ListAssetGroups" {
		var groups []model.AssetGroup
		groups, total, err = model.ListAssetGroups(ctx, q)
		if len(groups) > limit {
			more = true
			groups = groups[:limit]
		}
		for _, group := range groups {
			items = append(items, assetGroupView(group, actor.AllUsers))
			last = assetCursor{ID: group.ID, Timestamp: group.CreatedAt}
			if q.SortBy == "UpdateTime" {
				last.Timestamp = group.UpdatedAt
			}
		}
	} else {
		var assets []model.Asset
		assets, total, err = model.ListAssets(ctx, q)
		if len(assets) > limit {
			more = true
			assets = assets[:limit]
		}
		for _, asset := range assets {
			view, viewErr := assetView(ctx, asset, actor.AllUsers)
			if viewErr != nil {
				return nil, viewErr
			}
			items = append(items, view)
			last = assetCursor{ID: asset.ID, Timestamp: asset.CreatedAt, GroupID: asset.GroupPublicID}
			if q.SortBy == "UpdateTime" {
				last.Timestamp = asset.UpdatedAt
			}
		}
	}
	if err != nil {
		return nil, err
	}
	result := map[string]any{"Items": items}
	if paged {
		result["TotalCount"] = total
		result["PageNumber"] = *request.PageNumber
		result["PageSize"] = limit
	} else if more {
		last.Fingerprint = fingerprint
		payload, _ := common.Marshal(last)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(payload)
		result["NextToken"] = base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	return result, nil
}

func PrepareAssetRouting(ctx context.Context, actor dto.AssetActor, ids []string, preferredChannelID int) ([]int, error) {
	if len(ids) == 0 {
		return slices.Clone(actor.ChannelIDs), nil
	}
	if len(ids) > 32 {
		return nil, assetError(http.StatusBadRequest, "InvalidParameter.AssetReferences", "Too many material references")
	}
	storage, err := model.LoadAssetStorageConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !storage.Enabled {
		return nil, assetError(http.StatusServiceUnavailable, "AssetStorageDisabled", "Material storage is not enabled")
	}
	assets := make([]model.Asset, 0, len(ids))
	databaseIDs := make([]int64, 0, len(ids))
	project := ""
	portraitChannelID := 0
	for _, id := range ids {
		asset, loadErr := model.GetAsset(ctx, actor.UserID, id, "")
		if errors.Is(loadErr, gorm.ErrRecordNotFound) {
			return nil, assetError(http.StatusNotFound, "ResourceNotFound", "A referenced material was not found")
		}
		if loadErr != nil {
			return nil, loadErr
		}
		if project != "" && project != asset.ProjectName {
			return nil, assetError(http.StatusBadRequest, "InvalidParameter.ProjectName", "All referenced materials must belong to one project")
		}
		group, groupErr := model.GetAssetGroupByID(ctx, asset.GroupID)
		if groupErr != nil {
			return nil, groupErr
		}
		if !group.Usable() {
			return nil, assetError(http.StatusConflict, "AssetGroupUnavailable", "The material group is being deleted")
		}
		if group.GroupType == model.AssetGroupLivenessFace {
			if portraitChannelID != 0 && portraitChannelID != group.InitialChannelID {
				return nil, assetError(http.StatusBadRequest, "AssetChannelConflict", "Referenced portraits must share one verification channel")
			}
			portraitChannelID = group.InitialChannelID
			if portraitChannelID <= 0 {
				return nil, assetError(http.StatusConflict, "AssetGroupUnavailable", "The portrait group requires verification")
			}
		}
		project = asset.ProjectName
		assets = append(assets, *asset)
		databaseIDs = append(databaseIDs, asset.ID)
	}
	if portraitChannelID != 0 {
		if !slices.Contains(actor.ChannelIDs, portraitChannelID) {
			return nil, assetError(http.StatusForbidden, "AssetChannelUnavailable", "The portrait verification channel is unavailable")
		}
		// Virtual references may be prepared on this account; a person's
		// authorization cannot be copied to a different account or channel.
		actor.ChannelIDs = []int{portraitChannelID}
		preferredChannelID = portraitChannelID
	}
	if len(actor.ChannelIDs) == 0 {
		return nil, assetError(http.StatusForbidden, "NoAvailableChannel", "No material channel is available to this caller")
	}
	if slices.Contains(actor.ChannelIDs, preferredChannelID) {
		if err = model.EnsureAssetReplicas(ctx, assets, preferredChannelID, actor.TokenID); err != nil {
			return nil, err
		}
	}
	replicas, err := model.AssetReplicas(ctx, databaseIDs)
	if err != nil {
		return nil, err
	}
	ready := []int{}
	for _, channelID := range actor.ChannelIDs {
		allActive := true
		for _, asset := range assets {
			if !slices.ContainsFunc(replicas, func(replica model.AssetReplica) bool {
				return replica.AssetID == asset.ID && replica.ChannelID == channelID && replica.Status == model.AssetActive && replica.State == model.AssetReady && replica.UpstreamID != ""
			}) {
				allActive = false
				break
			}
		}
		if allActive {
			ready = append(ready, channelID)
		}
	}
	if len(ready) > 0 {
		return ready, nil
	}
	for _, replica := range replicas {
		if replica.ChannelID == preferredChannelID && replica.Status == model.AssetFailed {
			return nil, assetError(http.StatusUnprocessableEntity, "AssetFailed", "A material failed preparation on the selected channel; inspect its replica status")
		}
	}
	return nil, &AssetError{HTTPStatus: http.StatusConflict, Code: "AssetNotReady", Message: "Materials are being prepared for an eligible channel; retry later", Retryable: true}
}

func ResolveAssetReferences(ctx context.Context, actor dto.AssetActor, channelID int, ids []string, taskID string) ([]dto.ResolvedAssetReference, error) {
	if taskID == "" || !slices.Contains(actor.ChannelIDs, channelID) {
		return nil, assetError(http.StatusForbidden, "AccessDenied", "The material channel is unavailable to this caller")
	}
	result, err := model.HoldAssetUses(ctx, actor.UserID, channelID, ids, taskID)
	if errors.Is(err, model.ErrAssetGroupUnavailable) {
		return nil, assetError(http.StatusConflict, "AssetGroupUnavailable", "The material group is being deleted")
	}
	if errors.Is(err, model.ErrAssetChannelConflict) {
		return nil, assetError(http.StatusBadRequest, "AssetChannelConflict", "Referenced portraits must share one verification channel")
	}
	if errors.Is(err, model.ErrAssetStorageDisabled) {
		return nil, assetError(http.StatusServiceUnavailable, "AssetStorageDisabled", "Material storage is not enabled")
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, &AssetError{HTTPStatus: http.StatusConflict, Code: "AssetNotReady", Message: "Material state changed; retry the request", Retryable: true}
	}
	return result, err
}
