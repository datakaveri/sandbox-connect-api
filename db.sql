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
    created_at TIMESTAMP WITHOUT TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    picked_at TIMESTAMP WITHOUT TIME ZONE,

    events VARCHAR(255)[] NOT NULL DEFAULT ARRAY['scheduled'],

    CONSTRAINT unique_name_namespace UNIQUE (name, namespace),
    CONSTRAINT unique_pvc_namespace UNIQUE (pvc_name, namespace)
);

CREATE TABLE profile_costs(
    profile_id UUID PRIMARY KEY,
    gpu_total_cost DECIMAL(12,6) NOT NULL DEFAULT 0,
    cpu_total_cost DECIMAL(12,6) NOT NULL DEFAULT 0,
    memory_total_cost DECIMAL(12,6) NOT NULL DEFAULT 0,
    total_cost DECIMAL(12,6) NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITHOUT TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITHOUT TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_profile_costs_updated_at ON profile_costs(updated_at);
