package main

import (
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"
)

type ApiEnv struct {
	Address           string `env:"API_ADDRESS,required"`
	KubeConfigPath    string `env:"API_KUBE_CONFIG_PATH" envDefault:""`
	KubeConfigMode    string `env:"API_KUBE_CONFIG_MODE" envDefault:"cluster"`
	POSTGRES_URL      string `env:"API_POSTGRES_URL,required"`
	KeycloakURL       string `env:"API_KEYCLOAK_URL,required"`
	KeycloakRealm     string `env:"API_KEYCLOAK_REALM,required"`
	KeycloakClientID  string `env:"API_KEYCLOAK_CLIENT_ID,required"`
	KeycloakPublicKey string `env:"API_KEYCLOAK_PUBLIC_KEY,required"`
	CORS_ORIGINS      string `env:"API_CORS_ORIGINS"`
	KYCEnabled        bool   `env:"API_KYC_ENABLED,required"`
	RateLimit         int    `env:"API_RATE_LIMIT"`
	RateWindowSecs    int    `env:"API_RATE_WINDOW_SECS"`
	TimeoutInSecs     int    `env:"API_TIMEOUT_SECS"`
	IdleTimeoutSecs   int    `env:"API_IDLE_TIMEOUT_SECS"`
	MaxBodySizeInMB   int    `env:"API_MAX_BODY_SIZE_IN_MB"`
	WriteTimeoutSecs  int    `env:"API_WRITE_TIMEOUT_SECS"`
	ReadTimeoutSecs   int    `env:"API_READ_TIMEOUT_SECS"`
	Version           string `env:"API_VERSION,required"`
	NotebookConfig       NotebookConfig
	RegistrySecretConfig RegistrySecretConfig
	RabbitMQConfig       RabbitMQConfig
}

// RabbitMQConfig holds the RabbitMQ connection configuration for audit message publishing.
// All fields are optional — if not configured, auditing is silently disabled.
type RabbitMQConfig struct {
	Host            string `env:"RABBITMQ_HOST"`
	Port            string `env:"RABBITMQ_PORT"`
	Vhost           string `env:"RABBITMQ_VHOST"`
	Username        string `env:"RABBITMQ_USERNAME"`
	Password        string `env:"RABBITMQ_PASSWORD"`
	Exchange        string `env:"RABBITMQ_EXCHANGE"`
	RoutingKey      string `env:"RABBITMQ_ROUTING_KEY"`
	OnlyForMahaAgx  string `env:"RABBITMQ_ONLY_FOR_MAHA_AGX"`
}

// RegistrySecretConfig is the single, generic registry-secret configuration.
// Set API_REGISTRY_SECRET_TYPE to control which flow is active:
//
//	"ecr"              — AWS ECR with rotating 12-hour tokens
//	"private-registry" — static username/password (e.g. CBR on-prem)
//	"none" (default)   — no registry secret is created
type RegistrySecretConfig struct {
	SecretType string `env:"API_REGISTRY_SECRET_TYPE" envDefault:"none"`
	SecretName string `env:"API_REGISTRY_SECRET_NAME" envDefault:"registry-cred"`
	URL        string `env:"API_REGISTRY_URL"`

	// Credentials for "private-registry" type
	Username string `env:"API_REGISTRY_USERNAME"`
	Password string `env:"API_REGISTRY_PASSWORD"`

	// Credentials for "ecr" type
	ECRRegion      string `env:"API_REGISTRY_ECR_REGION"`
	AWSAccessKeyID string `env:"API_REGISTRY_AWS_ACCESS_KEY_ID"`
	AWSSecretKey   string `env:"API_REGISTRY_AWS_SECRET_KEY"`
}

type ECRClient struct {
	ECRClient *ecr.Client
	Config    RegistrySecretConfig
}

type application struct {
	env            ApiEnv
	k8sClient      *k8s.K8sClient
	pgPool         *db.PgPool
	rateLimiter    *IPRateLimiter
	ecrClient      *ECRClient // non-nil only when SecretType == "ecr"
	registrySecret RegistrySecretConfig
	auditService   *AuditService // nil if auditing is disabled
}
type Resource struct {
	Request float64 `json:"request" validate:"required,gt=0.1"`
	Limit   float64 `json:"limit" validate:"required,gt=0.1"`
}
type CreateGPUResource struct {
	Name string `json:"name" validate:"required"`
}
type GPUResource struct {
	Type  string `json:"type" validate:"omitempty"`
	Limit int    `json:"limit" validate:"omitempty"`
}

type CheckStatusRequest struct {
	Id int64 `json:"id" validate:"required"`
}
type NotebookConfig struct {
	CPUStorageSize string `env:"API_DEFAULT_CPU_STORAGE_SIZE,required"`
	GPUStorageSize string `env:"API_DEFAULT_GPU_STORAGE_SIZE,required"`
	CPURequest     string `env:"API_DEFAULT_CPU_REQUEST,required"`
	CPULimit       string `env:"API_DEFAULT_CPU_LIMIT,required"`
	MemoryRequest  string `env:"API_DEFAULT_MEMORY_REQUEST,required"`
	MemoryLimit    string `env:"API_DEFAULT_MEMORY_LIMIT,required"`

	GPUType          string `env:"API_DEFAULT_GPU_TYPE,required"`
	GPURequest       string `env:"API_DEFAULT_GPU_REQUEST,required"`
	GPULimit         string `env:"API_DEFAULT_GPU_LIMIT,required"`
	GPUMemoryRequest string `env:"API_DEFAULT_GPU_MEMORY_REQUEST,required"`
	GPUMemoryLimit   string `env:"API_DEFAULT_GPU_MEMORY_LIMIT,required"`
	GPUCPULimit      string `env:"API_DEFAULT_GPU_CPU_LIMIT,required"`
	GPUCPURequest    string `env:"API_DEFAULT_GPU_CPU_REQUEST,required"`

	GPUNodeInstanceTypes string `env:"API_GPU_NODE_INSTANCE_TYPES,required"`

	KubeFlowURL              string `env:"API_KUBEFLOW_URL,required"`
	DefaultNotebookListLimit int    `env:"API_NOTEBOOK_LIST_LIMIT"`

	MaxRunningCPU int `env:"API_MAX_RUNNING_CPU"`
	MaxRunningGPU int `env:"API_MAX_RUNNING_GPU"`
	MaxTotalCPU   int `env:"API_MAX_TOTAL_CPU"`
	MaxTotalGPU   int `env:"API_MAX_TOTAL_GPU"`
}

type NotebookRequest struct {
	Name         string  `json:"name" validate:"required"`
	Type         string  `json:"type" validate:"required"`
	InstanceType string  `json:"instanceType" validate:"omitempty"`
	ImageName    *string `json:"imageName" validate:"omitempty"`
}

type NotebookStatus struct {
	ID            int64              `json:"id"`
	Name          string             `json:"name"`
	Namespace     string             `json:"namespace"`
	StorageSize   string             `json:"storageSize"`
	PVCName       string             `json:"pvcName"`
	CPURequest    float64            `json:"cpuRequest"`
	CPULimit      float64            `json:"cpuLimit"`
	MemoryRequest string             `json:"memoryRequest"`
	MemoryLimit   string             `json:"memoryLimit"`
	GPUType       *string            `json:"gpuType,omitempty"`
	GPURequest    *int               `json:"gpuRequest,omitempty"`
	GPULimit      *int               `json:"gpuLimit,omitempty"`
	InstanceType  *string            `json:"instanceType,omitempty"`
	TemplateName  *string            `json:"templateName,omitempty"`
	Events        []constants.Events `json:"events"`
	Status        NotebookState      `json:"status"`
	URL           string             `json:"notebookUrl,omitempty"`
	CreatedAt     time.Time          `json:"createdAt"`
}
type NotebookListResponse struct {
	Notebooks  []NotebookStatus `json:"notebooks"`
	NextOffset int              `json:"next_offset"`
}

type StopNotebookRequest struct {
	Name string `json:"name" validate:"required,min=4,max=50"`
}
type StartNotebookRequest struct {
	Name string `json:"name" validate:"required,min=4,max=50"`
}
type DeleteNotebookRequest struct {
	Name string `json:"name" validate:"required,min=4,max=50"`
}

type NotebookState string

const (
	NotebookStateOpening  NotebookState = "opening"
	NotebookStateRunning  NotebookState = "running"
	NotebookStateStopped  NotebookState = "stopped"
	NotebookStateFailed   NotebookState = "failed"
	NotebookStateOrphaned NotebookState = "orphaned"
)

// GPUInstanceType represents a single GPU instance type option with display metadata
type GPUInstanceType struct {
	InstanceType    string `json:"instanceType"`
	DisplayName     string `json:"displayName"`
	GPUMemory       string `json:"gpuMemory"`
	Description     string `json:"description"`
	SessionDuration string `json:"sessionDuration"`
}

// GPUInstanceTypesResponse represents the available GPU instance types with metadata
type GPUInstanceTypesResponse struct {
	InstanceTypes []GPUInstanceType `json:"instanceTypes"`
}

// GPUInstanceTypeMetadata is a static code-level mapping of instance type → display metadata.
// To add a new GPU type, add its metadata here AND add it to the API_GPU_NODE_INSTANCE_TYPES env var.
var GPUInstanceTypeMetadata = map[string]GPUInstanceType{
	"g4dn.xlarge": {
		InstanceType:    "g4dn.xlarge",
		DisplayName:     "Basic",
		GPUMemory:       "16 GB",
		Description:     "16 GB NVIDIA T4 GPU",
		SessionDuration: "4h Session",
	},
	"p4d.24xlarge": {
		InstanceType:    "p4d.24xlarge",
		DisplayName:     "Advance",
		GPUMemory:       "40 GB",
		Description:     "40 GB NVIDIA A100 GPU",
		SessionDuration: "22h Session",
	},
	"p5.48xlarge": {
		InstanceType:    "p5.48xlarge",
		DisplayName:     "Pro",
		GPUMemory:       "80 GB",
		Description:     "80 GB NVIDIA H100 GPU",
		SessionDuration: "22h Session",
	},
}

type CreateProfileRequest struct {
	UserID string `json:"userId" validate:"required,uuid"`
	Email  string `json:"email" validate:"required,email"`
}

type RealmAccess struct {
	Roles []string `json:"roles"`
}

type ResourceAccess struct {
	Account struct {
		Roles []string `json:"roles"`
	} `json:"account"`
}

type JWTPayload struct {
	Exp            int64          `json:"exp"`
	Iat            int64          `json:"iat"`
	Jti            string         `json:"jti"`
	Iss            string         `json:"iss"`
	Aud            string         `json:"aud"`
	Sub            string         `json:"sub" validate:"required,uuid"`
	Typ            string         `json:"typ"`
	Azp            string         `json:"azp" validate:"required"`
	EmailVerified  bool           `json:"email_verified"`
	KycVerified    bool           `json:"kyc_verified"`
	Name           string         `json:"name"`
	Email          string         `json:"email" validate:"required,email"`
	RealmAccess    RealmAccess    `json:"realm_access"`
	ResourceAccess ResourceAccess `json:"resource_access"`
}

func (p *JWTPayload) GetExpirationTime() (*jwt.NumericDate, error) {
	if p.Exp == 0 {
		return nil, nil
	}
	return jwt.NewNumericDate(time.Unix(p.Exp, 0)), nil
}

func (p *JWTPayload) GetIssuedAt() (*jwt.NumericDate, error) {
	if p.Iat == 0 {
		return nil, nil
	}
	return jwt.NewNumericDate(time.Unix(p.Iat, 0)), nil
}

func (p *JWTPayload) GetNotBefore() (*jwt.NumericDate, error) {
	return nil, nil // NBF claim not used
}

func (p *JWTPayload) GetIssuer() (string, error) {
	return p.Iss, nil
}

func (p *JWTPayload) GetSubject() (string, error) {
	return p.Sub, nil
}

func (p *JWTPayload) GetAudience() (jwt.ClaimStrings, error) {
	if p.Aud == "" {
		return nil, nil
	}
	return jwt.ClaimStrings{p.Aud}, nil
}

type UserInfo struct {
	Sub   string   `json:"sub" validate:"required,uuid"`
	Email string   `json:"email" validate:"required,email"`
	Roles []string `json:"roles"`
}

type userContextKey string

const UserContextKey userContextKey = "user"

// SwaggerExistsResponse represents the response for check-exists endpoint
// @Description Response structure for check-exists endpoint
type SwaggerExistsResponse struct {
	// Whether the resource exists
	Exists bool `json:"exists" example:"true"`
}

// Error400 represents a 400 Bad Request error response
// @Description Response for bad request errors
type Error400 struct {
	// Error message
	Detail string `json:"detail" example:"string"`
	Type   string `json:"type" example:"error"`
}

// Error401 represents a 401 Unauthorized error response
// @Description Response for authentication errors
type Error401 struct {
	// Error message
	Detail string `json:"detail" example:"string"`
	Type   string `json:"type" example:"error"`
}

// Error403 represents a 403 Forbidden error response
// @Description Response for permission errors
type Error403 struct {
	// Error message
	Detail string `json:"detail" example:"string"`
	Type   string `json:"type" example:"error"`
}

// Error404 represents a 404 Not Found error response
// @Description Response for resource not found errors
type Error404 struct {
	// Error message
	Detail string `json:"detail" example:"string"`
	Type   string `json:"type" example:"error"`
}

// Error422 represents a 422 Unprocessable Entity error response
// @Description Response for invalid request body errors
type Error422 struct {
	// Error message
	Detail string `json:"detail" example:"string"`
	Type   string `json:"type" example:"error"`
}

// Error429 represents a 429 Too Many Requests error response
// @Description Response for rate limiting errors
type Error429 struct {
	// Error message
	Detail string `json:"detail" example:"string"`
	Type   string `json:"type" example:"error"`
}

// Error500 represents a 500 Internal Server Error response
// @Description Response for internal server errors
type Error500 struct {
	// Error message
	Detail string `json:"detail" example:"string"`
	Type   string `json:"type" example:"error"`
}

// SwaggerMessageResponse represents the standard success response structure
// @Description Standard success response structure with a message
type SwaggerMessageResponse struct {
	// Success message
	Message string `json:"message" example:"string"`
}

// @Description Standard health response structure
type SwaggerHealthResponse struct {
	// Success message
	Status  string `json:"status" example:"string"`
	Version string `json:"version" example:"string"`
}

// Error409 represents a 409 Conflict error response
// @Description Response for resource conflict errors
type Error409 struct {
	// Error message
	Detail string `json:"detail" example:"string"`
	Type   string `json:"type" example:"error"`
}
