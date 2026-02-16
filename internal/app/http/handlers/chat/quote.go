package chat

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"iq-home/go_beckend/internal/domain/quote"
	pdfgen "iq-home/go_beckend/internal/domain/quote/pdf/gofpdf"
)

func (s *Service) generateQuotePDF(products []SupabaseMatch) ([]byte, error) {
	q := quote.Quote{
		Number:    "NF-1",
		CreatedAt: time.Now(),
		Customer:  quote.Customer{Name: "Клиент"},
	}
	var subtotal int64
	for _, p := range products {
		name := extractProductName(p)
		price := extractProductPrice(p)
		if price <= 0 {
			continue
		}
		line := price
		q.Items = append(q.Items, quote.Item{
			ProductID: p.ID,
			Name:      name,
			Qty:       1,
			UnitPrice: price,
			LineTotal: line,
		})
		subtotal += line
	}
	if len(q.Items) == 0 {
		return nil, errors.New("no products for quote")
	}
	q.Subtotal = subtotal
	q.Total = subtotal
	gen := pdfgen.New()
	return gen.Generate(q)
}

func extractProductName(p SupabaseMatch) string {
	if p.Metadata != nil {
		if v, ok := p.Metadata["name"]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	if i := strings.Index(p.Content, " | "); i > 0 {
		return strings.TrimSpace(p.Content[:i])
	}
	return strings.TrimSpace(p.Content)
}

func extractProductPrice(p SupabaseMatch) int64 {
	if p.Metadata != nil {
		if v, ok := p.Metadata["price"]; ok {
			switch t := v.(type) {
			case float64:
				return int64(t)
			case int64:
				return t
			case json.Number:
				if f, err := t.Float64(); err == nil {
					return int64(f)
				}
			case string:
				if f, err := strconv.ParseFloat(strings.ReplaceAll(t, ",", "."), 64); err == nil {
					return int64(f)
				}
			}
		}
	}
	return 0
}
