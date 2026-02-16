package handlers

import (
	"net/http"
	"time"

	"iq-home/go_beckend/internal/app/config"
	"iq-home/go_beckend/internal/infra/db/postgres"
)

type Handlers struct {
	DB       *postgres.DB
	Cfg      config.Config
	HTTP     *http.Client
	tgBuffer *telegramBuffer
}

func New(db *postgres.DB, cfg config.Config) *Handlers {
	h := &Handlers{
		DB:  db,
		Cfg: cfg,
		HTTP: &http.Client{
			Timeout: 15 * time.Second,
		},
		tgBuffer: newTelegramBuffer(),
	}
	h.startManagerRelay()
	return h
}
