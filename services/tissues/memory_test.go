package tissues

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tedla-brandsema/tissues/lib/core/config"
)

type memoryRepository struct {
	mu       sync.Mutex
	projects map[string]*Project
	issues   map[string]map[string]*Issue
	comments map[string]map[string]map[string]*Comment
}

type memoryAssetStore struct {
	mu     sync.Mutex
	assets map[AssetKey]struct {
		asset Asset
		data  []byte
	}
	nextGeneration int64
}

func newMemoryAssetStore() *memoryAssetStore {
	return &memoryAssetStore{assets: make(map[AssetKey]struct {
		asset Asset
		data  []byte
	})}
}

func (m *memoryAssetStore) Put(_ context.Context, key AssetKey, write AssetWrite) (*Asset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextGeneration++
	asset := Asset{Key: key, ContentType: write.ContentType, Width: write.Width, Height: write.Height, Size: int64(len(write.Data)), Generation: m.nextGeneration}
	m.assets[key] = struct {
		asset Asset
		data  []byte
	}{asset: asset, data: bytes.Clone(write.Data)}
	copy := asset
	return &copy, nil
}

func (m *memoryAssetStore) Open(_ context.Context, key AssetKey) (*AssetContent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.assets[key]
	if !ok {
		return nil, ErrNotFound
	}
	return &AssetContent{Asset: stored.asset, Body: io.NopCloser(bytes.NewReader(stored.data))}, nil
}

func (m *memoryAssetStore) List(_ context.Context, ref IssueRef) ([]*Asset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Asset
	for key, stored := range m.assets {
		if key.ProjectKey == ref.ProjectKey && key.IssueNumber == ref.Number {
			asset := stored.asset
			out = append(out, &asset)
		}
	}
	SortAssets(out)
	return out, nil
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		projects: map[string]*Project{},
		issues:   map[string]map[string]*Issue{},
		comments: map[string]map[string]map[string]*Comment{},
	}
}

func (m *memoryRepository) ListProjectsPage(_ context.Context, request PageRequest) (*ProjectPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Project, 0, len(m.projects))
	for _, project := range m.projects {
		out = append(out, cloneProject(project))
	}
	SortProjects(out)
	start, err := memoryCursor(request.Cursor)
	if err != nil || start > len(out) {
		return nil, ErrInvalid
	}
	end := min(start+request.Size, len(out))
	next := ""
	if end < len(out) {
		next = memoryPageCursor(end)
	}
	return &ProjectPage{Projects: out[start:end], NextCursor: next}, nil
}
func (m *memoryRepository) ListIssueOverviewsPage(_ context.Context, request PageRequest) (*IssueOverviewPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*IssueOverview
	for projectKey, issues := range m.issues {
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
	start, err := memoryCursor(request.Cursor)
	if err != nil || start > len(out) {
		return nil, ErrInvalid
	}
	end := min(start+request.Size, len(out))
	next := ""
	if end < len(out) {
		next = memoryPageCursor(end)
	}
	return &IssueOverviewPage{Issues: out[start:end], NextCursor: next}, nil
}

func memoryPageCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}
func memoryCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(string(decoded))
}
func (m *memoryRepository) GetProject(_ context.Context, key string) (*Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	project := m.projects[key]
	if project == nil {
		return nil, ErrNotFound
	}
	return cloneProject(project), nil
}
func (m *memoryRepository) ListIssues(_ context.Context, projectKey string) ([]*Issue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.projects[projectKey] == nil {
		return nil, ErrNotFound
	}
	return m.issueTree(projectKey)
}
func (m *memoryRepository) GetIssue(_ context.Context, ref IssueRef) (*Issue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.issues[ref.ProjectKey][ref.String()] == nil {
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
func (m *memoryRepository) RunInTransaction(ctx context.Context, fn func(Transaction) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := m.copy()
	if err := fn(memoryTx{repo: copy}); err != nil {
		return err
	}
	m.projects, m.issues, m.comments = copy.projects, copy.issues, copy.comments
	return nil
}

type memoryTx struct{ repo *memoryRepository }

func (t memoryTx) GetProject(_ context.Context, key string) (*Project, error) {
	project := t.repo.projects[key]
	if project == nil {
		return nil, ErrNotFound
	}
	return cloneProject(project), nil
}
func (t memoryTx) PutProject(_ context.Context, project *Project) error {
	if err := project.Validate(); err != nil {
		return err
	}
	t.repo.projects[project.Key] = cloneProject(project)
	if t.repo.issues[project.Key] == nil {
		t.repo.issues[project.Key] = map[string]*Issue{}
	}
	if t.repo.comments[project.Key] == nil {
		t.repo.comments[project.Key] = map[string]map[string]*Comment{}
	}
	return nil
}
func (t memoryTx) GetIssue(_ context.Context, ref IssueRef) (*Issue, error) {
	issue := t.repo.issues[ref.ProjectKey][ref.String()]
	if issue == nil {
		return nil, ErrNotFound
	}
	return cloneIssue(issue), nil
}
func (t memoryTx) GetComment(_ context.Context, ref IssueRef, id string) (*Comment, error) {
	comment := t.repo.comments[ref.ProjectKey][ref.String()][id]
	if comment == nil {
		return nil, ErrNotFound
	}
	return cloneComment(comment), nil
}
func (t memoryTx) GetLastComment(_ context.Context, ref IssueRef) (*Comment, error) {
	var latest *Comment
	for _, comment := range t.repo.comments[ref.ProjectKey][ref.String()] {
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
	if t.repo.issues[issue.ProjectKey] == nil {
		t.repo.issues[issue.ProjectKey] = map[string]*Issue{}
	}
	copy := cloneIssue(issue)
	copy.Children = nil
	copy.Comments = nil
	t.repo.issues[issue.ProjectKey][issue.Ref] = copy
	return nil
}
func (t memoryTx) PutComment(_ context.Context, ref IssueRef, comment *Comment) error {
	if err := comment.Validate(); err != nil {
		return err
	}
	if t.repo.comments[ref.ProjectKey][ref.String()] == nil {
		t.repo.comments[ref.ProjectKey][ref.String()] = map[string]*Comment{}
	}
	t.repo.comments[ref.ProjectKey][ref.String()][comment.ID] = cloneComment(comment)
	return nil
}

func (m *memoryRepository) issueTree(projectKey string) ([]*Issue, error) {
	flat := map[string]*Issue{}
	for ref, issue := range m.issues[projectKey] {
		flat[ref] = cloneIssue(issue)
		flat[ref].Children = nil
		flat[ref].Comments = nil
		for _, comment := range m.comments[projectKey][ref] {
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
func (m *memoryRepository) copy() *memoryRepository {
	out := newMemoryRepository()
	for key, project := range m.projects {
		out.projects[key] = cloneProject(project)
	}
	for projectKey, issues := range m.issues {
		out.issues[projectKey] = map[string]*Issue{}
		for ref, issue := range issues {
			out.issues[projectKey][ref] = cloneIssue(issue)
		}
	}
	for projectKey, issueComments := range m.comments {
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
	t.Helper()
	profile, err := config.NewServiceProfile("test", Config{})
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
