package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tedla-brandsema/tissues/internal/model"
)

func fixedID(c byte) string { return strings.Repeat(string(c), IDLen) }

func newIssue(id, title string, created time.Time) *model.Issue {
	return &model.Issue{
		ID: id, Title: title, State: model.StateOpen,
		Created: created, Updated: created,
		Description: "Description of " + title + ".",
	}
}

func newComment(id, author string, created time.Time, body string) *model.Comment {
	return &model.Comment{ID: id, Author: author, Created: created, Updated: created, Body: body}
}

// writeIssueAt writes a canonical issue.md at an arbitrary directory, without
// going through the tree. Used to build repositories by hand.
func writeIssueAt(t *testing.T, root, dir string, iss *model.Issue) {
	t.Helper()
	writeFileAt(t, root, filepath.Join(dir, IssueFile), mustRenderIssue(t, iss))
}

func writeCommentAt(t *testing.T, root, issueDir string, c *model.Comment) {
	t.Helper()
	data, err := RenderComment(c)
	if err != nil {
		t.Fatal(err)
	}
	writeFileAt(t, root, filepath.Join(issueDir, CommentsDir, c.ID+".md"), data)
}

func writeFileAt(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRenderIssue(t *testing.T, iss *model.Issue) []byte {
	t.Helper()
	data, err := RenderIssue(iss)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustLoad(t *testing.T, root string) *Tree {
	t.Helper()
	tree, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return tree
}

// Sub-second precision is canonical now, and it survives a write/reload.
func TestSubSecondTimestampsRoundTripThroughTheStore(t *testing.T) {
	root := t.TempDir()
	created := ts("2026-08-23T13:00:00.000000001Z")
	tree := mustLoad(t, root)

	iss := newIssue(NewID(), "Precise", created)
	if _, err := tree.CreateIssue("", iss); err != nil {
		t.Fatalf("CreateIssue with a sub-second timestamp: %v", err)
	}
	c := newComment(NewID(), "a@example", ts("2026-08-23T13:00:00.5Z"), "Body.")
	if _, err := tree.CreateComment(iss.ID, c); err != nil {
		t.Fatalf("CreateComment with a sub-second timestamp: %v", err)
	}

	got, ok := mustLoad(t, root).Issue(iss.ID)
	if !ok {
		t.Fatal("issue not found")
	}
	if !got.Created.Equal(created) || got.Created.Nanosecond() != 1 {
		t.Errorf("Created = %v (%d ns), want %v", got.Created, got.Created.Nanosecond(), created)
	}
	if len(got.Comments) != 1 || got.Comments[0].Created.Nanosecond() != 500000000 {
		t.Errorf("comment Created = %v", got.Comments[0].Created)
	}
}

func TestLoadEmptyRepository(t *testing.T) {
	tree := mustLoad(t, t.TempDir())
	if len(tree.Roots()) != 0 {
		t.Errorf("Roots() = %d, want 0", len(tree.Roots()))
	}
	if _, ok := tree.Issue(fixedID('a')); ok {
		t.Error("Issue() found something in an empty repository")
	}
}

func TestCreateIssueTreeAndReload(t *testing.T) {
	root := t.TempDir()
	now := ts("2026-08-23T13:00:00Z")

	tree := mustLoad(t, root)
	rootIssue := newIssue(NewID(), "Root issue", now)
	rootPath, err := tree.CreateIssue("", rootIssue)
	if err != nil {
		t.Fatalf("CreateIssue(root): %v", err)
	}
	if want := IssuesDir + "/" + DirName(rootIssue.ID, rootIssue.Title) + "/" + IssueFile; rootPath != want {
		t.Errorf("root path = %q, want %q", rootPath, want)
	}

	child := newIssue(NewID(), "Child issue", now.Add(time.Minute))
	if _, err := tree.CreateIssue(rootIssue.ID, child); err != nil {
		t.Fatalf("CreateIssue(child): %v", err)
	}
	grandchild := newIssue(NewID(), "Grandchild issue", now.Add(2*time.Minute))
	if _, err := tree.CreateIssue(child.ID, grandchild); err != nil {
		t.Fatalf("CreateIssue(grandchild): %v", err)
	}
	if _, err := tree.CreateComment(child.ID, newComment(NewID(), "agent@example", now, "A comment.")); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	// Everything must be reconstructable from the filesystem alone.
	reloaded := mustLoad(t, root)
	if len(reloaded.Roots()) != 1 {
		t.Fatalf("Roots() = %d, want 1", len(reloaded.Roots()))
	}
	gotRoot := reloaded.Roots()[0]
	checkIssue(t, gotRoot, rootIssue)
	if gotRoot.ParentID != "" {
		t.Errorf("root ParentID = %q, want empty", gotRoot.ParentID)
	}
	if len(gotRoot.Children) != 1 {
		t.Fatalf("root has %d children, want 1", len(gotRoot.Children))
	}
	gotChild := gotRoot.Children[0]
	checkIssue(t, gotChild, child)
	if gotChild.ParentID != rootIssue.ID {
		t.Errorf("child ParentID = %q, want %q", gotChild.ParentID, rootIssue.ID)
	}
	if len(gotChild.Comments) != 1 {
		t.Errorf("child has %d comments, want 1", len(gotChild.Comments))
	}
	if len(gotChild.Children) != 1 {
		t.Fatalf("child has %d children, want 1", len(gotChild.Children))
	}
	gotGrand := gotChild.Children[0]
	checkIssue(t, gotGrand, grandchild)
	if gotGrand.ParentID != child.ID {
		t.Errorf("grandchild ParentID = %q, want %q", gotGrand.ParentID, child.ID)
	}

	// Lookup by immutable ID, at any depth.
	for _, want := range []*model.Issue{rootIssue, child, grandchild} {
		got, ok := reloaded.Issue(want.ID)
		if !ok {
			t.Fatalf("Issue(%q) not found", want.ID)
		}
		checkIssue(t, got, want)
	}
}

func TestMoveIssuePreservesSubtreeAndDirectoryName(t *testing.T) {
	root := t.TempDir()
	now := ts("2026-08-23T13:00:00Z")
	tree := mustLoad(t, root)
	a := newIssue(NewID(), "Alpha", now)
	b := newIssue(NewID(), "Beta", now.Add(time.Minute))
	c := newIssue(NewID(), "Gamma", now.Add(2*time.Minute))
	for _, create := range []struct {
		parent string
		issue  *model.Issue
	}{{"", a}, {"", b}, {a.ID, c}} {
		if _, err := tree.CreateIssue(create.parent, create.issue); err != nil {
			t.Fatal(err)
		}
	}
	comment := newComment(NewID(), "human@example", now.Add(3*time.Minute), "Still here.")
	if _, err := tree.CreateComment(c.ID, comment); err != nil {
		t.Fatal(err)
	}
	oldDir, _ := tree.IssueDir(a.ID)
	oldBase := filepath.Base(oldDir)
	a.Updated = now.Add(4 * time.Minute)
	gotOld, newDir, err := tree.MoveIssue(a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotOld != oldDir || filepath.Base(newDir) != oldBase {
		t.Fatalf("move paths = %q -> %q, want old path and preserved basename %q", gotOld, newDir, oldBase)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(oldDir))); !os.IsNotExist(err) {
		t.Errorf("old directory still exists: %v", err)
	}

	fresh := mustLoad(t, root)
	moved, _ := fresh.Issue(a.ID)
	if moved.ParentID != b.ID || len(moved.Children) != 1 || moved.Children[0].ID != c.ID {
		t.Fatalf("moved subtree = %#v", moved)
	}
	grandchild, _ := fresh.Issue(c.ID)
	if grandchild.ParentID != a.ID || len(grandchild.Comments) != 1 || grandchild.Comments[0].ID != comment.ID {
		t.Fatalf("grandchild after move = %#v", grandchild)
	}
	if !moved.Created.Equal(now) || !moved.Updated.Equal(now.Add(4*time.Minute)) {
		t.Errorf("moved timestamps = %v / %v", moved.Created, moved.Updated)
	}

	detachOld, detachNew, err := fresh.MoveIssue(a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if detachOld != newDir || filepath.Base(detachNew) != oldBase {
		t.Errorf("detach paths = %q -> %q", detachOld, detachNew)
	}
	detached, _ := mustLoad(t, root).Issue(a.ID)
	if detached.ParentID != "" || len(detached.Children) != 1 {
		t.Fatalf("detached subtree = %#v", detached)
	}
}

func TestMoveIssueRejectsInvalidParentsBeforeWriting(t *testing.T) {
	root := t.TempDir()
	now := ts("2026-08-23T13:00:00Z")
	tree := mustLoad(t, root)
	a := newIssue(NewID(), "Alpha", now)
	b := newIssue(NewID(), "Beta", now)
	if _, err := tree.CreateIssue("", a); err != nil {
		t.Fatal(err)
	}
	if _, err := tree.CreateIssue(a.ID, b); err != nil {
		t.Fatal(err)
	}
	oldDir, _ := tree.IssueDir(a.ID)
	for _, parent := range []string{a.ID, b.ID, fixedID('z')} {
		if _, _, err := tree.MoveIssue(a.ID, parent); err == nil {
			t.Errorf("MoveIssue(parent %q) succeeded", parent)
		}
	}
	if got, _ := tree.IssueDir(a.ID); got != oldDir {
		t.Errorf("issue moved after rejection: %q -> %q", oldDir, got)
	}
}

// Parentage comes from containment only: the documents never mention it, and
// moving a directory reparents the issue.
func TestHierarchyIsDerivedFromContainment(t *testing.T) {
	root := t.TempDir()
	now := ts("2026-08-23T13:00:00Z")
	a, b, c := fixedID('a'), fixedID('b'), fixedID('c')

	// Deliberately stale/absent slugs: the path is not identity.
	aDir := IssuesDir + "/" + a + "-completely-unrelated-slug"
	bDir := aDir + "/" + IssuesDir + "/" + b
	cDir := bDir + "/" + IssuesDir + "/" + c + "-third"
	writeIssueAt(t, root, aDir, newIssue(a, "Alpha", now))
	writeIssueAt(t, root, bDir, newIssue(b, "Beta", now))
	writeIssueAt(t, root, cDir, newIssue(c, "Gamma", now))

	for _, dir := range []string{aDir, bDir, cDir} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), IssueFile))
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{a, b, c} {
			if n := strings.Count(string(data), id); (dir == aDir && id == a) || (dir == bDir && id == b) || (dir == cDir && id == c) {
				if n != 1 {
					t.Errorf("%s: own ID appears %d times, want 1", dir, n)
				}
			} else if n != 0 {
				t.Errorf("%s: document mentions foreign ID %q", dir, id)
			}
		}
	}

	tree := mustLoad(t, root)
	got, ok := tree.Issue(c)
	if !ok {
		t.Fatal("grandchild not found")
	}
	if got.ParentID != b {
		t.Errorf("Gamma ParentID = %q, want %q", got.ParentID, b)
	}
	if mid, _ := tree.Issue(b); mid.ParentID != a {
		t.Errorf("Beta ParentID = %q, want %q", mid.ParentID, a)
	}
	if top, _ := tree.Issue(a); top.ParentID != "" {
		t.Errorf("Alpha ParentID = %q, want empty", top.ParentID)
	}

	// Move Gamma up to be a sibling of Beta; only the filesystem changed.
	moved := aDir + "/" + IssuesDir + "/" + c + "-third"
	if err := os.Rename(filepath.Join(root, filepath.FromSlash(cDir)), filepath.Join(root, filepath.FromSlash(moved))); err != nil {
		t.Fatal(err)
	}
	tree = mustLoad(t, root)
	if got, _ := tree.Issue(c); got.ParentID != a {
		t.Errorf("after move: Gamma ParentID = %q, want %q", got.ParentID, a)
	}
}

func TestMissingCommentsAndIssuesDirsAreEmpty(t *testing.T) {
	root := t.TempDir()
	id := fixedID('a')
	writeIssueAt(t, root, IssuesDir+"/"+id, newIssue(id, "Lonely", ts("2026-08-23T13:00:00Z")))

	iss, ok := mustLoad(t, root).Issue(id)
	if !ok {
		t.Fatal("issue not found")
	}
	if len(iss.Comments) != 0 || len(iss.Children) != 0 {
		t.Errorf("comments=%d children=%d, want 0 and 0", len(iss.Comments), len(iss.Children))
	}
}

func TestCommentOrdering(t *testing.T) {
	root := t.TempDir()
	base := ts("2026-08-23T09:00:00Z")
	dir := IssuesDir + "/" + fixedID('z')
	writeIssueAt(t, root, dir, newIssue(fixedID('z'), "Discussion", base))

	// Written in an order that matches neither creation nor the eventual
	// presentation order; a/b/c/d is also exactly the filename order on disk.
	writeCommentAt(t, root, dir, newComment(fixedID('a'), "one@example", base.Add(time.Hour), "ten a.m., id a"))
	writeCommentAt(t, root, dir, newComment(fixedID('b'), "two@example", base.Add(2*time.Hour), "eleven a.m."))
	writeCommentAt(t, root, dir, newComment(fixedID('c'), "three@example", base.Add(time.Hour), "ten a.m., id c"))
	writeCommentAt(t, root, dir, newComment(fixedID('d'), "four@example", base, "nine a.m."))

	want := []string{fixedID('d'), fixedID('a'), fixedID('c'), fixedID('b')}
	tree := mustLoad(t, root)
	iss, _ := tree.Issue(fixedID('z'))
	if got := commentIDs(iss); !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v (Created ASC, ID ASC)", abbrev(got), abbrev(want))
	}

	// Editing the earliest comment must not move it.
	edited, ok := tree.Comment(fixedID('z'), fixedID('d'))
	if !ok {
		t.Fatal("comment d not found")
	}
	edited.Body = "nine a.m., edited much later"
	edited.Updated = ts("2026-09-30T23:59:59Z")
	if _, err := tree.WriteComment(fixedID('z'), edited); err != nil {
		t.Fatalf("WriteComment: %v", err)
	}
	if got := commentIDs(iss); !equalStrings(got, want) {
		t.Errorf("in-memory order after edit = %v, want %v", abbrev(got), abbrev(want))
	}

	reloaded := mustLoad(t, root)
	iss2, _ := reloaded.Issue(fixedID('z'))
	if got := commentIDs(iss2); !equalStrings(got, want) {
		t.Errorf("reloaded order after edit = %v, want %v", abbrev(got), abbrev(want))
	}
	got, _ := reloaded.Comment(fixedID('z'), fixedID('d'))
	if got.Body != edited.Body {
		t.Errorf("edited body not persisted: %q", got.Body)
	}
	if !got.Created.Equal(ts("2026-08-23T09:00:00Z")) {
		t.Errorf("Created changed on edit: %v", got.Created)
	}
	if !got.Updated.Equal(edited.Updated) {
		t.Errorf("Updated = %v, want %v", got.Updated, edited.Updated)
	}
}

func commentIDs(i *model.Issue) []string {
	ids := make([]string, len(i.Comments))
	for n, c := range i.Comments {
		ids[n] = c.ID
	}
	return ids
}

func abbrev(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id[:1]
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A title change rewrites the document but never the directory: the slug is
// decoration and is allowed to go stale.
func TestWriteIssueKeepsIdentityAndDirectory(t *testing.T) {
	root := t.TempDir()
	created := ts("2026-08-23T13:00:00Z")
	tree := mustLoad(t, root)
	iss := newIssue(NewID(), "Original title", created)
	path, err := tree.CreateIssue("", iss)
	if err != nil {
		t.Fatal(err)
	}

	iss.Title = "A completely different title"
	iss.State = model.StateClosed
	iss.Updated = created.Add(time.Hour)
	got, err := tree.WriteIssue(iss)
	if err != nil {
		t.Fatalf("WriteIssue: %v", err)
	}
	if got != path {
		t.Errorf("WriteIssue path = %q, want unchanged %q", got, path)
	}
	if !strings.Contains(path, "original-title") {
		t.Errorf("directory %q does not carry the original slug", path)
	}

	reloaded, ok := mustLoad(t, root).Issue(iss.ID)
	if !ok {
		t.Fatal("issue not found after rewrite")
	}
	if reloaded.Title != iss.Title || reloaded.State != model.StateClosed {
		t.Errorf("rewrite not persisted: %q %q", reloaded.Title, reloaded.State)
	}
	if !reloaded.Created.Equal(created) {
		t.Errorf("Created = %v, want %v (immutable)", reloaded.Created, created)
	}
	if !reloaded.Updated.Equal(iss.Updated) {
		t.Errorf("Updated = %v, want %v", reloaded.Updated, iss.Updated)
	}
}

func TestWriteRejectsChangedIdentity(t *testing.T) {
	root := t.TempDir()
	created := ts("2026-08-23T13:00:00Z")
	tree := mustLoad(t, root)
	iss := newIssue(NewID(), "Immutable", created)
	if _, err := tree.CreateIssue("", iss); err != nil {
		t.Fatal(err)
	}
	c := newComment(NewID(), "agent@example", created, "Body.")
	if _, err := tree.CreateComment(iss.ID, c); err != nil {
		t.Fatal(err)
	}

	t.Run("issue created is immutable", func(t *testing.T) {
		iss.Created = created.Add(-time.Hour)
		iss.Updated = created
		if _, err := tree.WriteIssue(iss); err == nil {
			t.Error("WriteIssue accepted a changed Created timestamp")
		}
		iss.Created = created
	})
	t.Run("issue id is immutable", func(t *testing.T) {
		renamed := *iss
		renamed.ID = NewID()
		if _, err := tree.WriteIssue(&renamed); err == nil {
			t.Error("WriteIssue accepted an unknown ID")
		}
	})
	t.Run("comment created is immutable", func(t *testing.T) {
		c.Created = created.Add(-time.Hour)
		if _, err := tree.WriteComment(iss.ID, c); err == nil {
			t.Error("WriteComment accepted a changed Created timestamp")
		}
		c.Created = created
	})
	t.Run("comment id is immutable", func(t *testing.T) {
		renamed := *c
		renamed.ID = NewID()
		if _, err := tree.WriteComment(iss.ID, &renamed); err == nil {
			t.Error("WriteComment accepted an unknown ID")
		}
	})
	t.Run("comment belongs to one issue", func(t *testing.T) {
		other := newIssue(NewID(), "Other", created)
		if _, err := tree.CreateIssue("", other); err != nil {
			t.Fatal(err)
		}
		if _, ok := tree.Comment(other.ID, c.ID); ok {
			t.Error("Comment() found a comment under the wrong issue")
		}
		if _, err := tree.WriteComment(other.ID, c); err == nil {
			t.Error("WriteComment accepted the wrong issue")
		}
	})
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	created := ts("2026-08-23T13:00:00Z")
	cases := map[string]func(tree *Tree) error{
		"malformed id": func(tree *Tree) error {
			_, err := tree.CreateIssue("", newIssue("nope", "Title", created))
			return err
		},
		"empty title": func(tree *Tree) error {
			_, err := tree.CreateIssue("", newIssue(NewID(), "   ", created))
			return err
		},
		"multiline title": func(tree *Tree) error {
			_, err := tree.CreateIssue("", newIssue(NewID(), "line\nbreak", created))
			return err
		},
		"unknown parent": func(tree *Tree) error {
			_, err := tree.CreateIssue(fixedID('q'), newIssue(NewID(), "Orphan", created))
			return err
		},
		"duplicate issue id": func(tree *Tree) error {
			id := NewID()
			if _, err := tree.CreateIssue("", newIssue(id, "First", created)); err != nil {
				return err
			}
			_, err := tree.CreateIssue("", newIssue(id, "Second", created))
			return err
		},
		"unknown issue for comment": func(tree *Tree) error {
			_, err := tree.CreateComment(fixedID('q'), newComment(NewID(), "a@b", created, "Body."))
			return err
		},
		"empty comment author": func(tree *Tree) error {
			iss := newIssue(NewID(), "Host", created)
			if _, err := tree.CreateIssue("", iss); err != nil {
				return err
			}
			_, err := tree.CreateComment(iss.ID, newComment(NewID(), "", created, "Body."))
			return err
		},
		"empty comment body": func(tree *Tree) error {
			iss := newIssue(NewID(), "Host", created)
			if _, err := tree.CreateIssue("", iss); err != nil {
				return err
			}
			_, err := tree.CreateComment(iss.ID, newComment(NewID(), "a@b", created, "  \n "))
			return err
		},
		"non-utc timestamp": func(tree *Tree) error {
			iss := newIssue(NewID(), "Zoned", created)
			iss.Created = created.In(time.FixedZone("CEST", 2*60*60))
			_, err := tree.CreateIssue("", iss)
			return err
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			if err := fn(mustLoad(t, t.TempDir())); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

// A directory can be perfectly good Git and still be invalid tissues.
func TestLoadRejectsInvalidRepository(t *testing.T) {
	created := ts("2026-08-23T13:00:00Z")
	a, b := fixedID('a'), fixedID('b')

	cases := map[string]func(t *testing.T, root string){
		"duplicate issue id": func(t *testing.T, root string) {
			writeIssueAt(t, root, IssuesDir+"/"+a+"-one", newIssue(a, "One", created))
			writeIssueAt(t, root, IssuesDir+"/"+a+"-two", newIssue(a, "Two", created))
		},
		"duplicate issue id at different depths": func(t *testing.T, root string) {
			writeIssueAt(t, root, IssuesDir+"/"+a, newIssue(a, "One", created))
			writeIssueAt(t, root, IssuesDir+"/"+a+"/"+IssuesDir+"/"+a, newIssue(a, "Nested", created))
		},
		"duplicate comment id": func(t *testing.T, root string) {
			writeIssueAt(t, root, IssuesDir+"/"+a, newIssue(a, "One", created))
			writeIssueAt(t, root, IssuesDir+"/"+b, newIssue(b, "Two", created))
			writeCommentAt(t, root, IssuesDir+"/"+a, newComment(fixedID('c'), "x@y", created, "Body."))
			writeCommentAt(t, root, IssuesDir+"/"+b, newComment(fixedID('c'), "x@y", created, "Body."))
		},
		"issue and comment share an id": func(t *testing.T, root string) {
			writeIssueAt(t, root, IssuesDir+"/"+a, newIssue(a, "One", created))
			writeCommentAt(t, root, IssuesDir+"/"+a, newComment(a, "x@y", created, "Body."))
		},
		"directory id does not match document": func(t *testing.T, root string) {
			writeIssueAt(t, root, IssuesDir+"/"+b+"-mismatch", newIssue(a, "One", created))
		},
		"comment filename does not match document": func(t *testing.T, root string) {
			writeIssueAt(t, root, IssuesDir+"/"+a, newIssue(a, "One", created))
			writeFileAt(t, root, IssuesDir+"/"+a+"/"+CommentsDir+"/"+b+".md",
				mustRenderComment(t, newComment(fixedID('c'), "x@y", created, "Body.")))
		},
		"issue directory is not an id": func(t *testing.T, root string) {
			writeIssueAt(t, root, IssuesDir+"/my-notes", newIssue(a, "One", created))
		},
		"comment filename is not an id": func(t *testing.T, root string) {
			writeIssueAt(t, root, IssuesDir+"/"+a, newIssue(a, "One", created))
			writeFileAt(t, root, IssuesDir+"/"+a+"/"+CommentsDir+"/notes.md",
				mustRenderComment(t, newComment(fixedID('c'), "x@y", created, "Body.")))
		},
		"malformed issue document": func(t *testing.T, root string) {
			writeFileAt(t, root, IssuesDir+"/"+a+"/"+IssueFile, []byte("# Just a heading\n"))
		},
		"malformed comment document": func(t *testing.T, root string) {
			writeIssueAt(t, root, IssuesDir+"/"+a, newIssue(a, "One", created))
			writeFileAt(t, root, IssuesDir+"/"+a+"/"+CommentsDir+"/"+b+".md", []byte("just some text\n"))
		},
		"missing issue document": func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, IssuesDir, a), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			build(t, root)
			if _, err := Load(root); err == nil {
				t.Error("Load accepted an invalid repository")
			}
		})
	}
}

func mustRenderComment(t *testing.T, c *model.Comment) []byte {
	t.Helper()
	data, err := RenderComment(c)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Files that are not tissues content are ignored rather than rejected.
func TestLoadIgnoresForeignFiles(t *testing.T) {
	root := t.TempDir()
	id := fixedID('a')
	writeIssueAt(t, root, IssuesDir+"/"+id, newIssue(id, "One", ts("2026-08-23T13:00:00Z")))
	writeFileAt(t, root, IssuesDir+"/README.md", []byte("Notes about this tracker.\n"))
	writeFileAt(t, root, IssuesDir+"/"+id+"/scratch.txt", []byte("scratch\n"))
	writeFileAt(t, root, IssuesDir+"/"+id+"/"+CommentsDir+"/.gitignore", []byte("\n"))

	tree := mustLoad(t, root)
	if len(tree.Roots()) != 1 {
		t.Errorf("Roots() = %d, want 1", len(tree.Roots()))
	}
}

func TestSlugAndDirName(t *testing.T) {
	cases := map[string]string{
		"Support nested issues":        "support-nested-issues",
		"  Leading and trailing  ":     "leading-and-trailing",
		"Punctuation!!! everywhere???": "punctuation-everywhere",
		"Ünïcode ontbreekt hier":       "n-code-ontbreekt-hier",
		"CamelCase And CAPS":           "camelcase-and-caps",
		"issue #42: fix the thing":     "issue-42-fix-the-thing",
		"日本語":                          "",
		"---":                          "",
		"":                             "",
		"a very long title that will certainly exceed the forty character limit": "a-very-long-title-that-will-certainly-ex",
	}
	for title, want := range cases {
		if got := Slug(title); got != want {
			t.Errorf("Slug(%q) = %q, want %q", title, got, want)
		}
		if got := Slug(title); len(got) > maxSlug {
			t.Errorf("Slug(%q) is %d chars, want at most %d", title, len(got), maxSlug)
		}
	}
	id := fixedID('a')
	if got, want := DirName(id, "Support nested issues"), id+"-support-nested-issues"; got != want {
		t.Errorf("DirName = %q, want %q", got, want)
	}
	if got := DirName(id, "日本語"); got != id {
		t.Errorf("DirName with no usable slug = %q, want %q", got, id)
	}
	// The ID is always recoverable from the directory name.
	for title := range cases {
		name := DirName(id, title)
		if got, _, _ := strings.Cut(name, "-"); got != id {
			t.Errorf("DirName(%q) = %q, ID not recoverable", title, name)
		}
	}
}

// Derived fields are store-owned. A create request carrying them is rejected
// outright rather than quietly normalized, so the store never discards state
// a caller believed it had supplied, and the live tree can never show a
// relationship that does not exist on disk.
func TestCreateIssueRejectsCallerSuppliedDerivedState(t *testing.T) {
	created := ts("2026-08-23T13:00:00Z")
	phantomChild := func() *model.Issue { return newIssue(fixedID('c'), "Phantom child", created) }
	phantomComment := func() *model.Comment {
		return newComment(fixedID('d'), "ghost@example", created, "Phantom comment.")
	}

	taints := map[string]func(iss *model.Issue){
		"parent id": func(iss *model.Issue) { iss.ParentID = fixedID('p') },
		"children":  func(iss *model.Issue) { iss.Children = []*model.Issue{phantomChild()} },
		"comments":  func(iss *model.Issue) { iss.Comments = []*model.Comment{phantomComment()} },
		"all three": func(iss *model.Issue) {
			iss.ParentID = fixedID('p')
			iss.Children = []*model.Issue{phantomChild()}
			iss.Comments = []*model.Comment{phantomComment()}
		},
	}
	for name, taint := range taints {
		t.Run(name, func(t *testing.T) {
			for _, asChild := range []bool{false, true} {
				root := t.TempDir()
				tree := mustLoad(t, root)

				parentID := ""
				if asChild {
					parent := newIssue(NewID(), "Real parent", created)
					if _, err := tree.CreateIssue("", parent); err != nil {
						t.Fatal(err)
					}
					parentID = parent.ID
				}

				iss := newIssue(NewID(), "Tainted", created)
				taint(iss)
				if _, err := tree.CreateIssue(parentID, iss); err == nil {
					t.Fatalf("child=%v: CreateIssue accepted caller-supplied derived state", asChild)
				}

				// The rejected issue must exist nowhere: not in the live
				// tree, not on disk, and not as a phantom relationship.
				if _, ok := tree.Issue(iss.ID); ok {
					t.Errorf("child=%v: rejected issue is in the live tree", asChild)
				}
				for _, id := range []string{fixedID('c'), fixedID('d'), fixedID('p')} {
					if _, ok := tree.Issue(id); ok {
						t.Errorf("child=%v: phantom %q reachable in the live tree", asChild, id)
					}
				}
				if parentID != "" {
					parent, _ := tree.Issue(parentID)
					if len(parent.Children) != 0 {
						t.Errorf("child=%v: parent gained %d children", asChild, len(parent.Children))
					}
				} else if len(tree.Roots()) != 0 {
					t.Errorf("child=%v: tree gained %d roots", asChild, len(tree.Roots()))
				}
				if got := len(mustLoad(t, root).Roots()); got != len(tree.Roots()) {
					t.Errorf("child=%v: reloaded roots = %d, live roots = %d", asChild, got, len(tree.Roots()))
				}

				// The rejection must not have consumed the ID either.
				clean := newIssue(iss.ID, "Untainted", created)
				if _, err := tree.CreateIssue(parentID, clean); err != nil {
					t.Errorf("child=%v: ID consumed by the rejected request: %v", asChild, err)
				}
			}
		})
	}
}

// The correction must not erase real relationships: a created issue gets the
// parent it was actually given, and starts with no children and no comments,
// both in the live tree and after a reload.
func TestCreateIssueDerivesRelationships(t *testing.T) {
	root := t.TempDir()
	created := ts("2026-08-23T13:00:00Z")
	tree := mustLoad(t, root)

	parent := newIssue(NewID(), "Real parent", created)
	if _, err := tree.CreateIssue("", parent); err != nil {
		t.Fatal(err)
	}
	child := newIssue(NewID(), "Real child", created)
	if _, err := tree.CreateIssue(parent.ID, child); err != nil {
		t.Fatal(err)
	}
	real := newComment(NewID(), "agent@example", created, "A real comment.")
	if _, err := tree.CreateComment(child.ID, real); err != nil {
		t.Fatal(err)
	}

	check := func(what string, tree *Tree) {
		t.Helper()
		gotParent, ok := tree.Issue(parent.ID)
		if !ok {
			t.Fatalf("%s: parent not found", what)
		}
		if gotParent.ParentID != "" {
			t.Errorf("%s: root ParentID = %q, want empty", what, gotParent.ParentID)
		}
		if len(gotParent.Comments) != 0 {
			t.Errorf("%s: root has %d comments, want 0", what, len(gotParent.Comments))
		}
		if len(gotParent.Children) != 1 || gotParent.Children[0].ID != child.ID {
			t.Fatalf("%s: root children = %v, want exactly the real child", what, commentless(gotParent.Children))
		}
		gotChild := gotParent.Children[0]
		if gotChild.ParentID != parent.ID {
			t.Errorf("%s: child ParentID = %q, want %q", what, gotChild.ParentID, parent.ID)
		}
		if len(gotChild.Children) != 0 {
			t.Errorf("%s: child has %d children, want 0", what, len(gotChild.Children))
		}
		if got := commentIDs(gotChild); !equalStrings(got, []string{real.ID}) {
			t.Errorf("%s: child comments = %v, want exactly the real comment", what, got)
		}
	}
	check("live", tree)
	check("reloaded", mustLoad(t, root))
}

func commentless(issues []*model.Issue) []string {
	ids := make([]string, len(issues))
	for i, iss := range issues {
		ids[i] = iss.ID
	}
	return ids
}
