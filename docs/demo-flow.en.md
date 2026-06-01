# ORD-DEMO Scheduling Demo Flow

This flow uses `ORD-DEMO-1` through `ORD-DEMO-9` from `db/migrations/002_seed_demo.sql` and `NewDemoMemoryStore()`. The demo is split between Sales and Scheduler actions to show high-priority ordering, due-date ordering, daily-capacity waterlines, low-priority displacement, and Sales draft conflict handling.

## Seed Data

| Order | Customer | Quantity | Priority | Due date | Demo purpose |
| --- | --- | ---: | --- | --- | --- |
| ORD-DEMO-1 | TSMC | 2,500 | high | 2026-06-03 | Fill same-day high-priority capacity |
| ORD-DEMO-2 | TSMC | 2,500 | high | 2026-06-03 | Fill same-day high-priority capacity |
| ORD-DEMO-3 | TSMC | 2,500 | high | 2026-06-03 | Fill same-day high-priority capacity |
| ORD-DEMO-4 | TSMC | 2,500 | high | 2026-06-03 | Fill same-day high-priority capacity |
| ORD-DEMO-5 | UMC | 2,500 | low | 2026-06-04 | Low-priority movable candidate |
| ORD-DEMO-6 | UMC | 2,500 | low | 2026-06-04 | Low-priority movable candidate |
| ORD-DEMO-7 | VIS | 2,500 | low | 2026-06-05 | Backlog ordered by due date |
| ORD-DEMO-8 | VIS | 2,000 | low | 2026-06-05 | Partial-capacity waterline case |
| ORD-DEMO-9 | ASE | 2,500 | low | 2026-06-06 | Later low-priority tail order |

## Sales Batches

1. Confirm the Sales page is filtered to pending orders. The June 3, 2026 calendar should show `ORD-DEMO-1` through `ORD-DEMO-4`, and every calendar card should include customer, quantity, and due date.
2. Add one high-priority draft order: customer `ACME`, line `A`, quantity `2500`, due date `2026-06-03`.
3. Preview the draft. `PREVIEW-DRAFT` should have a highlighted outline and the preview should enter conflict handling.
4. In conflict handling, choose `預覽最早完成解法` to inspect the earliest completion plan, or choose `取消選取目前訂單`. The note prompt defaults to `發生衝突，請修改！`, and confirming moves the draft to Sales follow-up.

## Scheduler Batches

1. Select `ORD-DEMO-1` through `ORD-DEMO-4`, preview from 2026-06-03, and submit. This fills the 10,000 wafer high-priority capacity on one day.
2. Select `ORD-DEMO-5` through `ORD-DEMO-6`, preview from 2026-06-04, and submit. This shows low-priority work being placed on the next day.
3. Select `ORD-DEMO-7` through `ORD-DEMO-9`, preview from 2026-06-04 or 2026-06-05, and inspect due-date ordering plus the 2,000 wafer partial-capacity case.
4. If Sales later inserts another high-priority June 3 draft, both Sales and Scheduler conflict panels should expose the earliest completion option and show moved/preview markers on the calendar.

## Production Flow

1. Scheduler clicks a scheduled calendar item to start production. The order moves to `生產中` and the allocation is locked for that day.
2. Production can report a partial quantity. Remaining quantity returns to pending while retaining the source relationship.
3. Schedule the remainder again to show child-order or quantity-change markers on the calendar.

## Validation Points

- High-priority orders are scheduled before low-priority orders.
- Same-priority orders are stably sorted by due date and creation order.
- The 10,000 wafer daily waterline correctly shows remaining capacity or overload.
- Both Sales and Scheduler conflict panels can preview the earliest completion plan.
- Sales draft cancellation opens a note prompt with `發生衝突，請修改！` as the default note.
