package api

import "github.com/d11nn/woms/internal/domain"

const defaultDueDate = "2026-06-04"

func NewDemoMemoryStore() *MemoryStore {
	store := NewMemoryStore()
	seed := []createOrderRequest{
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityHigh, DueDate: defaultDueDate},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityHigh, DueDate: defaultDueDate},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityLow, DueDate: defaultDueDate},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityLow, DueDate: defaultDueDate},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityLow, DueDate: defaultDueDate},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityLow, DueDate: defaultDueDate},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityLow, DueDate: defaultDueDate},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityLow, DueDate: defaultDueDate},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityLow, DueDate: defaultDueDate},
	}
	for _, req := range seed {
		_, _ = store.CreateOrder(req, "user-sales")
	}
	return store
}
