package config

import (
	"log"
	"os"
)

type Config struct {
	HTTPAddr               string
	DatabaseURL            string
	InternalToken          string
	SupabaseURL            string
	SupabaseServiceRoleKey string
	OllamaURL              string
	OllamaEmbeddingModel   string
	OpenAIBaseURL          string
	OpenAIAPIKey           string
	OpenAIModel            string
	OpenAIVisionModel      string
	OpenAITranscribeModel  string
	TikaURL                string
	TelegramBotToken       string
	TelegramWebhookSecret  string
	TelegramBaseURL        string
	ManagerChatID          string
	DirectorChatID         string
}

func MustLoad() Config {
	return Config{
		HTTPAddr:               env("HTTP_ADDR", ":8080"),
		DatabaseURL:            mustEnv("DATABASE_URL"),
		InternalToken:          mustEnv("INTERNAL_TOKEN"),
		SupabaseURL:            mustEnv("SUPABASE_URL"),
		SupabaseServiceRoleKey: mustEnv("SUPABASE_SERVICE_ROLE_KEY"),
		OllamaURL:              env("OLLAMA_URL", "http://127.0.0.1:11434"),
		OllamaEmbeddingModel:   env("OLLAMA_EMBEDDING_MODEL", "mxbai-embed-large"),
		OpenAIBaseURL:          env("OPENAI_BASE_URL", "https://api.openai.com"),
		OpenAIAPIKey:           mustEnv("OPENAI_API_KEY"),
		OpenAIModel:            env("OPENAI_MODEL", "gpt-4o-mini"),
		OpenAIVisionModel:      env("OPENAI_VISION_MODEL", "gpt-4o-mini"),
		OpenAITranscribeModel:  env("OPENAI_TRANSCRIBE_MODEL", "gpt-4o-mini-transcribe"),
		TikaURL:                env("TIKA_URL", ""),
		TelegramBotToken:       env("TELEGRAM_BOT_TOKEN", ""),
		TelegramWebhookSecret:  env("TELEGRAM_WEBHOOK_SECRET", ""),
		TelegramBaseURL:        env("TELEGRAM_BASE_URL", "https://api.telegram.org"),
		ManagerChatID:          env("MANAGER_CHAT_ID", ""),
		DirectorChatID:         env("DIRECTOR_CHAT_ID", ""),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing env %s", k)
	}
	return v
}
