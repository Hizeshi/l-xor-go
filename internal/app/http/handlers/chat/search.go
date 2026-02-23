package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (s *Service) getEmbedding(ctx context.Context, text string) ([]float64, error) {
	payload := map[string]interface{}{
		"model":  s.Cfg.OllamaEmbeddingModel,
		"prompt": text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	urlStr := strings.TrimRight(s.Cfg.OllamaURL, "/") + "/api/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("ollama status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out ollamaEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embedding) == 0 {
		return nil, errors.New("empty embedding")
	}
	return out.Embedding, nil
}

func vectorString(vec []float64) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(v, 'f', -1, 64))
	}
	b.WriteByte(']')
	return b.String()
}

func (s *Service) searchProductsHybrid(ctx context.Context, queryText string, queryEmbedding string, limit int) ([]SupabaseMatch, error) {
	if limit <= 0 {
		limit = 5
	}
	payload := map[string]interface{}{
		"arg_query_text":          queryText,
		"arg_query_embedding":     queryEmbedding,
		"arg_match_threshold":     0.2,
		"arg_page_limit":          limit,
		"arg_page_offset":         0,
		"arg_sort_by":             "relevance",
		"arg_filter_min_price":    nil,
		"arg_filter_max_price":    nil,
		"arg_filter_brand_id":     nil,
		"arg_filter_color_id":     nil,
		"arg_filter_product_type": nil,
		"arg_filter_series_id":    nil,
	}
	var rows []productSearchRow
	if err := s.callSupabaseRPC(ctx, "search_products", payload, &rows); err != nil {
		// Retry text-only if vector casting fails.
		payload["arg_query_embedding"] = nil
		var rowsText []productSearchRow
		if errText := s.callSupabaseRPC(ctx, "search_products", payload, &rowsText); errText != nil {
			return nil, fmt.Errorf("search_products failed: %w; text-only failed: %v", err, errText)
		}
		rows = rowsText
	}
	if len(rows) == 0 && strings.TrimSpace(queryText) != "" {
		return s.searchProductsFallback(ctx, queryText, limit)
	}
	res := make([]SupabaseMatch, 0, len(rows))
	for _, r := range rows {
		content := r.NameRaw
		if r.Price != nil {
			content = fmt.Sprintf("%s | Цена: %v", content, *r.Price)
		}
		meta := map[string]interface{}{}
		meta["name"] = r.NameRaw
		if r.Price != nil {
			meta["price"] = *r.Price
		}
		if r.ImageURL != nil {
			meta["image"] = *r.ImageURL
		}
		if r.DetectedBrand != nil {
			meta["brand"] = *r.DetectedBrand
		}
		if r.DetectedColor != nil {
			meta["color"] = *r.DetectedColor
		}
		if r.DetectedSeries != nil {
			meta["series"] = *r.DetectedSeries
		}
		res = append(res, SupabaseMatch{
			ID:         r.ID,
			Content:    content,
			Metadata:   meta,
			Similarity: r.Score,
		})
	}
	return res, nil
}

func (s *Service) searchProductsFallback(ctx context.Context, queryText string, limit int) ([]SupabaseMatch, error) {
	payload := map[string]interface{}{
		"search_text":        queryText,
		"filter_brand_id":    nil,
		"filter_category_id": nil,
	}
	var rows []productFallbackRow
	if err := s.callSupabaseRPC(ctx, "get_all_products", payload, &rows); err != nil {
		return nil, err
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	res := make([]SupabaseMatch, 0, len(rows))
	for _, r := range rows {
		content := r.NameRaw
		if r.Brand != nil && *r.Brand != "" {
			content += " | Бренд: " + *r.Brand
		}
		if r.Price != nil {
			content += fmt.Sprintf(" | Цена: %v", *r.Price)
		}
		meta := map[string]interface{}{}
		meta["name"] = r.NameRaw
		if r.Article != nil {
			meta["article"] = *r.Article
		}
		if r.ProductType != nil {
			meta["type"] = *r.ProductType
		}
		if r.Price != nil {
			meta["price"] = *r.Price
		}
		if r.ImageURL != nil {
			meta["image"] = *r.ImageURL
		}
		if r.Brand != nil {
			meta["brand"] = *r.Brand
		}
		if r.Color != nil {
			meta["color"] = *r.Color
		}
		if r.Category != nil {
			meta["category"] = *r.Category
		}
		res = append(res, SupabaseMatch{
			ID:         r.ID,
			Content:    content,
			Metadata:   meta,
			Similarity: 0,
		})
	}
	return res, nil
}

func (s *Service) loadProductsFromHistory(ctx context.Context, history []chatMessageRow) ([]SupabaseMatch, error) {
	ids := extractProductIDsFromHistory(history)
	if len(ids) == 0 {
		return nil, nil
	}
	values := url.Values{}
	values.Set("select", "id,name_raw,price,brand_name,color_name,series_name,product_type")
	values.Set("id", "in.("+joinIDs(ids)+")")

	urlStr := strings.TrimRight(s.Cfg.SupabaseURL, "/") + "/rest/v1/products_full?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", s.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+s.Cfg.SupabaseServiceRoleKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var rows []productFullRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	res := make([]SupabaseMatch, 0, len(rows))
	for _, r := range rows {
		content := r.NameRaw
		if r.BrandName != nil && *r.BrandName != "" {
			content += " | Бренд: " + *r.BrandName
		}
		if r.Price != nil {
			content += fmt.Sprintf(" | Цена: %v", *r.Price)
		}
		meta := map[string]interface{}{}
		meta["name"] = r.NameRaw
		if r.Price != nil {
			meta["price"] = *r.Price
		}
		if r.BrandName != nil {
			meta["brand"] = *r.BrandName
		}
		if r.ColorName != nil {
			meta["color"] = *r.ColorName
		}
		if r.SeriesName != nil {
			meta["series"] = *r.SeriesName
		}
		if r.ProductType != nil {
			meta["type"] = *r.ProductType
		}
		res = append(res, SupabaseMatch{
			ID:         r.ID,
			Content:    content,
			Metadata:   meta,
			Similarity: 0,
		})
	}
	return res, nil
}

func joinIDs(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}

func (s *Service) searchProductsByArticles(ctx context.Context, articles []string, limit int) ([]SupabaseMatch, error) {
	if limit <= 0 {
		limit = 5
	}
	normalized := normalizeArticles(articles)
	if len(normalized) == 0 {
		return nil, nil
	}
	seen := map[int64]struct{}{}
	out := make([]SupabaseMatch, 0, limit)
	// 1) Diversify: first pass returns at most one item per article.
	for _, article := range normalized {
		if len(out) >= limit {
			break
		}
		rows, err := s.fetchProductsByArticle(ctx, article, 1)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if _, ok := seen[row.ID]; ok {
				continue
			}
			seen[row.ID] = struct{}{}
			out = append(out, row)
		}
	}
	// 2) Fill remaining slots: allow extra matches per article.
	if len(out) < limit {
		for _, article := range normalized {
			if len(out) >= limit {
				break
			}
			rows, err := s.fetchProductsByArticle(ctx, article, 5)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				if _, ok := seen[row.ID]; ok {
					continue
				}
				seen[row.ID] = struct{}{}
				out = append(out, row)
				if len(out) >= limit {
					break
				}
			}
		}
	}
	return out, nil
}

func (s *Service) fetchProductsByArticle(ctx context.Context, article string, limit int) ([]SupabaseMatch, error) {
	if limit <= 0 {
		limit = 1
	}
	values := url.Values{}
	values.Set("select", "id,article,name_raw,price,product_type")
	values.Set("article", "ilike.%"+article+"%")
	values.Set("order", "id.desc")
	values.Set("limit", strconv.Itoa(limit))

	urlStr := strings.TrimRight(s.Cfg.SupabaseURL, "/") + "/rest/v1/products?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", s.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+s.Cfg.SupabaseServiceRoleKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var rows []struct {
		ID          int64    `json:"id"`
		Article     *string  `json:"article"`
		NameRaw     string   `json:"name_raw"`
		Price       *float64 `json:"price"`
		ProductType *string  `json:"product_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}

	out := make([]SupabaseMatch, 0, len(rows))
	for _, r := range rows {
		content := strings.TrimSpace(r.NameRaw)
		if r.Price != nil {
			content += fmt.Sprintf(" | Цена: %v", *r.Price)
		}
		meta := map[string]interface{}{"name": r.NameRaw}
		if r.Article != nil {
			meta["article"] = *r.Article
		}
		if r.Price != nil {
			meta["price"] = *r.Price
		}
		if r.ProductType != nil {
			meta["type"] = *r.ProductType
		}
		out = append(out, SupabaseMatch{
			ID:       r.ID,
			Content:  content,
			Metadata: meta,
		})
	}
	return out, nil
}

func normalizeArticles(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, a := range in {
		a = strings.ToUpper(strings.TrimSpace(a))
		if a == "" || len(a) < 3 {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	return out
}
