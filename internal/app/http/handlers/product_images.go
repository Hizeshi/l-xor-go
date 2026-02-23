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
	"time"
)

type uploadResult struct {
	Filename  string `json:"filename"`
	Article   string `json:"article,omitempty"`
	ProductID int64  `json:"product_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
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

type productImageRow struct {
	ID           int64   `json:"id"`
	ProductID    int64   `json:"product_id"`
	ImageURL     *string `json:"image_url"`
	DisplayOrder int     `json:"display_order"`
	Bucket       string  `json:"bucket"`
	ObjectPath   string  `json:"object_path"`
}

type productImageItemResponse struct {
	ID           int64  `json:"id"`
	ProductID    int64  `json:"product_id"`
	ImageURL     string `json:"image_url"`
	DisplayOrder int    `json:"display_order"`
	ObjectPath   string `json:"object_path"`
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
			objectBase := buildObjectName(article, order, "")
			existingRows, err := h.fetchProductImagesByObjectBase(r.Context(), productID, objectBase)
			if err != nil {
				resp.Errors = append(resp.Errors, uploadResult{
					Filename:  filename,
					Article:   article,
					ProductID: productID,
					Reason:    "existing image lookup failed",
				})
				continue
			}
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
					Filename:  filename,
					Article:   article,
					ProductID: productID,
					Reason:    "db upsert failed",
				})
				continue
			}

			cleanupFailed := false
			for _, row := range existingRows {
				oldPath := strings.TrimSpace(row.ObjectPath)
				if oldPath == "" || oldPath == objectName {
					continue
				}
				if err := h.deleteFromStorage(r.Context(), oldPath); err != nil {
					cleanupFailed = true
					continue
				}
				if err := h.deleteProductImageByID(r.Context(), row.ID); err != nil {
					cleanupFailed = true
				}
			}
			if cleanupFailed {
				resp.Errors = append(resp.Errors, uploadResult{
					Filename:  filename,
					Article:   article,
					ProductID: productID,
					Reason:    "uploaded but old format cleanup failed",
				})
			}

			resp.Uploaded = append(resp.Uploaded, uploadResult{
				Filename:  filename,
				Article:   article,
				ProductID: productID,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handlers) AddProductImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}
	productID, err := parseInt64Field(r, "product_id")
	if err != nil || productID <= 0 {
		http.Error(w, "invalid product_id", http.StatusBadRequest)
		return
	}
	displayOrder := parseIntField(r, "display_order", 0)

	file, fh, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "file read failed", http.StatusBadRequest)
		return
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if ext == "" {
		ext = ".jpg"
	}
	objectName := fmt.Sprintf("%d-%d%s", productID, time.Now().UnixNano(), ext)
	contentType := fh.Header.Get("Content-Type")
	if contentType == "" {
		contentType = mime.TypeByExtension(ext)
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}

	if err := h.uploadToStorage(r.Context(), objectName, contentType, data); err != nil {
		http.Error(w, "storage upload failed", http.StatusBadGateway)
		return
	}

	imageURL := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/storage/v1/object/public/product-images/" + objectName
	id, err := h.insertProductImage(r.Context(), productID, objectName, imageURL, displayOrder)
	if err != nil {
		_ = h.deleteFromStorage(r.Context(), objectName)
		http.Error(w, "db insert failed", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(productImageItemResponse{
		ID:           id,
		ProductID:    productID,
		ImageURL:     imageURL,
		DisplayOrder: displayOrder,
		ObjectPath:   objectName,
	})
}

func (h *Handlers) UpdateProductImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}
	imageID, err := parseInt64Field(r, "image_id")
	if err != nil || imageID <= 0 {
		http.Error(w, "invalid image_id", http.StatusBadRequest)
		return
	}
	oldRow, err := h.fetchProductImageByID(r.Context(), imageID)
	if err != nil {
		http.Error(w, "image lookup failed", http.StatusBadGateway)
		return
	}
	if oldRow == nil {
		http.Error(w, "image not found", http.StatusNotFound)
		return
	}

	productID := oldRow.ProductID
	if raw := strings.TrimSpace(r.FormValue("product_id")); raw != "" {
		pid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || pid <= 0 {
			http.Error(w, "invalid product_id", http.StatusBadRequest)
			return
		}
		productID = pid
	}
	displayOrder := oldRow.DisplayOrder
	if raw := strings.TrimSpace(r.FormValue("display_order")); raw != "" {
		o, err := strconv.Atoi(raw)
		if err != nil || o < 0 {
			http.Error(w, "invalid display_order", http.StatusBadRequest)
			return
		}
		displayOrder = o
	}

	file, fh, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "file read failed", http.StatusBadRequest)
		return
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if ext == "" {
		ext = ".jpg"
	}
	objectName := fmt.Sprintf("%d-%d%s", productID, time.Now().UnixNano(), ext)
	contentType := fh.Header.Get("Content-Type")
	if contentType == "" {
		contentType = mime.TypeByExtension(ext)
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if err := h.uploadToStorage(r.Context(), objectName, contentType, data); err != nil {
		http.Error(w, "storage upload failed", http.StatusBadGateway)
		return
	}

	imageURL := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/storage/v1/object/public/product-images/" + objectName
	if err := h.updateProductImageByID(r.Context(), imageID, productID, displayOrder, objectName, imageURL); err != nil {
		_ = h.deleteFromStorage(r.Context(), objectName)
		http.Error(w, "db update failed", http.StatusBadGateway)
		return
	}

	if strings.TrimSpace(oldRow.ObjectPath) != "" && oldRow.ObjectPath != objectName {
		if err := h.deleteFromStorage(r.Context(), oldRow.ObjectPath); err != nil {
			http.Error(w, "updated but old file delete failed", http.StatusBadGateway)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(productImageItemResponse{
		ID:           imageID,
		ProductID:    productID,
		ImageURL:     imageURL,
		DisplayOrder: displayOrder,
		ObjectPath:   objectName,
	})
}

func (h *Handlers) DeleteProductImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	imageID, err := parseInt64IDFromDelete(r)
	if err != nil || imageID <= 0 {
		http.Error(w, "invalid image_id", http.StatusBadRequest)
		return
	}
	row, err := h.fetchProductImageByID(r.Context(), imageID)
	if err != nil {
		http.Error(w, "image lookup failed", http.StatusBadGateway)
		return
	}
	if row == nil {
		http.Error(w, "image not found", http.StatusNotFound)
		return
	}
	if strings.TrimSpace(row.ObjectPath) != "" {
		if err := h.deleteFromStorage(r.Context(), row.ObjectPath); err != nil {
			http.Error(w, "storage delete failed", http.StatusBadGateway)
			return
		}
	}
	if err := h.deleteProductImageByID(r.Context(), imageID); err != nil {
		http.Error(w, "db delete failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":       true,
		"image_id": imageID,
	})
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

func (h *Handlers) insertProductImage(ctx context.Context, productID int64, objectName, imageURL string, order int) (int64, error) {
	payload := map[string]interface{}{
		"product_id":    productID,
		"image_url":     imageURL,
		"display_order": order,
		"bucket":        "product-images",
		"object_path":   objectName,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	urlStr := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/rest/v1/product_images"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Prefer", "return=representation")

	resp, err := h.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var rows []productImageRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("empty insert response")
	}
	return rows[0].ID, nil
}

func (h *Handlers) fetchProductImageByID(ctx context.Context, imageID int64) (*productImageRow, error) {
	values := url.Values{}
	values.Set("select", "id,product_id,image_url,display_order,bucket,object_path")
	values.Set("id", "eq."+strconv.FormatInt(imageID, 10))
	values.Set("limit", "1")
	urlStr := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/rest/v1/product_images?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+h.Cfg.SupabaseServiceRoleKey)
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var rows []productImageRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func (h *Handlers) fetchProductImagesByObjectBase(ctx context.Context, productID int64, objectBase string) ([]productImageRow, error) {
	values := url.Values{}
	values.Set("select", "id,product_id,image_url,display_order,bucket,object_path")
	values.Set("product_id", "eq."+strconv.FormatInt(productID, 10))
	values.Set("bucket", "eq.product-images")
	values.Set("object_path", "like."+objectBase+".*")
	urlStr := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/rest/v1/product_images?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+h.Cfg.SupabaseServiceRoleKey)
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var rows []productImageRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (h *Handlers) updateProductImageByID(ctx context.Context, imageID, productID int64, order int, objectName, imageURL string) error {
	payload := map[string]interface{}{
		"product_id":    productID,
		"display_order": order,
		"object_path":   objectName,
		"image_url":     imageURL,
		"bucket":        "product-images",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	values := url.Values{}
	values.Set("id", "eq."+strconv.FormatInt(imageID, 10))
	urlStr := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/rest/v1/product_images?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, urlStr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Prefer", "return=minimal")
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (h *Handlers) deleteProductImageByID(ctx context.Context, imageID int64) error {
	values := url.Values{}
	values.Set("id", "eq."+strconv.FormatInt(imageID, 10))
	urlStr := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/rest/v1/product_images?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Prefer", "return=minimal")
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (h *Handlers) deleteFromStorage(ctx context.Context, objectName string) error {
	urlStr := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/storage/v1/object/product-images/" + url.PathEscape(objectName)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("apikey", h.Cfg.SupabaseServiceRoleKey)
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("storage status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func parseInt64Field(r *http.Request, key string) (int64, error) {
	raw := strings.TrimSpace(r.FormValue(key))
	if raw == "" {
		return 0, fmt.Errorf("missing %s", key)
	}
	return strconv.ParseInt(raw, 10, 64)
}

func parseIntField(r *http.Request, key string, def int) int {
	raw := strings.TrimSpace(r.FormValue(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func parseInt64IDFromDelete(r *http.Request) (int64, error) {
	if q := strings.TrimSpace(r.URL.Query().Get("image_id")); q != "" {
		return strconv.ParseInt(q, 10, 64)
	}
	var payload struct {
		ImageID int64 `json:"image_id"`
	}
	if r.Body == nil {
		return 0, fmt.Errorf("image_id is required")
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return 0, err
	}
	return payload.ImageID, nil
}
