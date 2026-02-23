package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Service) callSupabaseRPC(ctx context.Context, fn string, payload interface{}, out interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	urlStr := strings.TrimRight(s.Cfg.SupabaseURL, "/") + "/rest/v1/rpc/" + fn
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		return fmt.Errorf("invalid supabase url")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+s.Cfg.SupabaseServiceRoleKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func (s *Service) ensureChatSession(ctx context.Context, sessionID, userID string) error {
	payload := map[string]interface{}{
		"session_id": sessionID,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	}
	if userID != "" {
		if isUUID(userID) {
			payload["auth_user_id"] = strings.ToLower(userID)
		} else {
			payload["user_id"] = userID
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	urlStr := strings.TrimRight(s.Cfg.SupabaseURL, "/") + "/rest/v1/chat_sessions?on_conflict=session_id"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+s.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Prefer", "resolution=merge-duplicates,return=minimal")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (s *Service) fetchChatHistory(ctx context.Context, sessionID string, limit int) ([]chatMessageRow, error) {
	if limit <= 0 {
		limit = 10
	}
	values := url.Values{}
	values.Set("select", "role,content,meta_data,created_at,sender_type")
	values.Set("session_id", "eq."+sessionID)
	values.Set("order", "created_at.asc")
	values.Set("limit", strconv.Itoa(limit))

	urlStr := strings.TrimRight(s.Cfg.SupabaseURL, "/") + "/rest/v1/chat_messages?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", s.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+s.Cfg.SupabaseServiceRoleKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var rows []chatMessageRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *Service) fetchDistinctValues(ctx context.Context, table, field string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	values := url.Values{}
	values.Set("select", field)
	values.Set("order", field+".asc")
	values.Set("limit", strconv.Itoa(limit))

	urlStr := strings.TrimRight(s.Cfg.SupabaseURL, "/") + "/rest/v1/" + table + "?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", s.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+s.Cfg.SupabaseServiceRoleKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var rows []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	uniq := map[string]struct{}{}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if v, ok := r[field]; ok {
			sv := strings.TrimSpace(fmt.Sprintf("%v", v))
			if sv == "" {
				continue
			}
			if _, ok := uniq[sv]; ok {
				continue
			}
			uniq[sv] = struct{}{}
			out = append(out, sv)
		}
	}
	return out, nil
}

func (s *Service) fetchProductTypes(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 500
	}
	values := url.Values{}
	values.Set("select", "product_type")
	values.Set("order", "product_type.asc")
	values.Set("limit", strconv.Itoa(limit))

	urlStr := strings.TrimRight(s.Cfg.SupabaseURL, "/") + "/rest/v1/products_full?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", s.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+s.Cfg.SupabaseServiceRoleKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var rows []struct {
		ProductType *string `json:"product_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	uniq := map[string]struct{}{}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.ProductType == nil {
			continue
		}
		sv := strings.TrimSpace(*r.ProductType)
		if sv == "" {
			continue
		}
		if _, ok := uniq[sv]; ok {
			continue
		}
		uniq[sv] = struct{}{}
		out = append(out, sv)
	}
	return out, nil
}

func (s *Service) fetchHumanMode(ctx context.Context, sessionID string) (bool, error) {
	values := url.Values{}
	values.Set("select", "is_human_mode")
	values.Set("session_id", "eq."+sessionID)
	values.Set("limit", "1")

	urlStr := strings.TrimRight(s.Cfg.SupabaseURL, "/") + "/rest/v1/chat_sessions?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("apikey", s.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+s.Cfg.SupabaseServiceRoleKey)

	resp, err := s.HTTP.Do(req)
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

func (s *Service) insertChatMessages(ctx context.Context, rows []chatMessageInsert) error {
	if len(rows) == 0 {
		return nil
	}
	// Safety net: ensure every session exists even if caller skipped ensureChatSession.
	seenSessions := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		sid := strings.TrimSpace(row.SessionID)
		if sid == "" {
			continue
		}
		if _, ok := seenSessions[sid]; ok {
			continue
		}
		seenSessions[sid] = struct{}{}
		if err := s.ensureChatSession(ctx, sid, ""); err != nil {
			return fmt.Errorf("ensure chat session %s failed: %w", sid, err)
		}
	}

	body, err := json.Marshal(rows)
	if err != nil {
		return err
	}

	urlStr := strings.TrimRight(s.Cfg.SupabaseURL, "/") + "/rest/v1/chat_messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+s.Cfg.SupabaseServiceRoleKey)
	req.Header.Set("Prefer", "return=minimal")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("supabase status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
