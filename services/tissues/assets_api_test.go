package tissues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
)

func TestAssetAPIUploadListRetrieveAndConditionalGET(t *testing.T) {
	svc, store, issue := assetAPIService(t)
	input := encodeTestPNG(t, 20, 10, true)
	request := multipartRequest(t, http.MethodPost, apiBasePath+"/issues/ASSETS-1/assets", "file", "Diagram.PNG", input, nil)
	recorder := httptest.NewRecorder()
	svc.apiHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("upload=%d %s", recorder.Code, recorder.Body.String())
	}
	var uploaded assetDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.Name != "diagram.png" || uploaded.URL != apiBasePath+"/issues/ASSETS-1/assets/diagram.png" || uploaded.ContentType != "image/png" || uploaded.Width != 20 || uploaded.Height != 10 || uploaded.Size <= 0 {
		t.Fatalf("upload DTO = %#v", uploaded)
	}
	after, err := svc.GetIssue(context.Background(), "ASSETS-1")
	if err != nil || !after.Updated.Equal(issue.Updated) {
		t.Fatalf("asset upload changed Issue.Updated: before=%s after=%v err=%v", issue.Updated, after, err)
	}

	tenantAssets := mustMemoryTenantAssets(t, store)
	if _, err := tenantAssets.Put(context.Background(), AssetKey{ProjectKey: "ASSETS", IssueNumber: 1, Name: "a.jpg"}, AssetWrite{ContentType: "image/jpeg", Width: 1, Height: 1, Data: []byte("jpeg")}); err != nil {
		t.Fatal(err)
	}
	list := httptest.NewRecorder()
	svc.apiHandler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, apiBasePath+"/issues/ASSETS-1/assets", nil))
	if list.Code != http.StatusOK || strings.Index(list.Body.String(), `"a.jpg"`) > strings.Index(list.Body.String(), `"diagram.png"`) {
		t.Fatalf("list=%d %s", list.Code, list.Body.String())
	}

	get := httptest.NewRecorder()
	svc.apiHandler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, uploaded.URL, nil))
	stored := store.tenants[testTenantID].assets[AssetKey{ProjectKey: "ASSETS", IssueNumber: 1, Name: "diagram.png"}]
	if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), stored.data) {
		t.Fatalf("get=%d bytes=%d", get.Code, get.Body.Len())
	}
	for header, want := range map[string]string{
		"Content-Type":           "image/png",
		"Content-Disposition":    `inline; filename="diagram.png"`,
		"Cache-Control":          "private, no-cache",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := get.Header().Get(header); got != want {
			t.Errorf("%s=%q want=%q", header, got, want)
		}
	}
	if get.Header().Get("Content-Length") != fmt.Sprint(uploaded.Size) || get.Header().Get("ETag") == "" || strings.HasPrefix(get.Header().Get("ETag"), "W/") {
		t.Fatalf("length=%q etag=%q", get.Header().Get("Content-Length"), get.Header().Get("ETag"))
	}
	conditionalRequest := httptest.NewRequest(http.MethodGet, uploaded.URL, nil)
	conditionalRequest.Header.Set("If-None-Match", get.Header().Get("ETag"))
	conditional := httptest.NewRecorder()
	svc.apiHandler().ServeHTTP(conditional, conditionalRequest)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional=%d %q", conditional.Code, conditional.Body.String())
	}
}

func TestAssetAPIUploadValidationAndLimits(t *testing.T) {
	svc, _, _ := assetAPIService(t)
	endpoint := apiBasePath + "/issues/ASSETS-1/assets"
	tests := []struct {
		name    string
		request *http.Request
		status  int
	}{
		{"invalid issue", multipartRequest(t, http.MethodPost, apiBasePath+"/issues/bad/assets", "file", "x.png", encodeTestPNG(t, 1, 1, false), nil), http.StatusBadRequest},
		{"missing issue", multipartRequest(t, http.MethodPost, apiBasePath+"/issues/ASSETS-99/assets", "file", "x.png", encodeTestPNG(t, 1, 1, false), nil), http.StatusNotFound},
		{"wrong field", multipartRequest(t, http.MethodPost, endpoint, "image", "x.png", encodeTestPNG(t, 1, 1, false), nil), http.StatusBadRequest},
		{"extra part", multipartRequest(t, http.MethodPost, endpoint, "file", "x.png", encodeTestPNG(t, 1, 1, false), map[string]string{"extra": "value"}), http.StatusBadRequest},
		{"unsupported", multipartRequest(t, http.MethodPost, endpoint, "file", "x.png", []byte("not an image"), nil), http.StatusBadRequest},
		{"mismatch", multipartRequest(t, http.MethodPost, endpoint, "file", "x.png", encodeTestJPEG(t, 2, 2), nil), http.StatusBadRequest},
		{"file too large", multipartRequest(t, http.MethodPost, endpoint, "file", "x.png", make([]byte, MaxUploadBytes+1), nil), http.StatusRequestEntityTooLarge},
		{"malformed multipart", malformedMultipartRequest(endpoint), http.StatusBadRequest},
		{"envelope overflow", oversizedMultipartHeaderRequest(t, endpoint), http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			svc.apiHandler().ServeHTTP(recorder, test.request)
			if recorder.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}

	exact := encodeTestJPEG(t, 2, 2)
	exact = append(exact, make([]byte, MaxUploadBytes-len(exact))...)
	recorder := httptest.NewRecorder()
	svc.apiHandler().ServeHTTP(recorder, multipartRequest(t, http.MethodPost, endpoint, "file", "exact.jpg", exact, nil))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("exact 6 MiB upload=%d %s", recorder.Code, recorder.Body.String())
	}
}

func TestAssetAPIRawMultipartFilenameValidation(t *testing.T) {
	svc, store, _ := assetAPIService(t)
	endpoint := apiBasePath + "/issues/ASSETS-1/assets"
	imageData := encodeTestPNG(t, 2, 2, false)
	for _, filename := range []string{"../x.png", "dir/x.png", "/tmp/x.png", "a/../x.png", `dir\ x.png`, `dir\x.png`, "a..b.png", "a b.png"} {
		t.Run(filename, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			svc.apiHandler().ServeHTTP(recorder, multipartRequest(t, http.MethodPost, endpoint, "file", filename, imageData, nil))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("filename %q status=%d body=%s", filename, recorder.Code, recorder.Body.String())
			}
		})
	}

	recorder := httptest.NewRecorder()
	svc.apiHandler().ServeHTTP(recorder, multipartRequest(t, http.MethodPost, endpoint, "file", "Screenshot.PNG", imageData, nil))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("browser filename upload=%d %s", recorder.Code, recorder.Body.String())
	}
	if _, ok := store.tenants[testTenantID].assets[AssetKey{ProjectKey: "ASSETS", IssueNumber: 1, Name: "screenshot.png"}]; !ok {
		t.Fatal("canonical screenshot.png asset was not stored")
	}
}

func TestAssetAPIMissingAndStorageDetailsArePrivate(t *testing.T) {
	svc, _, _ := assetAPIService(t)
	missing := httptest.NewRecorder()
	svc.apiHandler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, apiBasePath+"/issues/ASSETS-1/assets/missing.png", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing=%d %s", missing.Code, missing.Body.String())
	}
	svc.assets = fixedAssetRoot{assets: failingAssetStore{err: fmt.Errorf("bucket secret-bucket object issues/ASSETS/1/x.png")}}
	failed := httptest.NewRecorder()
	svc.apiHandler().ServeHTTP(failed, httptest.NewRequest(http.MethodGet, apiBasePath+"/issues/ASSETS-1/assets/x.png", nil))
	if failed.Code != http.StatusInternalServerError || strings.Contains(failed.Body.String(), "secret-bucket") || strings.Contains(failed.Body.String(), "issues/ASSETS") {
		t.Fatalf("failed=%d %s", failed.Code, failed.Body.String())
	}
}

func assetAPIService(t *testing.T) (*Service, *memoryAssetStore, *Issue) {
	t.Helper()
	repo := newMemoryRepository()
	store := newMemoryAssetStore()
	svc := testServiceWithAssets(t, repo, store)
	if _, err := svc.CreateProject(context.Background(), "ASSETS"); err != nil {
		t.Fatal(err)
	}
	issue, err := svc.CreateIssue(context.Background(), "ASSETS", CreateIssueRequest{Title: "Asset issue", Description: "Body"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, issue
}

func multipartRequest(t *testing.T, method, target, field, filename string, data []byte, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func malformedMultipartRequest(target string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader("broken"))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	return request
}

func oversizedMultipartHeaderRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="x.png"`)
	header.Set("X-Padding", strings.Repeat("a", maxAssetRequestBody))
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("x"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

type failingAssetStore struct{ err error }

func (f failingAssetStore) Put(context.Context, AssetKey, AssetWrite) (*Asset, error) {
	return nil, f.err
}
func (f failingAssetStore) Open(context.Context, AssetKey) (*AssetContent, error) {
	return nil, f.err
}
func (f failingAssetStore) List(context.Context, IssueRef) ([]*Asset, error) {
	return nil, f.err
}
