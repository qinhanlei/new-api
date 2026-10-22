package assetprovider

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

var errorCode = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,95}$`)

type upstreamError struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}

// action speaks the Ark 2024-01-01 OpenAPI envelope for native and compatible
// endpoints. Authentication is configured by the channel, never inferred from a host.
func (c *client) action(ctx context.Context, action string, body any) (common.RawMessage, error) {
	endpoint := *c.baseURL
	endpoint.RawQuery = url.Values{"Action": {action}, "Version": {"2024-01-01"}}.Encode()
	create := action == "CreateAsset" || action == "CreateAssetGroup"
	data, status, err := c.do(ctx, http.MethodPost, &endpoint, body, create)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		ResponseMetadata struct {
			Error *upstreamError `json:"Error"`
		} `json:"ResponseMetadata"`
		Result common.RawMessage `json:"Result"`
	}
	if err := common.Unmarshal(data, &envelope); err != nil {
		return nil, &Error{Code: "invalid_response", Message: "Invalid asset provider response", StatusCode: status, Uncertain: create}
	}
	if envelope.ResponseMetadata.Error != nil {
		code := envelope.ResponseMetadata.Error.Code
		// Error codes are persisted by callers. Do not copy arbitrary provider text
		// or signing diagnostics into task records or downstream responses.
		if !errorCode.MatchString(code) || strings.Contains(code, c.config.APIKey) || (c.config.SecretKey != "" && strings.Contains(code, c.config.SecretKey)) {
			code = "provider_rejected"
		}
		return nil, &Error{Code: code, Message: "Asset provider rejected the operation: " + code, StatusCode: status, Uncertain: create && (strings.HasPrefix(code, "InternalError") || code == "ServiceUnavailable" || code == "RequestTimeout")}
	}
	if common.GetJsonType(envelope.Result) != "object" {
		return nil, &Error{Code: "invalid_response", Message: "Asset provider returned no result", StatusCode: status, Uncertain: create}
	}
	return envelope.Result, nil
}

func (c *client) asset(ctx context.Context, action string, body map[string]string) (Result, error) {
	data, err := c.action(ctx, action, body)
	if err != nil {
		return Result{}, err
	}
	create := action == "CreateAsset"
	var result struct {
		ID     string         `json:"Id"`
		Status string         `json:"Status"`
		Error  *upstreamError `json:"Error"`
	}
	if err := common.Unmarshal(data, &result); err != nil {
		return Result{}, &Error{Code: "invalid_response", Message: "Invalid asset provider response", Uncertain: create}
	}
	normalized, known := normalizeStatus(result.Status)
	if !validID(result.ID) || !known || (!create && result.Status == "") {
		return Result{}, &Error{Code: "invalid_response", Message: "Asset provider returned an invalid identifier or status", Uncertain: create}
	}
	out := Result{ID: result.ID, Reference: "asset://" + result.ID, Status: normalized}
	if result.Error != nil && (result.Error.Code != "" || result.Error.Message != "") {
		out.Error = c.safeMessage(result.Error.Code + ": " + result.Error.Message)
	}
	if normalized == StatusFailed && out.Error == "" {
		out.Error = "Asset provider rejected the asset"
	}
	return out, nil
}

func (c *client) CreateGroup(ctx context.Context, name string) (Group, error) {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 64 {
		return Group{}, &Error{Code: "invalid_group_name", Message: "Asset group name must contain 1 to 64 characters"}
	}
	data, err := c.action(ctx, "CreateAssetGroup", map[string]string{"Name": name, "GroupType": "AIGC", "ProjectName": c.config.ProjectName})
	if err != nil {
		return Group{}, err
	}
	var created struct {
		ID string `json:"Id"`
	}
	if common.Unmarshal(data, &created) != nil || !validID(created.ID) {
		return Group{}, &Error{Code: "invalid_response", Message: "Asset provider returned an invalid group identifier", Uncertain: true}
	}
	// Create returns only Id; the remaining identity comes from the exact request.
	return Group{ID: created.ID, Name: name, ProjectName: c.config.ProjectName, Type: "AIGC"}, nil
}

func (c *client) GetGroup(ctx context.Context, id string) (Group, error) {
	if !validID(id) {
		return Group{}, &Error{Code: "invalid_group_id", Message: "Invalid upstream asset group identifier"}
	}
	data, err := c.action(ctx, "GetAssetGroup", map[string]string{"Id": id, "ProjectName": c.config.ProjectName})
	if err != nil {
		return Group{}, err
	}
	var group Group
	if common.Unmarshal(data, &group) != nil || group.ID != id || group.Name == "" || group.Type != "AIGC" || group.ProjectName != c.config.ProjectName {
		return Group{}, &Error{Code: "invalid_response", Message: "Asset provider returned a different group identity"}
	}
	return group, nil
}

// FindGroup reconciles a possibly completed CreateAssetGroup. Name filtering is
// fuzzy upstream, so every page is checked for one exact account/project match.
// An incomplete or ambiguous listing must never be treated as confirmed absence.
func (c *client) FindGroup(ctx context.Context, name string) (Group, bool, error) {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 64 {
		return Group{}, false, &Error{Code: "invalid_group_name", Message: "Asset group name must contain 1 to 64 characters"}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var found Group
	seen := make(map[string]bool)
	const pageSize = 100
	for page := 1; page <= 200; page++ {
		data, err := c.action(ctx, "ListAssetGroups", map[string]any{
			"Filter":      map[string]string{"Name": name, "GroupType": "AIGC"},
			"ProjectName": c.config.ProjectName, "PageNumber": page, "PageSize": pageSize,
			"SortBy": "CreateTime", "SortOrder": "Asc",
		})
		if err != nil {
			return Group{}, false, err
		}
		var result struct {
			Items      []Group `json:"Items"`
			TotalCount *int    `json:"TotalCount"`
			PageNumber int     `json:"PageNumber"`
			PageSize   int     `json:"PageSize"`
		}
		if common.Unmarshal(data, &result) != nil || result.Items == nil || result.TotalCount == nil || *result.TotalCount < 0 || result.PageNumber != page || result.PageSize != pageSize || len(result.Items) > pageSize {
			return Group{}, false, &Error{Code: "invalid_response", Message: "Asset provider returned an incomplete group listing"}
		}
		for _, group := range result.Items {
			if !validID(group.ID) || seen[group.ID] || group.Name == "" || group.Type == "" || group.ProjectName == "" {
				return Group{}, false, &Error{Code: "invalid_response", Message: "Asset provider returned an invalid group listing"}
			}
			seen[group.ID] = true
			if group.Name != name || group.ProjectName != c.config.ProjectName || group.Type != "AIGC" {
				continue
			}
			if found.ID != "" {
				return Group{}, false, &Error{Code: "ambiguous_group", Message: "Multiple upstream asset groups match this channel"}
			}
			found = group
		}
		if len(seen) == *result.TotalCount {
			return found, found.ID != "", nil
		}
		if len(result.Items) != pageSize || len(seen) > *result.TotalCount {
			return Group{}, false, &Error{Code: "invalid_response", Message: "Asset provider returned an incomplete group listing"}
		}
	}
	return Group{}, false, &Error{Code: "group_search_limit", Message: "Asset group lookup exceeded the supported page range"}
}

func (c *client) DeleteGroup(ctx context.Context, id string) error {
	if !validID(id) {
		return &Error{Code: "invalid_group_id", Message: "Invalid upstream asset group identifier"}
	}
	_, err := c.action(ctx, "DeleteAssetGroup", map[string]string{"Id": id, "ProjectName": c.config.ProjectName})
	if IsNotFound(err) {
		return nil
	}
	return err
}
