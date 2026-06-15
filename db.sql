CREATE TABLE notebooks (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL,
    name VARCHAR(255) NOT NULL,
    namespace VARCHAR(255) NOT NULL,

    storage_size VARCHAR(50) NOT NULL,
    pvc_name  VARCHAR(50) NOT NULL,

    cpu_request DECIMAL(6,4) NOT NULL,
    cpu_limit DECIMAL(6,4) NOT NULL,

    memory_request VARCHAR(20) NOT NULL,
    memory_limit VARCHAR(20) NOT NULL,

    gpu_type VARCHAR(50),
    gpu_request INTEGER,
    gpu_limit INTEGER,

    template_name VARCHAR(255),
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    picked_at TIMESTAMP WITHOUT TIME ZONE,

    events VARCHAR(255)[] NOT NULL DEFAULT ARRAY['scheduled'],

    CONSTRAINT unique_name_namespace UNIQUE (name, namespace),
    CONSTRAINT unique_pvc_namespace UNIQUE (pvc_name, namespace)
);

ALTER TABLE notebooks ADD COLUMN updated_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP;

ALTER TABLE notebooks ADD COLUMN instance_type VARCHAR(100);

CREATE TABLE profiles (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL,
    email VARCHAR(255) NOT NULL,
    total_paid_credit DECIMAL(26,15) NOT NULL DEFAULT 0,
    can_create_gpu_notebook BOOLEAN NOT NULL DEFAULT true,
    aaa_and_opencost_synced_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    pending_deduction DECIMAL(26, 15) NOT NULL DEFAULT 0,
    last_sync_balance DECIMAL(26, 15) NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT profiles_unique_user_id UNIQUE (user_id)
);

CREATE TABLE IF NOT EXISTS failed_aaa_requests (
    id BIGSERIAL PRIMARY KEY,
    user_id VARCHAR(255) NOT NULL,
    cost DECIMAL(26, 15) NOT NULL,
    requested_at TIMESTAMP NOT NULL,
    error_message TEXT,
    status_code INT,
    request_payload JSONB,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    resolved BOOLEAN DEFAULT FALSE,
    resolved_at TIMESTAMP
);

CREATE TABLE bookings (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL,
    category_name VARCHAR(50) NOT NULL,
    resource_type VARCHAR(10) NOT NULL,
    slot_key VARCHAR(50) NOT NULL,
    slot_keys VARCHAR(50)[] NOT NULL DEFAULT '{}',
    extension_used BOOLEAN NOT NULL DEFAULT false,
    notebook_name VARCHAR(255) NOT NULL,
    slot_date DATE NOT NULL,
    slot_start TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    slot_end TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    status VARCHAR(30) NOT NULL DEFAULT 'scheduled',
    notebook_id BIGINT REFERENCES notebooks(id) ON DELETE SET NULL,
    session_started_at TIMESTAMP WITHOUT TIME ZONE,
    session_ended_at TIMESTAMP WITHOUT TIME ZONE,
    cleanup_completed_at TIMESTAMP WITHOUT TIME ZONE,
    shutdown_warning_sent_at TIMESTAMP WITHOUT TIME ZONE,
    ready_at TIMESTAMPTZ,
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Existing deployments: add column (safe to re-run).
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS ready_at TIMESTAMPTZ;
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS slot_keys VARCHAR(50)[] NOT NULL DEFAULT '{}';
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS extension_used BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS file_url TEXT;
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS git_url TEXT;
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS git_token_secret_name VARCHAR(253);
UPDATE bookings SET slot_keys = ARRAY[slot_key] WHERE slot_keys = '{}'::varchar[] OR slot_keys IS NULL;

ALTER TABLE notebooks
    ADD COLUMN booking_id BIGINT REFERENCES bookings(id) ON DELETE SET NULL;

CREATE OR REPLACE FUNCTION update_modified_column()   
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;   
END;
$$ language 'plpgsql';


CREATE TRIGGER update_notebooks_modtime 
BEFORE UPDATE ON notebooks 
FOR EACH ROW EXECUTE PROCEDURE update_modified_column();

CREATE TRIGGER update_profile_costs_modtime 
BEFORE UPDATE ON profiles 
FOR EACH ROW EXECUTE PROCEDURE update_modified_column();

CREATE TRIGGER update_bookings_modtime
BEFORE UPDATE ON bookings
FOR EACH ROW EXECUTE PROCEDURE update_modified_column();

CREATE INDEX idx_notebooks_user_id ON notebooks(user_id);
CREATE INDEX idx_notebooks_namespace ON notebooks(namespace);    
CREATE INDEX idx_notebooks_created_at ON notebooks(created_at); 
CREATE INDEX idx_notebooks_picked_at_null ON notebooks(picked_at) WHERE picked_at IS NULL;
CREATE INDEX idx_notebooks_booking_id ON notebooks(booking_id);
CREATE INDEX idx_bookings_user_id ON bookings(user_id);
CREATE INDEX idx_bookings_status ON bookings(status);
CREATE INDEX idx_bookings_slot_date ON bookings(slot_date);
CREATE INDEX idx_bookings_slot_start ON bookings(slot_start);
CREATE INDEX idx_bookings_category_status ON bookings(category_name, status);
CREATE INDEX idx_bookings_lifecycle ON bookings(status, slot_start, slot_end);
CREATE INDEX idx_bookings_shutdown ON bookings(status, slot_end, shutdown_warning_sent_at);
CREATE UNIQUE INDEX idx_bookings_unique_user_slot_active
    ON bookings(user_id, slot_key, slot_date)
    WHERE status IN ('scheduled', 'ready', 'active', 'shutting_down');
CREATE UNIQUE INDEX idx_bookings_unique_user_notebook_name_active
    ON bookings(user_id, notebook_name)
    WHERE status IN ('scheduled', 'ready', 'active', 'shutting_down');

ALTER TABLE notebooks ADD COLUMN IF NOT EXISTS image_name VARCHAR(512);
ALTER TABLE notebooks ADD COLUMN IF NOT EXISTS file_url TEXT;
ALTER TABLE notebooks ADD COLUMN IF NOT EXISTS git_url TEXT;
ALTER TABLE notebooks ADD COLUMN IF NOT EXISTS git_token_secret_name VARCHAR(253);
