package media

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// S3Provider stores blobs in any S3-compatible object store (AWS S3,
// Cloudflare R2, MinIO) using a hand-rolled SigV4 signer — no SDK dependency.
// Path-style addressing ({endpoint}/{bucket}/{key}) keeps R2/MinIO happy.
//
// No credentials are ever logged or persisted; they come from env only.
type S3Provider struct {
	Endpoint      string // e.g. https://<account>.r2.cloudflarestorage.com
	Bucket        string
	Region        string // "auto" works for R2; us-east-1 default for AWS
	AccessKeyID   string
	SecretKey     string
	PublicBaseURL string // public bucket / CDN base the stored key is appended to

	HTTPClient *http.Client
	Now        func() time.Time
}

// NewS3ProviderFromEnv reads MEDIA_S3_* and validates the full set is present.
func NewS3ProviderFromEnv() (*S3Provider, error) {
	p := &S3Provider{
		Endpoint:      strings.TrimRight(os.Getenv("MEDIA_S3_ENDPOINT"), "/"),
		Bucket:        os.Getenv("MEDIA_S3_BUCKET"),
		Region:        os.Getenv("MEDIA_S3_REGION"),
		AccessKeyID:   os.Getenv("MEDIA_S3_ACCESS_KEY_ID"),
		SecretKey:     os.Getenv("MEDIA_S3_SECRET_ACCESS_KEY"),
		PublicBaseURL: strings.TrimRight(os.Getenv("MEDIA_S3_PUBLIC_BASE_URL"), "/"),
	}
	if p.Region == "" {
		p.Region = "auto"
	}
	var missing []string
	for name, v := range map[string]string{
		"MEDIA_S3_ENDPOINT":          p.Endpoint,
		"MEDIA_S3_BUCKET":            p.Bucket,
		"MEDIA_S3_ACCESS_KEY_ID":     p.AccessKeyID,
		"MEDIA_S3_SECRET_ACCESS_KEY": p.SecretKey,
		"MEDIA_S3_PUBLIC_BASE_URL":   p.PublicBaseURL,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("media: MEDIA_STORAGE=s3 but missing %s", strings.Join(missing, ", "))
	}
	return p, nil
}

// Name identifies the provider in responses/logs.
func (p *S3Provider) Name() string { return "s3" }

// Store PUTs the object and returns its public URL.
func (p *S3Provider) Store(ctx context.Context, key, contentType string, data []byte) (StoredMedia, error) {
	if p == nil {
		return StoredMedia{}, ErrNotConfigured
	}
	clean, err := SafeKey(key)
	if err != nil {
		return StoredMedia{}, err
	}
	target := p.Endpoint + "/" + p.Bucket + "/" + clean
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(data))
	if err != nil {
		return StoredMedia{}, fmt.Errorf("media: build s3 request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	if err := p.sign(req, data); err != nil {
		return StoredMedia{}, err
	}

	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return StoredMedia{}, fmt.Errorf("media: s3 put: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return StoredMedia{}, fmt.Errorf("media: s3 put %s: status %d: %s", clean, resp.StatusCode, string(body))
	}
	return StoredMedia{
		Key:         clean,
		URL:         p.PublicBaseURL + "/" + clean,
		ContentType: contentType,
		Size:        int64(len(data)),
		Provider:    p.Name(),
	}, nil
}

// sign applies AWS Signature Version 4 to req for the s3 service.
func (p *S3Provider) sign(req *http.Request, payload []byte) error {
	nowFn := p.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	payloadHash := sha256.Sum256(payload)
	payloadHashHex := hex.EncodeToString(payloadHash[:])

	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHashHex)

	signedHeaders := []string{"content-type", "host", "x-amz-content-sha256", "x-amz-date"}
	var canonicalHeaders strings.Builder
	for _, h := range signedHeaders {
		v := req.Header.Get(h)
		if h == "host" {
			v = req.URL.Host
		}
		canonicalHeaders.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		"", // no query string
		canonicalHeaders.String(),
		strings.Join(signedHeaders, ";"),
		payloadHashHex,
	}, "\n")
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))

	scope := strings.Join([]string{dateStamp, p.Region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(canonicalHash[:]),
	}, "\n")

	if p.SecretKey == "" {
		return errors.New("media: s3 secret key missing")
	}
	kDate := hmacSHA256([]byte("AWS4"+p.SecretKey), dateStamp)
	kRegion := hmacSHA256(kDate, p.Region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		p.AccessKeyID, scope, strings.Join(signedHeaders, ";"), signature,
	))
	return nil
}

// canonicalURI escapes each path segment per SigV4 (RFC 3986, segment-wise).
func canonicalURI(u *url.URL) string {
	if u.Path == "" {
		return "/"
	}
	segments := strings.Split(u.Path, "/")
	for i, s := range segments {
		segments[i] = strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
	}
	return strings.Join(segments, "/")
}

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}
