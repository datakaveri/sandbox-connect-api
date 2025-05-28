package main

type CronEnv struct {
	OpenCostURL  string `env:"PROFILE_CREDIT_SYNC_OPENCOST_URL,required"`
	POSTGRES_URL string `env:"PROFILE_CREDIT_SYNC_POSTGRES_URL,required"`
	BatchSize    int    `env:"PROFILE_CREDIT_SYNC_BATCH_SIZE" envDefault:"50"`
	MaxRetries   int    `env:"PROFILE_CREDIT_SYNC_MAX_RETRIES" envDefault:"3"`
}

type CostAllocationResponse struct {
	Data []map[string]CostAllocation `json:"data"`
}

type CostAllocation struct {
	Name       string `json:"name"`
	Properties struct {
		Cluster   string `json:"cluster"`
		Namespace string `json:"namespace"`
	} `json:"properties"`
	CPUCost   float64 `json:"cpuCost"`
	GPUCost   float64 `json:"gpuCost"`
	RAMCost   float64 `json:"ramCost"`
	TotalCost float64 `json:"totalCost"`
}
