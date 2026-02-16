package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"iq-home/go_beckend/internal/domain/quote"
	pdfgen "iq-home/go_beckend/internal/domain/quote/pdf/gofpdf"
)

type CreateQuoteRequest struct {
	Customer struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
		City  string `json:"city"`
	} `json:"customer"`
	Items []struct {
		ProductID int64 `json:"product_id"`
		Qty       int   `json:"qty"`
		Name      string `json:"name"`       // временно: можно передавать с фронта/n8n
		UnitPrice int64  `json:"unit_price"` // временно: потом будем тянуть из БД
	} `json:"items"`
	DiscountPercent int    `json:"discount_percent"`
	Comment         string `json:"comment"`
}

func (h *Handlers) CreateQuote(w http.ResponseWriter, r *http.Request) {
	var req CreateQuoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	q := quote.Quote{
		Number:    "NF-1",
		CreatedAt: time.Now(),
		Customer: quote.Customer{
			Name:  req.Customer.Name,
			Phone: req.Customer.Phone,
			City:  req.Customer.City,
		},
		DiscountPercent: req.DiscountPercent,
		Comment:         req.Comment,
	}

	var subtotal int64
	for _, it := range req.Items {
		if it.Qty <= 0 {
			http.Error(w, "qty must be > 0", http.StatusBadRequest)
			return
		}
		line := it.UnitPrice * int64(it.Qty)
		q.Items = append(q.Items, quote.Item{
			ProductID: it.ProductID,
			Name:      it.Name,
			Qty:       it.Qty,
			UnitPrice: it.UnitPrice,
			LineTotal: line,
		})
		subtotal += line
	}

	q.Subtotal = subtotal
	q.DiscountAmount = subtotal * int64(q.DiscountPercent) / 100
	q.Total = subtotal - q.DiscountAmount

	gen := pdfgen.New()
	pdfBytes, err := gen.Generate(q)
	if err != nil {
		http.Error(w, "pdf generation failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="KP-NF-1.pdf"`)
	w.WriteHeader(http.StatusOK)
	w.Write(pdfBytes)
}
