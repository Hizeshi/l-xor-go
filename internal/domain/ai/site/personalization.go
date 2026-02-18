package site

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type Product struct {
	ID       int64
	Content  string
	Metadata map[string]interface{}
}

type Behavior struct {
	RecentlyViewed []Product
	Favorites      []Product
	Cart           []Product
	Orders         []Product
}

type Personalizer struct {
	SupabaseURL            string
	SupabaseServiceRoleKey string
	HTTP                   *http.Client
}

func (p Personalizer) FetchBehavior(ctx context.Context, userID string) (*Behavior, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}

	recentIDs, err := p.fetchUserProductIDsByTable(ctx, "product_views", userID, "viewed_at.desc", 30)
	if err != nil {
		return nil, err
	}
	favoriteIDs, err := p.fetchUserProductIDsByTable(ctx, "favorites", userID, "created_at.desc", 30)
	if err != nil {
		return nil, err
	}
	cartIDs, err := p.fetchUserProductIDsByTable(ctx, "cart_items", userID, "updated_at.desc", 30)
	if err != nil {
		return nil, err
	}
	orderedIDs, err := p.fetchOrderedProductIDs(ctx, userID, 50)
	if err != nil {
		return nil, err
	}

	behavior := &Behavior{}
	if len(recentIDs) > 0 {
		behavior.RecentlyViewed, _ = p.fetchProductsByIDs(ctx, recentIDs, 10)
	}
	if len(favoriteIDs) > 0 {
		behavior.Favorites, _ = p.fetchProductsByIDs(ctx, favoriteIDs, 10)
	}
	if len(cartIDs) > 0 {
		behavior.Cart, _ = p.fetchProductsByIDs(ctx, cartIDs, 10)
	}
	if len(orderedIDs) > 0 {
		behavior.Orders, _ = p.fetchProductsByIDs(ctx, orderedIDs, 10)
	}
	if len(behavior.RecentlyViewed) == 0 && len(behavior.Favorites) == 0 && len(behavior.Cart) == 0 && len(behavior.Orders) == 0 {
		return nil, nil
	}
	return behavior, nil
}

func RankProductsByBehavior(products []Product, behavior *Behavior) []Product {
	if len(products) == 0 || behavior == nil {
		return products
	}
	types, brands, colors, series := collectBehaviorTraits(behavior)
	if len(types) == 0 && len(brands) == 0 && len(colors) == 0 && len(series) == 0 {
		return products
	}

	type scored struct {
		p Product
		s int
		i int
	}
	scoredProducts := make([]scored, 0, len(products))
	for i, p := range products {
		score := 0
		if p.Metadata != nil {
			if v := strings.ToLower(strings.TrimSpace(toString(p.Metadata["type"]))); v != "" && types[v] {
				score += 4
			}
			if v := strings.ToLower(strings.TrimSpace(toString(p.Metadata["brand"]))); v != "" && brands[v] {
				score += 3
			}
			if v := strings.ToLower(strings.TrimSpace(toString(p.Metadata["color"]))); v != "" && colors[v] {
				score += 2
			}
			if v := strings.ToLower(strings.TrimSpace(toString(p.Metadata["series"]))); v != "" && series[v] {
				score += 1
			}
		}
		scoredProducts = append(scoredProducts, scored{p: p, s: score, i: i})
	}

	sort.SliceStable(scoredProducts, func(i, j int) bool {
		if scoredProducts[i].s == scoredProducts[j].s {
			return scoredProducts[i].i < scoredProducts[j].i
		}
		return scoredProducts[i].s > scoredProducts[j].s
	})

	out := make([]Product, 0, len(products))
	for _, sp := range scoredProducts {
		out = append(out, sp.p)
	}
	return out
}

func FallbackProductsFromBehavior(behavior *Behavior, limit int) []Product {
	if behavior == nil {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}
	seen := map[int64]struct{}{}
	out := make([]Product, 0, limit)
	add := func(items []Product) {
		for _, p := range items {
			if len(out) >= limit {
				return
			}
			if p.ID <= 0 {
				continue
			}
			if _, exists := seen[p.ID]; exists {
				continue
			}
			seen[p.ID] = struct{}{}
			out = append(out, p)
		}
	}
	add(behavior.Cart)
	add(behavior.Favorites)
	add(behavior.Orders)
	add(behavior.RecentlyViewed)
	return out
}

func (p Personalizer) fetchUserProductIDsByTable(ctx context.Context, table, userID, orderBy string, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 20
	}
	values := url.Values{}
	values.Set("select", "product_id")
	values.Set("user_id", "eq."+userID)
	if strings.TrimSpace(orderBy) != "" {
		values.Set("order", orderBy)
	}
	values.Set("limit", strconv.Itoa(limit))

	urlStr := strings.TrimRight(p.SupabaseURL, "/") + "/rest/v1/" + table + "?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", p.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+p.SupabaseServiceRoleKey)

	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("table=%s status=%d body=%s", table, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var rows []struct {
		ProductID int64 `json:"product_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		if r.ProductID <= 0 {
			continue
		}
		if _, exists := seen[r.ProductID]; exists {
			continue
		}
		seen[r.ProductID] = struct{}{}
		out = append(out, r.ProductID)
	}
	return out, nil
}

func (p Personalizer) fetchOrderedProductIDs(ctx context.Context, userID string, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 50
	}
	values := url.Values{}
	values.Set("select", "id,order_items(product_id)")
	values.Set("user_id", "eq."+userID)
	values.Set("order", "created_at.desc")
	values.Set("limit", strconv.Itoa(limit))

	urlStr := strings.TrimRight(p.SupabaseURL, "/") + "/rest/v1/orders?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", p.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+p.SupabaseServiceRoleKey)

	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("orders status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var rows []struct {
		OrderItems []struct {
			ProductID *int64 `json:"product_id"`
		} `json:"order_items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}

	seen := map[int64]struct{}{}
	out := make([]int64, 0, limit)
	for _, row := range rows {
		for _, item := range row.OrderItems {
			if item.ProductID == nil || *item.ProductID <= 0 {
				continue
			}
			if _, exists := seen[*item.ProductID]; exists {
				continue
			}
			seen[*item.ProductID] = struct{}{}
			out = append(out, *item.ProductID)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func (p Personalizer) fetchProductsByIDs(ctx context.Context, ids []int64, limit int) ([]Product, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}

	values := url.Values{}
	values.Set("select", "id,name_raw,price,brand_name,color_name,series_name,product_type")
	values.Set("id", "in.("+joinIDs(ids)+")")

	urlStr := strings.TrimRight(p.SupabaseURL, "/") + "/rest/v1/products_full?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", p.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+p.SupabaseServiceRoleKey)

	resp, err := p.HTTP.Do(req)
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
		NameRaw     string   `json:"name_raw"`
		Price       *float64 `json:"price"`
		BrandName   *string  `json:"brand_name"`
		ColorName   *string  `json:"color_name"`
		SeriesName  *string  `json:"series_name"`
		ProductType *string  `json:"product_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	res := make([]Product, 0, len(rows))
	for _, r := range rows {
		content := r.NameRaw
		if r.BrandName != nil && *r.BrandName != "" {
			content += " | Бренд: " + *r.BrandName
		}
		if r.Price != nil {
			content += fmt.Sprintf(" | Цена: %v", *r.Price)
		}
		meta := map[string]interface{}{"name": r.NameRaw}
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
		res = append(res, Product{ID: r.ID, Content: content, Metadata: meta})
	}
	return res, nil
}

func collectBehaviorTraits(behavior *Behavior) (map[string]bool, map[string]bool, map[string]bool, map[string]bool) {
	types := map[string]bool{}
	brands := map[string]bool{}
	colors := map[string]bool{}
	series := map[string]bool{}
	all := make([]Product, 0, len(behavior.RecentlyViewed)+len(behavior.Favorites)+len(behavior.Cart)+len(behavior.Orders))
	all = append(all, behavior.RecentlyViewed...)
	all = append(all, behavior.Favorites...)
	all = append(all, behavior.Cart...)
	all = append(all, behavior.Orders...)
	for _, p := range all {
		if p.Metadata == nil {
			continue
		}
		if v := strings.ToLower(strings.TrimSpace(toString(p.Metadata["type"]))); v != "" {
			types[v] = true
		}
		if v := strings.ToLower(strings.TrimSpace(toString(p.Metadata["brand"]))); v != "" {
			brands[v] = true
		}
		if v := strings.ToLower(strings.TrimSpace(toString(p.Metadata["color"]))); v != "" {
			colors[v] = true
		}
		if v := strings.ToLower(strings.TrimSpace(toString(p.Metadata["series"]))); v != "" {
			series[v] = true
		}
	}
	return types, brands, colors, series
}

func joinIDs(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}

func toString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprintf("%v", v)
	}
}
