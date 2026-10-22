package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"gorm.io/gorm"
)

const localAssetSigningKey = ".signing-key"
const localAssetURLTTL = 24 * time.Hour

var localAssetKeyPattern = regexp.MustCompile(`^assets/na_asset_[0-9a-f]{32}$`)
var ErrAssetAccessInvalid = errors.New("Asset download access is invalid")

type localAssetStore struct {
	directory string
	secret    []byte
}

func newLocalAssetStore(initialize bool) (*localAssetStore, error) {
	if !common.IsMasterNode {
		return nil, ErrAssetStorageUnavailable
	}
	directory := os.Getenv("ASSET_LOCAL_DIR")
	if directory == "" {
		directory = "./assets"
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, ErrAssetStorageUnavailable
	}
	if initialize && os.MkdirAll(directory, 0700) != nil {
		return nil, ErrAssetStorageUnavailable
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrAssetStorageUnavailable
	}
	defer root.Close()
	secret, err := loadLocalAssetSigningKey(root, initialize)
	if err != nil {
		return nil, ErrAssetStorageUnavailable
	}
	if initialize && (root.MkdirAll("assets", 0700) != nil || syncLocalAssetDirectory(root, ".") != nil) {
		return nil, ErrAssetStorageUnavailable
	}
	return &localAssetStore{directory: directory, secret: secret}, nil
}

// The key is published only after all bytes are durable. Link cannot replace a
// concurrent initializer's key. Missing keys in an existing store require repair,
// not silent regeneration that would invalidate in-flight provider downloads.
func loadLocalAssetSigningKey(root *os.Root, initialize bool) ([]byte, error) {
	info, err := root.Lstat(localAssetSigningKey)
	if initialize && errors.Is(err, os.ErrNotExist) {
		directory, openErr := root.Open(".")
		if openErr != nil {
			return nil, openErr
		}
		entries, readErr := directory.ReadDir(-1)
		closeErr := directory.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Name() != localAssetSigningKey && !strings.HasPrefix(entry.Name(), ".signing-key-") {
				// Another initializer may have published the key and data directory
				// since our first lookup. Recheck before rejecting a missing key.
				if _, err := root.Lstat(localAssetSigningKey); err != nil {
					return nil, ErrAssetStorageUnavailable
				}
			}
		}
		secret := make([]byte, 32)
		_, _ = rand.Read(secret)
		temporary := NewAssetID(".signing-key-")
		file, openErr := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			return nil, openErr
		}
		defer root.Remove(temporary)
		_, writeErr := file.Write(secret)
		if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
			return nil, err
		}
		if err := root.Link(temporary, localAssetSigningKey); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := syncLocalAssetDirectory(root, "."); err != nil {
			return nil, err
		}
		info, err = root.Lstat(localAssetSigningKey)
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() != 32 {
		return nil, ErrAssetStorageUnavailable
	}
	file, err := root.Open(localAssetSigningKey)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	secret, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil || len(secret) != 32 {
		return nil, ErrAssetStorageUnavailable
	}
	return secret, nil
}

func syncLocalAssetDirectory(root *os.Root, name string) error {
	// Windows does not support Sync on directory handles. File data is flushed
	// separately on all platforms; Unix also flushes the directory entry.
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func (*localAssetStore) Backend() string { return model.AssetStorageLocal }

func (s *localAssetStore) Put(ctx context.Context, key string, src io.Reader, size int64, _ string) error {
	if !localAssetKeyPattern.MatchString(key) || size <= 0 || size > AssetMaxUploadBytes || ctx.Err() != nil {
		return ErrAssetStorageUnavailable
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return ErrAssetStorageUnavailable
	}
	defer root.Close()
	file, err := root.OpenFile(key, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrAssetStorageUnavailable
	}
	// Uploading rows are not downloadable. Failed/partial objects stay attached
	// to their row for the existing cleanup worker; an immutable key is never reused.
	written, copyErr := io.Copy(file, io.LimitReader(src, size+1))
	err = errors.Join(copyErr, file.Sync(), file.Close(), ctx.Err())
	if err != nil || written != size || syncLocalAssetDirectory(root, "assets") != nil {
		return ErrAssetStorageUnavailable
	}
	return nil
}

func (s *localAssetStore) Presign(ctx context.Context, key string) (string, error) {
	if !localAssetKeyPattern.MatchString(key) || ctx.Err() != nil {
		return "", ErrAssetStorageUnavailable
	}
	address := system_setting.TaskPublicAddress
	if address == "" {
		address = system_setting.ServerAddress
	}
	base, err := url.Parse(address)
	if ValidateTaskArtifactBaseURL(address) != nil || err != nil || base.Scheme != "https" || strings.EqualFold(base.Hostname(), "localhost") {
		return "", ErrAssetStorageUnavailable
	}
	if ip := net.ParseIP(base.Hostname()); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
		return "", ErrAssetStorageUnavailable
	}
	id := strings.TrimPrefix(key, "assets/")
	expires := strconv.FormatInt(time.Now().Add(localAssetURLTTL).Unix(), 10)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte("asset-source:v1\x00" + id + "\x00" + expires))
	escapedBasePath := strings.TrimRight(base.EscapedPath(), "/")
	suffix := "/v1/assets/" + id + "/content"
	base.Path = strings.TrimRight(base.Path, "/") + suffix
	base.RawPath = escapedBasePath + suffix
	base.RawQuery = url.Values{"expires": {expires}, "access": {base64.RawURLEncoding.EncodeToString(mac.Sum(nil))}}.Encode()
	return base.String(), nil
}

func (s *localAssetStore) Delete(ctx context.Context, key string) error {
	if !localAssetKeyPattern.MatchString(key) || ctx.Err() != nil {
		return ErrAssetStorageUnavailable
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return ErrAssetStorageUnavailable
	}
	defer root.Close()
	err = root.Remove(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || syncLocalAssetDirectory(root, "assets") != nil {
		return ErrAssetStorageUnavailable
	}
	return nil
}

// OpenLocalAssetContent authenticates the capability before consulting database
// state. Deleted sources remain readable only while an existing task holds them.
func OpenLocalAssetContent(ctx context.Context, id, expires, access string) (*model.Asset, *os.File, error) {
	if !common.AssetLibraryEnabled || !localAssetKeyPattern.MatchString("assets/"+id) || len(expires) > 20 || len(access) != 43 {
		return nil, nil, ErrAssetAccessInvalid
	}
	deadline, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || strconv.FormatInt(deadline, 10) != expires || time.Now().Unix() >= deadline {
		return nil, nil, ErrAssetAccessInvalid
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(access)
	if err != nil {
		return nil, nil, ErrAssetAccessInvalid
	}
	// Anonymous reads never create directories, generate keys or flush storage.
	store, err := newLocalAssetStore(false)
	if err != nil {
		return nil, nil, err
	}
	mac := hmac.New(sha256.New, store.secret)
	_, _ = mac.Write([]byte("asset-source:v1\x00" + id + "\x00" + expires))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, nil, ErrAssetAccessInvalid
	}
	asset, err := model.GetAsset(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if asset.StorageBackend != model.AssetStorageLocal || asset.ObjectKey != "assets/"+id {
		return nil, nil, gorm.ErrRecordNotFound
	}
	if asset.Status == model.AssetStatusDeleted {
		used, err := model.AssetHasConsumers(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if !used {
			return nil, nil, gorm.ErrRecordNotFound
		}
	} else if asset.Status != model.AssetStatusReady || asset.DeletedAt != 0 {
		return nil, nil, gorm.ErrRecordNotFound
	}
	root, err := os.OpenRoot(store.directory)
	if err != nil {
		return nil, nil, ErrAssetStorageUnavailable
	}
	defer root.Close()
	file, err := root.Open(asset.ObjectKey)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, gorm.ErrRecordNotFound
	}
	if err != nil {
		return nil, nil, ErrAssetStorageUnavailable
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != asset.Size {
		file.Close()
		return nil, nil, ErrAssetStorageUnavailable
	}
	return asset, file, nil
}
