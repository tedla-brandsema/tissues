package gcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/tedla-brandsema/tissues/services/tissues"
	"google.golang.org/api/iterator"
)

func TestGCSAssetStoreIntegration(t *testing.T) {
	if os.Getenv("TISSUES_GCS_INTEGRATION") != "1" {
		t.Skip("set TISSUES_GCS_INTEGRATION=1 to run against the dogfood bucket")
	}
	bucket := os.Getenv("TISSUES_GCS_TEST_BUCKET")
	if bucket == "" {
		t.Fatal("TISSUES_GCS_TEST_BUCKET is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client, err := storage.NewClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	root, err := New(client, bucket)
	if err != nil {
		t.Fatal(err)
	}
	tenantID := tissues.TenantID("7womw3jzkek74oggxj6f42xak4")
	bound, err := root.ForTenant(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	store := bound.(*TenantStore)
	project := fmt.Sprintf("IT%X", time.Now().UnixNano())
	if len(project) > 16 {
		project = project[:16]
	}
	ref := tissues.IssueRef{ProjectKey: project, Number: 1}
	prefix, err := issuePrefix(tenantID, ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated GCS integration prefix: %s", prefix)
	cleanup := func() {
		it := client.Bucket(bucket).Objects(context.Background(), &storage.Query{Prefix: prefix})
		for {
			attrs, nextErr := it.Next()
			if errors.Is(nextErr, iterator.Done) {
				return
			}
			if nextErr != nil {
				t.Errorf("cleanup list: %v", nextErr)
				return
			}
			if deleteErr := client.Bucket(bucket).Object(attrs.Name).Generation(attrs.Generation).Delete(context.Background()); deleteErr != nil {
				t.Errorf("cleanup %s: %v", attrs.Name, deleteErr)
			}
		}
	}
	defer cleanup()

	keyB := tissues.AssetKey{ProjectKey: project, IssueNumber: 1, Name: "b.png"}
	keyA := tissues.AssetKey{ProjectKey: project, IssueNumber: 1, Name: "a.png"}
	write := tissues.AssetWrite{ContentType: "image/png", Width: 2, Height: 3, Data: integrationPNG(t, 2, 3, color.NRGBA{R: 10, G: 20, B: 30, A: 255})}
	created, err := store.Put(ctx, keyB, write)
	if err != nil || created.Generation <= 0 {
		t.Fatalf("create = %#v, %v", created, err)
	}
	if _, err := store.Put(ctx, keyA, tissues.AssetWrite{ContentType: "image/png", Width: 1, Height: 1, Data: integrationPNG(t, 1, 1, color.NRGBA{A: 255})}); err != nil {
		t.Fatal(err)
	}
	opened, err := store.Open(ctx, keyB)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(opened.Body)
	closeErr := opened.Body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(data, write.Data) || opened.Asset.Width != 2 || opened.Asset.Height != 3 {
		t.Fatalf("open = %#v, %q, %v, %v", opened.Asset, data, readErr, closeErr)
	}
	listed, err := store.List(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("list count = %d", len(listed))
	}
	names := []string{listed[0].Key.Name, listed[1].Key.Name}
	if !sort.StringsAreSorted(names) || fmt.Sprint(names) != "[a.png b.png]" {
		t.Fatalf("list names = %v", names)
	}
	replaced, err := store.Put(ctx, keyB, tissues.AssetWrite{ContentType: "image/png", Width: 4, Height: 5, Data: integrationPNG(t, 4, 5, color.NRGBA{G: 200, A: 255})})
	if err != nil || replaced.Generation == created.Generation {
		t.Fatalf("replace = %#v, %v", replaced, err)
	}
	createRaceKey := tissues.AssetKey{ProjectKey: project, IssueNumber: 1, Name: "create-race.png"}
	assertPutRace(t, ctx, store, createRaceKey)
	assertPutRace(t, ctx, store, keyB)

	cleanup()
	remaining, err := store.List(ctx, ref)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("residual objects = %d, %v", len(remaining), err)
	}
}

func assertPutRace(t *testing.T, ctx context.Context, store *TenantStore, key tissues.AssetKey) {
	t.Helper()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	store.root.beforeWrite = func() { entered <- struct{}{}; <-release }
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, value := range []string{"race-one", "race-two"} {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			_, putErr := store.Put(ctx, key, tissues.AssetWrite{ContentType: "image/png", Width: 6, Height: 7, Data: []byte(value)})
			results <- putErr
		}(value)
	}
	<-entered
	<-entered
	close(release)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, tissues.ErrConflict):
			conflicts++
		default:
			t.Errorf("race result = %v", result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("race successes=%d conflicts=%d", successes, conflicts)
	}
	store.root.beforeWrite = nil
}

func integrationPNG(t *testing.T, width, height int, fill color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, fill)
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
