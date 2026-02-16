package handlers

import (
	"net/http"

	"iq-home/go_beckend/internal/app/http/handlers/chat"
)

func (h *Handlers) Chat(w http.ResponseWriter, r *http.Request) {
	chat.New(h.Cfg, h.HTTP).Handle(w, r)
}

func (h *Handlers) ChatMedia(w http.ResponseWriter, r *http.Request) {
	chat.New(h.Cfg, h.HTTP).HandleMedia(w, r)
}
