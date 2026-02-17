package chat

type ChatRequest struct {
	Message     string                 `json:"message"`
	SessionID   string                 `json:"session_id"`
	UserID      *string                `json:"user_id"`
	UserMeta    map[string]interface{} `json:"user_meta,omitempty"`
	MatchCount  int                    `json:"match_count"`
	TopicFilter *string                `json:"topic_filter"`
}

type ChatResponse struct {
	Answer    string          `json:"answer"`
	Products  []SupabaseMatch `json:"products"`
	Knowledge []SupabaseMatch `json:"knowledge"`
}

type SupabaseMatch struct {
	ID         int64                  `json:"id"`
	Content    string                 `json:"content"`
	Metadata   map[string]interface{} `json:"metadata"`
	Similarity float64                `json:"similarity"`
}

type productSearchRow struct {
	ID             int64    `json:"id"`
	NameRaw        string   `json:"name_raw"`
	Price          *float64 `json:"price"`
	ImageURL       *string  `json:"image_url"`
	Score          float64  `json:"score"`
	DetectedBrand  *string  `json:"detected_brand"`
	DetectedColor  *string  `json:"detected_color"`
	DetectedSeries *string  `json:"detected_series"`
}

type productFallbackRow struct {
	ID          int64    `json:"id"`
	Article     *string  `json:"article"`
	NameRaw     string   `json:"name_raw"`
	ProductType *string  `json:"product_type"`
	Price       *float64 `json:"price"`
	ImageURL    *string  `json:"image_url"`
	Brand       *string  `json:"brand"`
	Color       *string  `json:"color"`
	Category    *string  `json:"category"`
}

type productFullRow struct {
	ID          int64    `json:"id"`
	NameRaw     string   `json:"name_raw"`
	Price       *float64 `json:"price"`
	BrandName   *string  `json:"brand_name"`
	ColorName   *string  `json:"color_name"`
	SeriesName  *string  `json:"series_name"`
	ProductType *string  `json:"product_type"`
}

type ollamaEmbeddingResponse struct {
	Embedding []float64 `json:"embedding"`
}

type openAIChatRequest struct {
	Model          string                `json:"model"`
	Messages       []openAIChatMessage   `json:"messages"`
	MaxTokens      int                   `json:"max_completion_tokens,omitempty"`
	Temperature    float64               `json:"temperature,omitempty"`
	ResponseFormat *openAIResponseFormat `json:"response_format,omitempty"`
}

type openAIResponseFormat struct {
	Type string `json:"type"`
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message openAIChatMessage `json:"message"`
	} `json:"choices"`
}

type productDecision struct {
	NeedProducts bool `json:"need_products"`
}

type chatMessageRow struct {
	Role       string                 `json:"role"`
	Content    string                 `json:"content"`
	MetaData   map[string]interface{} `json:"meta_data,omitempty"`
	CreatedAt  string                 `json:"created_at,omitempty"`
	SenderType string                 `json:"sender_type,omitempty"`
}

type chatMessageInsert struct {
	SessionID string                 `json:"session_id"`
	Role      string                 `json:"role"`
	Content   string                 `json:"content"`
	MetaData  map[string]interface{} `json:"meta_data"`
}
