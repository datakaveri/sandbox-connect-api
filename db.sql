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
    gpu_count INTEGER,

    template_name VARCHAR(255),
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    picked_at TIMESTAMP WITHOUT TIME ZONE,

    events VARCHAR(255)[] NOT NULL DEFAULT ARRAY['scheduled'],

    CONSTRAINT unique_name_namespace UNIQUE (name, namespace),
    CONSTRAINT unique_pvc_namespace UNIQUE (pvc_name, namespace)
);

ALTER TABLE notebooks ADD COLUMN updated_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP;


CREATE TABLE profiles (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL,
    email VARCHAR(255) NOT NULL,
    total_credit DECIMAL(12,6) NOT NULL DEFAULT 0,
    can_create_gpu_notebook BOOLEAN NOT NULL DEFAULT true,
    aaa_and_opencost_synced_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    pending_deduction DECIMAL(12,6) NOT NULL DEFAULT 0,
    last_sync_balance DECIMAL(12,6) NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT profiles_unique_user_id UNIQUE (user_id)
);

CREATE TABLE IF NOT EXISTS failed_aaa_requests (
    id BIGSERIAL PRIMARY KEY,
    user_id VARCHAR(255) NOT NULL,
    cost DECIMAL(10, 2) NOT NULL,
    requested_at TIMESTAMP NOT NULL,
    error_message TEXT,
    status_code INT,
    request_payload JSONB,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    totalRetry INT DEFAULT 0,
    last_retry_at TIMESTAMP,
    resolved BOOLEAN DEFAULT FALSE,
    resolved_at TIMESTAMP
);
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
