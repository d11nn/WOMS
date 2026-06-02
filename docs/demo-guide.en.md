# WOMS Demo Walkthrough Guide

This document is designed for the demo walkthrough scheduled for tomorrow evening (June 3). Using **2026-06-03** as the simulated "current date (Today)", this guide utilizes the updated seed data to showcase all existing Sales and Scheduling functions within the Wafer Order Management and Scheduling system (WOMS).

---

## Initial Database Seed Configuration

To ensure a smooth presentation, we have set the daily capacity of Line A to **10,000**, and completely filled its capacity for June 4 and June 5 with Low-priority orders:
*   **Line A**:
    *   `ORD-DEMO-3` ~ `ORD-DEMO-6` (Low, 2,500 each, Due June 4) - Exactly fills June 4 capacity (10,000).
    *   `ORD-DEMO-2`, `ORD-DEMO-7`, `ORD-DEMO-8`, `ORD-DEMO-9` (Low, 2,500 each, Due June 5) - Exactly fills June 5 capacity (10,000).
*   **Line B**:
    *   `ORD-DEMO-1` (High, 2,500, Due June 4)

---

## Phase 1: Sales Order Modifications and Creation (Sales Persona)

### Step 1. Login
1. On the login page, select the Role: **Sales**. Log in with username `sales` and password `demo`.
2. Once logged in, you can see all initial orders in the order list.

### Step 2. Create Low-Priority Order
*   **Action**: Click the "Create Order" button and create a new Low-priority order:
    *   **Customer**: `TSMC-Demo`
    *   **Production Line**: `A`
    *   **Quantity**: `2500`
    *   **Priority**: `Low`
    *   **Due Date**: `2026-06-06`
*   **Objective**: Showcases the capability of Sales to create a basic low-priority order. This order will be scheduled for June 6.

### Step 3. Edit Pending Order
*   **Action**:
    1. Locate the newly created low-priority order in the Line A backlog / order list.
    2. Click the "Order Edit" button to expand the modification form.
    3. Change the **Due Date to June 5** and **Quantity to 5000**, and click "Resubmit".
*   **Objective**: Showcases the "Resubmit" editing flow for pending orders.

---

## Phase 2: View Backlog, Auto-Scheduling, and Conflict Alerts (Scheduler Persona)

### Step 4. Log in and View Priorities
1. Log out from the Sales page. Log in with the Role: **Scheduler**, using username `scheduler-a` and password `demo`.
2. Go to the scheduler calendar page and select **Line A**.
3. In the Backlog list, you can observe:
    *   Visual distinction (labels/colors) between High and Low priority orders.
    *   The newly edited low-priority order with 5,000 quantity due on June 5.

### Step 5. Run Scheduling Algorithm
*   **Action**: Click "Start Auto-Scheduling" or "Preview".
*   **Algorithm Logic Observation**:
    *   Since June 4 is filled by `ORD-DEMO-3`~`6` (10,000) and June 5 is filled by `ORD-DEMO-7`~`8` (10,000), scheduling the newly edited 5,000 order before June 5 will exceed total capacity.
    *   The preview result will display a red **"Late Capacity Conflict"** alert, indicating the low-priority order cannot be completed in time, and listing the affected order IDs.

---

## Phase 3: Preemption and Deferral / Rejection (Sales & Scheduler Collaboration)

### Step 6. Preemption Logic Walkthrough
1. Log in as Sales, locate the conflicting order, and click "Order Edit".
2. Change the priority to **"High"** and click "Resubmit".
3. Log back in as Scheduler and preview the schedule.
4. **Observe Preemption**:
    *   Because the order is now High-priority, the scheduling algorithm prioritizes it at the top.
    *   The high-priority order preempts capacity on June 4 and 5.
    *   The low-priority order `ORD-DEMO-8` is pushed to June 6.
    *   Because `ORD-DEMO-8` is due on June 5, the system automatically flags a **Late Capacity Conflict** for `ORD-DEMO-8` (highlighting the delayed low-priority order in red).

### Step 7. Conflict Management (Confirm and Defer)
*   **Action (Sales View)**:
    1. As Sales, click the conflict alert and choose "Confirm with Deferral".
    2. Defer the preempted `ORD-DEMO-8` order to **"Pending Sales Action"** (StatusRejected) to release capacity.
    3. Submit. The scheduling conflict warning disappears, and the high-priority order is successfully scheduled.

### Step 8. Showcase Scheduler Rejection
*   **Action (Scheduler View)**:
    *   Instead of letting Sales defer, the scheduler can also tick the conflicting order and click **"Reject Selected Orders"**.
    *   This sets the order status to **"Pending Sales Action"** (StatusRejected) and returns it to Sales, releasing the capacity immediately. This demonstrates the Scheduler's rejection authority.

---

## Phase 4: Production Progress and Second-Wave Conflict

### Step 9. Start Production and Partial Completion
1. As Scheduler, select the scheduled order `ORD-DEMO-3` (2,500) and click **"Start Production"**. Its status transitions to `In Progress` (StatusInProgress), locking its scheduled slots.
2. Click **"Confirm Production"**:
    *   Input a produced quantity of `500` (representing partial production) and submit.
3. **Observe Secondary Conflict**:
    *   `ORD-DEMO-3` is marked `Completed` (StatusCompleted), and a Remainder Order of `2,000` is automatically created with `Pending` (StatusPending) status.
    *   Since the remainder order inherits the original tight due date **(June 4)**, and June 4 capacity is already fully booked by other orders, the 2,000 remainder order immediately triggers a **new Late Capacity Conflict** upon auto-scheduling.
    *   This completes the loop, demonstrating how partial delivery returns to the backlog and triggers subsequent scheduling and capacity alerts in production reality.
