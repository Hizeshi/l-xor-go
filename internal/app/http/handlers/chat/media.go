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
	extraText := strings.TrimSpace(r.FormValue("extra_text"))
	matchCount := parseIntDefault(r.FormValue("match_count"), 5)
	topicFilter := strings.TrimSpace(r.FormValue("topic_filter"))

	file, fh, err := r.FormFile("file")
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

	var message string
	switch messageType {
	case "voice":
		log.Printf("chat media: voice file=%s size=%d mime=%s", fh.Filename, len(data), contentType)
		message, err = s.transcribeAudio(r.Context(), fh.Filename, contentType, data)
	case "photo":
		log.Printf("chat media: photo file=%s size=%d mime=%s", fh.Filename, len(data), contentType)
		message, err = s.analyzeImage(r.Context(), contentType, data)
	case "document":
		log.Printf("chat media: document file=%s size=%d mime=%s", fh.Filename, len(data), contentType)
		message, err = s.extractDocumentText(r.Context(), fh.Filename, contentType, data)
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
		MatchCount:  matchCount,
		TopicFilter: topicPtr,
	}
	log.Printf("chat media: forwarding to chat session_id=%s user_id=%v msg_len=%d", sessionID, userID != "", len(message))
	s.handleMessage(w, r, req)
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

func (s *Service) extractDocumentText(ctx context.Context, filename, contentType string, data []byte) (string, error) {
	if strings.TrimSpace(s.Cfg.TikaURL) == "" {
		return "", fmt.Errorf("TIKA_URL is not configured")
	}
	urlStr := strings.TrimRight(s.Cfg.TikaURL, "/") + "/tika"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		log.Printf("chat media: tika request failed: %v", err)
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("chat media: tika status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(msg)))
		return "", fmt.Errorf("tika status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
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
