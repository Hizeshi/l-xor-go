package chat

import (
	"context"
	"regexp"
	"strings"

	"iq-home/go_beckend/internal/domain/ai/messenger"
	"iq-home/go_beckend/internal/domain/ai/site"
)

var uuidRE = regexp.MustCompile(`(?i)^[a-f0-9]{8}-[a-f0-9]{4}-[1-5][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

func (s *Service) fetchUserBehavior(ctx context.Context, userID string) (*userBehaviorContext, error) {
	if !isUUID(userID) {
		return nil, nil
	}
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

func isUUID(v string) bool {
	return uuidRE.MatchString(strings.TrimSpace(v))
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
