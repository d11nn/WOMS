package scheduler

import (
	"errors"
	"sort"
	"time"

	"github.com/d11nn/woms/internal/domain"
)

const dateLayout = "2006-01-02"

var ErrInvalidRequest = errors.New("invalid schedule request")

type OrderInput struct {
	ID                 string
	Customer           string
	LineID             string
	Quantity           int
	Priority           domain.Priority
	Status             domain.OrderStatus
	DueDate            time.Time
	CreatedAtTimestamp int64
}

type ExistingAllocation struct {
	OrderID  string
	LineID   string
	Date     time.Time
	Quantity int
	Priority domain.Priority
	Locked   bool
}

type Allocation struct {
	OrderID            string             `json:"orderId"`
	SourceOrderID      string             `json:"sourceOrderId,omitempty"`
	Customer           string             `json:"customer,omitempty"`
	LineID             string             `json:"lineId"`
	Date               time.Time          `json:"date"`
	Quantity           int                `json:"quantity"`
	Priority           domain.Priority    `json:"priority"`
	Status             domain.OrderStatus `json:"status,omitempty"`
	Locked             bool               `json:"locked"`
	DueDate            time.Time          `json:"dueDate,omitempty"`
	CreatedAtTimestamp int64              `json:"createdAtTimestamp,omitempty"`
}

type Conflict struct {
	OrderID            string    `json:"orderId"`
	Reason             string    `json:"reason"`
	EarliestFinishDate time.Time `json:"earliestFinishDate"`
	AffectedOrderIDs   []string  `json:"affectedOrderIds,omitempty"`
}

type Request struct {
	LineID              string
	CapacityPerDay      int
	StartDate           time.Time
	CurrentDate         time.Time
	Orders              []OrderInput
	ExistingAllocations []ExistingAllocation
	ManualForce         bool
	ForceReason         string
	AllowLateCompletion bool
}

type Result struct {
	Allocations []Allocation `json:"allocations"`
	Conflicts   []Conflict   `json:"conflicts"`
	FinishDate  time.Time    `json:"finishDate"`
}

func Plan(req Request) (Result, error) {
	if req.LineID == "" || req.CapacityPerDay <= 0 || req.StartDate.IsZero() {
		return Result{}, ErrInvalidRequest
	}

	start := scheduleStartDate(req.StartDate, req.CurrentDate)
	orders := append([]OrderInput(nil), req.Orders...)
	sort.SliceStable(orders, func(i, j int) bool {
		if orders[i].Priority != orders[j].Priority {
			return orders[i].Priority == domain.PriorityHigh
		}
		if !orders[i].DueDate.Equal(orders[j].DueDate) {
			return orders[i].DueDate.Before(orders[j].DueDate)
		}
		if orders[i].CreatedAtTimestamp != orders[j].CreatedAtTimestamp {
			return orders[i].CreatedAtTimestamp < orders[j].CreatedAtTimestamp
		}
		return orders[i].ID < orders[j].ID
	})

	highUsed := map[string]int{}
	lowUsed := map[string]int{}
	lowByDate := map[string][]string{}
	lockedByDate := map[string][]string{}
	for _, allocation := range req.ExistingAllocations {
		if allocation.LineID != req.LineID {
			continue
		}
		key := dateKey(allocation.Date)
		if req.ManualForce {
			if allocation.Priority == domain.PriorityHigh || allocation.Locked {
				lockedByDate[key] = appendUnique(lockedByDate[key], allocation.OrderID)
			} else {
				lowByDate[key] = appendUnique(lowByDate[key], allocation.OrderID)
			}
			continue
		}
		if allocation.Locked {
			highUsed[key] += allocation.Quantity
			lockedByDate[key] = appendUnique(lockedByDate[key], allocation.OrderID)
			continue
		}
		lowUsed[key] += allocation.Quantity
		lowByDate[key] = appendUnique(lowByDate[key], allocation.OrderID)
	}

	var result Result
	newUsed := map[string]int{}
	reportedAffected := map[string]bool{}

	for _, order := range orders {
		if err := validateOrder(req.LineID, order); err != nil {
			return Result{}, err
		}
		remaining := order.Quantity
		day := start
		due := truncateDate(order.DueDate)

		for remaining > 0 {
			key := dateKey(day)
			if day.After(due) {
				if req.AllowLateCompletion {
					// Conflict solutions intentionally show the earliest completion plan,
					// even when that plan finishes after the customer due date.
				} else {
					finish := estimateFinishDate(req, order, day, remaining, highUsed, lowUsed, newUsed)
					affectedEnd := finish
					if affectedEnd.Before(due) {
						affectedEnd = due
					}
					result.Conflicts = append(result.Conflicts, Conflict{
						OrderID:            order.ID,
						Reason:             "capacity cannot satisfy order before due date",
						EarliestFinishDate: finish,
						AffectedOrderIDs:   affectedOrdersBetween(start, affectedEnd, lowByDate),
					})
					break
				}
			}

			used := highUsed[key] + newUsed[key]
			if !req.ManualForce {
				used += lowUsed[key]
			}
			available := req.CapacityPerDay - used
			if available <= 0 {
				day = day.AddDate(0, 0, 1)
				continue
			}

			qty := min(remaining, available)
			result.Allocations = append(result.Allocations, Allocation{
				OrderID:            order.ID,
				Customer:           order.Customer,
				LineID:             req.LineID,
				Date:               day,
				Quantity:           qty,
				Priority:           order.Priority,
				Status:             order.Status,
				Locked:             order.Priority == domain.PriorityHigh,
				DueDate:            order.DueDate,
				CreatedAtTimestamp: order.CreatedAtTimestamp,
			})
			newUsed[key] += qty
			remaining -= qty
			if result.FinishDate.Before(day) {
				result.FinishDate = day
			}

			if req.ManualForce {
				affected := append([]string{}, lowByDate[key]...)
				if req.ManualForce {
					affected = appendUniqueMany(affected, lockedByDate[key])
				}
				if len(affected) > 0 && !reportedAffected[order.ID+"@"+key] {
					result.Conflicts = append(result.Conflicts, Conflict{
						OrderID:            order.ID,
						Reason:             "existing allocations require manual review or reschedule",
						EarliestFinishDate: day,
						AffectedOrderIDs:   affected,
					})
					reportedAffected[order.ID+"@"+key] = true
				}
			}
		}
	}

	return result, nil
}

type ConfirmationResult struct {
	Completed bool
	Remainder *domain.Order
}

func ConfirmProduction(order domain.Order, produced int, now time.Time) (ConfirmationResult, error) {
	if produced < 0 || produced > order.Quantity {
		return ConfirmationResult{}, ErrInvalidRequest
	}
	if produced == order.Quantity {
		return ConfirmationResult{Completed: true}, nil
	}

	remainder := order
	remainder.ID = ""
	remainder.Quantity = order.Quantity - produced
	remainder.Status = domain.StatusPending
	remainder.SourceOrder = order.ID
	remainder.CreatedAt = now
	remainder.UpdatedAt = now
	return ConfirmationResult{Completed: false, Remainder: &remainder}, nil
}

func validateOrder(lineID string, order OrderInput) error {
	if order.ID == "" || order.LineID != lineID || order.Quantity <= 0 || order.DueDate.IsZero() {
		return ErrInvalidRequest
	}
	return nil
}

func estimateFinishDate(req Request, order OrderInput, start time.Time, remaining int, highUsed, lowUsed, newUsed map[string]int) time.Time {
	day := truncateDate(start)
	for remaining > 0 {
		key := dateKey(day)
		used := highUsed[key] + newUsed[key]
		if !req.ManualForce {
			used += lowUsed[key]
		}
		available := req.CapacityPerDay - used
		if available > 0 {
			remaining -= min(remaining, available)
		}
		if remaining > 0 {
			day = day.AddDate(0, 0, 1)
		}
	}
	return day
}

func truncateDate(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func scheduleStartDate(requested, current time.Time) time.Time {
	start := truncateDate(requested)
	if current.IsZero() {
		return start
	}
	earliest := truncateDate(current).AddDate(0, 0, 1)
	if start.Before(earliest) {
		return earliest
	}
	return start
}

func affectedOrdersBetween(start, due time.Time, byDate map[string][]string) []string {
	affected := []string{}
	for day := truncateDate(start); !day.After(due); day = day.AddDate(0, 0, 1) {
		affected = appendUniqueMany(affected, byDate[dateKey(day)])
	}
	return affected
}

func dateKey(value time.Time) string {
	return truncateDate(value).Format(dateLayout)
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueMany(values []string, additions []string) []string {
	for _, value := range additions {
		values = appendUnique(values, value)
	}
	return values
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
