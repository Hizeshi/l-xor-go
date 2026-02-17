package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type escalationRule struct {
	Type     string                 `json:"type"`
	Manager  escalationManagerRule  `json:"manager"`
	Director escalationDirectorRule `json:"director"`
}

type escalationManagerRule struct {
	Trigger escalationTrigger `json:"trigger"`
	Message string            `json:"message_template"`
}

type escalationDirectorRule struct {
	TimeoutMinutes int    `json:"timeout_minutes"`
	Message        string `json:"message_template"`
}

type escalationTrigger struct {
	MaxConsecutiveClarifyQuestions int      `json:"max_consecutive_clarify_questions"`
	NegativeKeywords               []string `json:"negative_keywords"`
}

type escalationState struct {
	ManagerNotifiedAt  string `json:"manager_notified_at,omitempty"`
	DirectorNotifiedAt string `json:"director_notified_at,omitempty"`
	LastReason         string `json:"last_escalation_reason,omitempty"`
	LastClarifyCount   int    `json:"last_clarify_count,omitempty"`
}

func (s *Service) fetchEscalationRule(ctx context.Context, vector string) (*escalationRule, error) {
	payload := map[string]interface{}{
		"query_embedding": vector,
		"match_threshold": 0.2,
		"match_count":     1,
		"filter": map[string]interface{}{
			"topic": "escalation_rule",
		},
	}
	var matches []SupabaseMatch
	if err := s.callSupabaseRPC(ctx, "match_sales_knowledge", payload, &matches); err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	raw := strings.TrimSpace(matches[0].Content)
	if raw == "" || !strings.Contains(raw, "{") {
		return nil, nil
	}
	var rule escalationRule
	if err := json.Unmarshal([]byte(raw), &rule); err != nil {
		return nil, nil
	}
	if strings.TrimSpace(rule.Type) != "escalation_rule" {
		return nil, nil
	}
	return &rule, nil
}

func (s *Service) maybeEscalate(ctx context.Context, sessionID, userMessage, answer string, history []chatMessageRow, rule *escalationRule) *escalationState {
	if rule == nil {
		return nil
	}
	chatID, ok := parseChatID(s.Cfg.ManagerChatID)
	if !ok || chatID == 0 {
		log.Printf("chat escalation: manager chat id missing")
		return nil
	}
	state := latestEscalationState(history)
	if state == nil {
		state = &escalationState{}
	}

	lastManagerReply := lastHumanAdminReply(history)
	if state.ManagerNotifiedAt != "" && lastManagerReply.After(parseTime(state.ManagerNotifiedAt)) {
		state.ManagerNotifiedAt = ""
		state.DirectorNotifiedAt = ""
		state.LastReason = ""
		state.LastClarifyCount = 0
	}

	clarifyCount := countConsecutiveClarify(history, answer)
	state.LastClarifyCount = clarifyCount

	triggered, reason := shouldTriggerManager(userMessage, clarifyCount, rule.Manager.Trigger)
	if triggered && state.ManagerNotifiedAt == "" {
		msg := renderTemplate(rule.Manager.Message, sessionID, userMessage, rule.Director.TimeoutMinutes)
		if err := s.sendTelegramToChat(ctx, chatID, msg); err != nil {
			log.Printf("chat escalation: manager notify failed: %v", err)
		} else {
			state.ManagerNotifiedAt = time.Now().UTC().Format(time.RFC3339)
			state.LastReason = reason
			log.Printf("chat escalation: manager notified session_id=%s reason=%s", sessionID, reason)
			s.scheduleDirectorEscalation(sessionID, userMessage, parseTime(state.ManagerNotifiedAt), rule)
		}
	}

	if state.ManagerNotifiedAt != "" && state.DirectorNotifiedAt == "" && rule.Director.TimeoutMinutes > 0 {
		directorID, ok := parseChatID(s.Cfg.DirectorChatID)
		if ok && directorID != 0 {
			managerAt := parseTime(state.ManagerNotifiedAt)
			if !managerAt.IsZero() && time.Since(managerAt) >= time.Duration(rule.Director.TimeoutMinutes)*time.Minute {
				if lastManagerReply.IsZero() || lastManagerReply.Before(managerAt) {
					msg := renderTemplate(rule.Director.Message, sessionID, userMessage, rule.Director.TimeoutMinutes)
					if err := s.sendTelegramToChat(ctx, directorID, msg); err != nil {
						log.Printf("chat escalation: director notify failed: %v", err)
					} else {
						state.DirectorNotifiedAt = time.Now().UTC().Format(time.RFC3339)
						log.Printf("chat escalation: director notified session_id=%s", sessionID)
					}
				}
			}
		}
	}

	return state
}

func (s *Service) scheduleDirectorEscalation(sessionID, lastUserMessage string, managerAt time.Time, rule *escalationRule) {
	if rule == nil || rule.Director.TimeoutMinutes <= 0 || managerAt.IsZero() {
		return
	}
	directorID, ok := parseChatID(s.Cfg.DirectorChatID)
	if !ok || directorID == 0 {
		return
	}
	timeout := time.Duration(rule.Director.TimeoutMinutes) * time.Minute
	time.AfterFunc(timeout, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		history, err := s.fetchChatHistory(ctx, sessionID, 50)
		if err != nil {
			log.Printf("chat escalation: director check failed session_id=%s err=%v", sessionID, err)
			return
		}
		if lastHumanAdminReply(history).After(managerAt) {
			return
		}
		if st := latestEscalationState(history); st != nil {
			if t := parseTime(st.DirectorNotifiedAt); !t.IsZero() && t.After(managerAt) {
				return
			}
		}
		msg := renderTemplate(rule.Director.Message, sessionID, lastUserMessage, rule.Director.TimeoutMinutes)
		if err := s.sendTelegramToChat(ctx, directorID, msg); err != nil {
			log.Printf("chat escalation: director notify failed: %v", err)
		} else {
			log.Printf("chat escalation: director notified session_id=%s", sessionID)
		}
	})
}

func shouldTriggerManager(userMessage string, clarifyCount int, trigger escalationTrigger) (bool, string) {
	if trigger.MaxConsecutiveClarifyQuestions > 0 && clarifyCount >= trigger.MaxConsecutiveClarifyQuestions {
		return true, "clarify_count"
	}
	for _, kw := range trigger.NegativeKeywords {
		kw = strings.TrimSpace(strings.ToLower(kw))
		if kw == "" {
			continue
		}
		if strings.Contains(strings.ToLower(userMessage), kw) {
			return true, "negative_keyword"
		}
	}
	return false, ""
}

func countConsecutiveClarify(history []chatMessageRow, currentAnswer string) int {
	count := 0
	if isClarifyQuestion(currentAnswer) {
		count++
	} else {
		return 0
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != "assistant" {
			continue
		}
		if isClarifyQuestion(history[i].Content) {
			count++
			continue
		}
		break
	}
	return count
}

func isClarifyQuestion(text string) bool {
	if !strings.Contains(text, "?") {
		return false
	}
	lower := strings.ToLower(text)
	keywords := []string{"уточните", "какой", "что именно", "какая", "какие", "какого"}
	for _, k := range keywords {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

func renderTemplate(tpl, sessionID, lastUserMessage string, timeout int) string {
	if tpl == "" {
		return fmt.Sprintf("Нужен менеджер для чата %s. Последний запрос: %s", sessionID, lastUserMessage)
	}
	out := strings.ReplaceAll(tpl, "{session_id}", sessionID)
	out = strings.ReplaceAll(out, "{last_user_message}", lastUserMessage)
	out = strings.ReplaceAll(out, "{timeout}", strconv.Itoa(timeout))
	return out
}

func parseChatID(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	raw = strings.TrimPrefix(raw, "tg:")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

func (s *Service) sendTelegramToChat(ctx context.Context, chatID int64, text string) error {
	if chatID == 0 || strings.TrimSpace(text) == "" || s.Cfg.TelegramBotToken == "" {
		return nil
	}
	base := strings.TrimRight(s.Cfg.TelegramBaseURL, "/")
	urlStr := fmt.Sprintf("%s/bot%s/sendMessage", base, s.Cfg.TelegramBotToken)
	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func latestEscalationState(history []chatMessageRow) *escalationState {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].MetaData == nil {
			continue
		}
		raw, ok := history[i].MetaData["escalation"]
		if !ok {
			continue
		}
		b, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var st escalationState
		if err := json.Unmarshal(b, &st); err == nil {
			return &st
		}
	}
	return nil
}

func lastHumanAdminReply(history []chatMessageRow) time.Time {
	for i := len(history) - 1; i >= 0; i-- {
		if strings.TrimSpace(strings.ToLower(history[i].SenderType)) == "human_admin" {
			if t := parseTime(history[i].CreatedAt); !t.IsZero() {
				return t
			}
		}
	}
	return time.Time{}
}

func parseTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05.999999Z07:00", raw); err == nil {
		return t
	}
	return time.Time{}
}
