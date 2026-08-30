package gcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/tedla-brandsema/tissues/services/tissues"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

type Store struct {
	bucket      *storage.BucketHandle
	beforeWrite func()
}

type TenantStore struct {
	root     *Store
	tenantID tissues.TenantID
}

func New(client *storage.Client, bucket string) (*Store, error) {
	if client == nil {
		return nil, fmt.Errorf("GCS client is required")
	}
	if strings.TrimSpace(bucket) == "" || bucket != strings.TrimSpace(bucket) {
		return nil, fmt.Errorf("GCS bucket is required")
	}
	return &Store{bucket: client.Bucket(bucket)}, nil
}

func (s *Store) ForTenant(id tissues.TenantID) (tissues.TenantAssetStore, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return &TenantStore{root: s, tenantID: id}, nil
}

func ObjectName(tenantID tissues.TenantID, key tissues.AssetKey) (string, error) {
	if err := tenantID.Validate(); err != nil {
		return "", err
	}
	if err := key.Validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/issues/%s/%d/%s", tenantID, key.ProjectKey, key.IssueNumber, key.Name), nil
}

func issuePrefix(tenantID tissues.TenantID, ref tissues.IssueRef) (string, error) {
	if err := tenantID.Validate(); err != nil {
		return "", err
	}
	if err := ref.Validate(); err != nil {
		return "", fmt.Errorf("%w: invalid asset Issue", tissues.ErrInvalid)
	}
	return fmt.Sprintf("tenants/%s/issues/%s/%d/", tenantID, ref.ProjectKey, ref.Number), nil
}

func (s *TenantStore) Put(ctx context.Context, key tissues.AssetKey, write tissues.AssetWrite) (*tissues.Asset, error) {
	name, err := ObjectName(s.tenantID, key)
	if err != nil {
		return nil, err
	}
	if err := validateWrite(write); err != nil {
		return nil, err
	}
	object := s.root.bucket.Object(name)
	condition := storage.Conditions{}
	attrs, err := object.Attrs(ctx)
	switch {
	case errors.Is(err, storage.ErrObjectNotExist):
		condition.DoesNotExist = true
	case err != nil:
		return nil, translateError(err)
	default:
		condition.GenerationMatch = attrs.Generation
	}
	if s.root.beforeWrite != nil {
		s.root.beforeWrite()
	}

	w := object.If(condition).NewWriter(ctx)
	w.ChunkSize = 0
	w.ContentType = write.ContentType
	w.CacheControl = "private, no-cache"
	w.Metadata = map[string]string{"width": strconv.Itoa(write.Width), "height": strconv.Itoa(write.Height)}
	if _, err := io.Copy(w, bytes.NewReader(write.Data)); err != nil {
		_ = w.CloseWithError(err)
		return nil, translateError(err)
	}
	if err := w.Close(); err != nil {
		return nil, translateError(err)
	}
	created := w.Attrs()
	return assetFromAttrs(key, created), nil
}

func validateWrite(write tissues.AssetWrite) error {
	if write.ContentType != "image/jpeg" && write.ContentType != "image/png" {
		return fmt.Errorf("%w: invalid asset content type", tissues.ErrInvalid)
	}
	if write.Width <= 0 || write.Height <= 0 || len(write.Data) == 0 || len(write.Data) > tissues.MaxStoredBytes {
		return fmt.Errorf("%w: invalid processed asset", tissues.ErrInvalid)
	}
	return nil
}

func (s *TenantStore) Open(ctx context.Context, key tissues.AssetKey) (*tissues.AssetContent, error) {
	name, err := ObjectName(s.tenantID, key)
	if err != nil {
		return nil, err
	}
	object := s.root.bucket.Object(name)
	attrs, err := object.Attrs(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	asset, err := assetFromObjectAttrs(key, attrs)
	if err != nil {
		return nil, err
	}
	r, err := object.Generation(attrs.Generation).NewReader(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return &tissues.AssetContent{Asset: *asset, Body: r}, nil
}

func (s *TenantStore) List(ctx context.Context, ref tissues.IssueRef) ([]*tissues.Asset, error) {
	prefix, err := issuePrefix(s.tenantID, ref)
	if err != nil {
		return nil, err
	}
	var assets []*tissues.Asset
	it := s.root.bucket.Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, translateError(err)
		}
		filename := strings.TrimPrefix(attrs.Name, prefix)
		if filename == "" || strings.Contains(filename, "/") {
			continue
		}
		key := tissues.AssetKey{ProjectKey: ref.ProjectKey, IssueNumber: ref.Number, Name: filename}
		asset, err := assetFromObjectAttrs(key, attrs)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	tissues.SortAssets(assets)
	return assets, nil
}

func assetFromAttrs(key tissues.AssetKey, attrs *storage.ObjectAttrs) *tissues.Asset {
	width, _ := strconv.Atoi(attrs.Metadata["width"])
	height, _ := strconv.Atoi(attrs.Metadata["height"])
	return &tissues.Asset{Key: key, ContentType: attrs.ContentType, Width: width, Height: height, Size: attrs.Size, Generation: attrs.Generation}
}

func assetFromObjectAttrs(key tissues.AssetKey, attrs *storage.ObjectAttrs) (*tissues.Asset, error) {
	asset := assetFromAttrs(key, attrs)
	if err := validateStoredAsset(asset); err != nil {
		return nil, err
	}
	return asset, nil
}

func validateStoredAsset(asset *tissues.Asset) error {
	if asset.ContentType != "image/jpeg" && asset.ContentType != "image/png" || asset.Width <= 0 || asset.Height <= 0 || asset.Size <= 0 || asset.Size > tissues.MaxStoredBytes || asset.Generation <= 0 {
		return fmt.Errorf("stored asset metadata is invalid")
	}
	return nil
}

func translateError(err error) error {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return tissues.ErrNotFound
	}
	var apiError *googleapi.Error
	if errors.As(err, &apiError) && apiError.Code == 412 {
		return tissues.ErrConflict
	}
	return err
}
