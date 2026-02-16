package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

type uploadResult struct {
	Filename string `json:"filename"`
	Article  string `json:"article,omitempty"`
	ProductID int64 `json:"product_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type uploadResponse struct {
	Uploaded []uploadResult `json:"uploaded"`
	Skipped  []uploadResult `json:"skipped"`
	Errors   []uploadResult `json:"errors"`
}

type productRow struct {
	ID      int64  `json:"id"`
	Article string `json:"article"`
}

func (h *Handlers) UploadProductImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(50 << 20); err != nil {
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	if r.MultipartForm == nil || len(r.MultipartForm.File) == 0 {
		http.Error(w, "no files", http.StatusBadRequest)
		return
	}

	var resp uploadResponse

	for _, headers := range r.MultipartForm.File {
		for _, fh := range headers {
			filename := fh.Filename
			article, order, ext, ok := parseImageFilename(filename)
			if !ok {
				resp.Skipped = append(resp.Skipped, uploadResult{
					Filename: filename,
					Reason:   "invalid filename",
				})
				continue
			}

			file, err := fh.Open()
			if err != nil {
				resp.Errors = append(resp.Errors, uploadResult{
					Filename: filename,
					Article:  article,
					Reason:   "open failed",
				})
				continue
			}
			data, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				resp.Errors = append(resp.Errors, uploadResult{
					Filename: filename,
					Article:  article,
					Reason:   "read failed",
				})
				continue
			}

			productID, err := h.lookupProductID(r.Context(), article)
			if err != nil {
				resp.Errors = append(resp.Errors, uploadResult{
					Filename: filename,
					Article:  article,
					Reason:   "product lookup failed",
				})
				continue
			}
			if productID == 0 {
				resp.Skipped = append(resp.Skipped, uploadResult{
					Filename: filename,
					Article:  article,
					Reason:   "product not found",
				})
				continue
			}

			objectName := buildObjectName(article, order, ext)
			contentType := fh.Header.Get("Content-Type")
			if contentType == "" {
				contentType = mime.TypeByExtension(ext)
			}
			if contentType == "" {
				contentType = http.DetectContentType(data)
			}

			if err := h.uploadToStorage(r.Context(), objectName, contentType, data); err != nil {
				resp.Errors = append(resp.Errors, uploadResult{
					Filename: filename,
					Article:  article,
					Reason:   "storage upload failed",
				})
				continue
			}

			imageURL := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/storage/v1/object/public/product-images/" + objectName
			if err := h.upsertProductImage(r.Context(), productID, objectName, imageURL, order); err != nil {
				resp.Errors = append(resp.Errors, uploadResult{
					Filename: filename,
					Article:  article,
					ProductID: productID,
					Reason:   "db upsert failed",
				})
				continue
			}

			resp.Uploaded = append(resp.Uploaded, uploadResult{
				Filename: filename,
				Article:  article,
				ProductID: productID,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func parseImageFilename(filename string) (article string, order int, ext string, ok bool) {
	base := filepath.Base(filename)
	ext = strings.ToLower(filepath.Ext(base))
	if ext == "" {
		return "", 0, "", false
	}
	name := strings.TrimSuffix(base, ext)
	if name == "" {
		return "", 0, "", false
	}

	order = 0
	article = name
	if i := strings.LastIndex(name, "-"); i > 0 && i < len(name)-1 {
		suffix := name[i+1:]
		if n, err := strconv.Atoi(suffix); err == nil {
			order = n
			article = name[:i]
		}
	}
	article = strings.TrimSpace(article)
	if article == "" {
		return "", 0, "", false
	}
	return article, order, ext, true
}

func buildObjectName(article string, order int, ext string) string {
	if order <= 0 {
		return article + ext
	}
	return fmt.Sprintf("%s-%d%s", article, order, ext)
}

func (h *Handlers) lookupProductID(ctx context.Context, article string) (int64, error) {
	values := url.Values{}
	values.Set("select", "id,article")
	values.Set("article", "eq."+article)
	values.Set("limit", "1")

	urlStr := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/rest/v1/products?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("apikey", h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+h.Cfg.SupabaseServiceRoleKey)

	resp, err := h.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var rows []productRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].ID, nil
}

func (h *Handlers) uploadToStorage(ctx context.Context, objectName, contentType string, data []byte) error {
	urlStr := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/storage/v1/object/product-images/" + url.PathEscape(objectName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, urlStr, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("apikey", h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("x-upsert", "true")

	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("storage status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (h *Handlers) upsertProductImage(ctx context.Context, productID int64, objectName, imageURL string, order int) error {
	payload := map[string]interface{}{
		"product_id":    productID,
		"image_url":     imageURL,
		"display_order": order,
		"bucket":        "product-images",
		"object_path":   objectName,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	urlStr := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/rest/v1/product_images?on_conflict=product_id,bucket,object_path"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Prefer", "resolution=merge-duplicates,return=minimal")

	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
