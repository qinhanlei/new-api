package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func SetArkVideoRouter(router *gin.Engine) {
	api := router.Group("/api/v3/contents/generations/tasks")
	api.Use(middleware.RouteTag("relay"), middleware.TokenAuth(), middleware.BodyStorageCleanup())
	api.POST("", middleware.SystemPerformanceCheck(), middleware.PrepareArkVideo(),
		middleware.PinTaskPluginEndpointForPath("/v1/videos"), middleware.ModelRequestRateLimit(),
		middleware.PrepareTaskPluginEndpoint(), middleware.Distribute(), controller.CreateArkVideo)
	api.GET("/:task_id", controller.GetArkVideo)
}
