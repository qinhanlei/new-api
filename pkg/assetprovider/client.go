// Package assetprovider imports and manages provider copies of gateway assets.
package assetprovider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/volcsign"
)

const (
	StatusPending    = "pending"
	StatusReady      = "ready"
	StatusFailed     = "failed"
	maxResponseBytes = 1 << 20
	requestTimeout   = 30 * time.Second
)

// Config identifies an upstream account and its asset namespace. BaseURL is the
// complete Ark OpenAPI endpoint, including any compatibility gateway path.
// Credentials are supplied by the host; adapters never load environment values.
type Config struct {
	BaseURL, ProjectName, GroupID, AuthMode, APIKey, SecretKey string
}

type Source struct {
	Name, Kind, MIMEType, URL string
}

// ID is the management identifier; Reference is the value used for generation.
type Result struct {
	ID, Reference, Status, Error string
}

type Group struct {
	ID          string `json:"Id"`
	Name        string `json:"Name"`
	ProjectName string `json:"ProjectName"`
	Type        string `json:"GroupType"`
}

type Client interface {
	Create(context.Context, Source) (Result, error)
	Get(context.Context, string) (Result, error)
	Delete(context.Context, string) error
	CreateGroup(context.Context, string) (Group, error)
	GetGroup(context.Context, string) (Group, error)
	FindGroup(context.Context, string) (Group, bool, error)
	DeleteGroup(context.Context, string) error
}

// Error distinguishes a rejected operation from a creation whose outcome is
// unknown. An uncertain creation must not be retried automatically.
type Error struct {
	Code, Message string
	StatusCode    int
	Uncertain     bool
}

func (e *Error) Error() string {
	return e.Message
}

type client struct {
	config  Config
	baseURL *url.URL
	http    *http.Client
}

func New(config Config, httpClient *http.Client) (Client, error) {
	baseURL, err := url.Parse(config.BaseURL)
	if err != nil || baseURL.Hostname() == "" || (baseURL.Scheme != "https" && baseURL.Scheme != "http") || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.ForceQuery || baseURL.Fragment != "" || strings.ContainsAny(config.BaseURL, "\\\r\n\t ") {
		return nil, fmt.Errorf("asset provider base URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if config.AuthMode != "bearer" && config.AuthMode != "aksk" {
		return nil, fmt.Errorf("asset provider authentication must be bearer or aksk")
	}
	if config.APIKey == "" || strings.ContainsAny(config.APIKey, "\r\n") || strings.ContainsAny(config.SecretKey, "\r\n") {
		return nil, fmt.Errorf("invalid asset provider credentials")
	}
	if config.AuthMode == "aksk" && config.SecretKey == "" {
		return nil, fmt.Errorf("AK/SK authentication requires a secret key")
	}
	if config.ProjectName == "" {
		config.ProjectName = "default"
	}
	if baseURL.Path == "" {
		baseURL.Path = "/"
	}
	boundedClient := http.Client{}
	if httpClient != nil {
		boundedClient = *httpClient
	}
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if boundedClient.Timeout <= 0 || boundedClient.Timeout > requestTimeout {
		boundedClient.Timeout = requestTimeout
	}
	return &client{config: config, baseURL: baseURL, http: &boundedClient}, nil
}

func (c *client) Create(ctx context.Context, source Source) (Result, error) {
	kind := ""
	switch strings.ToLower(source.Kind) {
	case "image":
		kind = "Image"
	case "video":
		kind = "Video"
	case "audio":
		kind = "Audio"
	default:
		return Result{}, &Error{Code: "unsupported_asset_type", Message: "Unsupported asset type"}
	}
	if !validID(c.config.GroupID) {
		return Result{}, &Error{Code: "invalid_group_id", Message: "Asset provider group is required"}
	}
	sourceURL, err := url.Parse(source.URL)
	if err != nil || sourceURL.Scheme != "https" || sourceURL.Hostname() == "" || sourceURL.User != nil || sourceURL.Fragment != "" {
		return Result{}, &Error{Code: "invalid_source_url", Message: "Asset source must be an absolute HTTPS URL without credentials or fragment"}
	}
	body := map[string]string{"GroupId": c.config.GroupID, "URL": source.URL, "AssetType": kind, "ProjectName": c.config.ProjectName}
	if source.Name != "" {
		body["Name"] = source.Name
	}
	return c.asset(ctx, "CreateAsset", body)
}

func (c *client) Get(ctx context.Context, id string) (Result, error) {
	if !validID(id) {
		return Result{}, &Error{Code: "invalid_asset_id", Message: "Invalid upstream asset identifier"}
	}
	result, err := c.asset(ctx, "GetAsset", map[string]string{"Id": id, "ProjectName": c.config.ProjectName})
	if err == nil && result.ID != id {
		return Result{}, &Error{Code: "invalid_response", Message: "Asset provider returned a different asset identifier"}
	}
	return result, err
}

func (c *client) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return &Error{Code: "invalid_asset_id", Message: "Invalid upstream asset identifier"}
	}
	_, err := c.action(ctx, "DeleteAsset", map[string]string{"Id": id, "ProjectName": c.config.ProjectName})
	if IsNotFound(err) {
		return nil
	}
	return err
}

// IsNotFound recognizes an absent managed resource, not an unstructured message.
func IsNotFound(err error) bool {
	var providerErr *Error
	return errors.As(err, &providerErr) && (providerErr.StatusCode == http.StatusNotFound || providerErr.StatusCode == http.StatusGone || providerErr.Code == "NotFound" || providerErr.Code == "NotFound.asset_id" || providerErr.Code == "NotFound.group_id")
}

// do performs exactly one request. It never returns response bodies or transport
// error text in errors, because these may contain credentials or signed URLs.
func (c *client) do(ctx context.Context, method string, endpoint *url.URL, body any, create bool) ([]byte, int, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = common.Marshal(body)
		if err != nil {
			return nil, 0, &Error{Code: "invalid_request", Message: "Unable to encode asset provider request"}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, 0, &Error{Code: "invalid_request", Message: "Unable to create asset provider request"}
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	if c.config.AuthMode == "bearer" {
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	} else {
		headers, signErr := volcsign.Sign(volcsign.Request{Method: method, URL: endpoint.String(), Body: string(payload), Headers: map[string]string{"content-type": req.Header.Get("Content-Type")}, AccessKey: c.config.APIKey, SecretKey: c.config.SecretKey, Region: "cn-beijing", Service: "ark"}, time.Now())
		if signErr != nil {
			return nil, 0, &Error{Code: "signing_failed", Message: "Unable to sign asset provider request"}
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, 0, &Error{Code: "transport_error", Message: "Asset provider request did not complete", Uncertain: create}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return nil, response.StatusCode, &Error{Code: "invalid_response", Message: "Unable to read asset provider response", StatusCode: response.StatusCode, Uncertain: create && (response.StatusCode < 300 || response.StatusCode >= 500 || response.StatusCode == http.StatusRequestTimeout)}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.StatusCode, &Error{Code: fmt.Sprintf("http_%d", response.StatusCode), Message: fmt.Sprintf("Asset provider returned HTTP %d", response.StatusCode), StatusCode: response.StatusCode, Uncertain: create && (response.StatusCode >= 500 || response.StatusCode == http.StatusRequestTimeout)}
	}
	return data, response.StatusCode, nil
}

var sensitiveURL = regexp.MustCompile(`(?i)(?:https?|asset)://[^\s"<>]+`)

func (c *client) safeMessage(message string) string {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "authorization") || strings.Contains(lower, "signature=") || strings.Contains(lower, "credential=") {
		return "Asset provider rejected the operation"
	}
	message = strings.ReplaceAll(message, c.config.APIKey, "[redacted]")
	if c.config.SecretKey != "" {
		message = strings.ReplaceAll(message, c.config.SecretKey, "[redacted]")
	}
	message = sensitiveURL.ReplaceAllString(message, "[redacted URL]")
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	runes := []rune(strings.TrimSpace(message))
	if len(runes) > 512 {
		runes = runes[:512]
	}
	if len(runes) == 0 {
		return "Asset provider rejected the operation"
	}
	return string(runes)
}

func validID(id string) bool {
	if id == "" || len(id) > 512 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune("/\\?#%", r) {
			return false
		}
	}
	return true
}

func normalizeStatus(status string) (string, bool) {
	switch strings.ToLower(status) {
	case "", "pending", "processing":
		return StatusPending, true
	case "active":
		return StatusReady, true
	case "failed":
		return StatusFailed, true
	default:
		return "", false
	}
}
