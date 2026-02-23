package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"iq-home/go_beckend/internal/app/config"
	"iq-home/go_beckend/internal/app/http/handlers"
	"iq-home/go_beckend/internal/app/http/middleware"
	"iq-home/go_beckend/internal/infra/db/postgres"
)

func NewRouter(cfg config.Config, db *postgres.DB) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logging)
	r.Use(middleware.CORS(cfg.CORSAllowOrigin))

	h := handlers.New(db, cfg)

	r.Get("/health", h.Health)

	r.Route("/v1", func(r chi.Router) {

		r.Post("/telegram/webhook", h.TelegramWebhook)

		r.Group(func(r chi.Router) {
			r.Use(middleware.InternalAuth(cfg.InternalToken))

			r.Post("/quotes", h.CreateQuote)
			r.Post("/chat", h.Chat)
			r.Post("/chat/media", h.ChatMedia)
			r.Post("/products/images", h.UploadProductImages)
			r.Post("/products/images/item", h.AddProductImage)
			r.Put("/products/images/item", h.UpdateProductImage)
			r.Delete("/products/images/item", h.DeleteProductImage)
		})
	})

	return r
}
