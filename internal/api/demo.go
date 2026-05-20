package api

import (
	"time"

	"github.com/d11nn/woms/internal/domain"
)

func NewDemoMemoryStore() *MemoryStore {
	store := NewMemoryStore()
	seed := []createOrderRequest{
		{Customer: "TSMC Demo", LineID: "A", Quantity: 2500, Priority: domain.PriorityHigh, DueDate: "2026-05-03"},
		{Customer: "ACME Silicon", LineID: "A", Quantity: 1800, Priority: domain.PriorityLow, DueDate: "2026-05-04"},
		{Customer: "Northstar Fabless", LineID: "B", Quantity: 2200, Priority: domain.PriorityLow, DueDate: "2026-05-05"},
		{Customer: "Orion Devices", LineID: "C", Quantity: 1250, Priority: domain.PriorityHigh, DueDate: "2026-05-02"},
		{Customer: "Helio Sensors", LineID: "D", Quantity: 900, Priority: domain.PriorityLow, DueDate: "2026-05-06"},
	}
	for _, req := range seed {
		_, _ = store.CreateOrder(req, "user-sales")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if order, ok := store.orders["ORD-0000002"]; ok {
		order.Status = domain.StatusScheduled
		store.orders[order.ID] = order
		store.allocations = append(store.allocations, domain.ScheduleAllocation{
			OrderID:  order.ID,
			LineID:   order.LineID,
			Date:     mustDemoDate("2026-05-01"),
			Quantity: order.Quantity,
			Priority: order.Priority,
			Locked:   false,
		})
	}
	if order, ok := store.orders["ORD-0000004"]; ok {
		order.Status = domain.StatusInProgress
		store.orders[order.ID] = order
	}
	return store
}

func mustDemoDate(value string) time.Time {
	parsed, err := time.Parse(dateLayout, value)
	if err != nil {
		panic(err)
	}
	return parsed
}
