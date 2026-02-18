package chat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxMediaSize = 25 << 20

func (s *Service) HandleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxMediaSize)
	if err := r.ParseMultipartForm(maxMediaSize); err != nil {
		log.Printf("chat media: parse multipart failed: %v", err)
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	messageType := strings.TrimSpace(r.FormValue("message_type"))
	sessionID := strings.TrimSpace(r.FormValue("session_id"))
	userID := strings.TrimSpace(r.FormValue("user_id"))
	extraText := firstNonEmptyFormValue(r, "extra_text", "text", "message", "caption")
	matchCount := parseIntDefault(r.FormValue("match_count"), 5)
	topicFilter := strings.TrimSpace(r.FormValue("topic_filter"))

	file, fh, err := firstFormFile(r, "file", "media", "image", "photo", "document", "audio")
	if err != nil {
		log.Printf("chat media: file missing: %v", err)
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		log.Printf("chat media: file read failed: %v", err)
		http.Error(w, "file read failed", http.StatusBadRequest)
		return
	}

	contentType := fh.Header.Get("Content-Type")
	if contentType == "" {
		contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(fh.Filename)))
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if messageType == "" {
		messageType = detectMessageType(contentType, fh.Filename)
	}

	var message string
	userMeta := map[string]interface{}{}
	switch messageType {
	case "voice":
		log.Printf("chat media: voice file=%s size=%d mime=%s", fh.Filename, len(data), contentType)
		message, err = s.transcribeAudio(r.Context(), fh.Filename, contentType, data)
	case "photo":
		log.Printf("chat media: photo file=%s size=%d mime=%s", fh.Filename, len(data), contentType)
		var signal photoProductSignal
		signal, err = s.analyzeImageForProduct(r.Context(), contentType, data)
		if err != nil {
			log.Printf("chat media: product signal failed, fallback OCR err=%v", err)
			message, err = s.analyzeImage(r.Context(), contentType, data)
		} else {
			message = buildPhotoProductSearchMessage(signal)
		}
	case "document":
		log.Printf("chat media: document file=%s size=%d mime=%s", fh.Filename, len(data), contentType)
		message, err = s.extractDocumentText(r.Context(), fh.Filename, contentType, data)
		if err == nil && isLikelyQuoteDocument(fh.Filename, message) {
			userMeta["incoming_quote_pdf"] = true
			message = buildDocumentProductSearchMessage(message)
		}
	default:
		log.Printf("chat media: unsupported message_type=%s", messageType)
		http.Error(w, "message_type must be voice, photo, or document", http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("chat media: processing failed type=%s err=%v", messageType, err)
		http.Error(w, "media processing failed", http.StatusBadGateway)
		return
	}
	if extraText != "" {
		if strings.TrimSpace(message) != "" {
			message = strings.TrimSpace(message) + "\n" + extraText
		} else {
			message = extraText
		}
	}

	var topicPtr *string
	if topicFilter != "" {
		topicPtr = &topicFilter
	}
	var userPtr *string
	if userID != "" {
		userPtr = &userID
	}

	req := ChatRequest{
		Message:     message,
		SessionID:   sessionID,
		UserID:      userPtr,
		UserMeta:    userMeta,
		MatchCount:  matchCount,
		TopicFilter: topicPtr,
	}
	log.Printf("chat media: forwarding to chat session_id=%s user_id=%v msg_len=%d", sessionID, userID != "", len(message))
	s.handleMessage(w, r, req)
}

type photoProductSignal struct {
	ProductType string   `json:"product_type"`
	Brand       string   `json:"brand"`
	Series      string   `json:"series"`
	Color       string   `json:"color"`
	Article     string   `json:"article"`
	Keywords    []string `json:"keywords"`
	Detected    bool     `json:"detected"`
}

func (s *Service) transcribeAudio(ctx context.Context, filename, contentType string, data []byte) (string, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	if err := writer.WriteField("model", s.Cfg.OpenAITranscribeModel); err != nil {
		return "", err
	}
	if err := writer.WriteField("response_format", "json"); err != nil {
		return "", err
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	urlStr := strings.TrimRight(s.Cfg.OpenAIBaseURL, "/") + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+s.Cfg.OpenAIAPIKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		log.Printf("chat media: transcribe request failed: %v", err)
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("chat media: transcribe status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(msg)))
		return "", fmt.Errorf("openai status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Text), nil
}

func (s *Service) analyzeImage(ctx context.Context, contentType string, data []byte) (string, error) {
	encoded := base64.StdEncoding.EncodeToString(data)
	dataURL := "data:" + contentType + ";base64," + encoded

	system := "Опиши изображение кратко. Затем извлеки весь видимый текст (OCR). Ответ в формате: ОПИСАНИЕ: ...\\nТЕКСТ: ..."

	payload := map[string]interface{}{
		"model": s.Cfg.OpenAIVisionModel,
		"messages": []map[string]interface{}{
			{
				"role":    "system",
				"content": system,
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Проанализируй изображение."},
					{"type": "image_url", "image_url": map[string]interface{}{"url": dataURL}},
				},
			},
		},
		"max_completion_tokens": 300,
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
		log.Printf("chat media: vision request failed: %v", err)
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("chat media: vision status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(msg)))
		return "", fmt.Errorf("openai status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("empty openai response")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func (s *Service) analyzeImageForProduct(ctx context.Context, contentType string, data []byte) (photoProductSignal, error) {
	encoded := base64.StdEncoding.EncodeToString(data)
	dataURL := "data:" + contentType + ";base64," + encoded

	system := "Ты анализируешь фото товара электрофурнитуры. Верни строго JSON без пояснений: {\"detected\":bool,\"product_type\":\"\",\"brand\":\"\",\"series\":\"\",\"color\":\"\",\"article\":\"\",\"keywords\":[\"...\"]}. Если не уверен, оставляй пустые строки и detected=false."
	payload := map[string]interface{}{
		"model": s.Cfg.OpenAIVisionModel,
		"messages": []map[string]interface{}{
			{
				"role":    "system",
				"content": system,
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Определи товар и его признаки."},
					{"type": "image_url", "image_url": map[string]interface{}{"url": dataURL}},
				},
			},
		},
		"max_completion_tokens": 220,
		"response_format":       map[string]interface{}{"type": "json_object"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return photoProductSignal{}, err
	}

	urlStr := strings.TrimRight(s.Cfg.OpenAIBaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return photoProductSignal{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Cfg.OpenAIAPIKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return photoProductSignal{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return photoProductSignal{}, fmt.Errorf("openai status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return photoProductSignal{}, err
	}
	if len(out.Choices) == 0 {
		return photoProductSignal{}, fmt.Errorf("empty openai response")
	}

	raw := stripCodeFences(strings.TrimSpace(out.Choices[0].Message.Content))
	var signal photoProductSignal
	if err := json.Unmarshal([]byte(raw), &signal); err != nil {
		return photoProductSignal{}, err
	}
	signal.Article = normalizeArticle(signal.Article)
	return signal, nil
}

func buildPhotoProductSearchMessage(signal photoProductSignal) string {
	parts := []string{}
	if signal.Article != "" {
		parts = append(parts, "артикул "+signal.Article)
	}
	if signal.Brand != "" {
		parts = append(parts, "бренд "+signal.Brand)
	}
	if signal.Series != "" {
		parts = append(parts, "серия "+signal.Series)
	}
	if signal.ProductType != "" {
		parts = append(parts, "тип "+signal.ProductType)
	}
	if signal.Color != "" {
		parts = append(parts, "цвет "+signal.Color)
	}
	parts = append(parts, signal.Keywords...)
	query := strings.TrimSpace(strings.Join(parts, " "))
	if query == "" {
		return "Пользователь прислал фото товара. Найди максимально похожий товар в базе по внешнему виду и названию."
	}
	return "Пользователь прислал фото товара. Найди максимально похожий товар в базе. Запрос: " + query
}

func normalizeArticle(raw string) string {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	re := regexp.MustCompile(`[^A-Z0-9\-]`)
	return re.ReplaceAllString(raw, "")
}

func isLikelyQuoteDocument(filename, text string) bool {
	src := strings.ToLower(filename + "\n" + text)
	keywords := []string{"кп", "коммерческ", "предложен", "счет", "счёт", "артикул", "qty", "кол-во", "цена"}
	hits := 0
	for _, kw := range keywords {
		if strings.Contains(src, kw) {
			hits++
		}
	}
	return hits >= 2
}

func buildDocumentProductSearchMessage(text string) string {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return "Пользователь прислал КП. Подбери товары из каталога по позициям документа."
	}
	articles := extractDocumentArticles(raw, 20)
	words := extractDocumentProductWords(raw, 20)
	parts := make([]string, 0, len(articles)+len(words))
	parts = append(parts, articles...)
	parts = append(parts, words...)
	if len(parts) == 0 {
		if len(raw) > 1200 {
			raw = raw[:1200]
		}
		return "Пользователь прислал КП. Подбери товары из каталога по позициям: " + raw
	}
	return "Пользователь прислал КП. Подбери товары из каталога по позициям: " + strings.Join(parts, " ")
}

func extractDocumentArticles(text string, max int) []string {
	re := regexp.MustCompile(`(?i)\b[а-яa-z]{0,4}\d{3,}[a-zа-я0-9\-]{0,8}\b`)
	matches := re.FindAllString(text, -1)
	seen := map[string]struct{}{}
	out := make([]string, 0, max)
	for _, m := range matches {
		m = normalizeArticle(m)
		if len(m) < 4 {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
		if len(out) >= max {
			break
		}
	}
	return out
}

func extractDocumentProductWords(text string, max int) []string {
	dict := []string{
		"розетка", "выключатель", "рамка", "диммер", "переключатель", "tv", "rj45", "rj11",
		"белый", "черный", "антрацит", "мокко", "тауп", "алюминий", "бронза",
		"jasmart", "fd-серия", "g-серия", "fs-серия",
	}
	src := strings.ToLower(text)
	seen := map[string]struct{}{}
	out := make([]string, 0, max)
	for _, w := range dict {
		if strings.Contains(src, w) {
			if _, ok := seen[w]; ok {
				continue
			}
			seen[w] = struct{}{}
			out = append(out, w)
		}
	}
	sort.Strings(out)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func firstNonEmptyFormValue(r *http.Request, keys ...string) string {
	for _, k := range keys {
		v := strings.TrimSpace(r.FormValue(k))
		if v != "" {
			return v
		}
	}
	return ""
}

func firstFormFile(r *http.Request, keys ...string) (multipart.File, *multipart.FileHeader, error) {
	var lastErr error
	for _, k := range keys {
		f, fh, err := r.FormFile(k)
		if err == nil {
			return f, fh, nil
		}
		lastErr = err
	}
	return nil, nil, lastErr
}

func detectMessageType(contentType, filename string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(filename)))
	if strings.HasPrefix(ct, "image/") || ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" {
		return "photo"
	}
	if strings.HasPrefix(ct, "audio/") || ext == ".ogg" || ext == ".mp3" || ext == ".wav" || ext == ".m4a" {
		return "voice"
	}
	return "document"
}

func (s *Service) extractDocumentText(ctx context.Context, filename, contentType string, data []byte) (string, error) {
	if strings.TrimSpace(s.Cfg.TikaURL) == "" {
		return "", fmt.Errorf("TIKA_URL is not configured")
	}
	urlStr := strings.TrimRight(s.Cfg.TikaURL, "/") + "/tika"
	resp, methodUsed, err := s.callTika(ctx, urlStr, contentType, data)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	log.Printf("chat media: tika ok method=%s status=%d", methodUsed, resp.StatusCode)

	text, err := io.ReadAll(io.LimitReader(resp.Body, 20000))
	if err != nil {
		return "", err
	}
	clean := strings.TrimSpace(string(text))
	if clean == "" {
		return "", fmt.Errorf("empty document text")
	}
	return "Документ: " + filename + "\n\n" + clean, nil
}

func (s *Service) callTika(ctx context.Context, urlStr, contentType string, data []byte) (*http.Response, string, error) {
	methods := []string{http.MethodPut, http.MethodPost}
	var lastErr error
	for _, method := range methods {
		req, err := http.NewRequestWithContext(ctx, method, urlStr, bytes.NewReader(data))
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("Content-Type", contentType)

		resp, err := s.HTTP.Do(req)
		if err != nil {
			log.Printf("chat media: tika request failed method=%s err=%v", method, err)
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusMethodNotAllowed {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			log.Printf("chat media: tika status=405 method=%s body=%s", method, strings.TrimSpace(string(msg)))
			resp.Body.Close()
			lastErr = fmt.Errorf("tika status 405")
			continue
		}
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()
			return nil, method, fmt.Errorf("tika status %d (%s): %s", resp.StatusCode, method, strings.TrimSpace(string(msg)))
		}
		return resp, method, nil
	}
	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", fmt.Errorf("tika request failed")
}
