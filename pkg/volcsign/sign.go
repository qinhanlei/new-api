// Package volcsign signs Volcengine API requests shared by task plugins and asset providers.
package volcsign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Request contains the exact wire values included in a Volcengine V4 signature.
type Request struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	AccessKey string            `json:"accessKey"`
	SecretKey string            `json:"secretKey"`
	Region    string            `json:"region"`
	Service   string            `json:"service"`
	Timestamp int64             `json:"timestamp"`
}

// Sign returns authentication headers without modifying the request.
func Sign(request Request, now time.Time) (map[string]string, error) {
	parsedURL, err := url.Parse(request.URL)
	if err != nil || parsedURL.Host == "" {
		return nil, fmt.Errorf("invalid Volcengine signing URL")
	}
	region := request.Region
	if region == "" {
		region = "cn-north-1"
	}
	service := request.Service
	if service == "" {
		service = "cv"
	}
	timestamp := now.UTC()
	if request.Timestamp != 0 {
		timestamp = time.Unix(request.Timestamp, 0).UTC()
	}
	xDate := timestamp.Format("20060102T150405Z")
	shortDate := timestamp.Format("20060102")
	bodyHash := sha256.Sum256([]byte(request.Body))
	requestPath := parsedURL.EscapedPath()
	if requestPath == "" {
		requestPath = "/"
	}

	headers := make(map[string]string, len(request.Headers)+3)
	for name, value := range request.Headers {
		headers[strings.ToLower(name)] = strings.TrimSpace(value)
	}
	headers["host"] = parsedURL.Host
	headers["x-date"] = xDate
	headers["x-content-sha256"] = hex.EncodeToString(bodyHash[:])
	keys := make([]string, 0, len(headers))
	for name := range headers {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	var canonicalHeaders strings.Builder
	for _, name := range keys {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(headers[name])
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(keys, ";")
	canonicalRequest := strings.Join([]string{
		strings.ToUpper(request.Method), requestPath, parsedURL.Query().Encode(),
		canonicalHeaders.String(), signedHeaders, hex.EncodeToString(bodyHash[:]),
	}, "\n")
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	scope := fmt.Sprintf("%s/%s/%s/request", shortDate, region, service)
	stringToSign := fmt.Sprintf("HMAC-SHA256\n%s\n%s\n%s", xDate, scope, hex.EncodeToString(canonicalHash[:]))
	sign := func(key []byte, value string) []byte {
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte(value))
		return mac.Sum(nil)
	}
	signingKey := sign(sign(sign([]byte(request.SecretKey), shortDate), region), service)
	signingKey = sign(signingKey, "request")
	signature := hex.EncodeToString(sign(signingKey, stringToSign))
	return map[string]string{
		"Authorization":    fmt.Sprintf("HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", request.AccessKey, scope, signedHeaders, signature),
		"X-Date":           xDate,
		"X-Content-Sha256": hex.EncodeToString(bodyHash[:]),
	}, nil
}
