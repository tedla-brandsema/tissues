package tissues

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCanonicalAssetName(t *testing.T) {
	for _, test := range []struct {
		input, name, format string
	}{
		{"Photo.JPG", "photo.jpg", "jpeg"},
		{"Photo.JPEG", "photo.jpg", "jpeg"},
		{"Diagram.PNG", "diagram.png", "png"},
	} {
		name, format, err := canonicalAssetName(test.input)
		if err != nil || name != test.name || format != test.format {
			t.Fatalf("canonicalAssetName(%q) = %q, %q, %v", test.input, name, format, err)
		}
	}
	valid100 := strings.Repeat("a", 96) + ".png"
	if name, _, err := canonicalAssetName(valid100); err != nil || len(name) != 100 {
		t.Fatalf("100-byte filename = %q, %v", name, err)
	}
	longJPEG := strings.Repeat("a", 96) + ".jpeg"
	if name, _, err := canonicalAssetName(longJPEG); err != nil || len(name) != 100 || !strings.HasSuffix(name, ".jpg") {
		t.Fatalf("canonicalized 100-byte JPEG filename = %q, %v", name, err)
	}
	for _, invalid := range []string{"", ".png", ".hidden.png", "_x.png", "-x.png", "../x.png", `dir\x.png`, "a b.png", "a\n.png", "a..b.png", "a.gif", strings.Repeat("a", 97) + ".png"} {
		if _, _, err := canonicalAssetName(invalid); !errors.Is(err, ErrInvalid) {
			t.Errorf("canonicalAssetName(%q) error = %v", invalid, err)
		}
	}
}

func TestProcessImageAcceptsJPEGAndPNG(t *testing.T) {
	jpegData := encodeTestJPEG(t, 20, 10)
	for _, filename := range []string{"photo.jpg", "photo.jpeg"} {
		processed, err := processImage(filename, jpegData)
		if err != nil || processed.Name != "photo.jpg" || processed.ContentType != "image/jpeg" || processed.Width != 20 || processed.Height != 10 || len(processed.Data) > MaxStoredBytes {
			t.Fatalf("JPEG %s = %#v, %v", filename, processed, err)
		}
		if bytes.Contains(processed.Data, []byte("EXIF-PRIVATE-METADATA")) {
			t.Fatal("JPEG metadata marker survived re-encoding")
		}
	}
	pngData := encodeTestPNG(t, 10, 20, true)
	processed, err := processImage("diagram.png", pngData)
	if err != nil || processed.ContentType != "image/png" || processed.Width != 10 || processed.Height != 20 || len(processed.Data) > MaxStoredBytes {
		t.Fatalf("PNG = %#v, %v", processed, err)
	}
	decoded, err := png.Decode(bytes.NewReader(processed.Data))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, alpha := decoded.At(0, 0).RGBA()
	if alpha != 0 {
		t.Fatalf("transparent alpha = %d", alpha)
	}
}

func TestProcessImageRejectsUnsupportedMalformedAndMismatch(t *testing.T) {
	var gifData bytes.Buffer
	if err := gif.Encode(&gifData, image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black}), nil); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"image.png", gifData.Bytes()},
		{"image.jpg", []byte("not an image")},
		{"image.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
		{"image.png", encodeTestJPEG(t, 2, 2)},
		{"image.jpg", encodeTestPNG(t, 2, 2, false)},
	} {
		if _, err := processImage(test.name, test.data); !errors.Is(err, ErrInvalid) {
			t.Errorf("processImage(%q) error = %v", test.name, err)
		}
	}
}

func TestImageDimensionGuardsAndDecodedRecheck(t *testing.T) {
	for _, dimensions := range [][2]int{{0, 1}, {1, 0}, {12001, 1}, {1, 12001}, {4000, 3001}} {
		if err := validateImageDimensions(dimensions[0], dimensions[1]); !errors.Is(err, ErrInvalid) {
			t.Errorf("dimensions %v error = %v", dimensions, err)
		}
	}
	decoder := imageDecoder{
		decodeConfig: func(io.Reader) (image.Config, string, error) { return image.Config{Width: 2, Height: 2}, "png", nil },
		decode: func(io.Reader) (image.Image, string, error) {
			return image.NewNRGBA(image.Rect(0, 0, 1, 1)), "png", nil
		},
	}
	if _, err := processImageWithDecoder("image.png", nil, decoder); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("decoded mismatch error = %v", err)
	}
}

func TestProcessImageResizeNoUpscaleAndDeterminism(t *testing.T) {
	small := encodeTestJPEG(t, 100, 50)
	a, err := processImage("small.jpg", small)
	if err != nil || a.Width != 100 || a.Height != 50 {
		t.Fatalf("small = %#v, %v", a, err)
	}
	large := encodeTestJPEG(t, 2400, 1200)
	b, err := processImage("large.jpg", large)
	if err != nil || b.Width != 1200 || b.Height != 600 || len(b.Data) > MaxStoredBytes {
		t.Fatalf("large = %#v, %v", b, err)
	}
	c, err := processImage("large.jpg", large)
	if err != nil || b.Width != c.Width || b.Height != c.Height || !bytes.Equal(b.Data, c.Data) {
		t.Fatal("image processing is not deterministic")
	}
}

func TestPNGHighEntropyShrinksBelowStoredLimit(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1200, 1200))
	var value uint32 = 1
	for i := range img.Pix {
		value = value*1664525 + 1013904223
		img.Pix[i] = byte(value >> 24)
	}
	var input bytes.Buffer
	if err := png.Encode(&input, img); err != nil {
		t.Fatal(err)
	}
	processed, err := processImage("noise.png", input.Bytes())
	if err != nil || len(processed.Data) > MaxStoredBytes || max(processed.Width, processed.Height) > maxStoredEdge {
		t.Fatalf("noise = %dx%d, %d bytes, %v", processed.Width, processed.Height, len(processed.Data), err)
	}
}

func TestImageProcessingAdmissionIsOneAtATimeAndCancelable(t *testing.T) {
	repo := newMemoryRepository()
	svc := testService(t, repo)
	if _, err := svc.CreateProject(context.Background(), "FLUENT"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateIssue(context.Background(), "FLUENT", CreateIssueRequest{Title: "Issue", Description: "Body"}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	svc.process = func(string, []byte) (processedImage, error) {
		calls.Add(1)
		close(entered)
		<-release
		return processedImage{Name: "one.png", ContentType: "image/png", Width: 1, Height: 1, Data: []byte{1}}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := svc.UploadAsset(context.Background(), "FLUENT-1", "one.png", bytes.NewReader(nil))
		done <- err
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.UploadAsset(ctx, "FLUENT-1", "two.png", bytes.NewReader(nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued upload error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("processor calls while occupied = %d", calls.Load())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func encodeTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: byte(x), G: byte(y), B: byte(x + y), A: 255})
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return append(output.Bytes(), []byte("EXIF-PRIVATE-METADATA")...)
}

func encodeTestPNG(t *testing.T, width, height int, transparent bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	alpha := uint8(255)
	if transparent {
		alpha = 0
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 20, G: 40, B: 60, A: alpha})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
