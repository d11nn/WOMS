INSERT INTO users (id, username, password_hash, role, line_id)
VALUES
    ('user-admin', 'admin', 'demo', 'admin', NULL),
    ('user-sales', 'sales', 'demo', 'sales', NULL),
    ('user-scheduler-a', 'scheduler-a', 'demo', 'scheduler', 'A'),
    ('user-scheduler-b', 'scheduler-b', 'demo', 'scheduler', 'B'),
    ('user-scheduler-c', 'scheduler-c', 'demo', 'scheduler', 'C'),
    ('user-scheduler-d', 'scheduler-d', 'demo', 'scheduler', 'D')
ON CONFLICT (id) DO NOTHING;

DELETE FROM schedule_allocations WHERE order_id LIKE 'ORD-DEMO-%';
DELETE FROM orders WHERE id LIKE 'ORD-DEMO-%';

INSERT INTO orders (
    id,
    customer,
    line_id,
    quantity,
    priority,
    status,
    due_date,
    created_by,
    created_at,
    updated_at
)
VALUES
    ('ORD-DEMO-1', 'TSMC', 'A', 2500, 'high', '待排程', '2026-06-03', 'user-sales', NOW(), NOW()), 
    ('ORD-DEMO-2', 'ASE', 'A', 2500, 'low', '待排程', '2026-06-05', 'user-sales', NOW(), NOW()),   
    ('ORD-DEMO-3', 'TSMC', 'A', 2500, 'low', '待排程', '2026-06-04', 'user-sales', NOW(), NOW()),  
    ('ORD-DEMO-4', 'TSMC', 'A', 2500, 'low', '待排程', '2026-06-04', 'user-sales', NOW(), NOW()),  
    ('ORD-DEMO-5', 'TSMC', 'A', 2500, 'low', '待排程', '2026-06-04', 'user-sales', NOW(), NOW()),  
    ('ORD-DEMO-6', 'TSMC', 'A', 2500, 'low', '待排程', '2026-06-04', 'user-sales', NOW(), NOW()),  
    ('ORD-DEMO-7', 'ASE', 'A', 2500, 'low', '待排程', '2026-06-05', 'user-sales', NOW(), NOW()),   
    ('ORD-DEMO-8', 'ASE', 'A', 2500, 'low', '待排程', '2026-06-05', 'user-sales', NOW(), NOW()),   
    ('ORD-DEMO-9', 'ASE', 'A', 2500, 'low', '待排程', '2026-06-05', 'user-sales', NOW(), NOW())
ON CONFLICT (id) DO NOTHING;
