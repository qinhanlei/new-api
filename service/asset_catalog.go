package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/abema/go-mp4"
	"golang.org/x/image/webp"
)

const AssetMaxUploadBytes int64 = 100 << 20
const AssetMaxUserBytes int64 = 10 << 30
const AssetMaxUserCount int64 = 1000

var (
	ErrAssetStorageUnavailable = errors.New("Asset storage is unavailable")
	ErrAssetInvalid            = errors.New("Invalid asset request")
	ErrAssetTooLarge           = errors.New("Asset exceeds the upload size limit")
	ErrAssetMediaType          = errors.New("Unsupported asset media type")
	ErrAssetSource             = errors.New("Unable to fetch the asset source")
)

func NewAssetID(prefix string) string {
	var bytes [16]byte
	_, _ = rand.Read(bytes[:]) // crypto/rand.Read never fails on supported Go platforms.
	return prefix + hex.EncodeToString(bytes[:])
}

func ValidateAssetName(name string) bool {
	return strings.TrimSpace(name) != "" && utf8.ValidString(name) && len(name) <= 255 && !strings.ContainsAny(name, "\x00\r\n")
}

func CreateAsset(ctx context.Context, userID int, name string, src io.Reader, store AssetStore) (*model.Asset, error) {
	if !common.AssetLibraryEnabled {
		return nil, ErrAssetStorageUnavailable
	}
	name = strings.TrimSpace(name)
	if userID <= 0 || !ValidateAssetName(name) {
		return nil, ErrAssetInvalid
	}
	file, err := os.CreateTemp("", "new-api-asset-*")
	if err != nil {
		return nil, ErrAssetStorageUnavailable
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(src, AssetMaxUploadBytes+1))
	if err != nil {
		return nil, ErrAssetSource
	}
	if size > AssetMaxUploadBytes {
		return nil, ErrAssetTooLarge
	}
	if size == 0 {
		return nil, ErrAssetMediaType
	}
	header := make([]byte, min(size, 512))
	if _, err := file.ReadAt(header, 0); err != nil {
		return nil, ErrAssetSource
	}
	mime := http.DetectContentType(header)
	if mime == "application/octet-stream" && len(header) >= 2 && header[0] == 0xff && header[1]&0xe0 == 0xe0 {
		mime = "audio/mpeg"
	}
	var kind string
	switch mime {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		kind = "image"
	case "video/mp4":
		kind = "video"
	case "audio/mpeg", "audio/wave", "audio/aiff":
		kind = "audio"
	default:
		return nil, ErrAssetMediaType
	}
	id := NewAssetID("na_asset_")
	now := time.Now().Unix()
	asset := &model.Asset{ID: id, UserID: userID, Name: name, Kind: kind, MIMEType: mime, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil)), ObjectKey: "assets/" + id, StorageBackend: store.Backend(), Status: model.AssetStatusUploading, CreatedAt: now, UpdatedAt: now}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, ErrAssetSource
	}
	switch kind {
	case "image":
		var config image.Config
		if mime == "image/webp" {
			config, err = webp.DecodeConfig(file)
		} else {
			config, _, err = image.DecodeConfig(file)
		}
		if err != nil {
			return nil, ErrAssetMediaType
		}
		asset.Width, asset.Height = config.Width, config.Height
	case "audio":
		ext := ".mp3"
		if mime == "audio/wave" {
			ext = ".wav"
		}
		if mime == "audio/aiff" {
			ext = ".aiff"
		}
		asset.DurationSeconds, err = common.GetAudioDuration(ctx, file, ext)
		if err != nil {
			return nil, ErrAssetMediaType
		}
	case "video":
		if err := readAssetMP4Metadata(ctx, file, asset); err != nil {
			return nil, ErrAssetMediaType
		}
	}
	// Header values are untrusted even after decoding. Bound them before they
	// can be used by downstream token or usage estimation.
	if kind != "audio" && (asset.Width <= 0 || asset.Height <= 0 || asset.Width > 32768 || asset.Height > 32768 || int64(asset.Width)*int64(asset.Height) > 100_000_000) {
		return nil, ErrAssetMediaType
	}
	if kind != "image" && (math.IsNaN(asset.DurationSeconds) || math.IsInf(asset.DurationSeconds, 0) || asset.DurationSeconds <= 0 || asset.DurationSeconds > relaycommon.MaxTaskDurationSeconds) {
		return nil, ErrAssetMediaType
	}
	if err := model.ReserveAsset(ctx, asset, AssetMaxUserCount, AssetMaxUserBytes); err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, ErrAssetStorageUnavailable
	}
	if err := store.Put(ctx, asset.ObjectKey, file, size, mime); err != nil {
		// Keep the row for cleanup even after an ambiguous storage timeout.
		_ = model.DB.WithContext(context.WithoutCancel(ctx)).Model(asset).Updates(map[string]any{"status": model.AssetStatusFailed, "deleted_at": now}).Error
		return nil, ErrAssetStorageUnavailable
	}
	asset.Status = model.AssetStatusReady
	if err := model.DB.WithContext(ctx).Model(asset).Updates(map[string]any{"status": asset.Status, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	return asset, nil
}

// readAssetMP4Metadata reads only fixed-size headers. Full probing also parses
// attacker-controlled sample tables, which are unnecessary for catalog metadata.
func readAssetMP4Metadata(ctx context.Context, file *os.File, asset *model.Asset) error {
	ranges := [][2]int64{{0, asset.Size}}
	boxes := 0
	for len(ranges) > 0 {
		current := ranges[len(ranges)-1]
		ranges = ranges[:len(ranges)-1]
		for offset := current[0]; offset < current[1]; {
			boxes++
			if boxes > 4096 || ctx.Err() != nil {
				return ErrAssetMediaType
			}
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				return err
			}
			box, err := mp4.ReadBoxInfo(file)
			if err != nil || box.Size < box.HeaderSize || box.Size > uint64(current[1]-offset) || box.Size == 0 {
				return ErrAssetMediaType
			}
			end := offset + int64(box.Size)
			switch box.Type {
			case mp4.BoxTypeMoov(), mp4.BoxTypeTrak():
				ranges = append(ranges, [2]int64{offset + int64(box.HeaderSize), end})
			case mp4.BoxTypeMvhd():
				if box.Size > 256 {
					return ErrAssetMediaType
				}
				var header mp4.Mvhd
				if _, err := mp4.Unmarshal(file, box.Size-box.HeaderSize, &header, box.Context); err != nil || header.Timescale == 0 {
					return ErrAssetMediaType
				}
				asset.DurationSeconds = float64(header.GetDuration()) / float64(header.Timescale)
			case mp4.BoxTypeTkhd():
				if box.Size > 256 {
					return ErrAssetMediaType
				}
				var header mp4.Tkhd
				if _, err := mp4.Unmarshal(file, box.Size-box.HeaderSize, &header, box.Context); err != nil {
					return ErrAssetMediaType
				}
				if header.GetWidthInt() > 0 && header.GetHeightInt() > 0 {
					asset.Width, asset.Height = int(header.GetWidthInt()), int(header.GetHeightInt())
				}
			}
			offset = end
		}
	}
	return nil
}

// Asset imports always validate resolved addresses at the actual connection.
// They do not inherit the administrator's optional relay SSRF bypass or proxy.
func assetSourceClient() *http.Client {
	protection := &common.SSRFProtection{AllowPrivateIp: false, ApplyIPFilterForDomain: true, AllowedPorts: []int{80, 443}}
	dialer := &protectedFetchDialer{resolver: net.DefaultResolver, dialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, getProtection: func() (*common.SSRFProtection, bool, error) { return protection, true, nil }}
	return &http.Client{Transport: &http.Transport{DialContext: dialer.DialContext, ResponseHeaderTimeout: 15 * time.Second, DisableCompression: true}, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func ImportAsset(ctx context.Context, userID int, name, source string, store AssetStore) (*model.Asset, error) {
	target, err := url.Parse(source)
	if err != nil || len(source) > 8192 || target.Host == "" || target.User != nil || target.Fragment != "" || (target.Scheme != "https" && target.Scheme != "http") {
		return nil, ErrAssetSource
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, ErrAssetSource
	}
	client := assetSourceClient()
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrAssetSource
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrAssetSource
	}
	if response.ContentLength > AssetMaxUploadBytes {
		return nil, ErrAssetTooLarge
	}
	return CreateAsset(ctx, userID, name, response.Body, store)
}
