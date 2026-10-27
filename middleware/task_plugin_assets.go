package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// The legacy video entry has no host-protocol decoder. Keep its request and
// response contracts while preparing assets before channel selection.
func prepareLegacyVideoAssets(c *gin.Context) error {
	var original map[string]any
	var request relaycommon.TaskSubmitReq
	if err := common.UnmarshalBodyReusable(c, &original); err != nil {
		return nil // Leave malformed legacy requests to their existing validator.
	}
	if err := common.UnmarshalBodyReusable(c, &request); err != nil {
		return nil
	}
	// Include decoded string metadata, and retain references in unknown fields
	// that TaskSubmitReq would otherwise discard.
	ids, err := jsplugin.ReservedAssetReferences(map[string]any{
		"kind": "json", "value": []any{original, request.Metadata},
	})
	if err != nil || len(ids) == 0 {
		return err
	}
	info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
	if taskErr := relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionImageToVideo); taskErr != nil {
		return &service.AssetError{HTTPStatus: taskErr.StatusCode, Code: taskErr.Code, Message: taskErr.Message}
	}
	body, _ := c.Get("task_request")
	encoded, err := common.Marshal(body)
	if err != nil {
		return err
	}
	var normalized any
	if err = common.Unmarshal(encoded, &normalized); err != nil {
		return err
	}
	c.Set("task_action", info.Action)
	c.Set(jsplugin.ContextKeyAssetReferences, ids)
	// Use video bindings only to find capable drivers. Pinning an OpenAI video
	// endpoint here would change the legacy decoder and response envelope.
	endpoint, _ := lookupTaskPluginEndpoint(jsplugin.DefaultRegistry.Generation(), http.MethodPost, "/v1/videos", request.Model)
	filter := dto.ChannelFilter{Kind: dto.FilterTaskPluginIdentity}
	var rejected error
	for _, candidate := range endpoint.Candidates {
		if err := prepareTaskPluginAssets(c, candidate.Plugin, normalized, "task", "create", request.Model); err != nil {
			rejected = err
			continue
		}
		if filter.TaskPluginKey == "" {
			filter.TaskPluginKey = candidate.Plugin.Meta.Key
		}
		filter.TaskPluginKeys = append(filter.TaskPluginKeys, candidate.Plugin.Meta.Key)
		filter.TaskPluginChannelTypes = append(filter.TaskPluginChannelTypes, candidate.Plugin.Meta.ChannelTypes...)
	}
	if filter.TaskPluginKey == "" {
		if rejected != nil {
			return rejected
		}
		return fmt.Errorf("No plugin supports the request's gateway asset references")
	}
	service.GetChannelConstraints(c).AddFilter(filter)
	return nil
}

func prepareTaskPluginAssets(c *gin.Context, plugin *jsplugin.LoadedPlugin, body any, protocol, operation, model string) error {
	decoded := map[string]any{"kind": "json", "value": body}
	ids, err := jsplugin.ExtractAssetReferences(c.Request.Context(), plugin, decoded, protocol, operation, model, true)
	if err != nil {
		return err
	}
	reserved, err := jsplugin.ReservedAssetReferences(decoded)
	if err != nil {
		return err
	}
	if !slices.Equal(reserved, ids) {
		return fmt.Errorf("Plugin does not support the request's gateway asset references")
	}
	if _, probed := c.Get(jsplugin.ContextKeyAssetReferences); probed && !slices.Equal(service.TaskAssetReferences(c), ids) {
		return fmt.Errorf("Decoded asset references differ from the original request")
	}
	if len(ids) > 0 {
		if common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled) {
			limit, _ := common.GetContextKeyType[map[string]bool](c, constant.ContextKeyTokenModelLimit)
			if !TokenModelLimitAllows(limit, model) {
				return &service.AssetError{HTTPStatus: http.StatusForbidden, Code: "AccessDenied", Message: i18n.T(c, i18n.MsgDistributorTokenModelForbidden, map[string]any{"Model": model})}
			}
		}
		c.Set(jsplugin.ContextKeyAssetReferences, ids)
	}
	return nil
}

// probeTaskPluginAssets runs only the optional pure preparation hook before a
// shared endpoint selects ordinary relay versus task-plugin handling. A model
// declaration alone still does not change how requests without assets route.
func probeTaskPluginAssets(c *gin.Context, endpoint jsplugin.PinnedEndpoint) (jsplugin.PinnedEndpoint, error) {
	request, err := buildTaskPluginRouteRequest(c)
	if err != nil {
		return endpoint, err
	}
	expected, err := jsplugin.ReservedAssetReferences(request.Body)
	if err != nil {
		return endpoint, err
	}
	c.Set(jsplugin.ContextKeyAssetReferences, expected)
	if len(expected) == 0 {
		return endpoint, nil
	}
	var accepted []jsplugin.ProtocolBinding
	for _, candidate := range endpoint.Candidates {
		ids, err := jsplugin.ExtractAssetReferences(c.Request.Context(), candidate.Plugin, request.Body, candidate.Protocol, candidate.Operation.Name, endpoint.Model, false)
		if err != nil {
			return endpoint, err
		}
		if len(ids) == 0 {
			continue
		}
		if !slices.Equal(expected, ids) {
			return endpoint, fmt.Errorf("Plugin asset references differ from the reserved gateway references")
		}
		accepted = append(accepted, candidate)
	}
	if len(accepted) == 0 {
		return endpoint, fmt.Errorf("No plugin supports the request's gateway asset references")
	}
	endpoint.Candidates = accepted
	endpoint.Plugin = accepted[0].Plugin
	endpoint.Protocol = accepted[0].Protocol
	endpoint.Operation = accepted[0].Operation
	return endpoint, nil
}

func abortTaskAssetError(c *gin.Context, err error) {
	var assetErr *service.AssetError
	if errors.As(err, &assetErr) {
		abortWithOpenAiMessage(c, assetErr.HTTPStatus, i18n.TSource(c, assetErr.Message), types.ErrorCode(assetErr.Code))
		return
	}
	abortWithOpenAiMessage(c, http.StatusBadRequest, i18n.TSource(c, assetReferenceErrorMessage(err)), "InvalidAssetReference")
}

func assetReferenceErrorMessage(err error) string {
	var assetErr *service.AssetError
	if errors.As(err, &assetErr) {
		return assetErr.Message
	}
	if message := taskPluginHookDetail(err); message != "" {
		return message
	}
	return "Invalid gateway asset references"
}
