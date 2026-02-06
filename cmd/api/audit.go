package main

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// AuditLogType represents the classification of an audit log entry.
type AuditLogType string

const (
	AuditLogTypeAsset      AuditLogType = "ASSET"
	AuditLogTypeUserAction AuditLogType = "USER_ACTION"
	AuditLogTypeCompute    AuditLogType = "COMPUTE"
)

// AuditMessage is the JSON message published to RabbitMQ.
// It maps to the user_activity_audit_log database table
// (migration: V46__Create_user_activity_audit_log).
type AuditMessage struct {
	// Primary key
	ID string `json:"id"`

	// User context (mandatory)
	UserID string `json:"user_id"`
	Role   string `json:"role"`
	Issuer string `json:"issuer"`

	// User context (optional)
	OrgID   string `json:"org_id,omitempty"`
	OrgName string `json:"org_name,omitempty"`
	OrgType string `json:"org_type,omitempty"`

	// Delegation (optional)
	DelegatorID   string `json:"delegator_id,omitempty"`
	DelegatorRole string `json:"delegator_role,omitempty"`

	// API metadata (mandatory)
	API          string `json:"api"`
	Method       string `json:"method"`
	Action       string `json:"action"`
	OriginServer string `json:"origin_server"`

	// Asset dimension (conditional — only when log_type is ASSET)
	AssetID           string `json:"asset_id,omitempty"`
	AssetAccessPolicy string `json:"asset_access_policy,omitempty"`
	AssetOrgID        string `json:"asset_org_id,omitempty"`
	AssetOrgName      string `json:"asset_org_name,omitempty"`
	AssetOrgType      string `json:"asset_org_type,omitempty"`
	AssetProviderID   string `json:"asset_provider_id,omitempty"`
	AssetProviderName string `json:"asset_provider_name,omitempty"`

	// Metrics / workflow (optional)
	Amount    *float64 `json:"amount,omitempty"`
	RequestID string   `json:"request_id,omitempty"`

	// Classification (mandatory)
	LogType     AuditLogType `json:"log_type"`
	SandboxType string       `json:"sandbox_type,omitempty"` // "cpu" or "gpu"

	// Technical metadata (optional)
	IPAddress string `json:"ip_address,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`

	// Time (mandatory) — microsecond precision: YYYY-MM-DDTHH:mm:ss.xxxxxx
	CreatedAt string `json:"created_at"`

	// Extensible (optional)
	Context map[string]any `json:"context,omitempty"`
}

// AuditContext is the input gathered by the audit middleware and
// passed to AuditService.PublishAuditMessage.
type AuditContext struct {
	API         string
	Method      string
	Action      string
	UserID      string
	Role        string
	AuthToken   string // Bearer token — used for JWT decode (org, issuer, delegation)
	IPAddress   string
	UserAgent   string
	RequestID   string
	LogType     AuditLogType
	SandboxType string // "cpu" or "gpu"
	Context     map[string]any
}

// ---------------------------------------------------------------------------
// AuditService
// ---------------------------------------------------------------------------

// AuditService builds AuditMessages from AuditContext and publishes them via RabbitMQ.
type AuditService struct {
	rabbitmq *RabbitMQService
}

// NewAuditService creates an AuditService backed by the given RabbitMQ service.
func NewAuditService(rabbitmq *RabbitMQService) *AuditService {
	return &AuditService{rabbitmq: rabbitmq}
}

// PublishAuditMessage builds the full audit message and publishes it.
// This method is designed to be called in a goroutine — it never panics or
// returns errors to the caller; all failures are logged.
func (s *AuditService) PublishAuditMessage(ctx AuditContext) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic recovered in audit message publishing", "panic", r)
		}
	}()

	// Extract organisation, issuer, and delegation info from the JWT token
	tokenInfo := s.decodeJWTToken(ctx.AuthToken)

	// Default log_type to COMPUTE for sandbox server operations
	logType := ctx.LogType
	if logType == "" {
		logType = AuditLogTypeCompute
	}

	msg := AuditMessage{
		// Primary key
		ID: uuid.New().String(),

		// User context (mandatory)
		UserID: ctx.UserID,
		Role:   strings.ToLower(ctx.Role),
		Issuer: tokenInfo.Issuer,

		// User context (optional — only included if present)
		OrgID:   tokenInfo.OrgID,
		OrgName: tokenInfo.OrgName,
		OrgType: tokenInfo.OrgType,

		// Delegation (optional)
		DelegatorID:   tokenInfo.DelegatorID,
		DelegatorRole: tokenInfo.DelegatorRole,

		// API metadata (mandatory)
		API:          ctx.API,
		Method:       strings.ToUpper(ctx.Method),
		Action:       ctx.Action,
		OriginServer: "SANDBOX",

		// Classification (mandatory)
		LogType:     logType,
		SandboxType: ctx.SandboxType,

		// Technical metadata (optional)
		IPAddress: ctx.IPAddress,
		UserAgent: ctx.UserAgent,

		// Metrics
		RequestID: ctx.RequestID,

		// Time (mandatory)
		CreatedAt: generateMicrosecondTimestamp(),

		// Extensible context
		Context: ctx.Context,
	}

	if err := s.rabbitmq.PublishAuditMessage(msg); err != nil {
		slog.Error("Failed to publish audit message",
			"error", err,
			"action", ctx.Action,
			"userId", ctx.UserID,
			"api", ctx.API,
		)
		return
	}

	slog.Info("Audit message published successfully",
		"auditId", msg.ID,
		"action", ctx.Action,
		"userId", ctx.UserID,
		"logType", string(logType),
		"api", ctx.API,
	)
}

// Close gracefully shuts down the underlying RabbitMQ connection.
func (s *AuditService) Close() error {
	if s.rabbitmq != nil {
		return s.rabbitmq.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// JWT token decoding (for organisation, issuer, delegation info)
// ---------------------------------------------------------------------------

// jwtTokenInfo holds the fields extracted from a JWT token for audit purposes.
type jwtTokenInfo struct {
	OrgID         string
	OrgName       string
	OrgType       string
	Issuer        string
	DelegatorID   string
	DelegatorRole string
}

// decodeJWTToken manually decodes the JWT payload (base64) to extract
// organisation, issuer, and delegation claims without performing signature
// validation (that is already done by the auth middleware).
func (s *AuditService) decodeJWTToken(token string) jwtTokenInfo {
	if token == "" {
		return jwtTokenInfo{}
	}

	// Remove "Bearer " prefix
	cleanToken := token
	if idx := strings.Index(strings.ToLower(token), "bearer "); idx == 0 {
		cleanToken = token[7:]
	}

	// JWT: header.payload.signature
	parts := strings.Split(cleanToken, ".")
	if len(parts) != 3 {
		slog.Warn("Invalid JWT token format for audit decoding")
		return jwtTokenInfo{}
	}

	payload := parts[1]

	// Add base64 padding if necessary
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	// Try URL-safe base64 first, then standard
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(payload)
		if err != nil {
			slog.Warn("Failed to decode JWT payload for audit", "error", err)
			return jwtTokenInfo{}
		}
	}

	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		slog.Warn("Failed to parse JWT claims for audit", "error", err)
		return jwtTokenInfo{}
	}

	info := jwtTokenInfo{}

	// Issuer
	if iss, ok := claims["iss"].(string); ok {
		info.Issuer = iss
	}

	// Organisation info
	if v, ok := claims["organisation_id"].(string); ok {
		info.OrgID = v
	}
	if v, ok := claims["organisation_name"].(string); ok {
		info.OrgName = v
	}
	if v, ok := claims["organisation_type"].(string); ok {
		info.OrgType = v
	} else if v, ok := claims["org_type"].(string); ok {
		info.OrgType = v
	}

	// Delegation (actor) claims
	if act, ok := claims["act"].(map[string]any); ok {
		if sub, ok := act["sub"].(string); ok {
			info.DelegatorID = sub
		}
		if role, ok := act["role"].(string); ok {
			info.DelegatorRole = role
		}
	}
	if info.DelegatorRole == "" {
		if role, ok := claims["delegator_role"].(string); ok {
			info.DelegatorRole = role
		}
	}

	return info
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// generateMicrosecondTimestamp produces a timestamp with microsecond precision:
// "YYYY-MM-DDTHH:mm:ss.xxxxxx"
// Go's time.Now() provides nanosecond precision; we format to 6 decimal places (microseconds).
func generateMicrosecondTimestamp() string {
	return time.Now().Format("2006-01-02T15:04:05.000000")
}

// determineUserRole picks the most meaningful role from a user's Keycloak realm roles.
// Priority: admin > provider > consumer > compute > first non-default role > "user"
func determineUserRole(roles []string) string {
	priorityRoles := []string{"admin", "provider", "consumer", "compute"}
	for _, pr := range priorityRoles {
		for _, r := range roles {
			if strings.EqualFold(r, pr) {
				return strings.ToLower(r)
			}
		}
	}
	// Skip default Keycloak roles
	for _, r := range roles {
		lower := strings.ToLower(r)
		if !strings.HasPrefix(lower, "default-roles-") &&
			lower != "offline_access" &&
			lower != "uma_authorization" {
			return lower
		}
	}
	if len(roles) > 0 {
		return strings.ToLower(roles[0])
	}
	return "user"
}
