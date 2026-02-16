package chat

import "strings"

func buildContext(history []chatMessageRow, products []SupabaseMatch, knowledge []SupabaseMatch) string {
	var b strings.Builder

	if len(history) == 0 {
		b.WriteString("История: нет.\n")
	} else {
		b.WriteString("История:\n")
		for _, m := range history {
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
