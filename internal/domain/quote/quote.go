package quote

import "time"

type Quote struct {
	Number    string
	CreatedAt time.Time
	Customer  Customer
	Items     []Item

	DiscountPercent int
	Subtotal        int64 
	DiscountAmount  int64
	Total           int64
	Comment         string
}

type Customer struct {
	Name  string
	Phone string
	City  string
}
