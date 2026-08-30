package tissues

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tedla-brandsema/tissues/lib/core/config"
)

type memoryRepository struct {
	mu      sync.Mutex
	tenants map[TenantID]*memoryTenantData
}

type memoryTenantData struct {
	projects map[string]*Project
	issues   map[string]map[string]*Issue
	comments map[string]map[string]map[string]*Comment
}

type memoryTenantRepository struct {
	root     *memoryRepository
	tenantID TenantID
}

type memoryAssetStore struct {
	mu      sync.Mutex
	tenants map[TenantID]*memoryTenantAssets
}

type memoryTenantAssets struct {
	assets map[AssetKey]struct {
		asset Asset
		data  []byte
	}
	nextGeneration int64
}

type memoryTenantAssetStore struct {
	root     *memoryAssetStore
	tenantID TenantID
}

type trackingRepositoryRoot struct {
	root Repository
	ids  []TenantID
}

func (r *trackingRepositoryRoot) ForTenant(id TenantID) (TenantRepository, error) {
	r.ids = append(r.ids, id)
	return r.root.ForTenant(id)
}

type trackingAssetRoot struct {
	root AssetStore
	ids  []TenantID
}

func (r *trackingAssetRoot) ForTenant(id TenantID) (TenantAssetStore, error) {
	r.ids = append(r.ids, id)
	return r.root.ForTenant(id)
}

func newMemoryAssetStore() *memoryAssetStore {
	return &memoryAssetStore{tenants: map[TenantID]*memoryTenantAssets{}}
}

func (m *memoryAssetStore) ForTenant(id TenantID) (TenantAssetStore, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tenants[id] == nil {
		m.tenants[id] = &memoryTenantAssets{assets: make(map[AssetKey]struct {
			asset Asset
			data  []byte
		})}
	}
	return &memoryTenantAssetStore{root: m, tenantID: id}, nil
}

func (m *memoryTenantAssetStore) Put(_ context.Context, key AssetKey, write AssetWrite) (*Asset, error) {
	m.root.mu.Lock()
	defer m.root.mu.Unlock()
	tenant := m.root.tenants[m.tenantID]
	tenant.nextGeneration++
	asset := Asset{Key: key, ContentType: write.ContentType, Width: write.Width, Height: write.Height, Size: int64(len(write.Data)), Generation: tenant.nextGeneration}
	tenant.assets[key] = struct {
		asset Asset
		data  []byte
	}{asset: asset, data: bytes.Clone(write.Data)}
	copy := asset
	return &copy, nil
}

func (m *memoryTenantAssetStore) Open(_ context.Context, key AssetKey) (*AssetContent, error) {
	m.root.mu.Lock()
	defer m.root.mu.Unlock()
	stored, ok := m.root.tenants[m.tenantID].assets[key]
	if !ok {
		return nil, ErrNotFound
	}
	return &AssetContent{Asset: stored.asset, Body: io.NopCloser(bytes.NewReader(stored.data))}, nil
}

func (m *memoryTenantAssetStore) List(_ context.Context, ref IssueRef) ([]*Asset, error) {
	m.root.mu.Lock()
	defer m.root.mu.Unlock()
	var out []*Asset
	for key, stored := range m.root.tenants[m.tenantID].assets {
		if key.ProjectKey == ref.ProjectKey && key.IssueNumber == ref.Number {
			asset := stored.asset
			out = append(out, &asset)
		}
	}
	SortAssets(out)
	return out, nil
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{tenants: map[TenantID]*memoryTenantData{}}
}

func mustMemoryTenantRepository(t *testing.T, root *memoryRepository) *memoryTenantRepository {
	t.Helper()
	bound, err := root.ForTenant(testTenantID)
	if err != nil {
		t.Fatal(err)
	}
	return bound.(*memoryTenantRepository)
}

func mustMemoryTenantAssets(t *testing.T, root *memoryAssetStore) *memoryTenantAssetStore {
	t.Helper()
	bound, err := root.ForTenant(testTenantID)
	if err != nil {
		t.Fatal(err)
	}
	return bound.(*memoryTenantAssetStore)
}

func newMemoryTenantData() *memoryTenantData {
	return &memoryTenantData{
		projects: map[string]*Project{},
		issues:   map[string]map[string]*Issue{},
		comments: map[string]map[string]map[string]*Comment{},
	}
}

func (m *memoryRepository) ForTenant(id TenantID) (TenantRepository, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tenants[id] == nil {
		m.tenants[id] = newMemoryTenantData()
	}
	return &memoryTenantRepository{root: m, tenantID: id}, nil
}

func (m *memoryTenantRepository) data() *memoryTenantData { return m.root.tenants[m.tenantID] }

func (m *memoryTenantRepository) ListProjectsPage(_ context.Context, request PageRequest) (*ProjectPage, error) {
	m.root.mu.Lock()
	defer m.root.mu.Unlock()
	data := m.data()
	out := make([]*Project, 0, len(data.projects))
	for _, project := range data.projects {
		out = append(out, cloneProject(project))
	}
	SortProjects(out)
	start, err := memoryCursor(m.tenantID, request.Cursor)
	if err != nil || start > len(out) {
		return nil, ErrInvalid
	}
	end := min(start+request.Size, len(out))
	next := ""
	if end < len(out) {
		next = memoryPageCursor(m.tenantID, end)
	}
	return &ProjectPage{Projects: out[start:end], NextCursor: next}, nil
}
func (m *memoryTenantRepository) ListIssueOverviewsPage(_ context.Context, request PageRequest) (*IssueOverviewPage, error) {
	m.root.mu.Lock()
	defer m.root.mu.Unlock()
	data := m.data()
	var out []*IssueOverview
	for projectKey, issues := range data.issues {
		if request.ProjectKey != "" && projectKey != request.ProjectKey {
			continue
		}
		for ref, issue := range issues {
			if ref != issue.Ref || issue.Validate() != nil {
				return nil, ErrInternal
			}
			if issue.ParentRef != "" {
				if issues[issue.ParentRef] == nil {
					return nil, ErrInternal
				}
			}
			out = append(out, &IssueOverview{ProjectKey: projectKey, Number: issue.Number, Ref: issue.Ref, Title: issue.Title, State: issue.State, ParentRef: issue.ParentRef, Updated: issue.Updated})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		if out[i].ProjectKey != out[j].ProjectKey {
			return out[i].ProjectKey < out[j].ProjectKey
		}
		return out[i].Number < out[j].Number
	})
	start, err := memoryCursor(m.tenantID, request.Cursor)
	if err != nil || start > len(out) {
		return nil, ErrInvalid
	}
	end := min(start+request.Size, len(out))
	next := ""
	if end < len(out) {
		next = memoryPageCursor(m.tenantID, end)
	}
	return &IssueOverviewPage{Issues: out[start:end], NextCursor: next}, nil
}

func memoryPageCursor(tenantID TenantID, offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(tenantID.String() + "\n" + strconv.Itoa(offset)))
}
func memoryCursor(tenantID TenantID, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	parts := bytes.SplitN(decoded, []byte{'\n'}, 2)
	if len(parts) != 2 || string(parts[0]) != tenantID.String() {
		return 0, ErrInvalid
	}
	return strconv.Atoi(string(parts[1]))
}
func (m *memoryTenantRepository) GetProject(_ context.Context, key string) (*Project, error) {
	m.root.mu.Lock()
	defer m.root.mu.Unlock()
	project := m.data().projects[key]
	if project == nil {
		return nil, ErrNotFound
	}
	return cloneProject(project), nil
}
func (m *memoryTenantRepository) ListIssues(_ context.Context, projectKey string) ([]*Issue, error) {
	m.root.mu.Lock()
	defer m.root.mu.Unlock()
	if m.data().projects[projectKey] == nil {
		return nil, ErrNotFound
	}
	return m.issueTree(projectKey)
}
func (m *memoryTenantRepository) GetIssue(_ context.Context, ref IssueRef) (*Issue, error) {
	m.root.mu.Lock()
	defer m.root.mu.Unlock()
	if m.data().issues[ref.ProjectKey][ref.String()] == nil {
		return nil, ErrNotFound
	}
	tree, err := m.issueTree(ref.ProjectKey)
	if err != nil {
		return nil, err
	}
	issue := findMemoryIssue(tree, ref.String())
	if issue == nil {
		return nil, ErrInternal
	}
	return issue, nil
}
func (m *memoryTenantRepository) RunInTransaction(ctx context.Context, fn func(Transaction) error) error {
	m.root.mu.Lock()
	defer m.root.mu.Unlock()
	copy := copyMemoryTenantData(m.data())
	if err := fn(memoryTx{data: copy}); err != nil {
		return err
	}
	m.root.tenants[m.tenantID] = copy
	return nil
}

type memoryTx struct{ data *memoryTenantData }

func (t memoryTx) GetProject(_ context.Context, key string) (*Project, error) {
	project := t.data.projects[key]
	if project == nil {
		return nil, ErrNotFound
	}
	return cloneProject(project), nil
}
func (t memoryTx) PutProject(_ context.Context, project *Project) error {
	if err := project.Validate(); err != nil {
		return err
	}
	t.data.projects[project.Key] = cloneProject(project)
	if t.data.issues[project.Key] == nil {
		t.data.issues[project.Key] = map[string]*Issue{}
	}
	if t.data.comments[project.Key] == nil {
		t.data.comments[project.Key] = map[string]map[string]*Comment{}
	}
	return nil
}
func (t memoryTx) GetIssue(_ context.Context, ref IssueRef) (*Issue, error) {
	issue := t.data.issues[ref.ProjectKey][ref.String()]
	if issue == nil {
		return nil, ErrNotFound
	}
	return cloneIssue(issue), nil
}
func (t memoryTx) GetComment(_ context.Context, ref IssueRef, id string) (*Comment, error) {
	comment := t.data.comments[ref.ProjectKey][ref.String()][id]
	if comment == nil {
		return nil, ErrNotFound
	}
	return cloneComment(comment), nil
}
func (t memoryTx) GetLastComment(_ context.Context, ref IssueRef) (*Comment, error) {
	var latest *Comment
	for _, comment := range t.data.comments[ref.ProjectKey][ref.String()] {
		if latest == nil || latest.Created.Before(comment.Created) || latest.Created.Equal(comment.Created) && latest.ID < comment.ID {
			latest = comment
		}
	}
	if latest == nil {
		return nil, ErrNotFound
	}
	return cloneComment(latest), nil
}
func (t memoryTx) PutIssue(_ context.Context, issue *Issue) error {
	if err := issue.Validate(); err != nil {
		return err
	}
	if t.data.issues[issue.ProjectKey] == nil {
		t.data.issues[issue.ProjectKey] = map[string]*Issue{}
	}
	copy := cloneIssue(issue)
	copy.Children = nil
	copy.Comments = nil
	t.data.issues[issue.ProjectKey][issue.Ref] = copy
	return nil
}
func (t memoryTx) PutComment(_ context.Context, ref IssueRef, comment *Comment) error {
	if err := comment.Validate(); err != nil {
		return err
	}
	if t.data.comments[ref.ProjectKey][ref.String()] == nil {
		t.data.comments[ref.ProjectKey][ref.String()] = map[string]*Comment{}
	}
	t.data.comments[ref.ProjectKey][ref.String()][comment.ID] = cloneComment(comment)
	return nil
}

func (m *memoryTenantRepository) issueTree(projectKey string) ([]*Issue, error) {
	data := m.data()
	flat := map[string]*Issue{}
	for ref, issue := range data.issues[projectKey] {
		flat[ref] = cloneIssue(issue)
		flat[ref].Children = nil
		flat[ref].Comments = nil
		for _, comment := range data.comments[projectKey][ref] {
			flat[ref].Comments = append(flat[ref].Comments, cloneComment(comment))
		}
		SortComments(flat[ref].Comments)
	}
	var roots []*Issue
	for _, issue := range flat {
		if issue.ParentRef == "" {
			roots = append(roots, issue)
			continue
		}
		parent := flat[issue.ParentRef]
		if parent == nil {
			return nil, ErrInternal
		}
		parent.Children = append(parent.Children, issue)
	}
	if hasMemoryCycle(flat) {
		return nil, ErrInternal
	}
	sortIssues(roots)
	return roots, nil
}
func hasMemoryCycle(flat map[string]*Issue) bool {
	for ref := range flat {
		seen := map[string]bool{}
		for current := ref; current != ""; current = flat[current].ParentRef {
			if flat[current] == nil || seen[current] {
				return true
			}
			seen[current] = true
		}
	}
	return false
}
func findMemoryIssue(issues []*Issue, ref string) *Issue {
	for _, issue := range issues {
		if issue.Ref == ref {
			return issue
		}
		if found := findMemoryIssue(issue.Children, ref); found != nil {
			return found
		}
	}
	return nil
}
func copyMemoryTenantData(data *memoryTenantData) *memoryTenantData {
	out := newMemoryTenantData()
	for key, project := range data.projects {
		out.projects[key] = cloneProject(project)
	}
	for projectKey, issues := range data.issues {
		out.issues[projectKey] = map[string]*Issue{}
		for ref, issue := range issues {
			out.issues[projectKey][ref] = cloneIssue(issue)
		}
	}
	for projectKey, issueComments := range data.comments {
		out.comments[projectKey] = map[string]map[string]*Comment{}
		for issueRef, comments := range issueComments {
			out.comments[projectKey][issueRef] = map[string]*Comment{}
			for id, comment := range comments {
				out.comments[projectKey][issueRef][id] = cloneComment(comment)
			}
		}
	}
	return out
}

func testService(t *testing.T, repo Repository) *Service {
	return testServiceWithAssets(t, repo, newMemoryAssetStore())
}

func testServiceWithAssets(t *testing.T, repo Repository, assets AssetStore) *Service {
	return testServiceForTenant(t, repo, assets, testTenantID)
}

func testServiceForTenant(t *testing.T, repo Repository, assets AssetStore, tenantID TenantID) *Service {
	t.Helper()
	profile, err := config.NewServiceProfile("test", Config{BootstrapTenantID: tenantID.String()})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := config.NewSlot(profile)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(slot, repo, assets)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	svc.now = func() time.Time { return base }
	sequence := 0
	svc.newID = func() (string, error) {
		sequence++
		return fmt.Sprintf("aaaaaaaaaaaaaaaaaaaaaaaaa%c", 'a'+rune(sequence)), nil
	}
	return svc
}

func TestMemoryRepositoryAndAssetsAreTenantIsolated(t *testing.T) {
	ctx := context.Background()
	tenantA := testTenantID
	tenantB := TenantID("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	repository := newMemoryRepository()
	assets := newMemoryAssetStore()
	serviceA := testServiceForTenant(t, repository, assets, tenantA)
	serviceB := testServiceForTenant(t, repository, assets, tenantB)
	sharedCommentID := "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	serviceA.newID = func() (string, error) { return sharedCommentID, nil }
	serviceB.newID = func() (string, error) { return sharedCommentID, nil }

	for _, setup := range []struct {
		service *Service
		title   string
	}{
		{serviceA, "Tenant A"},
		{serviceB, "Tenant B"},
	} {
		if _, err := setup.service.CreateProject(ctx, "ALPHA"); err != nil {
			t.Fatal(err)
		}
		parent, err := setup.service.CreateIssue(ctx, "ALPHA", CreateIssueRequest{Title: setup.title, Description: "parent"})
		if err != nil || parent.Ref != "ALPHA-1" {
			t.Fatalf("parent = %#v, %v", parent, err)
		}
		child, err := setup.service.CreateIssue(ctx, "ALPHA", CreateIssueRequest{Title: setup.title + " child", Description: "child"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := setup.service.MoveIssue(ctx, child.Ref, parent.Ref); err != nil {
			t.Fatal(err)
		}
		comment, err := setup.service.AddComment(ctx, parent.Ref, setup.title, "same public ID")
		if err != nil || comment.ID != sharedCommentID {
			t.Fatalf("comment = %#v, %v", comment, err)
		}
		_, tenantAssets, err := setup.service.tenantStores(ctx)
		if err != nil {
			t.Fatal(err)
		}
		asset, err := putProcessedAsset(ctx, tenantAssets, parent, processedImage{Name: "example.png", ContentType: "image/png", Width: 1, Height: 1, Data: []byte(setup.title)})
		if err != nil || asset.Key.Name != "example.png" {
			t.Fatalf("asset = %#v, %v", asset, err)
		}
	}

	issueA, err := serviceA.GetIssue(ctx, "ALPHA-1")
	if err != nil {
		t.Fatal(err)
	}
	issueB, err := serviceB.GetIssue(ctx, "ALPHA-1")
	if err != nil {
		t.Fatal(err)
	}
	if issueA.Title != "Tenant A" || issueB.Title != "Tenant B" || len(issueA.Children) != 1 || len(issueB.Children) != 1 || issueA.Children[0].ParentRef != "ALPHA-1" || issueB.Children[0].ParentRef != "ALPHA-1" || len(issueA.Comments) != 1 || len(issueB.Comments) != 1 || issueA.Comments[0].ID != sharedCommentID || issueB.Comments[0].ID != sharedCommentID {
		t.Fatalf("isolated hierarchies A=%#v B=%#v", issueA, issueB)
	}
	beforeB := issueB
	beforeJSON, _ := json.Marshal(beforeB)
	updatedTitle := "Tenant A updated"
	if _, err := serviceA.UpdateIssue(ctx, UpdateIssueRequest{Ref: "ALPHA-1", Title: &updatedTitle}); err != nil {
		t.Fatal(err)
	}
	afterB, err := serviceB.GetIssue(ctx, "ALPHA-1")
	afterJSON, _ := json.Marshal(afterB)
	if err != nil || !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatalf("Tenant A mutation changed B: before=%s after=%s err=%v", beforeJSON, afterJSON, err)
	}

	for _, service := range []*Service{serviceA, serviceB} {
		projects, err := service.ListProjects(ctx)
		if err != nil || len(projects) != 1 || projects[0].Key != "ALPHA" {
			t.Fatalf("projects = %#v, %v", projects, err)
		}
		overviews, err := service.ListIssueOverviewsPage(ctx, 10, "", "")
		wantTitle := map[*Service]string{serviceA: "Tenant A updated", serviceB: "Tenant B"}[service]
		if err != nil || len(overviews.Issues) != 2 || overviews.Issues[0].Title != wantTitle && overviews.Issues[1].Title != wantTitle {
			t.Fatalf("overviews = %#v, %v", overviews, err)
		}
		issues, err := service.ListIssues(ctx, "ALPHA")
		if err != nil || len(issues) != 1 || len(issues[0].Children) != 1 || issues[0].Title != wantTitle {
			t.Fatalf("issues = %#v, %v", issues, err)
		}
	}

	if _, err := serviceB.CreateProject(ctx, "BRAVO"); err != nil {
		t.Fatal(err)
	}
	if _, err := serviceB.CreateIssue(ctx, "BRAVO", CreateIssueRequest{Title: "Only B", Description: "private"}); err != nil {
		t.Fatal(err)
	}
	if _, err := serviceA.GetProject(ctx, "BRAVO"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Project read = %v", err)
	}
	if _, err := serviceA.GetIssue(ctx, "BRAVO-1"); !errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), tenantB.String()) {
		t.Fatalf("cross-tenant point read leaked existence: %v", err)
	}
	boundA := mustMemoryTenantRepository(t, repository)
	if err := boundA.RunInTransaction(ctx, func(tx Transaction) error {
		_, getErr := tx.GetProject(ctx, "BRAVO")
		return getErr
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Tenant A transaction observed Tenant B: %v", err)
	}

	if _, err := serviceA.CreateProject(ctx, "CHARLIE"); err != nil {
		t.Fatal(err)
	}
	pageA, err := serviceA.ListProjectsPage(ctx, 1, "")
	if err != nil || pageA.NextCursor == "" {
		t.Fatalf("Tenant A cursor = %#v, %v", pageA, err)
	}
	if _, err := serviceB.ListProjectsPage(ctx, 1, pageA.NextCursor); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-tenant cursor = %v", err)
	}

	for _, test := range []struct {
		service *Service
		want    string
	}{{serviceA, "Tenant A"}, {serviceB, "Tenant B"}} {
		content, err := test.service.OpenAsset(ctx, "ALPHA-1", "example.png")
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(content.Body)
		closeErr := content.Body.Close()
		if readErr != nil || closeErr != nil || string(data) != test.want {
			t.Fatalf("asset data = %q, %v, %v", data, readErr, closeErr)
		}
		listed, err := test.service.ListAssets(ctx, "ALPHA-1")
		if err != nil || len(listed) != 1 || listed[0].Key.Name != "example.png" {
			t.Fatalf("assets = %#v, %v", listed, err)
		}
	}
}

type tenantContextKey struct{}

func TestOneServiceResolvesTwoTenantsAndAssetOperationsResolveOnce(t *testing.T) {
	tenantA := testTenantID
	tenantB := TenantID("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	repository := newMemoryRepository()
	assets := newMemoryAssetStore()
	repositoryRoot := &trackingRepositoryRoot{root: repository}
	assetRoot := &trackingAssetRoot{root: assets}
	svc := testServiceWithAssets(t, repositoryRoot, assetRoot)
	svc.resolveTenant = func(ctx context.Context) (TenantID, error) {
		id, ok := ctx.Value(tenantContextKey{}).(TenantID)
		if !ok {
			return "", ErrInvalid
		}
		return id, nil
	}
	ctxA := context.WithValue(context.Background(), tenantContextKey{}, tenantA)
	ctxB := context.WithValue(context.Background(), tenantContextKey{}, tenantB)
	for _, setup := range []struct {
		ctx   context.Context
		title string
	}{{ctxA, "Tenant A"}, {ctxB, "Tenant B"}} {
		if _, err := svc.CreateProject(setup.ctx, "ALPHA"); err != nil {
			t.Fatal(err)
		}
		issue, err := svc.CreateIssue(setup.ctx, "ALPHA", CreateIssueRequest{Title: setup.title, Description: "same ref"})
		if err != nil || issue.Ref != "ALPHA-1" {
			t.Fatalf("issue = %#v, %v", issue, err)
		}
	}
	issueA, errA := svc.GetIssue(ctxA, "ALPHA-1")
	issueB, errB := svc.GetIssue(ctxB, "ALPHA-1")
	if errA != nil || errB != nil || issueA.Title != "Tenant A" || issueB.Title != "Tenant B" {
		t.Fatalf("one-Service isolation A=%#v/%v B=%#v/%v", issueA, errA, issueB, errB)
	}
	tenantAssets, err := assets.ForTenant(tenantA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenantAssets.Put(ctxA, AssetKey{ProjectKey: "ALPHA", IssueNumber: 1, Name: "existing.png"}, AssetWrite{ContentType: "image/png", Width: 1, Height: 1, Data: []byte("Tenant A")}); err != nil {
		t.Fatal(err)
	}

	assertSingleResolution := func(t *testing.T, operation func() error) {
		t.Helper()
		calls := 0
		svc.resolveTenant = func(context.Context) (TenantID, error) {
			calls++
			if calls == 1 {
				return tenantA, nil
			}
			return tenantB, nil
		}
		repositoryRoot.ids = nil
		assetRoot.ids = nil
		if err := operation(); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || !reflect.DeepEqual(repositoryRoot.ids, []TenantID{tenantA}) || !reflect.DeepEqual(assetRoot.ids, []TenantID{tenantA}) {
			t.Fatalf("resolution calls=%d repository IDs=%v asset IDs=%v", calls, repositoryRoot.ids, assetRoot.ids)
		}
	}
	assertSingleResolution(t, func() error {
		listed, err := svc.ListAssets(context.Background(), "ALPHA-1")
		if err == nil && (len(listed) != 1 || listed[0].Key.Name != "existing.png") {
			return fmt.Errorf("listed assets = %#v", listed)
		}
		return err
	})
	assertSingleResolution(t, func() error {
		content, err := svc.OpenAsset(context.Background(), "ALPHA-1", "existing.png")
		if err != nil {
			return err
		}
		defer content.Body.Close()
		data, err := io.ReadAll(content.Body)
		if err == nil && string(data) != "Tenant A" {
			return fmt.Errorf("asset data = %q", data)
		}
		return err
	})
	assertSingleResolution(t, func() error {
		_, err := svc.UploadAsset(context.Background(), "ALPHA-1", "uploaded.png", bytes.NewReader(encodeTestPNG(t, 1, 1, false)))
		return err
	})
	if _, ok := assets.tenants[tenantA].assets[AssetKey{ProjectKey: "ALPHA", IssueNumber: 1, Name: "uploaded.png"}]; !ok {
		t.Fatal("upload was not stored in the resolved tenant")
	}
	if tenantAssetsB := assets.tenants[tenantB]; tenantAssetsB != nil {
		if _, ok := tenantAssetsB.assets[AssetKey{ProjectKey: "ALPHA", IssueNumber: 1, Name: "uploaded.png"}]; ok {
			t.Fatal("upload crossed into the resolver's hypothetical second tenant")
		}
	}
}

func TestRootStoresRejectInvalidTenantAndBootstrapResolverIsOperationScoped(t *testing.T) {
	repository := newMemoryRepository()
	assets := newMemoryAssetStore()
	if _, err := repository.ForTenant("default"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("repository invalid tenant = %v", err)
	}
	if _, err := assets.ForTenant("default"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("asset invalid tenant = %v", err)
	}
	service := testServiceWithAssets(t, repository, assets)
	resolved, err := service.resolveTenant(context.Background())
	if err != nil || resolved != testTenantID || len(repository.tenants) != 0 || len(assets.tenants) != 0 {
		t.Fatalf("resolution tenant=%q err=%v repository tenants=%d asset tenants=%d", resolved, err, len(repository.tenants), len(assets.tenants))
	}
	if _, err := service.CreateProject(context.Background(), "ALPHA"); err != nil {
		t.Fatal(err)
	}
	if repository.tenants[testTenantID] == nil || len(assets.tenants) != 0 {
		t.Fatalf("repository=%v assets=%v", repository.tenants[testTenantID] != nil, assets.tenants[testTenantID] != nil)
	}
}

func issueNumbers(issues []*Issue) []int64 {
	var out []int64
	var walk func([]*Issue)
	walk = func(items []*Issue) {
		for _, issue := range items {
			out = append(out, issue.Number)
			walk(issue.Children)
		}
	}
	walk(issues)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
