package app

import (
	"log"
	"net/http"
	"time"

	"iq-home/go_beckend/internal/app/config"
	apphttp "iq-home/go_beckend/internal/app/http"
	"iq-home/go_beckend/internal/infra/db/postgres"
)

func Run() {
	cfg := config.MustLoad()

	db, err := postgres.New(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	router := apphttp.NewRouter(cfg, db)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("listening on %s", cfg.HTTPAddr)
	log.Fatal(srv.ListenAndServe())
}
