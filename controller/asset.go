package controller

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

const assetAPIVersion = "2024-01-01"

func assetRequestAction(c *gin.Context) (string, error) {
	action := c.Query("Action")
	if c.Query("Version") != assetAPIVersion {
		return action, &service.AssetError{HTTPStatus: http.StatusBadRequest, Code: "InvalidParameter.Version", Message: "Version must be 2024-01-01."}
	}
	switch action {
	case "CreateAssetGroup", "GetAssetGroup", "ListAssetGroups", "UpdateAssetGroup", "DeleteAssetGroup",
		"CreateAsset", "GetAsset", "ListAssets", "UpdateAsset", "DeleteAsset":
		return action, nil
	default:
		return action, &service.AssetError{HTTPStatus: http.StatusBadRequest, Code: "InvalidAction", Message: "The asset action is not supported."}
	}
}

// ArkAssets is a resource API. It never enters the video task/billing pipeline.
func ArkAssets(c *gin.Context) {
	action, err := assetRequestAction(c)
	if err != nil {
		writeAssetActionResponse(c, action, nil, err)
		return
	}
	actor, err := service.ResolveAssetActor(c)
	if err != nil {
		writeAssetActionResponse(c, action, nil, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	if err != nil {
		writeAssetActionResponse(c, action, nil, &service.AssetError{HTTPStatus: http.StatusBadRequest, Code: "InvalidParameter", Message: "The asset request body is invalid or too large."})
		return
	}
	result, err := service.ExecuteAssetAction(c.Request.Context(), actor, action, body)
	writeAssetActionResponse(c, action, result, err)
}

func writeAssetActionResponse(c *gin.Context, action string, result any, err error) {
	metadata := gin.H{"RequestId": c.GetString(common.RequestIdKey), "Action": action, "Version": assetAPIVersion, "Service": "ark", "Region": "cn-beijing"}
	if err != nil {
		failure := assetHTTPError(err)
		metadata["Error"] = dto.AssetFailure{Code: failure.Code, Message: i18n.TSource(c, failure.Message)}
		if failure.Retryable {
			c.Header("Retry-After", "5")
		}
		c.JSON(failure.HTTPStatus, gin.H{"ResponseMetadata": metadata})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ResponseMetadata": metadata, "Result": result})
}

func assetHTTPError(err error) *service.AssetError {
	var failure *service.AssetError
	if errors.As(err, &failure) {
		return failure
	}
	if errors.Is(err, model.ErrAssetConflict) {
		return &service.AssetError{HTTPStatus: http.StatusConflict, Code: "Conflict", Message: "Asset settings changed. Reload and try again."}
	}
	if errors.Is(err, model.ErrAssetInUse) {
		return &service.AssetError{HTTPStatus: http.StatusConflict, Code: "ResourceInUse", Message: "Remove existing asset resources before changing their storage or upstream account."}
	}
	if errors.Is(err, model.ErrAssetStorageDisabled) {
		return &service.AssetError{HTTPStatus: http.StatusServiceUnavailable, Code: "AssetStorageDisabled", Message: "Material storage is not enabled"}
	}
	return &service.AssetError{HTTPStatus: http.StatusInternalServerError, Code: "InternalError", Message: "The asset operation could not be completed."}
}

func writeAssetManagementError(c *gin.Context, err error) {
	failure := assetHTTPError(err)
	common.ApiErrorStatus(c, failure.HTTPStatus, common.NewMessage(failure.Message), gin.H{"code": failure.Code})
}

// assetManagementActor permits administrative ownership selection explicitly;
// a user-supplied owner never changes the authenticated identity in the context.
func assetManagementActor(c *gin.Context, action string) (dto.AssetActor, bool) {
	read := strings.HasPrefix(action, "Get") || strings.HasPrefix(action, "List")
	scope := "asset:write"
	if read {
		scope = "asset:read"
	}
	if c.GetBool("use_access_token") && c.GetInt("access_token_id") > 0 && !service.AccessTokenScopeGranted(c.GetStringSlice("access_token_scopes"), scope) {
		common.ApiErrorStatus(c, http.StatusForbidden, common.NewMessage("You do not have permission to perform this action."))
		return dto.AssetActor{}, false
	}
	userID := c.GetInt("id")
	requestedOwner := c.Query("user_id")
	if requestedOwner != "" {
		id, err := strconv.Atoi(requestedOwner)
		if err != nil || id <= 0 {
			common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
			return dto.AssetActor{}, false
		}
		userID = id
	}
	allUsers := c.Query("all_users") == "true"
	if userID != c.GetInt("id") || allUsers {
		adminScope := "user:write"
		if read {
			adminScope = "user:read"
		}
		if c.GetInt("role") < common.RoleAdminUser ||
			(c.GetBool("use_access_token") && c.GetInt("access_token_id") > 0 && !service.AccessTokenScopeGranted(c.GetStringSlice("access_token_scopes"), adminScope)) {
			common.ApiErrorStatus(c, http.StatusForbidden, common.NewMessage("You do not have permission to perform this action."))
			return dto.AssetActor{}, false
		}
	}
	if allUsers && (!strings.HasPrefix(action, "List") || requestedOwner != "") {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Select an asset owner before changing assets."))
		return dto.AssetActor{}, false
	}
	actor := dto.AssetActor{UserID: userID}
	// Existing assets remain manageable after an owner is disabled or removed.
	// Only creation needs an active owner and currently eligible channels.
	if strings.HasPrefix(action, "Create") {
		var err error
		actor, err = service.AssetActorForUser(c.Request.Context(), userID, 0)
		if err != nil {
			writeAssetManagementError(c, err)
			return actor, false
		}
	}
	actor.AllUsers = allUsers
	return actor, true
}

func ManageAssetAction(c *gin.Context) {
	action, err := assetRequestAction(c)
	if err != nil {
		writeAssetManagementError(c, err)
		return
	}
	actor, ok := assetManagementActor(c, action)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	result, err := service.ExecuteAssetAction(c.Request.Context(), actor, action, body)
	if err != nil {
		writeAssetManagementError(c, err)
		return
	}
	if strings.HasPrefix(action, "Create") || strings.HasPrefix(action, "Update") || strings.HasPrefix(action, "Delete") {
		recordManageAuditFor(c, actor.UserID, "asset.manage", map[string]any{"action": action})
	}
	common.ApiSuccess(c, result)
}

func UploadAsset(c *gin.Context) {
	actor, ok := assetManagementActor(c, "CreateAsset")
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.MaxAssetBytes+(1<<20))
	if err := c.Request.ParseMultipartForm(8 << 20); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("The asset upload is invalid or too large."))
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Choose a file to upload."))
		return
	}
	defer file.Close()
	input := dto.AssetUpload{
		GroupID: c.PostForm("GroupId"), ProjectName: c.PostForm("ProjectName"), Name: c.PostForm("Name"),
		AssetType: c.PostForm("AssetType"), Size: header.Size, ContentType: header.Header.Get("Content-Type"),
	}
	result, err := service.CreateUploadedAsset(c.Request.Context(), actor, input, file)
	if err != nil {
		writeAssetManagementError(c, err)
		return
	}
	recordManageAuditFor(c, actor.UserID, "asset.manage", map[string]any{"action": "CreateAsset"})
	common.ApiSuccess(c, result)
}

func GetAssetAvailability(c *gin.Context) {
	config, err := service.GetAssetStorageConfig(c.Request.Context())
	if err != nil {
		writeAssetManagementError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"enabled": config.Enabled})
}

func GetAssetStorage(c *gin.Context) {
	config, err := service.GetAssetStorageConfig(c.Request.Context())
	if err != nil {
		writeAssetManagementError(c, err)
		return
	}
	common.ApiSuccess(c, config)
}

func UpdateAssetStorage(c *gin.Context) {
	var request dto.AssetStorageUpdate
	if err := common.DecodeJson(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10), &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	view, err := service.UpdateAssetStorageConfig(c.Request.Context(), request)
	if err != nil {
		writeAssetManagementError(c, err)
		return
	}
	recordManageAudit(c, "asset.storage.update", map[string]any{"enabled": view.Enabled, "revision": view.Revision})
	common.ApiSuccess(c, view)
}

func TestAssetStorage(c *gin.Context) {
	var request dto.AssetStorageUpdate
	if err := common.DecodeJson(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10), &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	err := service.TestAssetStorageConfig(c.Request.Context(), request)
	recordManageAudit(c, "asset.storage.test", map[string]any{"success": err == nil})
	if err != nil {
		writeAssetManagementError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func GetChannelAssets(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	view, err := service.GetAssetChannelConfig(c.Request.Context(), id)
	if err != nil {
		writeAssetManagementError(c, err)
		return
	}
	common.ApiSuccess(c, view)
}

func UpdateChannelAssets(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	var input dto.AssetChannelUpdate
	if err != nil || id <= 0 || common.DecodeJson(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10), &input) != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	view, err := service.UpdateAssetChannelConfig(c.Request.Context(), id, input)
	if err != nil {
		writeAssetManagementError(c, err)
		return
	}
	recordManageAudit(c, "channel.assets.update", map[string]any{"id": id, "enabled": view.Enabled, "revision": view.Revision})
	common.ApiSuccess(c, view)
}

func GetAssetReplicas(c *gin.Context) {
	if c.GetInt("role") < common.RoleAdminUser || !authz.Can(c.GetInt("id"), c.GetInt("role"), authz.ChannelRead) ||
		(c.GetBool("use_access_token") && c.GetInt("access_token_id") > 0 && !service.AccessTokenScopeGranted(c.GetStringSlice("access_token_scopes"), service.AccessTokenScopeOf(authz.ChannelRead))) {
		common.ApiErrorStatus(c, http.StatusForbidden, common.NewMessage("You do not have permission to perform this action."))
		return
	}
	actor, ok := assetManagementActor(c, "GetAsset")
	if !ok {
		return
	}
	items, err := model.GetAssetReplicaViews(c.Request.Context(), actor.UserID, c.Param("id"))
	if err != nil {
		writeAssetManagementError(c, err)
		return
	}
	common.ApiSuccess(c, items)
}
