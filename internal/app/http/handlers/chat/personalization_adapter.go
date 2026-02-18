package chat

import (
	"context"
	"strings"

	"iq-home/go_beckend/internal/domain/ai/messenger"
	"iq-home/go_beckend/internal/domain/ai/site"
)

func (s *Service) fetchUserBehavior(ctx context.Context, userID string) (*userBehaviorContext, error) {
	p := site.Personalizer{
		SupabaseURL:            s.Cfg.SupabaseURL,
		SupabaseServiceRoleKey: s.Cfg.SupabaseServiceRoleKey,
		HTTP:                   s.HTTP,
	}
	behavior, err := p.FetchBehavior(ctx, userID)
	if err != nil || behavior == nil {
		return nil, err
	}
	return fromSiteBehavior(behavior), nil
}

func rankProductsByBehavior(products []SupabaseMatch, behavior *userBehaviorContext) []SupabaseMatch {
	if behavior == nil || len(products) == 0 {
		return products
	}
	ranked := site.RankProductsByBehavior(toSiteProducts(products), toSiteBehavior(behavior))
	return fromSiteProducts(ranked)
}

func fallbackProductsFromBehavior(behavior *userBehaviorContext, limit int) []SupabaseMatch {
	if behavior == nil {
		return nil
	}
	items := site.FallbackProductsFromBehavior(toSiteBehavior(behavior), limit)
	return fromSiteProducts(items)
}

func shouldUseSitePersonalization(sessionID string) bool {
	return messenger.IsSiteSession(strings.TrimSpace(sessionID))
}

func toSiteProducts(items []SupabaseMatch) []site.Product {
	out := make([]site.Product, 0, len(items))
	for _, p := range items {
		out = append(out, site.Product{
			ID:       p.ID,
			Content:  p.Content,
			Metadata: p.Metadata,
		})
	}
	return out
}

func fromSiteProducts(items []site.Product) []SupabaseMatch {
	out := make([]SupabaseMatch, 0, len(items))
	for _, p := range items {
		out = append(out, SupabaseMatch{
			ID:       p.ID,
			Content:  p.Content,
			Metadata: p.Metadata,
		})
	}
	return out
}

func toSiteBehavior(b *userBehaviorContext) *site.Behavior {
	if b == nil {
		return nil
	}
	return &site.Behavior{
		RecentlyViewed: toSiteProducts(b.RecentlyViewed),
		Favorites:      toSiteProducts(b.Favorites),
		Cart:           toSiteProducts(b.Cart),
		Orders:         toSiteProducts(b.Orders),
	}
}

func fromSiteBehavior(b *site.Behavior) *userBehaviorContext {
	if b == nil {
		return nil
	}
	return &userBehaviorContext{
		RecentlyViewed: fromSiteProducts(b.RecentlyViewed),
		Favorites:      fromSiteProducts(b.Favorites),
		Cart:           fromSiteProducts(b.Cart),
		Orders:         fromSiteProducts(b.Orders),
	}
}
