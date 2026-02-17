package chat

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func appendProductLinks(answer string, products []SupabaseMatch) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(answer))
	b.WriteString("\n\nСсылки на товары:\n")
	for _, p := range products {
		b.WriteString("https://apache.iq-home.kz/products/")
		b.WriteString(strconv.FormatInt(p.ID, 10))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func joinProductIDs(products []SupabaseMatch, max int) string {
	if max <= 0 || len(products) == 0 {
		return ""
	}
	if len(products) < max {
		max = len(products)
	}
	parts := make([]string, 0, max)
	for i := 0; i < max; i++ {
		parts = append(parts, strconv.FormatInt(products[i].ID, 10))
	}
	return strings.Join(parts, ",")
}

func joinProductNames(products []SupabaseMatch, max int) string {
	if max <= 0 || len(products) == 0 {
		return ""
	}
	if len(products) < max {
		max = len(products)
	}
	parts := make([]string, 0, max)
	for i := 0; i < max; i++ {
		name := extractProductName(products[i])
		if name == "" {
			continue
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, " | ")
}

func collectProductIDs(products []SupabaseMatch) []int64 {
	if len(products) == 0 {
		return nil
	}
	out := make([]int64, 0, len(products))
	for _, p := range products {
		out = append(out, p.ID)
	}
	return out
}

func extractProductIDsFromHistory(history []chatMessageRow) []int64 {
	for i := len(history) - 1; i >= 0; i-- {
		meta := history[i].MetaData
		if meta == nil {
			continue
		}
		raw, ok := meta["product_ids"]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case []interface{}:
			ids := make([]int64, 0, len(v))
			for _, item := range v {
				switch t := item.(type) {
				case float64:
					ids = append(ids, int64(t))
				case int64:
					ids = append(ids, t)
				case json.Number:
					if n, err := t.Int64(); err == nil {
						ids = append(ids, n)
					}
				}
			}
			if len(ids) > 0 {
				return ids
			}
		case []int64:
			if len(v) > 0 {
				return v
			}
		}
	}
	return nil
}

func isFollowUpMessage(msg string) bool {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return false
	}
	words := strings.Fields(msg)
	return len(words) <= 3
}

func hasKPOffered(history []chatMessageRow) bool {
	for _, m := range history {
		if m.MetaData == nil {
			continue
		}
		if v, ok := m.MetaData["kp_offer"]; ok {
			if b, ok := v.(bool); ok && b {
				return true
			}
		}
	}
	return false
}

func hasRecentKPOffer(history []chatMessageRow) bool {
	if len(history) == 0 {
		return false
	}
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Role != "assistant" {
			continue
		}
		if m.MetaData == nil {
			return false
		}
		if v, ok := m.MetaData["kp_offer"]; ok {
			if b, ok := v.(bool); ok && b {
				return true
			}
		}
		return false
	}
	return false
}

func detectKpIntent(message string, history []chatMessageRow) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "кп") || strings.Contains(msg, "коммерческ") || strings.Contains(msg, "смет") || strings.Contains(msg, "счет") || strings.Contains(msg, "счёт") || strings.Contains(msg, "предложен") {
		return true
	}
	if hasRecentKPOffer(history) && isAffirmative(msg) && isShortYes(msg) {
		return true
	}
	return false
}

func isAffirmative(msg string) bool {
	if strings.Contains(msg, "не надо") || strings.Contains(msg, "не нужно") || strings.Contains(msg, "нет") {
		return false
	}
	keywords := []string{"да", "собери", "сделай", "давай", "хочу", "нужно", "оформи", "согласен"}
	for _, k := range keywords {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}

func isShortYes(msg string) bool {
	words := strings.Fields(msg)
	if len(words) == 0 {
		return false
	}
	if len(words) > 2 {
		return false
	}
	blockers := []string{
		"розет", "выключ", "кабель", "рамк", "цвет", "бел", "черн", "мокко", "антрац",
		"серия", "бренд", "цена", "штук", "шт", "нужн", "хочу",
	}
	for _, k := range blockers {
		if strings.Contains(msg, k) {
			return false
		}
	}
	return true
}

func mergeMeta(dst, src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return dst
	}
	if dst == nil {
		dst = map[string]interface{}{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func detectPingMessage(msg string) bool {
	m := strings.ToLower(strings.TrimSpace(msg))
	if m == "" {
		return false
	}
	pings := []string{"вы тут", "ты тут", "алло", "вы здесь", "ты здесь", "живы", "на связи"}
	for _, p := range pings {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

func detectAssortmentQuery(msg string) string {
	m := strings.ToLower(strings.TrimSpace(msg))
	if m == "" {
		return ""
	}
	if strings.Contains(m, "какие цвет") || strings.Contains(m, "какого цвет") {
		return "colors"
	}
	if strings.Contains(m, "какие брен") || strings.Contains(m, "какого брен") || strings.Contains(m, "какие марки") {
		return "brands"
	}
	if strings.Contains(m, "какие сери") || strings.Contains(m, "каких сери") {
		return "series"
	}
	if strings.Contains(m, "какие тип") || strings.Contains(m, "какого тип") || strings.Contains(m, "какие виды") {
		return "types"
	}
	return ""
}

func extractSlots(msg string) map[string]string {
	m := strings.ToLower(strings.TrimSpace(msg))
	if m == "" {
		return nil
	}
	slots := map[string]string{}
	colors := []string{"бел", "черн", "черный", "чёрн", "сер", "серый", "графит", "сереб", "золот", "мокко", "тауп", "сахар", "беж", "алюмин", "бронз", "антрац"}
	types := []string{"розет", "выключ", "рамк", "кабель", "вилк", "патрон", "люстр", "светиль", "реле", "автомат", "щит", "удлин", "диммер", "датчик", "шнур"}
	for _, c := range colors {
		if strings.Contains(m, c) {
			slots["last_color"] = c
			break
		}
	}
	for _, t := range types {
		if strings.Contains(m, t) {
			slots["last_product_type"] = t
			break
		}
	}
	if len(slots) == 0 {
		return nil
	}
	return slots
}

func isLikelyProductQuery(msg string) bool {
	m := strings.ToLower(strings.TrimSpace(msg))
	if m == "" {
		return false
	}
	productKeywords := []string{
		"розет", "выключ", "рамк", "кабель", "вилк", "патрон", "люстр", "светиль",
		"реле", "автомат", "щит", "удлин", "диммер", "датчик", "шнур",
	}
	for _, k := range productKeywords {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

func mergeSlots(dst, src map[string]string) map[string]string {
	if src == nil {
		return dst
	}
	if dst == nil {
		dst = map[string]string{}
	}
	for k, v := range src {
		if strings.TrimSpace(v) == "" {
			continue
		}
		dst[k] = v
	}
	return dst
}

func latestSummary(history []chatMessageRow) string {
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.MetaData == nil {
			continue
		}
		if v, ok := m.MetaData["summary"]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func latestSlots(history []chatMessageRow) map[string]string {
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.MetaData == nil {
			continue
		}
		if v, ok := m.MetaData["slots"]; ok {
			if m2, ok := v.(map[string]interface{}); ok {
				out := map[string]string{}
				for k, val := range m2 {
					out[k] = strings.TrimSpace(fmt.Sprintf("%v", val))
				}
				if len(out) > 0 {
					return out
				}
			}
		}
	}
	return nil
}

func shouldUpdateSummary(history []chatMessageRow, threshold int) bool {
	if threshold <= 0 {
		threshold = 6
	}
	lastSummaryIndex := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].MetaData != nil {
			if _, ok := history[i].MetaData["summary"]; ok {
				lastSummaryIndex = i
				break
			}
		}
	}
	if lastSummaryIndex == -1 {
		return len(history) >= threshold
	}
	return len(history)-1-lastSummaryIndex >= threshold
}

func toString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func parseIntDefault(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
