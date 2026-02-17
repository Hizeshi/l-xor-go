package chat

import "strings"

func buildContext(history []chatMessageRow, products []SupabaseMatch, knowledge []SupabaseMatch) string {
	var b strings.Builder

	summary := latestSummary(history)
	slots := latestSlots(history)
	if summary != "" {
		b.WriteString("Сводка:\n")
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	if len(slots) > 0 {
		b.WriteString("Текущие предпочтения: ")
		parts := make([]string, 0, len(slots))
		if v := strings.TrimSpace(slots["last_product_type"]); v != "" {
			parts = append(parts, "тип="+v)
		}
		if v := strings.TrimSpace(slots["last_color"]); v != "" {
			parts = append(parts, "цвет="+v)
		}
		if v := strings.TrimSpace(slots["last_series"]); v != "" {
			parts = append(parts, "серия="+v)
		}
		if v := strings.TrimSpace(slots["last_room"]); v != "" {
			parts = append(parts, "комната="+v)
		}
		if v := strings.TrimSpace(slots["last_intent"]); v != "" {
			parts = append(parts, "намерение="+v)
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString("\n\n")
	}

	if len(history) == 0 {
		b.WriteString("История: нет.\n")
	} else {
		b.WriteString("История:\n")
		start := 0
		if len(history) > 15 {
			start = len(history) - 15
		}
		for _, m := range history[start:] {
			role := strings.ToLower(strings.TrimSpace(m.Role))
			switch role {
			case "assistant":
				b.WriteString("Ассистент: ")
			default:
				b.WriteString("Пользователь: ")
			}
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(products) == 0 {
		b.WriteString("Товары: не найдено.\n")
	} else {
		b.WriteString("Товары:\n")
		for i, p := range products {
			if i >= 5 {
				break
			}
			b.WriteString("- ")
			b.WriteString(p.Content)
			if p.Metadata != nil {
				if price, ok := p.Metadata["price"]; ok {
					b.WriteString(" | Цена: ")
					b.WriteString(toString(price))
				}
				if brand, ok := p.Metadata["brand"]; ok {
					b.WriteString(" | Бренд: ")
					b.WriteString(toString(brand))
				}
			}
			b.WriteString("\n")
		}
	}

	if len(knowledge) == 0 {
		b.WriteString("\nМетодички: не найдено.\n")
	} else {
		b.WriteString("\nПравило:\n")
		k := knowledge[0]
		if k.Metadata != nil {
			if topic, ok := k.Metadata["topic"]; ok {
				b.WriteString(toString(topic))
				b.WriteString("\n")
			}
		}
		b.WriteString(k.Content)
		b.WriteString("\n")
	}

	return b.String()
}
