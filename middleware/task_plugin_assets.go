package middleware

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
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
	body, err := buildTaskPluginAssetProbeBody(c)
	if err != nil {
		return endpoint, err
	}
	expected, err := jsplugin.ReservedAssetReferences(body)
	if err != nil {
		return endpoint, err
	}
	c.Set(jsplugin.ContextKeyAssetReferences, expected)
	if len(expected) == 0 {
		return endpoint, nil
	}
	var accepted []jsplugin.ProtocolBinding
	var rejected error
	for _, candidate := range endpoint.Candidates {
		if err := c.Request.Context().Err(); err != nil {
			return endpoint, err
		}
		ids, err := jsplugin.ExtractAssetReferences(c.Request.Context(), candidate.Plugin, body, candidate.Protocol, candidate.Operation.Name, endpoint.Model, false)
		if canceled := c.Request.Context().Err(); canceled != nil {
			return endpoint, canceled
		}
		if err != nil {
			rejected = err
			continue
		}
		if !slices.Equal(expected, ids) {
			rejected = fmt.Errorf("Plugin asset references differ from the reserved gateway references")
			continue
		}
		accepted = append(accepted, candidate)
	}
	if len(accepted) == 0 {
		if rejected != nil {
			return endpoint, rejected
		}
		return endpoint, fmt.Errorf("No plugin supports the request's gateway asset references")
	}
	endpoint.Candidates = accepted
	endpoint.Plugin = accepted[0].Plugin
	endpoint.Protocol = accepted[0].Protocol
	endpoint.Operation = accepted[0].Operation
	return endpoint, nil
}

// Asset probing precedes channel selection. Only a selected plugin may impose
// its upload limits; ordinary relays retain their existing request limits.
func buildTaskPluginAssetProbeBody(c *gin.Context) (any, error) {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil {
		request, requestErr := buildTaskPluginRouteRequest(c)
		return request.Body, requestErr
	}
	switch mediaType {
	case "application/x-www-form-urlencoded":
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return nil, err
		}
		raw, err := storage.Bytes()
		if err != nil {
			return nil, err
		}
		fields, err := url.ParseQuery(string(raw))
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": string(jsplugin.BodyForm), "fields": map[string][]string(fields)}, nil
	case "multipart/form-data":
		form, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return nil, err
		}
		defer form.RemoveAll()
		files := make([]map[string]any, 0)
		for field, headers := range form.File {
			for index, header := range headers {
				files = append(files, map[string]any{
					"ref": jsplugin.FileReference(field, index), "field": field,
					"filename": header.Filename, "mimeType": header.Header.Get("Content-Type"), "size": header.Size,
				})
			}
		}
		return map[string]any{"kind": string(jsplugin.BodyMultipart), "fields": form.Value, "files": files}, nil
	default:
		request, err := buildTaskPluginRouteRequest(c)
		return request.Body, err
	}
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
