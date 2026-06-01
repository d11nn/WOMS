package api

import (
	"testing"

	"github.com/d11nn/woms/internal/auth"
	"github.com/d11nn/woms/internal/domain"
)

func TestNewDemoMemoryStore(t *testing.T) {
	store := NewDemoMemoryStore()
	if store == nil {
		t.Fatal("expected NewDemoMemoryStore to return a MemoryStore, got nil")
	}
	orders := store.ListOrders(auth.Claims{Role: domain.RoleAdmin})
	if len(orders) != 9 {
		t.Errorf("expected 9 seeded orders, got %d", len(orders))
	}
	byID := map[string]domain.Order{}
	for _, order := range orders {
		byID[order.ID] = order
	}
	if byID["ORD-DEMO-1"].Priority != domain.PriorityHigh || byID["ORD-DEMO-1"].DueDate.Format(dateLayout) != "2026-06-03" {
		t.Errorf("expected ORD-DEMO-1 to fill the high-priority 2026-06-03 batch, got %+v", byID["ORD-DEMO-1"])
	}
	if byID["ORD-DEMO-8"].Quantity != 2000 || byID["ORD-DEMO-8"].DueDate.Format(dateLayout) != "2026-06-05" {
		t.Errorf("expected ORD-DEMO-8 to cover partial-capacity backlog demo, got %+v", byID["ORD-DEMO-8"])
	}
	if byID["ORD-DEMO-9"].Note == "" {
		t.Errorf("expected demo orders to include operator demo notes")
	}
}
