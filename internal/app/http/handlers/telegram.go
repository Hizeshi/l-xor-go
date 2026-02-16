package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"iq-home/go_beckend/internal/app/http/handlers/chat"
)

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message,omitempty"`
}

type telegramMessage struct {
	MessageID int64             `json:"message_id"`
	From      *telegramUser     `json:"from,omitempty"`
	Chat      telegramChat      `json:"chat"`
	Text      string            `json:"text,omitempty"`
	Voice     *telegramVoice    `json:"voice,omitempty"`
	Photo     []telegramPhoto   `json:"photo,omitempty"`
	Document  *telegramDocument `json:"document,omitempty"`
}

type telegramUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

type telegramChat struct {
	ID int64 `json:"id"`
}

type telegramVoice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration,omitempty"`
}

type telegramPhoto struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

type telegramDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

type telegramGetFileResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		FilePath string `json:"file_path"`
	} `json:"result"`
}

func (h *Handlers) TelegramWebhook(w http.ResponseWriter, r *http.Request) {
	if h.Cfg.TelegramBotToken == "" {
		http.Error(w, "telegram not configured", http.StatusBadRequest)
		return
	}
	var upd telegramUpdate
	if err := json.NewDecoder(r.Body).Decode(&upd); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if upd.Message == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	msg := upd.Message
	chatID := msg.Chat.ID
	sessionID := fmt.Sprintf("tg:%d", chatID)
	userID := ""
	if msg.From != nil {
		userID = fmt.Sprintf("%d", msg.From.ID)
	}
	go h.sendTelegramAction(r.Context(), chatID, "typing")

	switch {
	case strings.TrimSpace(msg.Text) != "":
		log.Printf("telegram: text received chat_id=%d len=%d", chatID, len(msg.Text))
		h.enqueueTelegramText(sessionID, userID, msg.Text)
		w.WriteHeader(http.StatusOK)
		return
	case msg.Voice != nil:
		log.Printf("telegram: voice received chat_id=%d file_id=%s duration=%d", chatID, msg.Voice.FileID, msg.Voice.Duration)
		h.enqueueTelegramMedia(r.Context(), sessionID, userID, "voice", msg.Voice.FileID, "voice.ogg", "")
		w.WriteHeader(http.StatusOK)
		return
	case msg.Document != nil:
		log.Printf("telegram: document received chat_id=%d file_id=%s name=%s mime=%s", chatID, msg.Document.FileID, msg.Document.FileName, msg.Document.MimeType)
		h.enqueueTelegramMedia(r.Context(), sessionID, userID, "document", msg.Document.FileID, msg.Document.FileName, msg.Document.MimeType)
		w.WriteHeader(http.StatusOK)
		return
	case len(msg.Photo) > 0:
		photo := msg.Photo[len(msg.Photo)-1]
		log.Printf("telegram: photo received chat_id=%d file_id=%s size=%dx%d", chatID, photo.FileID, photo.Width, photo.Height)
		h.enqueueTelegramMedia(r.Context(), sessionID, userID, "photo", photo.FileID, "photo.jpg", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		return
	default:
		log.Printf("telegram: message ignored chat_id=%d", chatID)
		w.WriteHeader(http.StatusOK)
		return
	}
}

func (h *Handlers) handleTelegramText(w http.ResponseWriter, r *http.Request, sessionID, userID, text string) {
	h.processTelegramText(r.Context(), sessionID, userID, text)
	w.WriteHeader(http.StatusOK)
}

func (h *Handlers) handleTelegramFile(w http.ResponseWriter, r *http.Request, sessionID, userID, messageType, fileID, fileName, mimeType string) {
	data, name, ct, err := h.telegramDownloadFile(r.Context(), fileID, fileName, mimeType)
	if err != nil {
		log.Printf("telegram: download failed session_id=%s type=%s file_id=%s err=%v", sessionID, messageType, fileID, err)
		h.sendTelegramText(r.Context(), sessionID, "Не удалось загрузить файл.")
		w.WriteHeader(http.StatusOK)
		return
	}

	h.processTelegramMedia(r.Context(), sessionID, userID, messageType, name, ct, data, "")
	w.WriteHeader(http.StatusOK)
}

func (h *Handlers) processTelegramText(ctx context.Context, sessionID, userID, text string) {
	rec := newResponseRecorder()
	req := chat.ChatRequest{
		Message:   text,
		SessionID: sessionID,
		UserID:    strPtr(userID),
	}
	buf, _ := json.Marshal(req)
	r2 := &http.Request{}
	r2 = r2.WithContext(ctx)
	r2.Method = http.MethodPost
	r2.Header = make(http.Header)
	r2.Header.Set("Content-Type", "application/json")
	r2.Body = io.NopCloser(bytes.NewReader(buf))
	r2.ContentLength = int64(len(buf))
	chat.New(h.Cfg, h.HTTP).Handle(rec, r2)

	if rec.status != http.StatusOK {
		h.sendTelegramText(ctx, sessionID, "Ошибка обработки запроса.")
		return
	}

	if strings.HasPrefix(rec.header.Get("Content-Type"), "application/pdf") {
		log.Printf("telegram: sending pdf session_id=%s bytes=%d", sessionID, rec.body.Len())
		h.sendTelegramDocument(ctx, sessionID, "KP.pdf", rec.body.Bytes())
		return
	}

	var out chat.ChatResponse
	if err := json.Unmarshal(rec.body.Bytes(), &out); err != nil {
		h.sendTelegramText(ctx, sessionID, "Ошибка ответа.")
		return
	}
	if strings.TrimSpace(out.Answer) != "" {
		h.sendTelegramText(ctx, sessionID, out.Answer)
	}
}

func (h *Handlers) processTelegramMedia(ctx context.Context, sessionID, userID, messageType, filename, contentType string, data []byte, extraText string) {
	rec := newResponseRecorder()
	req := &http.Request{Method: http.MethodPost, Header: make(http.Header)}
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "multipart/form-data")

	body, ct := buildMultipart(messageType, sessionID, userID, filename, contentType, data, extraText)
	req.Body = io.NopCloser(body)
	req.ContentLength = int64(body.Len())
	req.Header.Set("Content-Type", ct)

	chat.New(h.Cfg, h.HTTP).HandleMedia(rec, req)
	if rec.status != http.StatusOK {
		log.Printf("telegram: media failed session_id=%s type=%s status=%d body=%s", sessionID, messageType, rec.status, strings.TrimSpace(rec.body.String()))
		h.sendTelegramText(ctx, sessionID, "Ошибка обработки файла.")
		return
	}

	if strings.HasPrefix(rec.header.Get("Content-Type"), "application/pdf") {
		h.sendTelegramDocument(ctx, sessionID, "result.pdf", rec.body.Bytes())
		return
	}

	var out chat.ChatResponse
	if err := json.Unmarshal(rec.body.Bytes(), &out); err != nil {
		log.Printf("telegram: media response parse failed session_id=%s type=%s err=%v body=%s", sessionID, messageType, err, strings.TrimSpace(rec.body.String()))
		h.sendTelegramText(ctx, sessionID, "Ошибка ответа.")
		return
	}
	if strings.TrimSpace(out.Answer) != "" {
		h.sendTelegramText(ctx, sessionID, out.Answer)
	}
}

func (h *Handlers) enqueueTelegramText(sessionID, userID, text string) {
	h.tgBuffer.AddText(sessionID, userID, text, func(p telegramPending) {
		h.processTelegramPending(p)
	})
}

func (h *Handlers) enqueueTelegramMedia(ctx context.Context, sessionID, userID, messageType, fileID, fileName, mimeType string) {
	data, name, ct, err := h.telegramDownloadFile(ctx, fileID, fileName, mimeType)
	if err != nil {
		log.Printf("telegram: download failed session_id=%s type=%s file_id=%s err=%v", sessionID, messageType, fileID, err)
		h.sendTelegramText(ctx, sessionID, "Не удалось загрузить файл.")
		return
	}
	media := telegramPendingMedia{
		messageType: messageType,
		filename:    name,
		contentType: ct,
		data:        data,
	}
	h.tgBuffer.AddMedia(sessionID, userID, media, func(p telegramPending) {
		h.processTelegramPending(p)
	})
}

func (h *Handlers) processTelegramPending(p telegramPending) {
	ctx := context.Background()
	if p.media != nil {
		extraText := strings.TrimSpace(strings.Join(p.texts, "\n"))
		h.processTelegramMedia(ctx, p.sessionID, p.userID, p.media.messageType, p.media.filename, p.media.contentType, p.media.data, extraText)
		return
	}
	if len(p.texts) > 0 {
		combined := strings.TrimSpace(strings.Join(p.texts, "\n"))
		if combined != "" {
			h.processTelegramText(ctx, p.sessionID, p.userID, combined)
		}
	}
}

func (h *Handlers) telegramDownloadFile(ctx context.Context, fileID, fallbackName, fallbackType string) ([]byte, string, string, error) {
	base := strings.TrimRight(h.Cfg.TelegramBaseURL, "/")
	getFileURL := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", base, h.Cfg.TelegramBotToken, fileID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getFileURL, nil)
	if err != nil {
		log.Printf("telegram: getFile request build failed file_id=%s err=%v", fileID, err)
		return nil, "", "", err
	}
	resp, err := h.HTTP.Do(req)
	if err != nil {
		log.Printf("telegram: getFile request failed file_id=%s err=%v", fileID, err)
		return nil, "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("telegram: getFile status=%d file_id=%s body=%s", resp.StatusCode, fileID, strings.TrimSpace(string(msg)))
		return nil, "", "", fmt.Errorf("telegram status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out telegramGetFileResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", "", err
	}
	if !out.OK || out.Result.FilePath == "" {
		return nil, "", "", fmt.Errorf("telegram getFile empty")
	}

	fileURL := fmt.Sprintf("%s/file/bot%s/%s", base, h.Cfg.TelegramBotToken, out.Result.FilePath)
	fileReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	fileResp, err := h.HTTP.Do(fileReq)
	if err != nil {
		return nil, "", "", err
	}
	defer fileResp.Body.Close()

	if fileResp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(fileResp.Body, 2048))
		return nil, "", "", fmt.Errorf("telegram file status %d: %s", fileResp.StatusCode, strings.TrimSpace(string(msg)))
	}

	data, err := io.ReadAll(fileResp.Body)
	if err != nil {
		return nil, "", "", err
	}
	name := fallbackName
	if name == "" {
		name = filepath.Base(out.Result.FilePath)
	}
	ct := fallbackType
	if ct == "" {
		ct = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	}
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	return data, name, ct, nil
}

func (h *Handlers) sendTelegramText(ctx context.Context, sessionID, text string) {
	if text == "" {
		return
	}
	base := strings.TrimRight(h.Cfg.TelegramBaseURL, "/")
	urlStr := fmt.Sprintf("%s/bot%s/sendMessage", base, h.Cfg.TelegramBotToken)
	payload := map[string]interface{}{
		"chat_id": sessionID[3:],
		"text":    text,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.HTTP.Do(req)
	if err != nil {
		log.Printf("telegram: sendMessage failed session_id=%s err=%v", sessionID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("telegram: sendMessage status=%d session_id=%s body=%s", resp.StatusCode, sessionID, strings.TrimSpace(string(msg)))
	}
}

func (h *Handlers) sendTelegramAction(ctx context.Context, chatID int64, action string) {
	if action == "" {
		return
	}
	base := strings.TrimRight(h.Cfg.TelegramBaseURL, "/")
	urlStr := fmt.Sprintf("%s/bot%s/sendChatAction", base, h.Cfg.TelegramBotToken)
	payload := map[string]interface{}{
		"chat_id": chatID,
		"action":  action,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.HTTP.Do(req)
	if err != nil {
		log.Printf("telegram: sendChatAction failed chat_id=%d err=%v", chatID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("telegram: sendChatAction status=%d chat_id=%d body=%s", resp.StatusCode, chatID, strings.TrimSpace(string(msg)))
	}
}

func (h *Handlers) sendTelegramDocument(ctx context.Context, sessionID, filename string, data []byte) {
	base := strings.TrimRight(h.Cfg.TelegramBaseURL, "/")
	urlStr := fmt.Sprintf("%s/bot%s/sendDocument", base, h.Cfg.TelegramBotToken)
	chatID := ""
	if strings.HasPrefix(sessionID, "tg:") {
		chatID = sessionID[3:]
	}
	body, contentType := buildTelegramDocumentMultipart(chatID, filename, "application/pdf", data)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, body)
	req.Header.Set("Content-Type", contentType)
	resp, err := h.HTTP.Do(req)
	if err != nil {
		log.Printf("telegram: sendDocument failed session_id=%s err=%v", sessionID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("telegram: sendDocument status=%d session_id=%s body=%s", resp.StatusCode, sessionID, strings.TrimSpace(string(msg)))
	}
}

func strPtr(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return &v
}

type responseRecorder struct {
	header http.Header
	body   *bytes.Buffer
	status int
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{
		header: make(http.Header),
		body:   &bytes.Buffer{},
		status: http.StatusOK,
	}
}

func (r *responseRecorder) Header() http.Header         { return r.header }
func (r *responseRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *responseRecorder) WriteHeader(statusCode int)  { r.status = statusCode }

func buildMultipart(messageType, sessionID, userID, filename, contentType string, data []byte, extraText string) (*bytes.Buffer, string) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("message_type", messageType)
	_ = writer.WriteField("session_id", sessionID)
	if userID != "" {
		_ = writer.WriteField("user_id", userID)
	}
	if strings.TrimSpace(extraText) != "" {
		_ = writer.WriteField("extra_text", extraText)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	header.Set("Content-Type", contentType)
	part, _ := writer.CreatePart(header)
	_, _ = part.Write(data)
	_ = writer.Close()
	return body, writer.FormDataContentType()
}

func buildTelegramDocumentMultipart(chatID, filename, contentType string, data []byte) (*bytes.Buffer, string) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if chatID != "" {
		_ = writer.WriteField("chat_id", chatID)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="document"; filename="%s"`, filename))
	header.Set("Content-Type", contentType)
	part, _ := writer.CreatePart(header)
	_, _ = part.Write(data)
	_ = writer.Close()
	return body, writer.FormDataContentType()
}

type managerMessage struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
}

func (h *Handlers) startManagerRelay() {
	if h.Cfg.SupabaseURL == "" || h.Cfg.SupabaseServiceRoleKey == "" {
		log.Printf("telegram relay: supabase not configured, skipping")
		return
	}
	if h.Cfg.TelegramBotToken == "" {
		log.Printf("telegram relay: telegram not configured, skipping")
		return
	}
	go func() {
		ctx := context.Background()
		lastID, err := h.fetchLatestManagerMessageID(ctx)
		if err != nil {
			log.Printf("telegram relay: init failed: %v", err)
		}
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			msgs, err := h.fetchManagerMessagesSince(ctx, lastID)
			if err != nil {
				log.Printf("telegram relay: poll failed: %v", err)
				continue
			}
			if len(msgs) > 0 {
				log.Printf("telegram relay: fetched %d manager messages since id=%d", len(msgs), lastID)
			}
			for _, m := range msgs {
				if strings.HasPrefix(m.SessionID, "tg:") && strings.TrimSpace(m.Content) != "" {
					humanMode, err := h.fetchSessionHumanMode(ctx, m.SessionID)
					if err != nil {
						log.Printf("telegram relay: human mode check failed session_id=%s err=%v", m.SessionID, err)
					}
					if humanMode {
						log.Printf("telegram relay: forward session_id=%s msg_id=%d", m.SessionID, m.ID)
						h.sendTelegramText(ctx, m.SessionID, m.Content)
					} else {
						log.Printf("telegram relay: skip session_id=%s msg_id=%d human_mode=false", m.SessionID, m.ID)
					}
				} else {
					log.Printf("telegram relay: skip session_id=%s msg_id=%d", m.SessionID, m.ID)
				}
				if m.ID > lastID {
					lastID = m.ID
				}
			}
		}
	}()
}

func (h *Handlers) fetchLatestManagerMessageID(ctx context.Context) (int64, error) {
	msgs, err := h.fetchManagerMessages(ctx, 0, 1, true)
	if err != nil {
		return 0, err
	}
	if len(msgs) == 0 {
		return 0, nil
	}
	return msgs[0].ID, nil
}

func (h *Handlers) fetchManagerMessagesSince(ctx context.Context, lastID int64) ([]managerMessage, error) {
	return h.fetchManagerMessages(ctx, lastID, 100, false)
}

func (h *Handlers) fetchManagerMessages(ctx context.Context, lastID int64, limit int, desc bool) ([]managerMessage, error) {
	values := url.Values{}
	values.Set("select", "id,session_id,content")
	values.Set("order", "id.asc")
	if desc {
		values.Set("order", "id.desc")
	}
	if lastID > 0 {
		values.Set("id", fmt.Sprintf("gt.%d", lastID))
	}
	values.Set("limit", strconv.Itoa(limit))
	values.Set("or", "(sender_type.eq.manager,sender_type.eq.human_admin,role.eq.manager)")

	urlStr := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/rest/v1/chat_messages?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+h.Cfg.SupabaseServiceRoleKey)

	resp, err := h.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out []managerMessage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *Handlers) fetchSessionHumanMode(ctx context.Context, sessionID string) (bool, error) {
	values := url.Values{}
	values.Set("select", "is_human_mode")
	values.Set("session_id", "eq."+sessionID)
	values.Set("limit", "1")

	urlStr := strings.TrimRight(h.Cfg.SupabaseURL, "/") + "/rest/v1/chat_sessions?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("apikey", h.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+h.Cfg.SupabaseServiceRoleKey)

	resp, err := h.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return false, fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var rows []struct {
		IsHumanMode bool `json:"is_human_mode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	return rows[0].IsHumanMode, nil
}

type telegramPendingMedia struct {
	messageType string
	filename    string
	contentType string
	data        []byte
}

type telegramPending struct {
	sessionID string
	userID    string
	texts     []string
	media     *telegramPendingMedia
}

type telegramSessionBuffer struct {
	pending telegramPending
	timer   *time.Timer
	started time.Time
}

type telegramBuffer struct {
	mu       sync.Mutex
	sessions map[string]*telegramSessionBuffer
}

const (
	telegramIdleWindow = 6 * time.Second
	telegramMaxWait    = 30 * time.Second
)

func newTelegramBuffer() *telegramBuffer {
	return &telegramBuffer{sessions: make(map[string]*telegramSessionBuffer)}
}

func (b *telegramBuffer) AddText(sessionID, userID, text string, onFlush func(telegramPending)) {
	b.mu.Lock()
	buf := b.sessions[sessionID]
	if buf == nil {
		buf = &telegramSessionBuffer{
			pending: telegramPending{sessionID: sessionID, userID: userID},
			started: time.Now(),
		}
		b.sessions[sessionID] = buf
	}
	if buf.pending.userID == "" {
		buf.pending.userID = userID
	}
	buf.pending.texts = append(buf.pending.texts, text)
	if time.Since(buf.started) >= telegramMaxWait {
		pending := b.detachLocked(sessionID)
		b.mu.Unlock()
		onFlush(pending)
		return
	}
	b.resetTimerLocked(sessionID, buf, onFlush)
	b.mu.Unlock()
}

func (b *telegramBuffer) AddMedia(sessionID, userID string, media telegramPendingMedia, onFlush func(telegramPending)) {
	var pending telegramPending
	b.mu.Lock()
	buf := b.sessions[sessionID]
	if buf == nil {
		buf = &telegramSessionBuffer{
			pending: telegramPending{sessionID: sessionID, userID: userID},
			started: time.Now(),
		}
		b.sessions[sessionID] = buf
	}
	if buf.pending.userID == "" {
		buf.pending.userID = userID
	}
	if buf.pending.media != nil {
		pending = b.detachLocked(sessionID)
	}
	buf = b.sessions[sessionID]
	if buf == nil {
		buf = &telegramSessionBuffer{
			pending: telegramPending{sessionID: sessionID, userID: userID},
			started: time.Now(),
		}
		b.sessions[sessionID] = buf
	}
	buf.pending.media = &media
	if time.Since(buf.started) >= telegramMaxWait {
		pending = b.detachLocked(sessionID)
		b.mu.Unlock()
		if pending.sessionID != "" {
			onFlush(pending)
		}
		return
	}
	b.resetTimerLocked(sessionID, buf, onFlush)
	b.mu.Unlock()

	if pending.sessionID != "" {
		onFlush(pending)
	}
}

func (b *telegramBuffer) resetTimerLocked(sessionID string, buf *telegramSessionBuffer, onFlush func(telegramPending)) {
	if buf.timer != nil {
		buf.timer.Stop()
	}
	buf.timer = time.AfterFunc(telegramIdleWindow, func() {
		b.flush(sessionID, onFlush)
	})
}

func (b *telegramBuffer) flush(sessionID string, onFlush func(telegramPending)) {
	b.mu.Lock()
	pending := b.detachLocked(sessionID)
	b.mu.Unlock()
	if pending.sessionID != "" {
		onFlush(pending)
	}
}

func (b *telegramBuffer) detachLocked(sessionID string) telegramPending {
	buf := b.sessions[sessionID]
	if buf == nil {
		return telegramPending{}
	}
	delete(b.sessions, sessionID)
	if buf.timer != nil {
		buf.timer.Stop()
	}
	return buf.pending
}
