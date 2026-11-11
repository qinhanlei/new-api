package jsplugin

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const ContextKeyAssetReferences = "task_plugin_asset_references"
const ContextKeyAssetSubmissionStarted = "task_plugin_asset_submission_started"

// SupportsAssetReferences means the driver understands host-resolved asset
// identities. The matching extraction hook is checked when the plugin loads.
func (m Meta) SupportsAssetReferences() bool {
	return slices.Contains(m.RequiredCapabilities, CapabilityAssetReferences)
}

// ReservedAssetReferences finds only the host's URI namespace, without
// interpreting vendor fields. Form fields may encode JSON containers.
func ReservedAssetReferences(body any) ([]string, error) {
	decoded, _ := body.(map[string]any)
	values := []any{decoded["value"]}
	form := decoded["kind"] == "multipart" || decoded["kind"] == "form"
	if form {
		values = []any{decoded["fields"]}
	}
	var ids []string
	for len(values) > 0 {
		value := values[len(values)-1]
		values = values[:len(values)-1]
		switch value := value.(type) {
		case map[string]any:
			for _, child := range value {
				values = append(values, child)
			}
		case map[string][]string:
			for _, child := range value {
				values = append(values, child)
			}
		case []any:
			values = append(values, value...)
		case []string:
			for _, child := range value {
				values = append(values, child)
			}
		case string:
			if form && (strings.HasPrefix(strings.TrimSpace(value), "{") || strings.HasPrefix(strings.TrimSpace(value), "[")) {
				var nested any
				if common.UnmarshalJsonStr(value, &nested) == nil {
					values = append(values, nested)
					continue
				}
			}
			if strings.HasPrefix(value, "asset://asset-na-") {
				id := strings.TrimPrefix(value, "asset://")
				if !validAssetReference(id) {
					return nil, fmt.Errorf("invalid gateway asset reference")
				}
				if !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
				if len(ids) > 32 {
					return nil, fmt.Errorf("too many gateway asset references")
				}
			}
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func validAssetReference(id string) bool {
	return strings.HasPrefix(id, "asset-na-") && len(id) > len("asset-na-") && len(id) <= 128 && !strings.ContainsAny(id, " /\\?#\t\r\n")
}

// ExtractAssetReferences runs without a channel or fetcher. Raw requests use
// the usual decoded-body union; normalized requests wrap the driver body in
// its JSON variant and set normalized=true.
func ExtractAssetReferences(ctx context.Context, plugin *LoadedPlugin, body any, protocol, operation, model string, normalized bool) ([]string, error) {
	if plugin == nil || !plugin.Meta.SupportsAssetReferences() {
		return nil, nil
	}
	result, err := plugin.Engine.Call(WithFetcher(ctx, nil), "extractAssetReferences", map[string]any{
		"body": body, "protocol": protocol, "operation": operation,
		"model": model, "normalized": normalized,
	})
	if err != nil {
		return nil, err
	}
	items, ok := result.([]any)
	if !ok || len(items) > 32 {
		return nil, fmt.Errorf("asset references must be an array of at most 32 IDs")
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		id, ok := item.(string)
		if !ok || !validAssetReference(id) {
			return nil, fmt.Errorf("invalid gateway asset reference")
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}
