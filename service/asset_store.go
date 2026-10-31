package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const assetURLLifetime = 12 * time.Hour

func assetS3(config system_setting.AssetStorageConfig) *s3.Client {
	return s3.NewFromConfig(aws.Config{
		Region:                     config.Region,
		Credentials:                credentials.NewStaticCredentialsProvider(config.AccessKey, config.SecretKey, ""),
		HTTPClient:                 &http.Client{Transport: GetHttpClient().Transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(config.Endpoint)
		options.UsePathStyle = config.ForcePathStyle
	})
}

func validateAssetStorage(config system_setting.AssetStorageConfig) error {
	if !config.Enabled && config.Endpoint == "" {
		return nil
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.ForceQuery || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return assetError(http.StatusBadRequest, "InvalidParameter.Endpoint", "Storage endpoint must be an HTTP or HTTPS origin")
	}
	if config.Region == "" || len(config.Region) > 64 || config.Bucket == "" || len(config.Bucket) > 63 || strings.ContainsAny(config.Bucket, "/\\:") || config.AccessKey == "" || config.SecretKey == "" {
		return assetError(http.StatusBadRequest, "InvalidParameter.Storage", "Region, bucket and storage credentials are required")
	}
	return nil
}

func storageView(config system_setting.AssetStorageConfig) dto.AssetStorageView {
	return dto.AssetStorageView{AssetStorageFields: config.AssetStorageFields, HasSecret: config.SecretKey != ""}
}

func GetAssetStorageConfig(ctx context.Context) (dto.AssetStorageView, error) {
	config, err := model.LoadAssetStorageConfig(ctx)
	return storageView(config), err
}

func mergeAssetStorage(ctx context.Context, input dto.AssetStorageUpdate) (system_setting.AssetStorageConfig, error) {
	current, err := model.LoadAssetStorageConfig(ctx)
	if err != nil {
		return current, err
	}
	if input.Revision != current.Revision {
		return current, model.ErrAssetConflict
	}
	next := system_setting.AssetStorageConfig{AssetStorageFields: input.AssetStorageFields, SecretKey: current.SecretKey}
	next.Endpoint = strings.TrimSpace(next.Endpoint)
	if next.Endpoint != "" && !strings.Contains(next.Endpoint, "://") && !strings.HasPrefix(strings.ToLower(next.Endpoint), "http:") && !strings.HasPrefix(strings.ToLower(next.Endpoint), "https:") {
		next.Endpoint = "https://" + next.Endpoint
	}
	next.Endpoint = strings.TrimRight(next.Endpoint, "/")
	next.Region = strings.TrimSpace(next.Region)
	next.Bucket = strings.TrimSpace(next.Bucket)
	next.AccessKey = strings.TrimSpace(next.AccessKey)
	if input.SecretKey != nil {
		next.SecretKey = *input.SecretKey
	}
	return next, validateAssetStorage(next)
}

func UpdateAssetStorageConfig(ctx context.Context, input dto.AssetStorageUpdate) (dto.AssetStorageView, error) {
	next, err := mergeAssetStorage(ctx, input)
	if err != nil {
		return dto.AssetStorageView{}, err
	}
	current, err := model.LoadAssetStorageConfig(ctx)
	if err != nil {
		return dto.AssetStorageView{}, err
	}
	if current.AccessKey != next.AccessKey || current.SecretKey != next.SecretKey {
		existing, loadErr := model.AssetStoredObject(ctx)
		if loadErr != nil {
			return dto.AssetStorageView{}, loadErr
		}
		if next.Enabled || existing != nil {
			if err = testAssetStorage(ctx, next); err != nil {
				return dto.AssetStorageView{}, err
			}
		}
		if existing != nil {
			_, err = assetS3(next).HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(next.Bucket), Key: aws.String(existing.ObjectKey)})
			if err != nil {
				return dto.AssetStorageView{}, assetError(http.StatusBadRequest, "StorageReadFailed", "The new storage credential cannot access existing materials")
			}
		}
	}
	if err = model.SaveAssetStorageConfig(ctx, next); err != nil {
		return dto.AssetStorageView{}, err
	}
	next.Revision++
	return storageView(next), nil
}

func TestAssetStorageConfig(ctx context.Context, input dto.AssetStorageUpdate) error {
	config, err := mergeAssetStorage(ctx, input)
	if err != nil {
		return err
	}
	if config.Endpoint == "" {
		return assetError(http.StatusBadRequest, "InvalidParameter.Storage", "Storage configuration is required")
	}
	return testAssetStorage(ctx, config)
}

func testAssetStorage(ctx context.Context, config system_setting.AssetStorageConfig) error {
	client := assetS3(config)
	key := "new-api-assets/check/" + common.GetUUID()
	payload := []byte("new-api asset storage check")
	_, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(key), Body: bytes.NewReader(payload), ContentType: aws.String("text/plain"), ContentLength: aws.Int64(int64(len(payload)))})
	if err != nil {
		return assetError(http.StatusBadGateway, "StorageWriteFailed", "Unable to write the storage test object")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = client.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(key)})
	}()
	signed, err := s3.NewPresignClient(client).PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(key)}, func(o *s3.PresignOptions) { o.Expires = 5 * time.Minute })
	if err != nil {
		return assetError(http.StatusBadGateway, "StorageReadFailed", "Unable to sign the storage test object")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, signed.URL, nil)
	if err != nil {
		return assetError(http.StatusBadGateway, "StorageReadFailed", "Unable to read the storage test object")
	}
	clientHTTP := *GetHttpClient()
	clientHTTP.Timeout = 30 * time.Second
	clientHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := clientHTTP.Do(request)
	if err != nil {
		return assetError(http.StatusBadGateway, "StorageReadFailed", "Unable to read the storage test object")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Equal(data, payload) {
		return assetError(http.StatusBadGateway, "StorageReadFailed", "Storage test content did not match")
	}
	if _, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(key)}); err != nil {
		return assetError(http.StatusBadGateway, "StorageDeleteFailed", "Unable to delete the storage test object")
	}
	return nil
}

func signedAssetURL(ctx context.Context, asset *model.Asset) (string, error) {
	if asset.ObjectState != model.AssetReady {
		return "", nil
	}
	config, err := model.LoadAssetStorageConfig(ctx)
	if err != nil {
		return "", err
	}
	result, err := s3.NewPresignClient(assetS3(config)).PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(asset.ObjectKey)}, func(o *s3.PresignOptions) { o.Expires = assetURLLifetime })
	if err != nil {
		return "", assetError(http.StatusBadGateway, "StorageUnavailable", "Unable to create a material access URL")
	}
	return result.URL, nil
}

func storeAssetContent(ctx context.Context, asset *model.Asset, reader io.Reader) error {
	limits, err := GetAssetSizeLimits(ctx)
	if err != nil {
		return err
	}
	// Multipart uploads are already seekable (large parts are spooled by net/http).
	// Only URL imports need another temporary file, so S3 retries can rewind
	// without keeping the whole material in memory. S3 remains the durable copy.
	body, seekable := reader.(io.ReadSeeker)
	sum := sha256.New()
	var destination io.Writer = sum
	var start int64
	if seekable {
		start, err = body.Seek(0, io.SeekCurrent)
		if err != nil {
			return assetError(http.StatusBadGateway, "DownloadFailed", "Unable to read the material source")
		}
	} else {
		file, err := os.CreateTemp("", "new-api-asset-*")
		if err != nil {
			return assetError(http.StatusBadGateway, "StorageWriteFailed", "Unable to save the original material")
		}
		defer os.Remove(file.Name())
		defer file.Close()
		body = file
		destination = io.MultiWriter(file, sum)
	}
	// Probe a small prefix before choosing the byte limit. Container signatures
	// alone cannot distinguish audio from video, so those are bounded by both
	// limits until their track metadata has been inspected.
	data := make([]byte, 512)
	n, err := io.ReadFull(reader, data)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return assetError(http.StatusBadGateway, "DownloadFailed", "Unable to read the material source")
	}
	data = data[:n]
	if n == 0 {
		return assetError(http.StatusBadRequest, "FileSizeTooLarge", "Material is empty or exceeds the size limit")
	}
	detected, container := sniffAssetMedia(data)
	assetType := assetMediaType(detected)
	limit := limits.ByteLimit(assetType)
	if container != "" {
		limit = max(limits.ByteLimit("Audio"), limits.ByteLimit("Video"))
	} else if assetType == "" {
		return assetError(http.StatusBadRequest, "UnsupportedMediaType", "Material format is not supported or its type cannot be determined")
	}
	if asset.AssetType != "" {
		limit = min(limit, limits.ByteLimit(asset.AssetType))
	}
	limited := io.LimitReader(reader, max(0, limit+1-int64(n)))
	size, err := io.Copy(destination, io.MultiReader(bytes.NewReader(data), limited))
	if err != nil {
		return assetError(http.StatusBadGateway, "DownloadFailed", "Unable to read the material source")
	}
	if size > limit {
		return assetError(http.StatusBadRequest, "FileSizeTooLarge", "Material is empty or exceeds the size limit")
	}
	if container != "" {
		detected, err = detectAssetContainer(body, start, size, container)
		if err != nil {
			return assetError(http.StatusBadRequest, "UnsupportedMediaType", "Material format is not supported or its type cannot be determined")
		}
		assetType = assetMediaType(detected)
	}
	if asset.AssetType != "" && asset.AssetType != assetType {
		return assetError(http.StatusBadRequest, "TypeMismatch", "Material content does not match AssetType")
	}
	if err = validateAssetSize(size, assetType, limits); err != nil {
		return err
	}
	config, err := model.LoadAssetStorageConfig(ctx)
	if err != nil {
		return err
	}
	if _, err = body.Seek(start, io.SeekStart); err != nil {
		return assetError(http.StatusBadGateway, "StorageWriteFailed", "Unable to save the original material")
	}
	_, err = assetS3(config).PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(asset.ObjectKey), Body: body, ContentLength: aws.Int64(size), ContentType: aws.String(detected)})
	if err != nil {
		return assetError(http.StatusBadGateway, "StorageWriteFailed", "Unable to save the original material")
	}
	asset.Size = size
	asset.AssetType = assetType
	asset.ContentType = detected
	asset.SHA256 = hex.EncodeToString(sum.Sum(nil))
	asset.ObjectState = model.AssetReady
	asset.SourceURL = ""
	return nil
}

func fetchAssetContent(ctx context.Context, asset *model.Asset) error {
	if err := ValidateSSRFProtectedFetchURL(asset.SourceURL); err != nil {
		return assetError(http.StatusBadRequest, "InvalidParameter.URL", "Material URL is not allowed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.SourceURL, nil)
	if err != nil {
		return assetError(http.StatusBadRequest, "InvalidParameter.URL", "Invalid material URL")
	}
	response, err := GetSSRFProtectedHTTPClient().Do(request)
	if err != nil {
		return assetError(http.StatusBadGateway, "DownloadFailed", "Unable to download the material source")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return assetError(http.StatusBadGateway, "DownloadFailed", "The material source did not return a successful response")
	}
	return storeAssetContent(ctx, asset, response.Body)
}

func deleteAssetObject(ctx context.Context, asset *model.Asset) error {
	config, err := model.LoadAssetStorageConfig(ctx)
	if err != nil {
		return err
	}
	_, err = assetS3(config).DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(asset.ObjectKey)})
	if err != nil {
		return errors.New("unable to delete the original material")
	}
	return nil
}
