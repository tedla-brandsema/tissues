package tissues

import (
	"context"
	"fmt"
	"io"
	"sort"
)

const (
	MaxUploadBytes = 6 * 1024 * 1024
	MaxStoredBytes = 1 * 1024 * 1024
)

type AssetKey struct {
	ProjectKey  string
	IssueNumber int64
	Name        string
}

func (k AssetKey) Validate() error {
	if project, err := CanonicalProjectKey(k.ProjectKey); err != nil || project != k.ProjectKey {
		return fmt.Errorf("%w: invalid asset project", ErrInvalid)
	}
	if k.IssueNumber <= 0 {
		return fmt.Errorf("%w: invalid asset issue number", ErrInvalid)
	}
	name, _, err := canonicalAssetName(k.Name)
	if err != nil || name != k.Name {
		return fmt.Errorf("%w: invalid canonical asset name", ErrInvalid)
	}
	return nil
}

type Asset struct {
	Key         AssetKey
	ContentType string
	Width       int
	Height      int
	Size        int64
	Generation  int64
}

type AssetWrite struct {
	ContentType string
	Width       int
	Height      int
	Data        []byte
}

type AssetContent struct {
	Asset Asset
	Body  io.ReadCloser
}

type AssetStore interface {
	ForTenant(TenantID) (TenantAssetStore, error)
}

// TenantAssetStore exposes asset operations only after immutable tenant
// binding. AssetKey remains tenant-relative by design.
type TenantAssetStore interface {
	Put(context.Context, AssetKey, AssetWrite) (*Asset, error)
	Open(context.Context, AssetKey) (*AssetContent, error)
	List(context.Context, IssueRef) ([]*Asset, error)
}

func SortAssets(assets []*Asset) {
	sort.Slice(assets, func(i, j int) bool { return assets[i].Key.Name < assets[j].Key.Name })
}
