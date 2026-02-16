package pdf

import "iq-home/go_beckend/internal/domain/quote"

type Generator interface {
	Generate(q quote.Quote) ([]byte, error)
}
