package api

import "github.com/d11nn/woms/internal/domain"

const defaultDueDate = "2026-06-04"

func NewDemoMemoryStore() *MemoryStore {
	store := NewMemoryStore()
	seed := []createOrderRequest{
		{Customer: "TSMC", LineID: "B", Quantity: 2500, Priority: domain.PriorityHigh, DueDate: "2026-06-04"},
		{Customer: "UMC", LineID: "B", Quantity: 2500, Priority: domain.PriorityHigh, DueDate: "2026-06-05"},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityLow, DueDate: "2026-06-04"},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityLow, DueDate: "2026-06-04"},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityLow, DueDate: "2026-06-04"},
		{Customer: "TSMC", LineID: "A", Quantity: 2500, Priority: domain.PriorityLow, DueDate: "2026-06-04"},
		{Customer: "ASE", LineID: "A", Quantity: 5000, Priority: domain.PriorityLow, DueDate: "2026-06-05"},
		{Customer: "ASE", LineID: "A", Quantity: 5000, Priority: domain.PriorityLow, DueDate: "2026-06-05"},
		{Customer: "UMC", LineID: "B", Quantity: 5000, Priority: domain.PriorityLow, DueDate: "2026-06-06"},
	}
	for _, req := range seed {
		_, _ = store.CreateOrder(req, "user-sales")
	}
	return store
}
