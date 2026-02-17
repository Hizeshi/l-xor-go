package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func (s *Service) decideProductSearch(ctx context.Context, userMessage string) (bool, error) {
	system := "Ты определяешь, нужно ли искать товары. Отвечай строго JSON без пояснений. Формат: {\"need_products\": true|false}. true — если пользователь явно просит подобрать/показать/найти/купить товар, цену или характеристики. false — если просит только консультацию или инструкцию."
	prompt := "Сообщение клиента: " + userMessage

	payload := openAIChatRequest{
		Model: s.Cfg.OpenAIModel,
		Messages: []openAIChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: prompt},
		},
		MaxTokens:      20,
		ResponseFormat: &openAIResponseFormat{Type: "json_object"},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return true, err
	}

	urlStr := strings.TrimRight(s.Cfg.OpenAIBaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return true, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Cfg.OpenAIAPIKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return true, fmt.Errorf("openai status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return true, err
	}
	if len(out.Choices) == 0 {
		return true, errors.New("empty openai response")
	}

	content := strings.TrimSpace(out.Choices[0].Message.Content)
	content = stripCodeFences(content)

	var decision productDecision
	if err := json.Unmarshal([]byte(content), &decision); err != nil {
		return true, fmt.Errorf("invalid decision json: %w", err)
	}
	return decision.NeedProducts, nil
}

func (s *Service) callOpenAI(ctx context.Context, userMessage string, history []chatMessageRow, products []SupabaseMatch, knowledge []SupabaseMatch) (string, error) {
	contextText := buildContext(history, products, knowledge)

	system := "Ты — консультант по электрофурнитуре. Отвечай коротко (2–4 предложения). Никогда не выдумывай товары, бренды, модели или характеристики. Используй только то, что есть в списке \"Товары\" в контексте. Если товаров нет — так и скажи и задай 1 уточняющий вопрос. Не повторяй вопросы. Не навязывай доп. функции. Все цены указывай в тенге (₸), не упоминай рубли. Если в контексте есть раздел \"Правило\", следуй ему строго. Если вопрос про связь/проверку присутствия (\"вы тут?\", \"алло?\") — ответь кратко без ссылок и без новых предложений. Если пользователь уточняет конкретику — не меняй тему и не предлагай новые товары."

	prompt := "Вопрос клиента: " + userMessage + "\n\nКонтекст:\n" + contextText

	payload := openAIChatRequest{
		Model: s.Cfg.OpenAIModel,
		Messages: []openAIChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: prompt},
		},
		MaxTokens: 350,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	urlStr := strings.TrimRight(s.Cfg.OpenAIBaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Cfg.OpenAIAPIKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("openai status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", errors.New("empty openai response")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}

	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToLower(s), "json") {
		s = strings.TrimSpace(s[4:])
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func (s *Service) summarizeHistory(ctx context.Context, history []chatMessageRow, lastAnswer string) (string, error) {
	prev := latestSummary(history)
	var b strings.Builder
	if prev != "" {
		b.WriteString("Предыдущая сводка:\n")
		b.WriteString(prev)
		b.WriteString("\n\n")
	}
	b.WriteString("Последние сообщения:\n")
	start := 0
	if len(history) > 10 {
		start = len(history) - 10
	}
	for _, m := range history[start:] {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "assistant" {
			b.WriteString("Ассистент: ")
		} else {
			b.WriteString("Пользователь: ")
		}
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	if strings.TrimSpace(lastAnswer) != "" {
		b.WriteString("Ассистент (новый ответ): ")
		b.WriteString(strings.TrimSpace(lastAnswer))
		b.WriteString("\n")
	}

	system := "Сделай краткую сводку диалога в 3-6 строках. Формат: \n- Пользователь ищет: ...\n- Требования: ...\n- Контекст/договоренности: ...\nСводка должна быть лаконичной."
	prompt := b.String()

	payload := openAIChatRequest{
		Model: s.Cfg.OpenAIModel,
		Messages: []openAIChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: prompt},
		},
		MaxTokens: 200,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	urlStr := strings.TrimRight(s.Cfg.OpenAIBaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Cfg.OpenAIAPIKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("openai status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", errors.New("empty openai response")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
