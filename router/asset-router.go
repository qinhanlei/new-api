package router

import (
	"bytes"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

// SetAssetRouter registers a single management path beside the plugin-owned
// video paths. A wildcard here would shadow the doubao task plugin.
func SetAssetRouter(engine *gin.Engine) {
	engine.POST("/doubao/", middleware.RouteTag("relay"), assetActionEnvelope(),
		middleware.BodyStorageCleanup(), middleware.GlobalAPIRateLimit(), middleware.TokenAuth(), controller.ArkAssets)
}

func registerAssetManagementRoutes(api *gin.RouterGroup) {
	api.GET("/assets/validation/callback/:state", middleware.DisableCache(), controller.AssetValidationCallback)
	assets := api.Group("/assets", middleware.UserAuth(), middleware.DisableCache())
	assets.GET("/config", controller.GetAssetAvailability)
	assets.GET("/validation/sessions", controller.ListAssetValidationSessions)
	assets.POST("/actions", assetMutationRateLimit(), controller.ManageAssetAction)
	assets.POST("/upload", middleware.UserCriticalRateLimit("assets"), controller.UploadAsset)
	assets.GET("/:id/replicas", controller.GetAssetReplicas)
	assets.POST("/:id/replicas/:channel_id/retry", middleware.UserCriticalRateLimit("assets"), controller.RetryAssetReplica)
}

func assetMutationRateLimit() gin.HandlerFunc {
	limit := middleware.UserCriticalRateLimit("assets")
	return func(c *gin.Context) {
		switch c.Query("Action") {
		case "GetAsset", "GetAssetGroup", "ListAssets", "ListAssetGroups":
			// These POST actions are reads, including normal status polling.
		default:
			limit(c)
		}
	}
}

// Existing authentication/rate-limit middleware returns gateway error JSON.
// Resource actions retain the Ark envelope even when those checks abort before
// the controller runs; the actual authentication policy is unchanged.
func assetActionEnvelope() gin.HandlerFunc {
	return func(c *gin.Context) {
		original := c.Writer
		buffer := &assetResponseBuffer{ResponseWriter: original, status: http.StatusOK}
		c.Writer = buffer
		defer func() { c.Writer = original }()
		c.Next()
		c.Writer = original
		var body map[string]any
		_ = common.Unmarshal(buffer.body.Bytes(), &body)
		if buffer.status >= http.StatusBadRequest && body["ResponseMetadata"] == nil {
			code := "InvalidParameter"
			switch buffer.status {
			case http.StatusUnauthorized:
				code = "AuthenticationError"
			case http.StatusForbidden:
				code = "AccessDenied"
			case http.StatusTooManyRequests:
				code = "Throttling"
			}
			message := "The asset request was rejected."
			if text, ok := body["message"].(string); ok && text != "" {
				message = text
			} else if failure, ok := body["error"].(map[string]any); ok {
				if text, ok := failure["message"].(string); ok && text != "" {
					message = text
				}
			}
			if buffer.status >= http.StatusInternalServerError {
				code, message = "InternalError", "The asset operation could not be completed."
			}
			c.Header("Content-Length", "")
			c.JSON(buffer.status, gin.H{"ResponseMetadata": gin.H{
				"RequestId": c.GetString(common.RequestIdKey), "Action": c.Query("Action"),
				"Version": "2024-01-01", "Service": "ark", "Region": "cn-beijing",
				"Error": gin.H{"Code": code, "Message": message},
			}})
			return
		}
		original.WriteHeader(buffer.status)
		_, _ = original.Write(buffer.body.Bytes())
	}
}

type assetResponseBuffer struct {
	gin.ResponseWriter
	body    bytes.Buffer
	status  int
	written bool
}

func (w *assetResponseBuffer) WriteHeader(status int) {
	if !w.written {
		w.status = status
	}
}
func (w *assetResponseBuffer) WriteHeaderNow() { w.written = true }
func (w *assetResponseBuffer) Written() bool   { return w.written }
func (w *assetResponseBuffer) Status() int     { return w.status }
func (w *assetResponseBuffer) Size() int       { return w.body.Len() }
func (w *assetResponseBuffer) Flush()          { w.written = true }
func (w *assetResponseBuffer) Write(body []byte) (int, error) {
	w.written = true
	return w.body.Write(body)
}
func (w *assetResponseBuffer) WriteString(body string) (int, error) {
	w.written = true
	return w.body.WriteString(body)
}
