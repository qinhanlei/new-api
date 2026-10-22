package controller

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/assetprovider"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var errAssetAccessLimited = errors.New("Asset access limit exceeded")

// ServeContent clears cache headers on errors such as an unsatisfiable Range.
// A capability-protected source must remain private on every response.
type assetContentResponseWriter struct {
	http.ResponseWriter
}

func (w assetContentResponseWriter) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.ResponseWriter.WriteHeader(status)
}

func AssetContent(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "no-referrer")
	release, ok := middleware.AcquireAnonymousContentAccess(c, c.Param("id"), "source")
	if !ok {
		assetError(c, errAssetAccessLimited)
		return
	}
	defer release()

	expires, access := middleware.LocalAssetContentAccessQuery(c)
	asset, file, err := service.OpenLocalAssetContent(c.Request.Context(), c.Param("id"), expires, access)
	if err != nil {
		if errors.Is(err, service.ErrAssetAccessInvalid) || errors.Is(err, gorm.ErrRecordNotFound) {
			err = gorm.ErrRecordNotFound
			if !middleware.AllowInvalidContentAccessAttempt(c) {
				err = errAssetAccessLimited
			}
		}
		assetError(c, err)
		return
	}
	defer file.Close()

	c.Header("Content-Type", asset.MIMEType)
	c.Header("Content-Disposition", "attachment")
	http.ServeContent(assetContentResponseWriter{c.Writer}, c.Request, "", time.Unix(asset.CreatedAt, 0), file)
}

// Reconciliation is an explicit administrator statement after checking the
// upstream console. The original request is never replayed on a mere timeout.
func ReconcileAssetProvider(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		RemoteID        string `json:"remote_id"`
		ConfirmedAbsent bool   `json:"confirmed_absent"`
		Note            string `json:"note"`
	}
	if c.ShouldBindJSON(&input) != nil || !service.ValidateAssetName(input.Note) || (input.RemoteID == "") == !input.ConfirmedAbsent || len(input.RemoteID) > 255 {
		assetError(c, service.ErrAssetInvalid)
		return
	}
	var replica model.AssetReplica
	if err := model.DB.WithContext(c.Request.Context()).Where("asset_id = ? AND space_id = ?", c.Param("id"), c.Param("space_id")).First(&replica).Error; err != nil {
		assetError(c, err)
		return
	}
	if replica.Status != model.AssetReplicaUnknown || replica.LeaseUntil > time.Now().Unix() {
		assetError(c, service.ErrAssetInvalid)
		return
	}
	updates := map[string]any{"reconciled_by": c.GetInt("id"), "reconcile_note": input.Note, "reconciled_at": time.Now().Unix(), "lease_owner": "", "lease_until": 0, "next_attempt": 0, "error": ""}
	if input.ConfirmedAbsent {
		updates["status"], updates["remote_id"], updates["generation_ref"] = model.AssetReplicaPending, "", ""
	} else {
		var space model.AssetSpace
		if err := model.DB.WithContext(c.Request.Context()).Where("id = ?", replica.SpaceID).First(&space).Error; err != nil {
			assetError(c, err)
			return
		}
		client, err := service.AssetProviderClient(c.Request.Context(), &space)
		if err != nil {
			assetError(c, service.ErrAssetStorageUnavailable)
			return
		}
		result, err := client.Get(c.Request.Context(), input.RemoteID)
		if err != nil || result.ID != input.RemoteID {
			assetError(c, service.ErrAssetInvalid)
			return
		}
		status := model.AssetReplicaProcessing
		switch result.Status {
		case assetprovider.StatusReady:
			status = model.AssetReplicaReady
		case assetprovider.StatusFailed:
			status = model.AssetReplicaFailed
		}
		updates["status"], updates["remote_id"], updates["generation_ref"] = status, result.ID, result.Reference
	}
	result := model.DB.WithContext(c.Request.Context()).Model(&replica).Where("status = ? AND lease_until <= ?", model.AssetReplicaUnknown, time.Now().Unix()).Updates(updates)
	if result.Error != nil {
		assetError(c, result.Error)
		return
	}
	if result.RowsAffected != 1 {
		assetError(c, service.ErrAssetInvalid)
		return
	}
	common.ApiSuccess(c, nil)
}

func ReconcileAssetTask(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		UpstreamTaskID  string `json:"upstream_task_id"`
		ConfirmedAbsent bool   `json:"confirmed_absent"`
		Note            string `json:"note"`
	}
	if c.ShouldBindJSON(&input) != nil || !service.ValidateAssetName(input.Note) || (input.UpstreamTaskID == "") == !input.ConfirmedAbsent || len(input.UpstreamTaskID) > 255 || strings.ContainsAny(input.UpstreamTaskID, "/\\?\r\n\t ") {
		assetError(c, service.ErrAssetInvalid)
		return
	}
	err := model.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var submission model.TaskSubmission
		if err := tx.Where("task_id = ? AND phase = ? AND lease_until <= ?", c.Param("task_id"), model.SubmissionUnknown, time.Now().Unix()).First(&submission).Error; err != nil {
			return err
		}
		updates := map[string]any{"reconciled_by": c.GetInt("id"), "reconcile_note": input.Note, "reconciled_at": time.Now().Unix(), "next_attempt": 0}
		if input.ConfirmedAbsent {
			updates["phase"], updates["error"] = model.SubmissionFailed, "Administrator confirmed submission was not accepted"
		} else {
			var task model.Task
			if err := tx.Where("task_id = ?", submission.TaskID).First(&task).Error; err != nil {
				return err
			}
			task.PrivateData.UpstreamTaskID = input.UpstreamTaskID
			if err := tx.Model(&task).Updates(map[string]any{"private_data": task.PrivateData, "status": model.TaskStatusSubmitting}).Error; err != nil {
				return err
			}
			updates["phase"], updates["result_status"], updates["result_quota"], updates["error"] = model.SubmissionAccepted, model.TaskStatusSubmitted, task.Quota, ""
		}
		result := tx.Model(&submission).Where("phase = ?", model.SubmissionUnknown).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return service.ErrAssetInvalid
		}
		return nil
	})
	if err != nil {
		assetError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func assetResponse(c *gin.Context, status int, data any) {
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		common.ApiSuccess(c, data)
		return
	}
	c.JSON(status, data)
}

func assetError(c *gin.Context, err error) {
	var consoleMessage *common.Message
	status, code, message := http.StatusInternalServerError, "asset_internal_error", "Unable to process the asset request"
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		status, code, message = http.StatusNotFound, "asset_not_found", "Asset not found"
	case errors.As(err, &consoleMessage):
		status, code, message = http.StatusConflict, "invalid_asset_request", consoleMessage.Error()
	case errors.Is(err, service.ErrAssetInvalid):
		status, code, message = http.StatusBadRequest, "invalid_asset_request", err.Error()
	case errors.Is(err, service.ErrAssetMediaType):
		status, code, message = http.StatusUnsupportedMediaType, "unsupported_asset_media", err.Error()
	case errors.Is(err, service.ErrAssetTooLarge):
		status, code, message = http.StatusRequestEntityTooLarge, "asset_too_large", err.Error()
	case errors.Is(err, model.ErrAssetLimit):
		status, code, message = http.StatusConflict, "asset_storage_limit", err.Error()
	case errors.Is(err, model.ErrAssetUploading):
		status, code, message = http.StatusConflict, "asset_upload_in_progress", err.Error()
	case errors.Is(err, service.ErrAssetSource):
		status, code, message = http.StatusBadRequest, "asset_source_error", err.Error()
	case errors.Is(err, service.ErrAssetStorageUnavailable):
		status, code, message = http.StatusServiceUnavailable, "asset_storage_unavailable", err.Error()
	case errors.Is(err, errAssetAccessLimited):
		status, code, message = http.StatusTooManyRequests, "asset_access_limited", err.Error()
		c.Header("Retry-After", "60")
	}
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		common.ApiErrorStatus(c, status, common.NewMessage(message), gin.H{"code": code})
		return
	}
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": i18n.T(c, "asset."+code), "type": "asset_error"}})
}

func CreateAsset(c *gin.Context) {
	store, err := service.GetAssetStore()
	if err != nil {
		assetError(c, err)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.AssetMaxUploadBytes+(1<<20))
	var asset *model.Asset
	if strings.HasPrefix(c.ContentType(), "multipart/form-data") {
		if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
			assetError(c, service.ErrAssetTooLarge)
			return
		}
		defer c.Request.MultipartForm.RemoveAll()
		files := c.Request.MultipartForm.File["file"]
		if len(files) != 1 || len(c.Request.MultipartForm.File) != 1 {
			assetError(c, service.ErrAssetInvalid)
			return
		}
		if files[0].Size > service.AssetMaxUploadBytes {
			assetError(c, service.ErrAssetTooLarge)
			return
		}
		file, openErr := files[0].Open()
		if openErr != nil {
			assetError(c, service.ErrAssetInvalid)
			return
		}
		defer file.Close()
		name := c.PostForm("name")
		if name == "" {
			name = filepath.Base(files[0].Filename)
		}
		asset, err = service.CreateAsset(c.Request.Context(), c.GetInt("id"), name, file, store)
	} else {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
		var input struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if c.ShouldBindJSON(&input) != nil || !service.ValidateAssetName(input.Name) {
			assetError(c, service.ErrAssetInvalid)
			return
		}
		asset, err = service.ImportAsset(c.Request.Context(), c.GetInt("id"), input.Name, input.URL, store)
	}
	if err != nil {
		assetError(c, err)
		return
	}
	assetResponse(c, http.StatusCreated, asset)
}

func ListAssets(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	page, size = max(1, min(page, 100000)), max(1, min(size, 100))
	assets, total, err := model.ListUserAssets(c.Request.Context(), c.GetInt("id"), (page-1)*size, size)
	if err != nil {
		assetError(c, err)
		return
	}
	assetResponse(c, http.StatusOK, gin.H{"object": "list", "items": assets, "total": total, "page": page, "page_size": size})
}

func GetAsset(c *gin.Context) {
	asset, err := model.GetAsset(c.Request.Context(), c.Param("id"))
	if err == nil && asset.UserID != c.GetInt("id") {
		err = gorm.ErrRecordNotFound
	}
	if err != nil {
		assetError(c, err)
		return
	}
	assetResponse(c, http.StatusOK, asset)
}

func UpdateAsset(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		Name string `json:"name"`
	}
	if c.ShouldBindJSON(&input) != nil || !service.ValidateAssetName(input.Name) {
		assetError(c, service.ErrAssetInvalid)
		return
	}
	if err := model.RenameUserAsset(c.Request.Context(), c.GetInt("id"), c.Param("id"), strings.TrimSpace(input.Name)); err != nil {
		assetError(c, err)
		return
	}
	GetAsset(c)
}

func DeleteAsset(c *gin.Context) {
	if err := model.DeleteUserAsset(c.Request.Context(), c.GetInt("id"), c.Param("id")); err != nil {
		assetError(c, err)
		return
	}
	assetResponse(c, http.StatusOK, gin.H{"id": c.Param("id"), "deleted": true})
}

func GetAssetProviders(c *gin.Context) {
	asset, err := model.GetAsset(c.Request.Context(), c.Param("id"))
	if err == nil && asset.UserID != c.GetInt("id") {
		err = gorm.ErrRecordNotFound
	}
	if err != nil {
		assetError(c, err)
		return
	}
	var replicas []model.AssetReplica
	if err := model.DB.WithContext(c.Request.Context()).Where("asset_id = ?", c.Param("id")).Order("space_id").Find(&replicas).Error; err != nil {
		assetError(c, err)
		return
	}
	providers := make([]gin.H, 0, len(replicas))
	for _, replica := range replicas {
		var space model.AssetSpace
		if err := model.DB.WithContext(c.Request.Context()).Select("id", "channel_id").Where("id = ?", replica.SpaceID).First(&space).Error; err != nil {
			assetError(c, err)
			return
		}
		var channel model.Channel
		if err := model.DB.WithContext(c.Request.Context()).Select("id", "name").Where("id = ?", space.ChannelID).First(&channel).Error; err != nil {
			assetError(c, err)
			return
		}
		providers = append(providers, gin.H{"channel_id": channel.Id, "name": channel.Name, "status": replica.Status, "error": replica.Error})
	}
	assetResponse(c, http.StatusOK, gin.H{"items": providers})
}

func GetChannelAssets(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		assetError(c, service.ErrAssetInvalid)
		return
	}
	space, err := model.GetChannelAssetSpace(c.Request.Context(), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		common.ApiSuccess(c, nil)
		return
	}
	if err != nil {
		assetError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"id": space.ID, "channel_id": space.ChannelID, "status": space.GroupState, "error": space.Error, "group_id": space.GroupID})
}

func CleanupChannelAssets(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 || !common.AssetLibraryEnabled {
		assetError(c, service.ErrAssetInvalid)
		return
	}
	if err := model.RequestChannelAssetCleanup(c.Request.Context(), id); err != nil {
		assetError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"status": "cleaning"})
}

func ReconcileAssetGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		GroupID         string `json:"group_id"`
		ConfirmedAbsent bool   `json:"confirmed_absent"`
		Note            string `json:"note"`
	}
	if err != nil || id <= 0 || c.ShouldBindJSON(&input) != nil || !service.ValidateAssetName(input.Note) || (input.GroupID == "") == !input.ConfirmedAbsent {
		assetError(c, service.ErrAssetInvalid)
		return
	}
	space, err := model.GetChannelAssetSpace(c.Request.Context(), id)
	if err != nil {
		assetError(c, err)
		return
	}
	if space.GroupState != "unknown" || space.LeaseUntil > time.Now().Unix() {
		assetError(c, service.ErrAssetInvalid)
		return
	}
	updates := map[string]any{"reconciled_by": c.GetInt("id"), "reconcile_note": input.Note, "reconciled_at": time.Now().Unix(),
		"group_id": "", "group_state": "pending", "lease_owner": "", "lease_until": 0, "next_attempt": 0, "error": ""}
	if !input.ConfirmedAbsent {
		client, err := service.AssetProviderClient(c.Request.Context(), space)
		if err != nil {
			assetError(c, service.ErrAssetInvalid)
			return
		}
		group, err := client.GetGroup(c.Request.Context(), input.GroupID)
		if err != nil || group.Name != space.ID {
			assetError(c, service.ErrAssetInvalid)
			return
		}
		updates["group_id"], updates["group_state"] = group.ID, "ready"
	}
	result := model.DB.WithContext(c.Request.Context()).Model(&model.AssetSpace{}).
		Where("id = ? AND group_state = ? AND lease_until <= ?", space.ID, "unknown", time.Now().Unix()).Updates(updates)
	if result.Error != nil {
		assetError(c, result.Error)
		return
	}
	if result.RowsAffected != 1 {
		assetError(c, service.ErrAssetInvalid)
		return
	}
	common.ApiSuccess(c, nil)
}
