package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryStoreCloneIsIndependent(t *testing.T) {
	ctx := context.Background()
	store := memoryWith("a", `{"value":"one"}`)
	if err := Clone(ctx, store, "a", "b"); err != nil {
		t.Fatal(err)
	}
	store.Put("b", Document{Format: "json", Data: []byte(`{"value":"two"}`)})
	a, _ := store.Load(ctx, "a")
	b, _ := store.Load(ctx, "b")
	if string(a.Data) == string(b.Data) {
		t.Fatal("clone definitions did not evolve independently")
	}
	if err := Clone(ctx, store, "a", "b"); !errors.Is(err, ErrProfileExists) {
		t.Fatalf("clone existing destination error = %v", err)
	}
}

func TestFileStoreCloneAndAmbiguity(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store := NewFileStore(directory)
	if err := store.Create(ctx, "a", Document{Format: "yaml", Data: []byte("value: one\n")}); err != nil {
		t.Fatal(err)
	}
	if err := Clone(ctx, store, "a", "b"); err != nil {
		t.Fatal(err)
	}
	doc, err := store.Load(ctx, "b")
	if err != nil || doc.Format != ".yaml" || string(doc.Data) != "value: one\n" {
		t.Fatalf("cloned document = %#v, %v", doc, err)
	}
	if err := store.Create(ctx, "b", doc); !errors.Is(err, ErrProfileExists) {
		t.Fatalf("create existing = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "a.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "a"); !errors.Is(err, ErrProfileAmbiguous) {
		t.Fatalf("ambiguous load = %v", err)
	}
	if _, err := store.Load(ctx, "../escape"); err == nil {
		t.Fatal("unsafe profile name accepted")
	}
}

type cloneConfig struct {
	Value string `cfg:"string,default=default"`
}

func TestClonedManagersHaveIndependentIdentityAndRevision(t *testing.T) {
	for _, setup := range []struct {
		name string
		new  func(*testing.T) (Store, func(string))
	}{
		{"memory", func(_ *testing.T) (Store, func(string)) {
			store := NewMemoryStore()
			store.Put("a", Document{Format: "json", Data: []byte(`{"value":"one"}`)})
			return store, func(value string) {
				store.Put("b", Document{Format: "json", Data: []byte(`{"value":"` + value + `"}`)})
			}
		}},
		{"file", func(t *testing.T) (Store, func(string)) {
			directory := t.TempDir()
			store := NewFileStore(directory)
			if err := store.Create(context.Background(), "a", Document{Format: "yaml", Data: []byte("value: one\n")}); err != nil {
				t.Fatal(err)
			}
			return store, func(value string) {
				if err := os.WriteFile(filepath.Join(directory, "b.yaml"), []byte("value: "+value+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}},
	} {
		t.Run(setup.name, func(t *testing.T) {
			ctx := context.Background()
			store, mutateB := setup.new(t)
			if err := Clone(ctx, store, "a", "b"); err != nil {
				t.Fatal(err)
			}
			managerA, err := NewManager[cloneConfig](ctx, LoadOptions{Name: "a", Store: store})
			if err != nil {
				t.Fatal(err)
			}
			managerB, err := NewManager[cloneConfig](ctx, LoadOptions{Name: "b", Store: store})
			if err != nil {
				t.Fatal(err)
			}
			mutateB("two")
			if _, err := managerB.Reload(ctx); err != nil {
				t.Fatal(err)
			}
			if a, b := managerA.Current(), managerB.Current(); a.Name != "a" || b.Name != "b" || a.Revision != 1 || b.Revision != 2 || a.Config.Value != "one" || b.Config.Value != "two" {
				t.Fatalf("independent profiles: a=%#v b=%#v", a, b)
			}
		})
	}
}
