CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('admin', 'sales', 'scheduler')),
    line_id TEXT,
    disabled BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS production_lines (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    capacity_per_day INTEGER NOT NULL CHECK (capacity_per_day > 0),
    timezone TEXT NOT NULL DEFAULT 'Asia/Taipei',
    schedule_revision BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS orders (
    id TEXT PRIMARY KEY,
    customer TEXT NOT NULL,
    line_id TEXT NOT NULL REFERENCES production_lines(id),
    quantity INTEGER NOT NULL CHECK (quantity BETWEEN 25 AND 2500),
    priority TEXT NOT NULL CHECK (priority IN ('low', 'high')),
    status TEXT NOT NULL CHECK (status IN ('待排程', '已排程', '生產中', '已完成', '需業務處理', '已取消')),
    due_date DATE NOT NULL,
    note TEXT,
    created_by TEXT NOT NULL REFERENCES users(id),
    source_order TEXT REFERENCES orders(id),
    rejection_reason TEXT,
    rejected_by TEXT REFERENCES users(id),
    rejected_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS schedule_jobs (
    id TEXT PRIMARY KEY,
    line_id TEXT NOT NULL REFERENCES production_lines(id),
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled')),
    message TEXT,
    source TEXT,
    preview_id TEXT,
    request_hash TEXT,
    line_revision BIGINT NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    order_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS schedule_previews (
    id TEXT PRIMARY KEY,
    actor_id TEXT NOT NULL REFERENCES users(id),
    actor_role TEXT NOT NULL CHECK (actor_role IN ('admin', 'sales', 'scheduler')),
    line_id TEXT NOT NULL REFERENCES production_lines(id),
    line_revision BIGINT NOT NULL,
    request_hash TEXT NOT NULL,
    request JSONB NOT NULL,
    allocations JSONB NOT NULL,
    conflicts JSONB NOT NULL,
    draft_order JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS schedule_allocations (
    id BIGSERIAL PRIMARY KEY,
    order_id TEXT NOT NULL REFERENCES orders(id),
    line_id TEXT NOT NULL REFERENCES production_lines(id),
    allocation_date DATE NOT NULL,
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    priority TEXT NOT NULL CHECK (priority IN ('low', 'high')),
    locked BOOLEAN NOT NULL DEFAULT FALSE,
    status TEXT CHECK (status IN ('待排程', '已排程', '生產中', '已完成', '需業務處理', '已取消'))
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id TEXT PRIMARY KEY,
    actor_id TEXT NOT NULL REFERENCES users(id),
    action TEXT NOT NULL,
    resource TEXT NOT NULL,
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL
);

ALTER TABLE production_lines ADD COLUMN IF NOT EXISTS timezone TEXT NOT NULL DEFAULT 'Asia/Taipei';
ALTER TABLE production_lines ADD COLUMN IF NOT EXISTS schedule_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS note TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS rejection_reason TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS rejected_by TEXT REFERENCES users(id);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS rejected_at TIMESTAMPTZ;
ALTER TABLE schedule_jobs ADD COLUMN IF NOT EXISTS source TEXT;
ALTER TABLE schedule_jobs ADD COLUMN IF NOT EXISTS preview_id TEXT;
ALTER TABLE schedule_jobs ADD COLUMN IF NOT EXISTS request_hash TEXT;
ALTER TABLE schedule_jobs ADD COLUMN IF NOT EXISTS line_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE schedule_jobs ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE schedule_jobs ADD COLUMN IF NOT EXISTS order_ids JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE schedule_jobs ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ;
ALTER TABLE schedule_jobs ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;
ALTER TABLE schedule_allocations ADD COLUMN IF NOT EXISTS status TEXT;
UPDATE schedule_allocations
SET status = '已排程'
WHERE status IS NULL;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('admin', 'sales', 'scheduler'));
ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK (status IN ('待排程', '已排程', '生產中', '已完成', '需業務處理', '已取消'));
ALTER TABLE schedule_jobs DROP CONSTRAINT IF EXISTS schedule_jobs_status_check;
ALTER TABLE schedule_jobs ADD CONSTRAINT schedule_jobs_status_check CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled'));
ALTER TABLE schedule_allocations DROP CONSTRAINT IF EXISTS schedule_allocations_status_check;
ALTER TABLE schedule_allocations ADD CONSTRAINT schedule_allocations_status_check CHECK (status IN ('待排程', '已排程', '生產中', '已完成', '需業務處理', '已取消'));

INSERT INTO production_lines (id, name, capacity_per_day, timezone)
VALUES
    ('A', 'Line A', 10000, 'Asia/Taipei'),
    ('B', 'Line B', 10000, 'Asia/Taipei'),
    ('C', 'Line C', 10000, 'Asia/Taipei'),
    ('D', 'Line D', 10000, 'Europe/London')
ON CONFLICT (id) DO NOTHING;

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
