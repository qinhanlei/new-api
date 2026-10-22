package controller

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/gin-gonic/gin"
)

func CreateArkVideo(c *gin.Context) {
	if _, exists := c.Get(jsplugin.ContextKeyPinnedEndpoint); !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "unsupported_model", "message": i18n.T(c, i18n.MsgVideoUnsupportedModel)}})
		return
	}
	RelayTask(c)
}

func GetArkVideo(c *gin.Context) {
	task, exists, err := model.GetByTaskId(c.GetInt("id"), c.Param("task_id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "server_error", "message": i18n.T(c, i18n.MsgVideoRetrieveFailed)}})
		return
	}
	if !exists || !task.ResultRetrievable() || (!strings.HasSuffix(task.Action, "_to_video") && task.Action != "remix") {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "task_not_found", "message": i18n.T(c, i18n.MsgVideoTaskNotFound)}})
		return
	}
	c.JSON(http.StatusOK, arkTaskView(task))
}

func arkTaskView(task *model.Task) map[string]any {
	status := "queued"
	switch task.Status {
	case model.TaskStatusInProgress:
		status = "running"
	case model.TaskStatusSuccess:
		status = "succeeded"
	case model.TaskStatusFailure:
		status = "failed"
	case model.TaskStatusSubmitUnknown:
		status = "unknown"
	}
	view := map[string]any{"id": task.TaskID, "model": task.Properties.OriginModelName, "status": status, "created_at": task.CreatedAt, "updated_at": task.UpdatedAt}
	if task.Status == model.TaskStatusWaitingAssets {
		view["phase"] = "waiting_assets"
	}
	if task.Status == model.TaskStatusSubmitUnknown {
		view["phase"] = "submission_unknown"
	}
	if task.Status == model.TaskStatusSubmitting && task.FailReason == "Accepted task is waiting for billing settlement" {
		view["phase"] = "billing_pending"
	}
	if task.Status == model.TaskStatusSuccess && task.GetResultURL() != "" {
		view["content"] = map[string]any{"video_url": task.GetResultURL()}
	}
	if task.FailReason != "" {
		view["error"] = map[string]any{"message": task.FailReason}
	}
	return view
}
