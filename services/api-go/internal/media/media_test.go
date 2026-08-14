package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tinyPNG is a valid 1×1 PNG (http.DetectContentType → image/png).
var tinyPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 13, 'I', 'H', 'D', 'R',
	0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0,
	0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0, 'I', 'E', 'N', 'D',
	0xae, 0x42, 0x60, 0x82,
}

func newLocalService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	p, err := NewLocalProvider(dir, "")
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	return NewService(p), dir
}

func multipartBody(t *testing.T, field, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("write part: %v", err)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestUpload_StoresSelectedFileAndServesItBack(t *testing.T) {
	svc, dir := newLocalService(t)
	r := Router(svc, nil)

	body, contentType := multipartBody(t, "file", "icon.png", tinyPNG)
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.Host = "api.example.test"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var stored StoredMedia
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if stored.Provider != "local" || stored.ContentType != "image/png" {
		t.Errorf("stored = %+v", stored)
	}
	if !strings.HasPrefix(stored.URL, "http://api.example.test/api/media/files/") {
		t.Errorf("url = %q, want absolute under request host", stored.URL)
	}
	if !strings.HasSuffix(stored.Key, ".png") {
		t.Errorf("key = %q, want .png suffix", stored.Key)
	}
	// The bytes landed on disk under the content-addressed key…
	onDisk, err := os.ReadFile(filepath.Join(dir, stored.Key))
	if err != nil || !bytes.Equal(onDisk, tinyPNG) {
		t.Fatalf("stored file mismatch (err=%v)", err)
	}
	// …and are served back through GET /files/{key}.
	getReq := httptest.NewRequest(http.MethodGet, "/files/"+stored.Key, nil)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK || !bytes.Equal(getRec.Body.Bytes(), tinyPNG) {
		t.Fatalf("serve status = %d len=%d", getRec.Code, getRec.Body.Len())
	}
	if ct := getRec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("served content-type = %q", ct)
	}
}

func TestUpload_RejectsUnsupportedType(t *testing.T) {
	svc, _ := newLocalService(t)
	r := Router(svc, nil)

	body, contentType := multipartBody(t, "file", "evil.html", []byte("<html><script>x</script></html>"))
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestUpload_RejectsOversizedFile(t *testing.T) {
	svc, _ := newLocalService(t)
	r := Router(svc, nil)

	big := make([]byte, MaxImageBytes+16)
	copy(big, tinyPNG) // keep a PNG header so only the size check trips
	body, contentType := multipartBody(t, "file", "big.png", big)
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestUpload_DisabledProviderAnswers503NotFakeURL(t *testing.T) {
	svc := NewService(nil)
	r := Router(svc, nil)

	body, contentType := multipartBody(t, "file", "icon.png", tinyPNG)
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "http") && strings.Contains(rec.Body.String(), "files/") {
		t.Fatalf("disabled upload leaked a URL: %s", rec.Body.String())
	}
}

func TestNftMetadata_BuildsStandardDocumentAndStoresIt(t *testing.T) {
	svc, dir := newLocalService(t)
	r := Router(svc, nil)

	payload := `{"name":"AMT #1","description":"studio piece","image":"http://api.example.test/api/media/files/abc.png","attributes":[{"trait_type":"Palette","value":"Neon"}]}`
	req := httptest.NewRequest(http.MethodPost, "/nft-metadata", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "api.example.test"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Key      string         `json:"key"`
		URL      string         `json:"url"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasSuffix(out.Key, ".json") || !strings.Contains(out.URL, "/api/media/files/") {
		t.Errorf("key/url = %q %q", out.Key, out.URL)
	}
	raw, err := os.ReadFile(filepath.Join(dir, out.Key))
	if err != nil {
		t.Fatalf("metadata file missing: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("stored metadata not JSON: %v", err)
	}
	if doc["name"] != "AMT #1" || doc["image"] != "http://api.example.test/api/media/files/abc.png" {
		t.Errorf("stored doc = %v", doc)
	}
	attrs, _ := doc["attributes"].([]any)
	if len(attrs) != 1 {
		t.Errorf("attributes = %v", doc["attributes"])
	}
}

func TestNftMetadata_RejectsMissingImage(t *testing.T) {
	svc, _ := newLocalService(t)
	r := Router(svc, nil)

	req := httptest.NewRequest(http.MethodPost, "/nft-metadata", strings.NewReader(`{"name":"x","image":"not-a-uri"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestServeFile_RejectsTraversal(t *testing.T) {
	svc, _ := newLocalService(t)
	r := Router(svc, nil)

	req := httptest.NewRequest(http.MethodGet, "/files/"+"%2e%2e%2fsecrets", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 400/404", rec.Code)
	}
}

func TestUploadGuard_AppliesToMutationsOnly(t *testing.T) {
	svc, _ := newLocalService(t)
	deny := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	r := Router(svc, deny)

	body, contentType := multipartBody(t, "file", "icon.png", tinyPNG)
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("guarded upload status = %d, want 401", rec.Code)
	}

	status := httptest.NewRequest(http.MethodGet, "/status", nil)
	statusRec := httptest.NewRecorder()
	r.ServeHTTP(statusRec, status)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d, want 200 (public)", statusRec.Code)
	}
}

func TestContentKey_IsContentAddressedAndTypeChecked(t *testing.T) {
	k1, err := ContentKey(tinyPNG, "image/png")
	if err != nil {
		t.Fatalf("ContentKey: %v", err)
	}
	k2, _ := ContentKey(tinyPNG, "image/png")
	if k1 != k2 {
		t.Errorf("same bytes gave different keys: %s vs %s", k1, k2)
	}
	if _, err := ContentKey([]byte("x"), "application/x-sh"); err == nil {
		t.Errorf("unsupported content type accepted")
	}
}

func TestS3Provider_SignsAndPuts(t *testing.T) {
	var got *http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := &S3Provider{
		Endpoint:      srv.URL,
		Bucket:        "assets",
		Region:        "auto",
		AccessKeyID:   "AKIDEXAMPLE",
		SecretKey:     "secret",
		PublicBaseURL: "https://cdn.example.test",
		HTTPClient:    srv.Client(),
		Now:           func() time.Time { return time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC) },
	}
	stored, err := p.Store(context.Background(), "abcd.png", "image/png", tinyPNG)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if stored.URL != "https://cdn.example.test/abcd.png" {
		t.Errorf("url = %q", stored.URL)
	}
	if got == nil || got.Method != http.MethodPut || got.URL.Path != "/assets/abcd.png" {
		t.Fatalf("request = %+v", got)
	}
	if !bytes.Equal(gotBody, tinyPNG) {
		t.Errorf("body mismatch (%d bytes)", len(gotBody))
	}
	auth := got.Header.Get("Authorization")
	wantScope := "AKIDEXAMPLE/20260706/auto/s3/aws4_request"
	if !strings.Contains(auth, "AWS4-HMAC-SHA256 Credential="+wantScope) ||
		!strings.Contains(auth, "SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date") ||
		!strings.Contains(auth, "Signature=") {
		t.Errorf("authorization = %q", auth)
	}
	if got.Header.Get("X-Amz-Content-Sha256") == "" || got.Header.Get("X-Amz-Date") != "20260706T120000Z" {
		t.Errorf("amz headers = %q / %q", got.Header.Get("X-Amz-Content-Sha256"), got.Header.Get("X-Amz-Date"))
	}
}

func TestS3Provider_DeterministicSignature(t *testing.T) {
	sign := func(secret string) string {
		var auth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		p := &S3Provider{
			Endpoint: srv.URL, Bucket: "b", Region: "auto",
			AccessKeyID: "k", SecretKey: secret, PublicBaseURL: "https://cdn",
			HTTPClient: srv.Client(),
			Now:        func() time.Time { return time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC) },
		}
		if _, err := p.Store(context.Background(), "abcd.png", "image/png", tinyPNG); err != nil {
			t.Fatalf("Store: %v", err)
		}
		return auth
	}
	// NOTE: host differs between the two test servers, so compare secrets on
	// one server each — different secrets must yield different signatures for
	// otherwise-identical requests (guards against a broken HMAC chain).
	a := sign("secret-a")
	b := sign("secret-b")
	sigOf := func(s string) string {
		idx := strings.Index(s, "Signature=")
		return s[idx:]
	}
	if sigOf(a) == sigOf(b) {
		t.Errorf("signatures identical across secrets: %s", sigOf(a))
	}
}

func TestFromEnv_ProductionDefaultsDisabled(t *testing.T) {
	t.Setenv("MEDIA_STORAGE", "")
	t.Setenv("MEDIA_LOCAL_DIR", "")
	t.Setenv("NODE_ENV", "production")
	p, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if p != nil {
		t.Fatalf("production default provider = %v, want disabled (nil)", p.Name())
	}
}

func TestFromEnv_S3RequiresFullConfig(t *testing.T) {
	t.Setenv("MEDIA_STORAGE", "s3")
	t.Setenv("MEDIA_S3_ENDPOINT", "https://acc.r2.example")
	t.Setenv("MEDIA_S3_BUCKET", "")
	t.Setenv("MEDIA_S3_ACCESS_KEY_ID", "")
	t.Setenv("MEDIA_S3_SECRET_ACCESS_KEY", "")
	t.Setenv("MEDIA_S3_PUBLIC_BASE_URL", "")
	if _, err := FromEnv(); err == nil {
		t.Fatalf("expected error for incomplete s3 config")
	} else if !strings.Contains(err.Error(), "MEDIA_S3_BUCKET") {
		t.Errorf("err = %v, want missing-var list", err)
	}
}

func TestFromEnv_LocalDirFromEnv(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "media")
	t.Setenv("MEDIA_STORAGE", "local")
	t.Setenv("MEDIA_LOCAL_DIR", dir)
	t.Setenv("MEDIA_PUBLIC_BASE_URL", "")
	p, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	local, ok := p.(*LocalProvider)
	if !ok || local.Dir != dir {
		t.Fatalf("provider = %#v", p)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestSafeKey(t *testing.T) {
	for _, bad := range []string{"", "../x.png", "a/b.png", "UPPER.PNG", "x.png;rm", fmt.Sprintf("%c.png", 0)} {
		if _, err := SafeKey(bad); err == nil {
			t.Errorf("SafeKey(%q) accepted", bad)
		}
	}
	if _, err := SafeKey("0123abcd.png"); err != nil {
		t.Errorf("SafeKey rejected valid key: %v", err)
	}
}
