package main

import "time"

type CronEnv struct {
	POSTGRES_URL string `env:"SLOT_LIFECYCLE_POSTGRES_URL,required"`

	// Log format: "json" (default, for production/aggregators) or "text" (for local dev).
	LogFormat string `env:"SLOT_LIFECYCLE_LOG_FORMAT" envDefault:"json"`

	KubeConfigPath string `env:"SLOT_LIFECYCLE_K8S_CONFIG_PATH" envDefault:""`
	KubeConfigMode string `env:"SLOT_LIFECYCLE_K8S_CONFIG_MODE" envDefault:"cluster"`

	// SLOT_CONFIG_PROFILE selects which code-defined slot/category configuration
	// to use.
	SlotConfigProfile string `env:"SLOT_CONFIG_PROFILE" envDefault:"production"`

	// Backward-compatible fallback (older deployments used GPU_SLOT_CONFIG_PROFILE).
	LegacyGPUSlotConfigProfile string `env:"GPU_SLOT_CONFIG_PROFILE" envDefault:""`

	// Optional: limits how many bookings to process per run.
	BatchSize int `env:"SLOT_LIFECYCLE_BATCH_SIZE" envDefault:"50"`

	// How often the lifecycle loop runs (seconds).
	TickIntervalSecs int `env:"SLOT_LIFECYCLE_TICK_INTERVAL_SECS" envDefault:"5"`

	// Hard timeout for a single lifecycle run. Must be less than TickIntervalSecs
	// to avoid back-to-back runs stacking up during slow K8s API conditions.
	RunTimeoutSecs int `env:"SLOT_LIFECYCLE_RUN_TIMEOUT_SECS" envDefault:"45"`
}

type GPUBookingRow struct {
	ID                 int64
	UserID             string
	Category           string
	SlotKey            string
	Notebook           string
	SlotDate           time.Time
	SlotStart          time.Time
	SlotEnd            time.Time
	CurrentStat        string
	FileURL            *string
	GitURL             *string
	GitTokenSecretName *string
}
