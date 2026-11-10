package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const dashboardBasePath = "/v1/platform/sandbox-dashboard"
const dashboardSessionBasis = "recorded_booking_interval"

// The API manages one cluster through its existing Kubernetes connection.
// These labels preserve the response contract without deployment configuration.
const dashboardClusterID = "sandbox-primary"
const dashboardClusterName = "Primary sandbox cluster"

var dashboardStates = []string{"opening", "running", "stopped", "failed", "orphaned", "unknown"}

type dashboardQuery struct {
	Kind, ResourceType, UserID, State, Search, Sort string
	Page, Size                                      int
	Now                                             time.Time
	Window                                          *DashboardWindow
}

func validateDashboardConfig(env ApiEnv) error {
	if env.DashboardEventTimezone != "" {
		if _, err := time.LoadLocation(env.DashboardEventTimezone); err != nil {
			return fmt.Errorf("API_DASHBOARD_EVENT_TIMEZONE must be a verified IANA timezone: %w", err)
		}
	}
	return nil
}

func parseDashboardQuery(values url.Values, kind string, now time.Time) (dashboardQuery, error) {
	q := dashboardQuery{Kind: kind, ResourceType: "all", Page: 1, Size: 20, Sort: "bookedSessionHours:desc", Now: now.UTC()}
	allowed := map[string]bool{"resourceType": true}
	if kind != "sandboxes" {
		allowed["from"], allowed["to"] = true, true
	}
	if kind == "users" || kind == "sandboxes" {
		allowed["page"], allowed["size"] = true, true
	}
	if kind == "users" {
		allowed["q"], allowed["sort"] = true, true
	}
	if kind == "sandboxes" {
		allowed["userId"], allowed["state"] = true, true
	}
	for name, v := range values {
		if !allowed[name] || len(v) != 1 || v[0] == "" {
			return q, fmt.Errorf("invalid query parameter %q", name)
		}
	}
	if v := values.Get("resourceType"); v != "" {
		q.ResourceType = v
	}
	if !contains([]string{"all", "cpu", "gpu"}, q.ResourceType) {
		return q, fmt.Errorf("resourceType must be all, cpu or gpu")
	}
	for name, dst := range map[string]*int{"page": &q.Page, "size": &q.Size} {
		if v := values.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || (name == "size" && n > 100) {
				return q, fmt.Errorf("invalid %s", name)
			}
			*dst = n
		}
	}
	if q.Page-1 > math.MaxInt/q.Size {
		return q, fmt.Errorf("page is too large")
	}
	q.Search, q.State, q.UserID = values.Get("q"), values.Get("state"), values.Get("userId")
	if utf8.RuneCountInString(q.Search) > 100 {
		return q, fmt.Errorf("q must have at most 100 characters")
	}
	if q.State != "" && !contains(dashboardStates, q.State) {
		return q, fmt.Errorf("invalid sandbox state")
	}
	if q.UserID != "" {
		id, err := uuid.Parse(q.UserID)
		if err != nil || len(q.UserID) != 36 {
			return q, fmt.Errorf("userId must be a UUID")
		}
		q.UserID = id.String()
	}
	if v := values.Get("sort"); v != "" {
		q.Sort = v
	}
	if !contains([]string{"bookedSessionHours:desc", "runningSandboxes:desc", "email:asc"}, q.Sort) {
		return q, fmt.Errorf("invalid user sort")
	}
	if kind == "sandboxes" {
		return q, nil
	}
	from, to := values.Get("from"), values.Get("to")
	if (from == "") != (to == "") {
		return q, fmt.Errorf("from and to must be supplied together")
	}
	loc := istLocation()
	today := q.Now.In(loc)
	start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -6)
	end := q.Now
	if from != "" {
		var err error
		start, err = time.Parse(time.RFC3339, from)
		if err != nil {
			return q, fmt.Errorf("from must be an offset-bearing RFC3339 instant")
		}
		end, err = time.Parse(time.RFC3339, to)
		if err != nil {
			return q, fmt.Errorf("to must be an offset-bearing RFC3339 instant")
		}
	}
	if !start.Before(end) || end.After(q.Now) || end.Sub(start) > 30*24*time.Hour {
		return q, fmt.Errorf("window must be positive, end no later than now, and span at most 30 days")
	}
	q.Window = &DashboardWindow{From: start.UTC(), To: end.UTC(), Timezone: "Asia/Kolkata"}
	return q, nil
}

func (app *application) requireCOSAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		user, ok := r.Context().Value(UserContextKey).(UserInfo)
		if !ok {
			sendError(w, getLogger(r), http.StatusUnauthorized, "Unauthorized")
			return
		}
		if !contains(user.Roles, "cos_admin") || user.ClientID != app.env.KeycloakClientID ||
			(app.env.PlatformTokenNotebookClientID != "" && user.ClientID == app.env.PlatformTokenNotebookClientID) {
			sendError(w, getLogger(r), http.StatusForbidden, "COS admin access is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (app *application) registerDashboardRoutes(mux *http.ServeMux) {
	for kind, handler := range map[string]http.HandlerFunc{"summary": app.dashboardSummary, "trends": app.dashboardTrends, "users": app.dashboardUsers, "sandboxes": app.dashboardSandboxes} {
		mux.Handle("GET "+dashboardBasePath+"/"+kind, app.requireCOSAdmin(handler))
	}
}

type dashboardView struct {
	Meta      DashboardMeta
	Stats     DashboardStats
	Users     []DashboardUser
	Sandboxes []DashboardSandbox
	Trends    DashboardTrendsResult
}

func (m *DashboardMeta) warn(code, detail string) {
	m.Partial = true
	for _, w := range m.Warnings {
		if w.Code == code {
			return
		}
	}
	m.Warnings = append(m.Warnings, DashboardWarning{Code: code, Detail: detail})
}

func (app *application) loadDashboard(r *http.Request, kind string) (dashboardQuery, dashboardView, int, error) {
	q, err := parseDashboardQuery(r.URL.Query(), kind, time.Now())
	if err != nil {
		return q, dashboardView{}, http.StatusBadRequest, err
	}
	store := app.dashboardStore
	if store == nil && app.pgPool != nil {
		store = postgresDashboardStore{pool: app.pgPool.Pool}
	}
	if store == nil {
		return q, dashboardView{}, http.StatusServiceUnavailable, fmt.Errorf("dashboard database is unavailable")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	data, err := store.Load(ctx, q, app.env.DashboardEventTimezone)
	if err != nil {
		getLogger(r).Error("dashboard read failed", "error", err)
		return q, dashboardView{}, http.StatusServiceUnavailable, fmt.Errorf("dashboard database is unavailable")
	}
	snapshot := dashboardRuntimeSnapshot{}
	if kind != "trends" {
		snapshot = app.dashboardRuntime(ctx, q.Now, data.Notebooks...)
	}
	return q, buildDashboardView(q, data, snapshot, dashboardClusterID), http.StatusOK, nil
}

// dashboardSummary godoc
// @Summary CPU/GPU dashboard overview
// @Description Requires cos_admin realm role and the configured API client. Current counts/requests describe now; period hours cover recorded booking intervals only. Partial-source warnings and null values indicate unavailable data.
// @Tags platform-dashboard
// @Produce json
// @Param resourceType query string false "Resource type" Enums(all,cpu,gpu) default(all)
// @Param from query string false "Inclusive RFC3339 instant; supply with to. Default last 7 IST calendar days."
// @Param to query string false "Exclusive RFC3339 instant; no later than now, maximum 30-day window."
// @Success 200 {object} DashboardSummaryResponse
// @Failure 400 {object} Error400
// @Failure 401 {object} Error401
// @Failure 403 {object} Error403
// @Failure 429 {object} Error429
// @Failure 500 {object} Error500
// @Failure 503 {object} Error503
// @Security BearerAuth
// @Router /v1/platform/sandbox-dashboard/summary [get]
func (app *application) dashboardSummary(w http.ResponseWriter, r *http.Request) {
	_, view, status, err := app.loadDashboard(r, "summary")
	if err != nil {
		sendError(w, getLogger(r), status, err.Error())
		return
	}
	sendResponseJson(w, getLogger(r), status, DashboardSummaryResponse{Meta: view.Meta, Result: DashboardSummaryResult{
		Cluster: DashboardCluster{ClusterID: dashboardClusterID, Name: dashboardClusterName}, Current: view.Stats.Current, Period: view.Stats.Period}})
}

// dashboardTrends godoc
// @Summary Daily CPU/GPU recorded session hours
// @Description Requires cos_admin. Daily IST buckets; hours are split across boundaries. Direct/browser session history is excluded. Unverified event timezone yields points=null with a warning.
// @Tags platform-dashboard
// @Produce json
// @Param resourceType query string false "Resource type" Enums(all,cpu,gpu) default(all)
// @Param from query string false "Inclusive RFC3339 instant; supply with to."
// @Param to query string false "Exclusive RFC3339 instant; maximum 30-day window."
// @Success 200 {object} DashboardTrendsResponse
// @Failure 400 {object} Error400
// @Failure 401 {object} Error401
// @Failure 403 {object} Error403
// @Failure 429 {object} Error429
// @Failure 500 {object} Error500
// @Failure 503 {object} Error503
// @Security BearerAuth
// @Router /v1/platform/sandbox-dashboard/trends [get]
func (app *application) dashboardTrends(w http.ResponseWriter, r *http.Request) {
	_, view, status, err := app.loadDashboard(r, "trends")
	if err != nil {
		sendError(w, getLogger(r), status, err.Error())
		return
	}
	sendResponseJson(w, getLogger(r), status, DashboardTrendsResponse{Meta: view.Meta, Result: view.Trends})
}

func dashboardPagination(total int, q dashboardQuery) (PaginationInfo, int, int) {
	pages := total / q.Size
	if total%q.Size != 0 {
		pages++
	}
	start := (q.Page - 1) * q.Size
	if start > total {
		start = total
	}
	end := start + min(q.Size, total-start)
	return PaginationInfo{Page: q.Page, Size: q.Size, TotalCount: total, TotalPages: pages, HasNext: q.Page < pages, HasPrevious: q.Page > 1}, start, end
}

// dashboardUsers godoc
// @Summary CPU/GPU sandbox usage by user
// @Description Requires cos_admin. Includes retained notebook/booking owners, excluding unused profiles. No credit fields. Current stats and period hours have separate scopes; q only filters the users table.
// @Tags platform-dashboard
// @Produce json
// @Param resourceType query string false "Resource type" Enums(all,cpu,gpu) default(all)
// @Param from query string false "Inclusive RFC3339 instant; supply with to."
// @Param to query string false "Exclusive RFC3339 instant; maximum 30-day window."
// @Param q query string false "Literal case-insensitive email or user ID substring, max 100 characters"
// @Param sort query string false "Sort (nulls last, user ID tie breaker)" Enums(bookedSessionHours:desc,runningSandboxes:desc,email:asc) default(bookedSessionHours:desc)
// @Param page query int false "One-based page" minimum(1) default(1)
// @Param size query int false "Page size" minimum(1) maximum(100) default(20)
// @Success 200 {object} DashboardUsersResponse
// @Failure 400 {object} Error400
// @Failure 401 {object} Error401
// @Failure 403 {object} Error403
// @Failure 429 {object} Error429
// @Failure 500 {object} Error500
// @Failure 503 {object} Error503
// @Security BearerAuth
// @Router /v1/platform/sandbox-dashboard/users [get]
func (app *application) dashboardUsers(w http.ResponseWriter, r *http.Request) {
	q, view, status, err := app.loadDashboard(r, "users")
	if err != nil {
		sendError(w, getLogger(r), status, err.Error())
		return
	}
	sortDashboardUsers(view.Users, q.Sort)
	pagination, start, end := dashboardPagination(len(view.Users), q)
	sendResponseJson(w, getLogger(r), status, DashboardUsersResponse{Meta: view.Meta, PaginationInfo: pagination, Result: view.Users[start:end]})
}

func sortDashboardUsers(users []DashboardUser, order string) {
	sort.Slice(users, func(i, j int) bool {
		a, b := users[i], users[j]
		switch order {
		case "email:asc":
			if (a.Email == nil) != (b.Email == nil) {
				return a.Email != nil
			}
			if a.Email != nil && b.Email != nil && strings.ToLower(*a.Email) != strings.ToLower(*b.Email) {
				return strings.ToLower(*a.Email) < strings.ToLower(*b.Email)
			}
		case "runningSandboxes:desc":
			x, y := a.Stats.Current.Sandboxes, b.Stats.Current.Sandboxes
			xKnown, yKnown := x.RunningCPU != nil && x.RunningGPU != nil, y.RunningCPU != nil && y.RunningGPU != nil
			if xKnown != yKnown {
				return xKnown
			}
			if xKnown && *x.RunningCPU+*x.RunningGPU != *y.RunningCPU+*y.RunningGPU {
				return *x.RunningCPU+*x.RunningGPU > *y.RunningCPU+*y.RunningGPU
			}
		default:
			x, y := a.Stats.Period, b.Stats.Period
			if (x == nil) != (y == nil) {
				return x != nil
			}
			if x != nil && y != nil && x.Hours.Total != y.Hours.Total {
				return x.Hours.Total > y.Hours.Total
			}
		}
		return a.UserID < b.UserID
	})
}

// dashboardSandboxes godoc
// @Summary Current sandbox inventory and user drill-down
// @Description Requires cos_admin. Non-deleted managed notebooks, numeric ID descending. No historical window. New identities missing from the cache force a refresh; only confirmed absence is orphaned. Live failures or unconfirmed absence yield unknown state. Read-only requests; no launch URLs or secrets.
// @Tags platform-dashboard
// @Produce json
// @Param resourceType query string false "Resource type" Enums(all,cpu,gpu) default(all)
// @Param userId query string false "Exact owner UUID"
// @Param state query string false "Runtime state" Enums(opening,running,stopped,failed,orphaned,unknown)
// @Param page query int false "One-based page" minimum(1) default(1)
// @Param size query int false "Page size" minimum(1) maximum(100) default(20)
// @Success 200 {object} DashboardSandboxesResponse
// @Failure 400 {object} Error400
// @Failure 401 {object} Error401
// @Failure 403 {object} Error403
// @Failure 429 {object} Error429
// @Failure 500 {object} Error500
// @Failure 503 {object} Error503
// @Security BearerAuth
// @Router /v1/platform/sandbox-dashboard/sandboxes [get]
func (app *application) dashboardSandboxes(w http.ResponseWriter, r *http.Request) {
	q, view, status, err := app.loadDashboard(r, "sandboxes")
	if err != nil {
		sendError(w, getLogger(r), status, err.Error())
		return
	}
	result := make([]DashboardSandbox, 0, len(view.Sandboxes))
	for _, n := range view.Sandboxes {
		if q.State == "" || n.State == q.State {
			result = append(result, n)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, _ := strconv.ParseInt(result[i].ID, 10, 64)
		b, _ := strconv.ParseInt(result[j].ID, 10, 64)
		return a > b
	})
	pagination, start, end := dashboardPagination(len(result), q)
	sendResponseJson(w, getLogger(r), status, DashboardSandboxesResponse{Meta: view.Meta, PaginationInfo: pagination, Result: result[start:end]})
}
