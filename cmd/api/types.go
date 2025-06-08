package main

import (
	"time"

	"github.com/golang-jwt/jwt/v5"

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
	RateLimit         int    `env:"API_RATE_LIMIT"`
	RateWindowSecs    int    `env:"API_RATE_WINDOW_SECS"`
	TimeoutInSecs     int    `env:"API_TIMEOUT_SECS"`
	IdleTimeoutSecs   int    `env:"API_IDLE_TIMEOUT_SECS"`
	MaxBodySizeInMB   int    `env:"API_MAX_BODY_SIZE_IN_MB"`
	WriteTimeoutSecs  int    `env:"API_WRITE_TIMEOUT_SECS"`
	ReadTimeoutSecs   int    `env:"API_READ_TIMEOUT_SECS"`
	Version           string `env:"API_VERSION,required"`
	NotebookConfig    NotebookConfig
}

type application struct {
	env         ApiEnv
	k8sClient   *k8s.K8sClient
	pgPool      *db.PgPool
	rateLimiter *IPRateLimiter
}
type Resource struct {
	Request float64 `json:"request" validate:"required,gt=0.1"`
	Limit   float64 `json:"limit" validate:"required,gt=0.1"`
}

type GPUResource struct {
	Type  string `json:"type" validate:"omitempty"`
	Limit int    `json:"limit" validate:"omitempty"`
}

type CheckStatusRequest struct {
	Id int64 `json:"id" validate:"required"`
}
type NotebookConfig struct {
	StorageSize              string `env:"API_DEFAULT_STORAGE_SIZE,required"`
	CPURequest               string `env:"API_DEFAULT_CPU_REQUEST,required"`
	CPULimit                 string `env:"API_DEFAULT_CPU_LIMIT,required"`
	MemoryRequest            string `env:"API_DEFAULT_MEMORY_REQUEST,required"`
	MemoryLimit              string `env:"API_DEFAULT_MEMORY_LIMIT,required"`
	GPUType                  string `env:"API_DEFAULT_GPU_TYPE,required"`
	GPULimit                 string `env:"API_DEFAULT_GPU_LIMIT,required"`
	KubeFlowURL              string `env:"API_KUBEFLOW_URL,required"`
	DefaultNotebookListLimit int    `env:"API_NOTEBOOK_LIST_LIMIT" envDefault:"10"`
}

type NotebookRequest struct {
	Name string `json:"name" validate:"required,gte=4,lte=50"`
	Type string `json:"type" validate:"required"`
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
	GPUCount      *int               `json:"gpuCount,omitempty"`
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
	Name string `json:"name" validate:"gt=3,required"`
}
type StartNotebookRequest struct {
	Name string `json:"name" validate:"gt=3,required"`
}
type DeleteNotebookRequest struct {
	Name string `json:"name" validate:"gt=3,required"`
}

type NotebookState string

const (
	NotebookStatePending  NotebookState = "pending"
	NotebookStateRunning  NotebookState = "running"
	NotebookStateStopped  NotebookState = "stopped"
	NotebookStateFailed   NotebookState = "failed"
	NotebookStateOrphaned NotebookState = "orphaned"
)

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
	Error string `json:"error" example:"string"`
}

// Error401 represents a 401 Unauthorized error response
// @Description Response for authentication errors
type Error401 struct {
	// Error message
	Error string `json:"error" example:"string"`
}

// Error403 represents a 403 Forbidden error response
// @Description Response for permission errors
type Error403 struct {
	// Error message
	Error string `json:"error" example:"string"`
}

// Error404 represents a 404 Not Found error response
// @Description Response for resource not found errors
type Error404 struct {
	// Error message
	Error string `json:"error" example:"string"`
}

// Error422 represents a 422 Unprocessable Entity error response
// @Description Response for invalid request body errors
type Error422 struct {
	// Error message
	Error string `json:"error" example:"string"`
}

// Error429 represents a 429 Too Many Requests error response
// @Description Response for rate limiting errors
type Error429 struct {
	// Error message
	Error string `json:"error" example:"string"`
}

// Error500 represents a 500 Internal Server Error response
// @Description Response for internal server errors
type Error500 struct {
	// Error message
	Error string `json:"error" example:"string"`
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
