package api

import (
	"time"

	"github.com/d11nn/woms/internal/domain"
)

func NewDemoMemoryStore() *MemoryStore {
	store := NewMemoryStore()
	seed := []struct {
		id       string
		customer string
		quantity int
		priority domain.Priority
		dueDate  string
		note     string
	}{
		{"ORD-DEMO-1", "TSMC", 2500, domain.PriorityHigh, "2026-06-03", "Demo batch 1: high-priority same-day capacity fill."},
		{"ORD-DEMO-2", "TSMC", 2500, domain.PriorityHigh, "2026-06-03", "Demo batch 1: high-priority same-day capacity fill."},
		{"ORD-DEMO-3", "TSMC", 2500, domain.PriorityHigh, "2026-06-03", "Demo batch 1: high-priority same-day capacity fill."},
		{"ORD-DEMO-4", "TSMC", 2500, domain.PriorityHigh, "2026-06-03", "Demo batch 1: high-priority same-day capacity fill."},
		{"ORD-DEMO-5", "UMC", 2500, domain.PriorityLow, "2026-06-04", "Demo batch 2: low-priority candidate for displacement."},
		{"ORD-DEMO-6", "UMC", 2500, domain.PriorityLow, "2026-06-04", "Demo batch 2: low-priority candidate for displacement."},
		{"ORD-DEMO-7", "VIS", 2500, domain.PriorityLow, "2026-06-05", "Demo batch 3: backlog ordering by due date."},
		{"ORD-DEMO-8", "VIS", 2000, domain.PriorityLow, "2026-06-05", "Demo batch 3: partial-capacity waterline case."},
		{"ORD-DEMO-9", "ASE", 2500, domain.PriorityLow, "2026-06-06", "Demo batch 4: later low-priority tail order."},
	}
	now := nowUTC()
	for _, item := range seed {
		dueDate, _ := time.Parse(dateLayout, item.dueDate)
		store.orders[item.id] = domain.Order{
			ID:        item.id,
			Customer:  item.customer,
			LineID:    "A",
			Quantity:  item.quantity,
			Priority:  item.priority,
			Status:    domain.StatusPending,
			DueDate:   dueDate,
			Note:      item.note,
			CreatedBy: "user-sales",
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	return store
}
