package router

import (
	"net/http"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

func SetAssetRouter(router *gin.Engine) {
	content := router.Group("/v1/assets", middleware.RouteTag("relay"))
	content.GET("/:id/content", controller.AssetContent)
	content.HEAD("/:id/content", controller.AssetContent)

	api := router.Group("/v1/assets", middleware.RouteTag("relay"), middleware.TokenAuth(), middleware.DisableCache(), middleware.GlobalAPIRateLimit())
	dashboard := router.Group("/api/assets", middleware.RouteTag("api"), middleware.AccessTokenAudit(), middleware.UserAuth(), middleware.DisableCache(), middleware.GlobalAPIRateLimit())
	for _, group := range []*gin.RouterGroup{api, dashboard} {
		group.GET("", controller.ListAssets)
		group.POST("", middleware.CriticalRateLimit(), controller.CreateAsset)
		group.GET("/:id", controller.GetAsset)
		group.PATCH("/:id", controller.UpdateAsset)
		group.DELETE("/:id", controller.DeleteAsset)
		group.GET("/:id/providers", controller.GetAssetProviders)
	}
	channels := router.Group("/api/channel", middleware.RouteTag("api"), middleware.AccessTokenAudit(), middleware.AdminAuth(), middleware.DisableCache(), middleware.GlobalAPIRateLimit())
	handlePermissionRoute(channels, http.MethodGet, "/:id/assets", authz.ChannelRead, controller.GetChannelAssets)
	handlePermissionRoute(channels, http.MethodPost, "/:id/assets/cleanup", authz.ChannelSensitiveWrite, middleware.CriticalRateLimit(), controller.CleanupChannelAssets)

	review := router.Group("/api/asset-reconciliation", middleware.RouteTag("api"), middleware.AccessTokenAudit(), middleware.AdminAuth(), middleware.DisableCache(), middleware.GlobalAPIRateLimit())
	handlePermissionRoute(review, http.MethodPost, "/assets/:id/providers/:space_id", authz.ChannelSensitiveWrite, middleware.CriticalRateLimit(), controller.ReconcileAssetProvider)
	handlePermissionRoute(review, http.MethodPost, "/tasks/:task_id", authz.ChannelSensitiveWrite, middleware.CriticalRateLimit(), controller.ReconcileAssetTask)
	handlePermissionRoute(review, http.MethodPost, "/channels/:id", authz.ChannelSensitiveWrite, middleware.CriticalRateLimit(), controller.ReconcileAssetGroup)
}
