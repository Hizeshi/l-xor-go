package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"iq-home/go_beckend/internal/app/config"
)

type Service struct {
	Cfg  config.Config
	HTTP *http.Client
}

func New(cfg config.Config, httpClient *http.Client) *Service {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Service{Cfg: cfg, HTTP: httpClient}
}

func (s *Service) Handle(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("chat req=unknown bad request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.handleMessage(w, r, req)
}

func (s *Service) handleMessage(w http.ResponseWriter, r *http.Request, req ChatRequest) {
	reqID := fmt.Sprintf("chat-%d", time.Now().UnixNano())
	if strings.TrimSpace(req.Message) == "" {
		log.Printf("chat req=%s empty message", reqID)
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}
	matchCount := req.MatchCount
	if matchCount <= 0 {
		matchCount = 5
	}
	if matchCount > 20 {
		matchCount = 20
	}
	sessionID := strings.TrimSpace(req.SessionID)
	userID := strings.TrimSpace(derefString(req.UserID))
	log.Printf("chat req=%s start session_id=%s user_id=%s message_len=%d match_count=%d topic_filter=%v",
		reqID, sessionID, userID, len(req.Message), matchCount, req.TopicFilter != nil)

	incomingQuotePDF := req.UserMeta != nil && boolMeta(req.UserMeta, "incoming_quote_pdf")
	fromDBRelay := req.UserMeta != nil && boolMeta(req.UserMeta, "from_db_relay")
	var history []chatMessageRow
	var behavior *userBehaviorContext
	if sessionID != "" {
		if err := s.ensureChatSession(r.Context(), sessionID, userID); err != nil {
			log.Printf("chat req=%s ensure session failed: %v", reqID, err)
		} else {
			humanMode, err := s.fetchHumanMode(r.Context(), sessionID)
			if err != nil {
				log.Printf("chat req=%s human mode check failed: %v", reqID, err)
			}
			if humanMode {
				log.Printf("chat req=%s human mode=true skip ai", reqID)
				if !fromDBRelay {
					if err := s.insertChatMessages(r.Context(), []chatMessageInsert{
						{SessionID: sessionID, Role: "user", Content: req.Message, MetaData: map[string]interface{}{}},
					}); err != nil {
						log.Printf("chat req=%s insert messages failed: %v", reqID, err)
					}
				}
				resp := ChatResponse{Answer: "", Products: nil, Knowledge: nil}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
				return
			}
			historyStart := time.Now()
			history, err = s.fetchChatHistory(r.Context(), sessionID, 30)
			if err != nil {
				log.Printf("chat req=%s history load failed: %v", reqID, err)
			} else {
				log.Printf("chat req=%s history loaded count=%d took=%s",
					reqID, len(history), time.Since(historyStart))
			}
		}
	}
	if userID != "" && shouldUseSitePersonalization(sessionID) {
		profileStart := time.Now()
		profile, profileErr := s.fetchUserBehavior(r.Context(), userID)
		if profileErr != nil {
			log.Printf("chat req=%s personalization failed: %v", reqID, profileErr)
		} else if profile != nil {
			behavior = profile
			log.Printf("chat req=%s personalization ok recent=%d favorites=%d cart=%d orders=%d took=%s", reqID, len(behavior.RecentlyViewed), len(behavior.Favorites), len(behavior.Cart), len(behavior.Orders), time.Since(profileStart))
		}
	}

	if detectPingMessage(req.Message) {
		answer := "Да, я здесь. Чем могу помочь?"
		if sessionID != "" {
			userMeta := mergeMeta(nil, req.UserMeta)
			assistantMeta := map[string]interface{}{"slots": extractSlots(req.Message)}
			rows := make([]chatMessageInsert, 0, 2)
			if !fromDBRelay {
				rows = append(rows, chatMessageInsert{SessionID: sessionID, Role: "user", Content: req.Message, MetaData: userMeta})
			}
			rows = append(rows, chatMessageInsert{SessionID: sessionID, Role: "assistant", Content: answer, MetaData: assistantMeta})
			if err := s.insertChatMessages(r.Context(), rows); err != nil {
				log.Printf("chat req=%s insert messages failed: %v", reqID, err)
			}
		}
		resp := ChatResponse{Answer: answer, Products: nil, Knowledge: nil}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	if kind := detectAssortmentQuery(req.Message); kind != "" {
		answer, err := s.handleAssortmentQuery(r.Context(), kind)
		if err != nil {
			log.Printf("chat req=%s assortment failed: %v", reqID, err)
			http.Error(w, "assortment lookup failed", http.StatusBadGateway)
			return
		}
		if sessionID != "" {
			userMeta := mergeMeta(nil, req.UserMeta)
			rows := make([]chatMessageInsert, 0, 2)
			if !fromDBRelay {
				rows = append(rows, chatMessageInsert{SessionID: sessionID, Role: "user", Content: req.Message, MetaData: userMeta})
			}
			rows = append(rows, chatMessageInsert{SessionID: sessionID, Role: "assistant", Content: answer, MetaData: map[string]interface{}{}})
			if err := s.insertChatMessages(r.Context(), rows); err != nil {
				log.Printf("chat req=%s insert messages failed: %v", reqID, err)
			}
		}
		resp := ChatResponse{Answer: answer, Products: nil, Knowledge: nil}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	userWantsQuote := detectKpIntent(req.Message, history)
	if incomingQuotePDF {
		userWantsQuote = false
	}
	if userWantsQuote {
		log.Printf("chat req=%s quote intent=true", reqID)
	}

	decisionStart := time.Now()
	needProducts, err := s.decideProductSearch(r.Context(), req.Message)
	if err != nil {
		log.Printf("chat req=%s product decision failed: %v", reqID, err)
		needProducts = true
	}
	log.Printf("chat req=%s product decision=%v took=%s", reqID, needProducts, time.Since(decisionStart))
	if userWantsQuote {
		needProducts = true
		log.Printf("chat req=%s force product search for quote", reqID)
	}
	if incomingQuotePDF {
		needProducts = true
		log.Printf("chat req=%s force product search for incoming quote pdf", reqID)
	}

	embedStart := time.Now()
	embedding, err := s.getEmbedding(r.Context(), req.Message)
	if err != nil {
		log.Printf("chat req=%s embedding failed: %v", reqID, err)
		http.Error(w, "embedding failed", http.StatusBadGateway)
		return
	}
	log.Printf("chat req=%s embedding ok dims=%d took=%s", reqID, len(embedding), time.Since(embedStart))

	vector := vectorString(embedding)

	filter := map[string]interface{}{}
	if req.TopicFilter != nil && strings.TrimSpace(*req.TopicFilter) != "" {
		filter["topic"] = strings.TrimSpace(*req.TopicFilter)
	}

	knowledgePayload := map[string]interface{}{
		"query_embedding": vector,
		"match_threshold": 0.75,
		"match_count":     1,
		"filter":          filter,
	}

	var products []SupabaseMatch
	if needProducts {
		productsStart := time.Now()
		docArticles := stringSliceMeta(req.UserMeta, "document_articles")
		if len(docArticles) > 0 {
			products, err = s.searchProductsByArticles(r.Context(), docArticles, matchCount)
			if err != nil {
				log.Printf("chat req=%s document articles search failed: %v", reqID, err)
			}
			if len(products) > 0 {
				log.Printf("chat req=%s products by articles ok count=%d ids=%s took=%s", reqID, len(products), joinProductIDs(products, 5), time.Since(productsStart))
			}
		}
		if len(products) == 0 {
			products, err = s.searchProductsHybrid(r.Context(), req.Message, vector, matchCount)
			if err != nil {
				log.Printf("chat req=%s supabase products failed: %v", reqID, err)
				http.Error(w, "supabase products search failed", http.StatusBadGateway)
				return
			}
			log.Printf("chat req=%s products ok count=%d ids=%s names=%s took=%s",
				reqID, len(products), joinProductIDs(products, 5), joinProductNames(products, 3), time.Since(productsStart))
		}
	}
	if len(products) == 0 && isFollowUpMessage(req.Message) {
		reused, err := s.loadProductsFromHistory(r.Context(), history)
		if err != nil {
			log.Printf("chat req=%s reuse products failed: %v", reqID, err)
		} else if len(reused) > 0 {
			products = reused
			log.Printf("chat req=%s reuse products ok count=%d ids=%s", reqID, len(products), joinProductIDs(products, 5))
		}
	}
	if len(products) > 0 && behavior != nil {
		products = rankProductsByBehavior(products, behavior)
	}
	if len(products) == 0 && needProducts && behavior != nil {
		products = fallbackProductsFromBehavior(behavior, matchCount)
		if len(products) > 0 {
			log.Printf("chat req=%s personalization fallback products count=%d ids=%s", reqID, len(products), joinProductIDs(products, 5))
		}
	}

	if userWantsQuote && len(products) > 0 {
		pdfStart := time.Now()
		pdfBytes, err := s.generateQuotePDF(products)
		if err != nil {
			log.Printf("chat req=%s quote pdf failed: %v", reqID, err)
			http.Error(w, "quote generation failed", http.StatusBadGateway)
			return
		}
		if sessionID != "" {
			userMeta := map[string]interface{}{}
			if hasKPOffered(history) {
				userMeta["kp_accept"] = true
			}
			assistantMeta := map[string]interface{}{"kp_pdf": true}
			rows := make([]chatMessageInsert, 0, 2)
			if !fromDBRelay {
				rows = append(rows, chatMessageInsert{SessionID: sessionID, Role: "user", Content: req.Message, MetaData: userMeta})
			}
			rows = append(rows, chatMessageInsert{SessionID: sessionID, Role: "assistant", Content: "Сформировано КП", MetaData: assistantMeta})
			if err := s.insertChatMessages(r.Context(), rows); err != nil {
				log.Printf("chat req=%s insert messages failed: %v", reqID, err)
			}
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="KP.pdf"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pdfBytes)
		log.Printf("chat req=%s quote pdf ok bytes=%d took=%s", reqID, len(pdfBytes), time.Since(pdfStart))
		return
	}

	knowledgeStart := time.Now()
	var knowledge []SupabaseMatch
	if err := s.callSupabaseRPC(r.Context(), "match_sales_knowledge", knowledgePayload, &knowledge); err != nil {
		log.Printf("chat req=%s supabase knowledge failed: %v", reqID, err)
		http.Error(w, "supabase knowledge search failed", http.StatusBadGateway)
		return
	}
	log.Printf("chat req=%s knowledge ok count=%d took=%s", reqID, len(knowledge), time.Since(knowledgeStart))

	var escRule *escalationRule
	if sessionID != "" {
		if rule, err := s.fetchEscalationRule(r.Context(), vector); err != nil {
			log.Printf("chat req=%s escalation rule fetch failed: %v", reqID, err)
		} else {
			escRule = rule
		}
	}

	openAIStart := time.Now()
	answer, err := s.callOpenAI(r.Context(), req.Message, history, products, knowledge, behavior)
	if err != nil {
		log.Printf("chat req=%s openai failed: %v", reqID, err)
		http.Error(w, "openai generation failed", http.StatusBadGateway)
		return
	}
	if strings.TrimSpace(answer) == "" {
		answer = "Нашёл несколько вариантов. Уточните, пожалуйста, что именно нужно (тип/серия/цвет)."
	}
	log.Printf("chat req=%s openai ok answer_len=%d took=%s", reqID, len(answer), time.Since(openAIStart))
	if needProducts && len(products) > 0 && isLikelyProductQuery(req.Message) {
		answer = appendProductLinks(answer, products)
	}

	offerKp := false
	if needProducts && len(products) > 0 && !userWantsQuote && !incomingQuotePDF && !hasKPOffered(history) {
		offerKp = true
		answer = strings.TrimSpace(answer) + "\n\nМогу собрать КП — собрать?"
	}

	if sessionID != "" {
		userMeta := map[string]interface{}{}
		if userWantsQuote && hasKPOffered(history) {
			userMeta["kp_accept"] = true
		}
		userMeta = mergeMeta(userMeta, req.UserMeta)

		assistantMeta := map[string]interface{}{}
		if offerKp {
			assistantMeta["kp_offer"] = true
		}
		if len(products) > 0 {
			assistantMeta["product_ids"] = collectProductIDs(products)
		}
		assistantMeta["slots"] = mergeSlots(latestSlots(history), extractSlots(req.Message))
		if shouldUpdateSummary(history, 6) {
			if summary, err := s.summarizeHistory(r.Context(), history, answer); err == nil && strings.TrimSpace(summary) != "" {
				assistantMeta["summary"] = summary
			} else if err != nil {
				log.Printf("chat req=%s summary update failed: %v", reqID, err)
			}
		}
		assistantMeta["slots"] = mergeSlots(latestSlots(history), extractSlots(req.Message))
		if escRule != nil {
			if state := s.maybeEscalate(r.Context(), sessionID, req.Message, answer, history, escRule); state != nil {
				assistantMeta["escalation"] = state
			}
		}

		log.Printf("chat req=%s persist session_id=%s user_meta=%t assistant_meta_keys=%d", reqID, sessionID, len(userMeta) > 0, len(assistantMeta))
		rows := make([]chatMessageInsert, 0, 2)
		if !fromDBRelay {
			rows = append(rows, chatMessageInsert{SessionID: sessionID, Role: "user", Content: req.Message, MetaData: userMeta})
		}
		rows = append(rows, chatMessageInsert{SessionID: sessionID, Role: "assistant", Content: answer, MetaData: assistantMeta})
		if err := s.insertChatMessages(r.Context(), rows); err != nil {
			log.Printf("chat req=%s insert messages failed: %v", reqID, err)
		} else {
			log.Printf("chat req=%s insert messages ok", reqID)
		}
	} else {
		log.Printf("chat req=%s session_id missing, skipping persistence", reqID)
	}

	resp := ChatResponse{Answer: answer, Products: products, Knowledge: knowledge}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
	log.Printf("chat req=%s done products=%d knowledge=%d", reqID, len(products), len(knowledge))
}

func boolMeta(meta map[string]interface{}, key string) bool {
	if meta == nil {
		return false
	}
	v, ok := meta[key]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	default:
		return false
	}
}

func stringSliceMeta(meta map[string]interface{}, key string) []string {
	if meta == nil {
		return nil
	}
	v, ok := meta[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return t
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s := strings.TrimSpace(fmt.Sprintf("%v", item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func (s *Service) handleAssortmentQuery(ctx context.Context, kind string) (string, error) {
	var values []string
	var err error
	switch kind {
	case "colors":
		values, err = s.fetchDistinctValues(ctx, "colors", "name", 100)
	case "brands":
		values, err = s.fetchDistinctValues(ctx, "brands", "name", 100)
	case "series":
		values, err = s.fetchDistinctValues(ctx, "product_series", "name", 100)
	case "types":
		values, err = s.fetchProductTypes(ctx, 500)
	default:
		return "", fmt.Errorf("unknown assortment kind")
	}
	if err != nil {
		return "", err
	}
	if len(values) == 0 {
		return "Сейчас нет данных по ассортименту. Уточните, что именно ищете.", nil
	}
	max := 20
	if len(values) < max {
		max = len(values)
	}
	list := strings.Join(values[:max], ", ")
	switch kind {
	case "colors":
		return "Доступные цвета: " + list + ". Уточните тип товара (розетки/выключатели/рамки).", nil
	case "brands":
		return "Доступные бренды: " + list + ". Уточните тип товара и цвет.", nil
	case "series":
		return "Доступные серии: " + list + ". Уточните тип товара и цвет.", nil
	case "types":
		return "Доступные типы: " + list + ". Уточните цвет или серию.", nil
	default:
		return "Уточните, что именно ищете.", nil
	}
}
