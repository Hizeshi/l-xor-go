package gofpdf

import (
	"bytes"
	"fmt"
	"log"
	"time"

	"github.com/jung-kurt/gofpdf"
	"iq-home/go_beckend/internal/domain/quote"
)

type Generator struct{}

func New() *Generator { return &Generator{} }

func (g *Generator) Generate(q quote.Quote) ([]byte, error) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetTitle("Коммерческое предложение", false)
	regularFont := "internal/domain/quote/pdf/gofpdf/fonts/DejaVuSans.ttf"
	boldFont := "internal/domain/quote/pdf/gofpdf/fonts/DejaVuSans-Bold.ttf"
	log.Printf("quote pdf: load fonts regular=%s bold=%s", regularFont, boldFont)
	pdf.AddUTF8Font("DejaVu", "", regularFont)
	pdf.AddUTF8Font("DejaVu", "B", boldFont)
	if err := pdf.Error(); err != nil {
		return nil, err
	}
	pdf.AddPage()

	pdf.SetFont("DejaVu", "B", 16)
	pdf.Cell(0, 10, "Коммерческое предложение")
	pdf.Ln(8)

	pdf.SetFont("DejaVu", "", 11)
	pdf.Cell(0, 6, fmt.Sprintf("№ %s от %s", q.Number, q.CreatedAt.Format("02.01.2006")))
	pdf.Ln(6)

	if q.Customer.Name != "" || q.Customer.Phone != "" {
		pdf.Cell(0, 6, fmt.Sprintf("Клиент: %s %s", q.Customer.Name, q.Customer.Phone))
		pdf.Ln(6)
	}

	pdf.Ln(4)
	pdf.SetFont("DejaVu", "B", 11)
	pdf.Cell(120, 7, "Товар")
	pdf.Cell(20, 7, "Кол-во")
	pdf.Cell(25, 7, "Цена")
	pdf.Cell(25, 7, "Сумма")
	pdf.Ln(8)

	pdf.SetFont("DejaVu", "", 10)
	for _, it := range q.Items {
		pdf.Cell(120, 6, trim(it.Name, 65))
		pdf.Cell(20, 6, fmt.Sprintf("%d", it.Qty))
		pdf.Cell(25, 6, fmt.Sprintf("%d", it.UnitPrice))
		pdf.Cell(25, 6, fmt.Sprintf("%d", it.LineTotal))
		pdf.Ln(6)
	}

	pdf.Ln(4)
	pdf.SetFont("DejaVu", "B", 11)
	pdf.Cell(0, 7, fmt.Sprintf("Итого: %d", q.Total))
	pdf.Ln(6)

	pdf.SetFont("DejaVu", "", 9)
	pdf.Cell(0, 5, "L-Xor • Электрика")
	pdf.Ln(5)
	pdf.Cell(0, 5, fmt.Sprintf("Сформировано: %s", time.Now().Format(time.RFC3339)))

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		log.Printf("quote pdf: output failed: %v", err)
		return nil, err
	}
	return buf.Bytes(), nil
}

func trim(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
