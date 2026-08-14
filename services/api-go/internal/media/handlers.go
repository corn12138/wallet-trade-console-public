package media

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Upload limits. Image cap matches typical launch-asset sizes; metadata JSON
// is tiny by construction.
const (
	MaxImageBytes    = 5 << 20  // 5 MiB
	MaxMetadataBytes = 64 << 10 // 64 KiB
)

// Service exposes the upload endpoints over a Provider. A nil provider means
// media storage is disabled: uploads answer 503, never a fake URL.
type Service struct {
	provider Provider
}

// NewService wraps a provider (nil = disabled).
func NewService(provider Provider) *Service {
	return &Service{provider: provider}
}

// Configured reports whether uploads can succeed.
func (s *Service) Configured() bool { return s != nil && s.provider != nil }

// ProviderName names the active provider ("" when disabled).
func (s *Service) ProviderName() string {
	if !s.Configured() {
		return ""
	}
	return s.provider.Name()
}

// NftMetadataInput is the POST /nft-metadata body. The server builds the
// ERC-721 metadata JSON itself so the stored document always has the standard
// shape (and never smuggles arbitrary content under our origin).
type NftMetadataInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Image       string `json:"image"`
	ExternalURL string `json:"externalUrl,omitempty"`
	Attributes  []struct {
		TraitType string `json:"trait_type"`
		Value     string `json:"value"`
	} `json:"attributes,omitempty"`
}

// Router mounts the media endpoints. The mutating uploads sit behind the SIWE
// web3 guard (same gate as token creation); serving stored files is public.
// A nil guard mounts the uploads open — dev-without-JWT_SECRET parity with
// every other module's mountGuarded behavior.
func Router(svc *Service, guard func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()

	mutations := chi.NewRouter()
	mutations.Post("/upload", svc.handleUpload)
	mutations.Post("/nft-metadata", svc.handleNftMetadata)
	if guard != nil {
		r.Group(func(g chi.Router) {
			g.Use(guard)
			g.Mount("/", mutations)
		})
	} else {
		r.Mount("/", mutations)
	}

	r.Get("/files/{key}", svc.handleServeFile)
	r.Get("/status", svc.handleStatus)
	return r
}

// handleStatus lets the FE know (and smokes assert) whether uploads are
// enabled without attempting one.
func (s *Service) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": s.Configured(),
		"provider":   s.ProviderName(),
	})
}

func (s *Service) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		writeError(w, http.StatusServiceUnavailable, ErrNotConfigured.Error())
		return
	}
	if err := r.ParseMultipartForm(MaxImageBytes + (1 << 20)); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "form field \"file\" is required")
		return
	}
	defer file.Close()

	if header.Size > MaxImageBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file exceeds %d bytes", MaxImageBytes))
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxImageBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read upload")
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "empty file")
		return
	}
	if len(data) > MaxImageBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file exceeds %d bytes", MaxImageBytes))
		return
	}

	// Sniff the real content type — the client-declared one is advisory.
	contentType := sniffImageType(data)
	if contentType == "" {
		writeError(w, http.StatusUnsupportedMediaType,
			"unsupported file type (accepted: png, jpeg, webp, gif)")
		return
	}

	stored, err := s.store(r, contentType, data)
	if err != nil {
		slog.ErrorContext(r.Context(), "media upload failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to store upload")
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}

func (s *Service) handleNftMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		writeError(w, http.StatusServiceUnavailable, ErrNotConfigured.Error())
		return
	}
	var in NftMetadataInput
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxMetadataBytes)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Image = strings.TrimSpace(in.Image)
	if in.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !isValidAssetURI(in.Image) {
		writeError(w, http.StatusBadRequest, "image must be an ipfs:// or http(s):// URI")
		return
	}

	// Standard ERC-721 metadata shape, keys in canonical order.
	doc := map[string]any{
		"name":        in.Name,
		"description": strings.TrimSpace(in.Description),
		"image":       in.Image,
	}
	if u := strings.TrimSpace(in.ExternalURL); u != "" {
		if !isValidAssetURI(u) {
			writeError(w, http.StatusBadRequest, "externalUrl must be an ipfs:// or http(s):// URI")
			return
		}
		doc["external_url"] = u
	}
	if len(in.Attributes) > 0 {
		attrs := make([]map[string]string, 0, len(in.Attributes))
		for _, a := range in.Attributes {
			tt, v := strings.TrimSpace(a.TraitType), strings.TrimSpace(a.Value)
			if tt == "" || v == "" {
				continue
			}
			attrs = append(attrs, map[string]string{"trait_type": tt, "value": v})
		}
		if len(attrs) > 0 {
			doc["attributes"] = attrs
		}
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode metadata")
		return
	}
	if len(payload) > MaxMetadataBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "metadata document too large")
		return
	}

	stored, err := s.store(r, "application/json", payload)
	if err != nil {
		slog.ErrorContext(r.Context(), "media nft-metadata store failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to store metadata")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":         stored.Key,
		"url":         stored.URL,
		"contentType": stored.ContentType,
		"size":        stored.Size,
		"provider":    stored.Provider,
		"metadata":    doc,
	})
}

// store runs ContentKey + Provider.Store and absolutizes relative local URLs
// from the request origin (honoring X-Forwarded-Proto behind the gateway).
func (s *Service) store(r *http.Request, contentType string, data []byte) (StoredMedia, error) {
	key, err := ContentKey(data, contentType)
	if err != nil {
		return StoredMedia{}, err
	}
	stored, err := s.provider.Store(r.Context(), key, contentType, data)
	if err != nil {
		return StoredMedia{}, err
	}
	if strings.HasPrefix(stored.URL, "/") {
		scheme := "http"
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		} else if r.TLS != nil {
			scheme = "https"
		}
		stored.URL = scheme + "://" + r.Host + stored.URL
	}
	return stored, nil
}

func (s *Service) handleServeFile(w http.ResponseWriter, r *http.Request) {
	local, ok := s.localProvider()
	if !ok {
		writeError(w, http.StatusNotFound, "file serving is only available with the local media provider")
		return
	}
	key := chi.URLParam(r, "key")
	path, err := local.Open(key)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key")
		return
	}
	if ct := contentTypeForKey(key); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// Content-addressed keys never change bytes → cache hard.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

func (s *Service) localProvider() (*LocalProvider, bool) {
	if s == nil {
		return nil, false
	}
	p, ok := s.provider.(*LocalProvider)
	return p, ok && p != nil
}

// sniffImageType returns the canonical accepted content type for the bytes,
// or "" when the payload is not one of the allowed image formats.
func sniffImageType(data []byte) string {
	detected := http.DetectContentType(data)
	switch detected {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return detected
	default:
		return ""
	}
}

func contentTypeForKey(key string) string {
	for ct, ext := range extByContentType {
		if strings.HasSuffix(key, "."+ext) {
			return ct
		}
	}
	return ""
}

// isValidAssetURI accepts ipfs:// URIs and absolute http(s) URLs — the two
// forms tokenURI/image consumers resolve.
func isValidAssetURI(u string) bool {
	return strings.HasPrefix(u, "ipfs://") && len(u) > len("ipfs://") ||
		strings.HasPrefix(u, "https://") && len(u) > len("https://") ||
		strings.HasPrefix(u, "http://") && len(u) > len("http://")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("media response encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statusCode": status,
		"message":    message,
	})
}
