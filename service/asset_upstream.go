package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	volcbase "github.com/volcengine/volcengine-go-sdk/volcengine/base"
)

const AssetAPIVersion = "2024-01-01"

type AssetError struct {
	HTTPStatus int
	Code       string
	Message    string
	Retryable  bool
}

func (err *AssetError) Error() string { return err.Message }

func assetError(status int, code, message string) *AssetError {
	return &AssetError{HTTPStatus: status, Code: code, Message: message, Retryable: status == http.StatusTooManyRequests || status >= 500}
}

func IsAssetChannelEnabled(channel *model.Channel, config *system_setting.AssetChannelConfig) bool {
	return channel != nil && config != nil && config.Enabled && config.Endpoint != "" && channel.Status == common.ChannelStatusEnabled && !channel.ChannelInfo.IsMultiKey && len(channel.GetKeys()) == 1 && config.ChannelScope == model.AssetChannelScope(channel)
}

func GetAssetChannelConfig(ctx context.Context, channelID int) (dto.AssetChannelView, error) {
	config, err := model.LoadAssetChannelConfig(ctx, channelID)
	return dto.AssetChannelView{AssetChannelFields: config.AssetChannelFields, ChannelID: channelID, HasSecret: config.SecretKey != ""}, err
}

func UpdateAssetChannelConfig(ctx context.Context, channelID int, input dto.AssetChannelUpdate) (dto.AssetChannelView, error) {
	current, err := model.LoadAssetChannelConfig(ctx, channelID)
	if err != nil {
		return dto.AssetChannelView{}, err
	}
	if current.Revision != input.Revision {
		return dto.AssetChannelView{}, model.ErrAssetConflict
	}
	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		return dto.AssetChannelView{}, err
	}
	next := system_setting.AssetChannelConfig{AssetChannelFields: input.AssetChannelFields, SecretKey: current.SecretKey, ChannelScope: model.AssetChannelScope(channel)}
	if err = validateAssetSizeLimits(next.SizeLimitsMB); err != nil {
		return dto.AssetChannelView{}, err
	}
	next.Endpoint = strings.TrimSpace(next.Endpoint)
	next.ProjectName = strings.TrimSpace(next.ProjectName)
	if next.ProjectName == "" {
		next.ProjectName = "default"
	}
	if next.AuthType == "" {
		next.AuthType = "bearer"
	}
	if input.SecretKey != nil {
		next.SecretKey = *input.SecretKey
	}
	u, parseErr := url.Parse(next.Endpoint)
	if next.Endpoint != "" && (parseErr != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "") {
		return dto.AssetChannelView{}, assetError(http.StatusBadRequest, "InvalidParameter.Endpoint", "Material endpoint must be an HTTPS URL without a query or credentials")
	}
	if next.Enabled && (next.Endpoint == "" || channel.ChannelInfo.IsMultiKey || len(channel.GetKeys()) != 1) {
		return dto.AssetChannelView{}, assetError(http.StatusBadRequest, "InvalidParameter.Channel", "Material channels require an endpoint and one API key")
	}
	if len(next.ProjectName) > 64 || strings.ContainsAny(next.ProjectName, "\r\n\t") {
		return dto.AssetChannelView{}, assetError(http.StatusBadRequest, "InvalidParameter.ProjectName", "Invalid upstream project name")
	}
	if next.AuthType != "bearer" && next.AuthType != "aksk" {
		return dto.AssetChannelView{}, assetError(http.StatusBadRequest, "InvalidParameter.AuthType", "Unsupported material authentication type")
	}
	if next.AuthType == "aksk" && next.Enabled && (next.AccessKey == "" || next.SecretKey == "") {
		return dto.AssetChannelView{}, assetError(http.StatusBadRequest, "InvalidParameter.Credentials", "Material access key and secret are required")
	}
	if next.Enabled {
		var page struct {
			Items []any `json:"Items"`
		}
		if err = callAssetUpstream(ctx, channel, next, "ListAssetGroups", map[string]any{"Filter": map[string]any{"GroupType": "AIGC"}, "PageNumber": 1, "PageSize": 1}, &page); err != nil {
			return dto.AssetChannelView{}, err
		}
		if page.Items == nil {
			return dto.AssetChannelView{}, assetError(http.StatusBadGateway, "UpstreamInvalidResponse", "The upstream material list was invalid")
		}
	}
	// A rotated signing secret must still be able to read a known resource before
	// replacing the credential needed to poll or remove existing replicas.
	if current.SecretKey != next.SecretKey && current.SecretKey != "" {
		replicas, loadErr := model.AssetChannelGroupReplicas(ctx, channelID)
		if loadErr != nil {
			return dto.AssetChannelView{}, loadErr
		}
		for _, replica := range replicas {
			if replica.UpstreamID == "" || replica.State == model.AssetDeleted {
				continue
			}
			var result struct {
				ID string `json:"Id"`
			}
			if err = callAssetUpstream(ctx, channel, next, "GetAssetGroup", map[string]any{"Id": replica.UpstreamID}, &result); err != nil {
				return dto.AssetChannelView{}, assetError(http.StatusBadRequest, "InvalidParameter.Credentials", "The new credential cannot access the existing material group")
			}
			if result.ID != replica.UpstreamID {
				return dto.AssetChannelView{}, assetError(http.StatusBadRequest, "InvalidParameter.Credentials", "The new credential returned a different material group")
			}
			break
		}
	}
	if err = model.SaveAssetChannelConfig(ctx, channelID, next); err != nil {
		return dto.AssetChannelView{}, err
	}
	next.Revision++
	return dto.AssetChannelView{AssetChannelFields: next.AssetChannelFields, ChannelID: channelID, HasSecret: next.SecretKey != ""}, nil
}

var assetErrorURLPattern = regexp.MustCompile(`https?://[^\s"<>]+`)

func safeAssetMessage(message string, secrets ...string) string {
	message = assetErrorURLPattern.ReplaceAllString(message, "[URL]")
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = strings.Map(func(r rune) rune {
		if r < ' ' && r != '\t' {
			return ' '
		}
		return r
	}, message)
	if len(message) > 1024 {
		message = message[:1024]
	}
	return message
}

// All actions use the configured management endpoint. Generation credentials
// stay on that administrator-selected host; only a trailing slash is corrected.
func callAssetUpstream(ctx context.Context, channel *model.Channel, config system_setting.AssetChannelConfig, action string, payload map[string]any, result any) error {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Host == "" {
		return assetError(http.StatusBadGateway, "UpstreamConfigurationInvalid", "The material endpoint is not configured")
	}
	query := endpoint.Query()
	query.Set("Action", action)
	query.Set("Version", AssetAPIVersion)
	endpoint.RawQuery = query.Encode()
	payload["ProjectName"] = config.ProjectName
	data, err := common.Marshal(payload)
	if err != nil {
		return err
	}
	baseClient, err := GetHttpClientWithProxySettings(channel.GetSetting().Proxy, channel.GetSetting())
	if err != nil {
		return assetError(http.StatusBadGateway, "UpstreamConfigurationInvalid", "The channel transport is unavailable")
	}
	client := *baseClient
	client.Timeout = 30 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var response *http.Response
	for attempt := range 2 {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
		if err != nil {
			return assetError(http.StatusBadGateway, "UpstreamConfigurationInvalid", "The material endpoint is invalid")
		}
		request.Header.Set("Content-Type", "application/json")
		if config.AuthType == "aksk" {
			credential := volcbase.Credentials{AccessKeyID: config.AccessKey, SecretAccessKey: config.SecretKey, Region: "cn-beijing", Service: "ark"}
			credential.Sign(request)
		} else {
			request.Header.Set("Authorization", "Bearer "+channel.Key)
		}
		response, err = client.Do(request)
		if err != nil {
			code := "UpstreamUnavailable"
			if strings.HasPrefix(action, "Create") {
				code = "UpstreamResultUnknown"
			}
			return assetError(http.StatusBadGateway, code, "The upstream material response could not be confirmed")
		}
		redirect := response.StatusCode == http.StatusMovedPermanently || response.StatusCode == http.StatusFound || response.StatusCode == http.StatusTemporaryRedirect || response.StatusCode == http.StatusPermanentRedirect
		if attempt != 0 || !redirect || endpoint.Scheme != "https" || strings.HasSuffix(endpoint.Path, "/") {
			break
		}
		location, locationErr := response.Location()
		if locationErr != nil || location.User != nil || location.Fragment != "" || location.Host != endpoint.Host ||
			(location.Scheme != "https" && location.Scheme != "http") || location.RawQuery != endpoint.RawQuery || location.EscapedPath() != endpoint.EscapedPath()+"/" {
			break
		}
		// Some proxies advertise HTTP behind TLS termination. Correct only our
		// original HTTPS path, preserving POST and re-signing it on the next attempt.
		response.Body.Close()
		endpoint.Path += "/"
		if endpoint.RawPath != "" {
			endpoint.RawPath += "/"
		}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return assetError(http.StatusBadGateway, "UpstreamResultUnknown", "The upstream material response could not be read")
	}
	var envelope struct {
		ResponseMetadata struct {
			Error *dto.AssetFailure `json:"Error"`
		} `json:"ResponseMetadata"`
		Result any `json:"Result"`
		Error  any `json:"error"`
	}
	if err = common.Unmarshal(body, &envelope); err != nil {
		status := http.StatusBadGateway
		code := "UpstreamInvalidResponse"
		message := "The upstream material response was not valid JSON"
		if response.StatusCode >= 400 {
			status = response.StatusCode
			code = "UpstreamHTTP" + strconv.Itoa(status)
		}
		switch response.StatusCode {
		case http.StatusNotFound:
			message = "The upstream material API returned HTTP 404 (not found). Check the complete material API address and path."
		case http.StatusMethodNotAllowed:
			message = "The upstream material API returned HTTP 405 (POST not allowed). Check the complete material API address and path."
		}
		if strings.HasPrefix(action, "Create") && (status >= 500 || status == http.StatusRequestTimeout) {
			code = "UpstreamResultUnknown"
		}
		return assetError(status, code, message)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.ResponseMetadata.Error != nil || envelope.Error != nil {
		status := response.StatusCode
		if status < 400 {
			status = http.StatusBadGateway
		}
		code, message := "UpstreamError", "The upstream material operation failed"
		if envelope.ResponseMetadata.Error != nil {
			code, message = envelope.ResponseMetadata.Error.Code, envelope.ResponseMetadata.Error.Message
		} else {
			switch upstreamError := envelope.Error.(type) {
			case string:
				message = upstreamError
			case map[string]any:
				if value, ok := upstreamError["code"].(string); ok && value != "" {
					code = value
				}
				if value, ok := upstreamError["message"].(string); ok && value != "" {
					message = value
				}
			}
		}
		code = safeAssetMessage(code, channel.Key, config.SecretKey)
		if code == "" || len(code) > 128 {
			code = "UpstreamError"
		}
		message = safeAssetMessage(message, channel.Key, config.SecretKey)
		if strings.HasPrefix(action, "Create") && (status >= 500 || status == http.StatusRequestTimeout) {
			code = "UpstreamResultUnknown"
		}
		return assetError(status, code, message)
	}
	if envelope.Result == nil {
		return assetError(http.StatusBadGateway, "UpstreamResultUnknown", "The upstream material response did not contain a result")
	}
	if result == nil {
		return nil
	}
	data, err = common.Marshal(envelope.Result)
	if err != nil {
		return err
	}
	if err = common.Unmarshal(data, result); err != nil {
		code := "UpstreamInvalidResponse"
		if strings.HasPrefix(action, "Create") {
			code = "UpstreamResultUnknown"
		}
		return assetError(http.StatusBadGateway, code, "The upstream material result was invalid")
	}
	return nil
}

func assetFailure(err error) (string, string, bool) {
	var failure *AssetError
	if errors.As(err, &failure) {
		return failure.Code, failure.Message, failure.Retryable
	}
	return "InternalError", "The material operation failed", true
}
