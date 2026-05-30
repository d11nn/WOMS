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
	ID       string
	Customer string
	LineID   string
	Quantity int
	Priority domain.Priority
	Status   domain.OrderStatus
	DueDate  time.Time
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
	OrderID       string             `json:"orderId"`
	SourceOrderID string             `json:"sourceOrderId,omitempty"`
	Customer      string             `json:"customer,omitempty"`
	LineID        string             `json:"lineId"`
	Date          time.Time          `json:"date"`
	Quantity      int                `json:"quantity"`
	Priority      domain.Priority    `json:"priority"`
	Status        domain.OrderStatus `json:"status,omitempty"`
	Locked        bool               `json:"locked"`
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
	if err := validateRequest(req); err != nil {
		return Result{}, err
	}

	orders := sortedOrders(req.Orders)
	ledger := buildCapacityLedger(req)

	var result Result
	for _, order := range orders {
		if err := planOrder(req, &ledger, order, &result); err != nil {
			return Result{}, err
		}
	}

	return result, nil
}

func validateRequest(req Request) error {
	if req.LineID == "" || req.CapacityPerDay <= 0 || req.StartDate.IsZero() {
		return ErrInvalidRequest
	}
	return nil
}

func sortedOrders(input []OrderInput) []OrderInput {
	orders := append([]OrderInput(nil), input...)
	sort.SliceStable(orders, func(i, j int) bool {
		if orders[i].Priority != orders[j].Priority {
			return orders[i].Priority == domain.PriorityHigh
		}
		if !orders[i].DueDate.Equal(orders[j].DueDate) {
			return orders[i].DueDate.Before(orders[j].DueDate)
		}
		return orders[i].ID < orders[j].ID
	})
	return orders
}

type capacityLedger struct {
	highUsed                     map[string]int
	lowUsed                      map[string]int
	newUsed                      map[string]int
	lowByDate                    map[string][]string
	lockedByDate                 map[string][]string
	reportedManualForceConflicts map[string]bool
}

func buildCapacityLedger(req Request) capacityLedger {
	ledger := capacityLedger{
		highUsed:                     map[string]int{},
		lowUsed:                      map[string]int{},
		newUsed:                      map[string]int{},
		lowByDate:                    map[string][]string{},
		lockedByDate:                 map[string][]string{},
		reportedManualForceConflicts: map[string]bool{},
	}
	for _, allocation := range req.ExistingAllocations {
		if allocation.LineID != req.LineID {
			continue
		}
		key := dateKey(allocation.Date)
		if req.ManualForce {
			if allocation.Priority == domain.PriorityHigh || allocation.Locked {
				ledger.lockedByDate[key] = appendUnique(ledger.lockedByDate[key], allocation.OrderID)
			} else {
				ledger.lowByDate[key] = appendUnique(ledger.lowByDate[key], allocation.OrderID)
			}
			continue
		}
		if allocation.Locked {
			ledger.highUsed[key] += allocation.Quantity
			ledger.lockedByDate[key] = appendUnique(ledger.lockedByDate[key], allocation.OrderID)
			continue
		}
		ledger.lowUsed[key] += allocation.Quantity
		ledger.lowByDate[key] = appendUnique(ledger.lowByDate[key], allocation.OrderID)
	}
	return ledger
}

func planOrder(req Request, ledger *capacityLedger, order OrderInput, result *Result) error {
	if err := validateOrder(req.LineID, order); err != nil {
		return err
	}

	start := scheduleStartDate(req.StartDate, req.CurrentDate)
	remaining := order.Quantity
	day := start
	due := truncateDate(order.DueDate)

	for remaining > 0 {
		if day.After(due) && !req.AllowLateCompletion {
			recordLateCapacityConflict(lateCapacityConflict{
				req:       req,
				ledger:    *ledger,
				order:     order,
				result:    result,
				start:     start,
				day:       day,
				due:       due,
				remaining: remaining,
			})
			break
		}

		available := availableCapacity(req, *ledger, day)
		if available <= 0 {
			day = day.AddDate(0, 0, 1)
			continue
		}

		qty := min(remaining, available)
		result.Allocations = append(result.Allocations, Allocation{
			OrderID:  order.ID,
			Customer: order.Customer,
			LineID:   req.LineID,
			Date:     day,
			Quantity: qty,
			Priority: order.Priority,
			Status:   order.Status,
			Locked:   order.Priority == domain.PriorityHigh,
		})
		ledger.newUsed[dateKey(day)] += qty
		remaining -= qty
		if result.FinishDate.Before(day) {
			result.FinishDate = day
		}

		recordManualForceConflict(req, ledger, order, result, day)
	}

	return nil
}

func availableCapacity(req Request, ledger capacityLedger, day time.Time) int {
	key := dateKey(day)
	used := ledger.highUsed[key] + ledger.newUsed[key]
	if !req.ManualForce {
		used += ledger.lowUsed[key]
	}
	return req.CapacityPerDay - used
}

func recordManualForceConflict(req Request, ledger *capacityLedger, order OrderInput, result *Result, day time.Time) {
	if !req.ManualForce {
		return
	}

	key := dateKey(day)
	affected := append([]string{}, ledger.lowByDate[key]...)
	affected = appendUniqueMany(affected, ledger.lockedByDate[key])
	if len(affected) == 0 {
		return
	}

	reportKey := order.ID + "@" + key
	if ledger.reportedManualForceConflicts[reportKey] {
		return
	}

	result.Conflicts = append(result.Conflicts, Conflict{
		OrderID:            order.ID,
		Reason:             "existing allocations require manual review or reschedule",
		EarliestFinishDate: day,
		AffectedOrderIDs:   affected,
	})
	ledger.reportedManualForceConflicts[reportKey] = true
}

type lateCapacityConflict struct {
	req       Request
	ledger    capacityLedger
	order     OrderInput
	result    *Result
	start     time.Time
	day       time.Time
	due       time.Time
	remaining int
}

func recordLateCapacityConflict(conflict lateCapacityConflict) {
	finish := estimateFinishDate(conflict.req, conflict.day, conflict.remaining, conflict.ledger)
	affectedEnd := finish
	if affectedEnd.Before(conflict.due) {
		affectedEnd = conflict.due
	}
	conflict.result.Conflicts = append(conflict.result.Conflicts, Conflict{
		OrderID:            conflict.order.ID,
		Reason:             "capacity cannot satisfy order before due date",
		EarliestFinishDate: finish,
		AffectedOrderIDs:   affectedOrdersBetween(conflict.start, affectedEnd, conflict.ledger.lowByDate),
	})
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

func estimateFinishDate(req Request, start time.Time, remaining int, ledger capacityLedger) time.Time {
	day := truncateDate(start)
	for remaining > 0 {
		available := availableCapacity(req, ledger, day)
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
