// Package store is the canonical Markdown repository layer: it serializes,
// parses, validates and locates issues and comments on the filesystem.
//
// It performs no Git operations, holds no locks, keeps no cache or index
// beyond a single scan, and implements no application commands.
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/tedla-brandsema/tissues/internal/model"
)

const (
	// IssuesDir holds child issues, both at the repository root and inside
	// every issue directory. Containment is parentage.
	IssuesDir = "issues"
	// CommentsDir holds an issue's comment documents.
	CommentsDir = "comments"

	maxSlug = 40
)

// Tree is one scan of a repository's issue tree. It is a snapshot: nothing
// refreshes it except a new Load. Write operations keep the snapshot
// consistent with what they wrote.
type Tree struct {
	root     string
	roots    []*model.Issue
	issues   map[string]*issueEntry
	comments map[string]*commentEntry
}

type issueEntry struct {
	issue *model.Issue
	dir   string // repository-relative, slash-separated
	// created is copied at load time so that an in-place mutation of a
	// caller's Issue cannot smuggle a new creation timestamp onto disk.
	created int64
}

type commentEntry struct {
	comment *model.Comment
	issueID string
	created int64
}

// Load scans root and reconstructs the complete issue tree. Malformed
// content is an error: nothing is guessed at or repaired. A repository with
// no issues/ directory loads as an empty tree.
func Load(root string) (*Tree, error) {
	t := &Tree{
		root:     root,
		issues:   make(map[string]*issueEntry),
		comments: make(map[string]*commentEntry),
	}
	roots, err := t.loadIssuesDir(IssuesDir, "")
	if err != nil {
		return nil, err
	}
	t.roots = roots
	return t, nil
}

// Root returns the repository root this tree was loaded from.
func (t *Tree) Root() string { return t.root }

// Roots returns the top-level issues.
func (t *Tree) Roots() []*model.Issue { return t.roots }

// Issue returns the issue with the given immutable ID.
func (t *Tree) Issue(id string) (*model.Issue, bool) {
	e, ok := t.issues[id]
	if !ok {
		return nil, false
	}
	return e.issue, true
}

// IssueDir returns the repository-relative directory of an issue. Paths are
// not identity; this is a lookup result, not an address.
func (t *Tree) IssueDir(id string) (string, bool) {
	e, ok := t.issues[id]
	if !ok {
		return "", false
	}
	return e.dir, true
}

// Comment returns a comment by its immutable ID within the given issue.
func (t *Tree) Comment(issueID, commentID string) (*model.Comment, bool) {
	e, ok := t.comments[commentID]
	if !ok || e.issueID != issueID {
		return nil, false
	}
	return e.comment, true
}

// CreateIssue writes a new issue. An empty parentID creates a root issue;
// otherwise the issue is created inside the parent's issues/ directory. The
// returned path is the repository-relative path of the written document.
//
// The derived fields ParentID, Children and Comments are owned by the store
// and must be empty in iss; a request carrying them is rejected rather than
// silently discarded, because none of it is canonical repository state. On
// success the store fills in ParentID from parentID and leaves the issue with
// no children and no comments.
func (t *Tree) CreateIssue(parentID string, iss *model.Issue) (string, error) {
	if err := iss.Validate(); err != nil {
		return "", err
	}
	if !ValidID(iss.ID) {
		return "", fmt.Errorf("issue %q: malformed ID", iss.ID)
	}
	if iss.ParentID != "" || len(iss.Children) > 0 || len(iss.Comments) > 0 {
		return "", fmt.Errorf("issue %s: ParentID, Children and Comments are derived and must be empty on create", iss.ID)
	}
	parentDir := IssuesDir
	var parent *model.Issue
	if parentID != "" {
		pe, ok := t.issues[parentID]
		if !ok {
			return "", fmt.Errorf("unknown parent issue %q", parentID)
		}
		parentDir = path.Join(pe.dir, IssuesDir)
		parent = pe.issue
	}
	if err := t.claim(iss.ID); err != nil {
		return "", err
	}
	dir := path.Join(parentDir, DirName(iss.ID, iss.Title))
	if err := os.MkdirAll(t.abs(dir), 0o755); err != nil {
		return "", err
	}
	file, err := t.writeIssueFile(dir, iss)
	if err != nil {
		return "", err
	}
	iss.ParentID = parentID
	t.issues[iss.ID] = &issueEntry{issue: iss, dir: dir, created: iss.Created.Unix()}
	if parent != nil {
		parent.Children = append(parent.Children, iss)
	} else {
		t.roots = append(t.roots, iss)
	}
	return file, nil
}

// WriteIssue rewrites an existing issue's document in place. The issue must
// already exist in the tree; its directory is never renamed, so a slug can
// go stale after a title change.
func (t *Tree) WriteIssue(iss *model.Issue) (string, error) {
	e, ok := t.issues[iss.ID]
	if !ok {
		return "", fmt.Errorf("unknown issue %q", iss.ID)
	}
	if iss.Created.Unix() != e.created {
		return "", fmt.Errorf("issue %s: created timestamp is immutable", iss.ID)
	}
	return t.writeIssueFile(e.dir, iss)
}

// CreateComment writes a new comment on an issue.
func (t *Tree) CreateComment(issueID string, c *model.Comment) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if !ValidID(c.ID) {
		return "", fmt.Errorf("comment %q: malformed ID", c.ID)
	}
	e, ok := t.issues[issueID]
	if !ok {
		return "", fmt.Errorf("unknown issue %q", issueID)
	}
	if err := t.claim(c.ID); err != nil {
		return "", err
	}
	if err := os.MkdirAll(t.abs(path.Join(e.dir, CommentsDir)), 0o755); err != nil {
		return "", err
	}
	file, err := t.writeCommentFile(e.dir, c)
	if err != nil {
		return "", err
	}
	t.comments[c.ID] = &commentEntry{comment: c, issueID: issueID, created: c.Created.Unix()}
	e.issue.Comments = append(e.issue.Comments, c)
	model.SortComments(e.issue.Comments)
	return file, nil
}

// WriteComment rewrites an existing comment. Ordering never consults Updated,
// so an edit cannot move a comment.
func (t *Tree) WriteComment(issueID string, c *model.Comment) (string, error) {
	ce, ok := t.comments[c.ID]
	if !ok || ce.issueID != issueID {
		return "", fmt.Errorf("unknown comment %q on issue %q", c.ID, issueID)
	}
	if c.Created.Unix() != ce.created {
		return "", fmt.Errorf("comment %s: created timestamp is immutable", c.ID)
	}
	return t.writeCommentFile(t.issues[issueID].dir, c)
}

func (t *Tree) writeIssueFile(dir string, iss *model.Issue) (string, error) {
	data, err := RenderIssue(iss)
	if err != nil {
		return "", err
	}
	rel := path.Join(dir, IssueFile)
	if err := os.WriteFile(t.abs(rel), data, 0o644); err != nil {
		return "", err
	}
	return rel, nil
}

func (t *Tree) writeCommentFile(dir string, c *model.Comment) (string, error) {
	data, err := RenderComment(c)
	if err != nil {
		return "", err
	}
	rel := path.Join(dir, CommentsDir, c.ID+".md")
	if err := os.WriteFile(t.abs(rel), data, 0o644); err != nil {
		return "", err
	}
	return rel, nil
}

// claim reserves an ID. Issue and comment IDs share one namespace across the
// whole tree, which keeps uniqueness a single check.
func (t *Tree) claim(id string) error {
	_, issue := t.issues[id]
	_, comment := t.comments[id]
	if issue || comment {
		return fmt.Errorf("duplicate ID %q", id)
	}
	return nil
}

func (t *Tree) abs(rel string) string {
	return filepath.Join(t.root, filepath.FromSlash(rel))
}

func (t *Tree) loadIssuesDir(rel, parentID string) ([]*model.Issue, error) {
	ents, err := os.ReadDir(t.abs(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*model.Issue
	for _, e := range ents {
		if !e.IsDir() {
			continue // stray files are not tissues content
		}
		dir := path.Join(rel, e.Name())
		dirID, _, _ := strings.Cut(e.Name(), "-")
		if !ValidID(dirID) {
			return nil, fmt.Errorf("%s: not a valid issue directory name", dir)
		}
		iss, err := t.loadIssue(dir, dirID, parentID)
		if err != nil {
			return nil, err
		}
		out = append(out, iss)
	}
	return out, nil
}

func (t *Tree) loadIssue(dir, dirID, parentID string) (*model.Issue, error) {
	file := path.Join(dir, IssueFile)
	data, err := os.ReadFile(t.abs(file))
	if err != nil {
		return nil, err
	}
	iss, err := ParseIssue(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if iss.ID != dirID {
		return nil, fmt.Errorf("%s: directory names issue %q but document declares %q", dir, dirID, iss.ID)
	}
	if err := t.claim(iss.ID); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	iss.ParentID = parentID
	t.issues[iss.ID] = &issueEntry{issue: iss, dir: dir, created: iss.Created.Unix()}

	if iss.Comments, err = t.loadComments(dir, iss.ID); err != nil {
		return nil, err
	}
	if iss.Children, err = t.loadIssuesDir(path.Join(dir, IssuesDir), iss.ID); err != nil {
		return nil, err
	}
	return iss, nil
}

func (t *Tree) loadComments(issueDir, issueID string) ([]*model.Comment, error) {
	rel := path.Join(issueDir, CommentsDir)
	ents, err := os.ReadDir(t.abs(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*model.Comment
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		file := path.Join(rel, e.Name())
		fileID := strings.TrimSuffix(e.Name(), ".md")
		if !ValidID(fileID) {
			return nil, fmt.Errorf("%s: not a valid comment filename", file)
		}
		data, err := os.ReadFile(t.abs(file))
		if err != nil {
			return nil, err
		}
		c, err := ParseComment(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if c.ID != fileID {
			return nil, fmt.Errorf("%s: filename names comment %q but document declares %q", file, fileID, c.ID)
		}
		if err := t.claim(c.ID); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		t.comments[c.ID] = &commentEntry{comment: c, issueID: issueID, created: c.Created.Unix()}
		out = append(out, c)
	}
	model.SortComments(out)
	return out, nil
}

// DirName is the directory name for an issue: its ID, optionally followed by
// a readable slug. The slug is decoration; only the ID prefix is identity.
func DirName(id, title string) string {
	if s := Slug(title); s != "" {
		return id + "-" + s
	}
	return id
}

// Slug reduces a title to lowercase ASCII letters and digits, collapsing
// everything else to single dashes.
func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > maxSlug {
		s = strings.TrimRight(s[:maxSlug], "-")
	}
	return s
}
