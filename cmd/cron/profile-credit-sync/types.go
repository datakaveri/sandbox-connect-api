package main

import (
	"context"
	"fmt"
	"log/slog"
	"sandbox-backend-service/pkg/k8s"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type CronEnv struct {
	OPENCOST_URL                 string `env:"PROFILE_CREDIT_SYNC_OPENCOST_URL,required"`
	POSTGRES_URL                 string `env:"PROFILE_CREDIT_SYNC_POSTGRES_URL,required"`
	MAX_PROFILE_CAN_SYNC_AT_ONCE int    `env:"PROFILE_CREDIT_SYNC_MAX_PROFILE_CAN_SYNC_AT_ONCE"`
	AAA_URL                      string `env:"PROFILE_CREDIT_SYNC_AAA_URL,required"`
	KEYCLOAK_URL                 string `env:"PROFILE_CREDIT_SYNC_KEYCLOAK_URL,required"`
	KEYCLOAK_REALM               string `env:"PROFILE_CREDIT_SYNC_KEYCLOAK_REALM,required"`
	KEYCLOAK_CLIENT_ID           string `env:"PROFILE_CREDIT_SYNC_KEYCLOAK_CLIENT_ID,required"`
	KEYCLOAK_USERNAME            string `env:"PROFILE_CREDIT_SYNC_KEYCLOAK_USERNAME,required"`
	KEYCLOAK_PASSWORD            string `env:"PROFILE_CREDIT_SYNC_KEYCLOAK_PASSWORD,required"`
	K8S_CONFIG_MODE              string `env:"PROFILE_CREDIT_SYNC_K8S_CONFIG_MODE"`
	K8S_CONFIG_PATH              string `env:"PROFILE_CREDIT_SYNC_K8S_CONFIG_PATH"`
	LOG_LEVEL                    string `env:"PROFILE_CREDIT_SYNC_LOG_LEVEL" envDefault:"info"`
	GPUResourceKeys              string `env:"PROFILE_CREDIT_SYNC_GPU_RESOURCE_KEYS"`
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

type AAAResponse struct {
	Type   string       `json:"type"`
	Title  string       `json:"title"`
	Detail string       `json:"detail,omitempty"`
	Result CreditResult `json:"result,omitempty"`
}

type CreditResult struct {
	ID                string  `json:"id"`
	UserID            string  `json:"userId"`
	Amount            float64 `json:"amount"`
	TransactedBy      string  `json:"transactedBy"`
	TransactionStatus string  `json:"transactionStatus"`
	TransactionType   string  `json:"transactionType"`
	CreatedAt         string  `json:"createdAt"`
	RequestedAt       string  `json:"requestedAt"`
	UpdatedBalance    float64 `json:"updatedBalance"`
	TableName         string  `json:"tableName"`
}

type KeycloakTokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	SessionState     string `json:"session_state"`
	Scope            string `json:"scope"`
}

type Profile struct {
	ProfileID              string    `json:"id"`
	UserID                 string    `json:"user_id"`
	Email                  string    `json:"email"`
	TotalPaidCredit        float64   `json:"total_paid_credit"`
	LastSyncBalance        float64   `json:"last_sync_balance"`
	CanCreateGpuNotebook   bool      `json:"can_create_gpu_notebook"`
	AaaAndOpenCostSyncedAt time.Time `json:"aaa_and_opencost_synced_at"`
	PendingDeduction       float64   `json:"pending_deduction"`
}

type KubeflowProfile struct {
	UserID    string
	Email     string
	CreatedAt time.Time
}

type ServerError struct {
	StatusCode int
	Message    string
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("server error: %d - %s", e.StatusCode, e.Message)
}

type profileSync struct {
	logger        *slog.Logger
	config        CronEnv
	pgPool        *pgxpool.Pool
	dynamicClient *k8s.K8sClient
	rootCtx       context.Context
}
