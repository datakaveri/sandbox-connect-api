package main

import "time"

type DashboardWarning struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type DashboardWindow struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Timezone string    `json:"timezone"`
}

type DashboardSource struct {
	Source     string     `json:"source"`
	Status     string     `json:"status"`
	ObservedAt *time.Time `extensions:"x-nullable" json:"observedAt"`
}

type DashboardMeta struct {
	GeneratedAt  time.Time          `json:"generatedAt"`
	ClusterID    string             `json:"clusterId"`
	ResourceType string             `json:"resourceType"`
	Window       *DashboardWindow   `extensions:"x-nullable" json:"window"`
	Sources      []DashboardSource  `json:"sources"`
	Partial      bool               `json:"partial"`
	Warnings     []DashboardWarning `json:"warnings"`
}

type DashboardCluster struct {
	ClusterID string `json:"clusterId"`
	Name      string `json:"name"`
}

type DashboardGPUUnits struct {
	ResourceName string `json:"resourceName"`
	Units        int64  `json:"units"`
}

type DashboardRequests struct {
	CPU    float64             `json:"cpuCores"`
	Memory int64               `json:"memoryBytes"`
	GPU    []DashboardGPUUnits `json:"gpuResources"`
}

type DashboardSandboxResources struct {
	CPU    float64             `json:"cpuRequestCores"`
	Memory int64               `json:"memoryRequestBytes"`
	GPU    []DashboardGPUUnits `json:"gpuResources"`
}

type DashboardSandboxCounts struct {
	Total      int            `json:"total"`
	CPU        int            `json:"cpu"`
	GPU        int            `json:"gpu"`
	ByState    map[string]int `json:"byState"`
	RunningCPU *int           `extensions:"x-nullable" json:"runningCpu"`
	RunningGPU *int           `extensions:"x-nullable" json:"runningGpu"`
}

type DashboardCurrent struct {
	RunningUsers    *int                   `extensions:"x-nullable" json:"runningUsers"`
	Sandboxes       DashboardSandboxCounts `json:"sandboxes"`
	RunningRequests *DashboardRequests     `extensions:"x-nullable" json:"runningRequests"`
}

type DashboardHours struct {
	Total float64 `json:"total"`
	CPU   float64 `json:"cpu"`
	GPU   float64 `json:"gpu"`
}

type DashboardPeriod struct {
	UsersWithSessions   int            `json:"usersWithSessions"`
	Hours               DashboardHours `json:"bookedSessionHours"`
	ExcludedSessionRows int            `json:"excludedSessionRows"`
	SessionHoursBasis   string         `json:"sessionHoursBasis"`
}

type DashboardStats struct {
	Current DashboardCurrent `json:"current"`
	Period  *DashboardPeriod `extensions:"x-nullable" json:"period"`
}

type DashboardSummaryResult struct {
	Cluster DashboardCluster `json:"cluster"`
	Current DashboardCurrent `json:"current"`
	Period  *DashboardPeriod `extensions:"x-nullable" json:"period"`
}

type DashboardSummaryResponse struct {
	Meta   DashboardMeta          `json:"meta"`
	Result DashboardSummaryResult `json:"result"`
}

type DashboardUser struct {
	UserID string         `json:"userId"`
	Email  *string        `extensions:"x-nullable" json:"email"`
	Stats  DashboardStats `json:"stats"`
}

type DashboardUsersResponse struct {
	Meta           DashboardMeta   `json:"meta"`
	PaginationInfo PaginationInfo  `json:"paginationInfo"`
	Result         []DashboardUser `json:"result"`
}

type DashboardSandbox struct {
	ID           string                     `json:"id"`
	ClusterID    string                     `json:"clusterId"`
	UserID       string                     `json:"userId"`
	Name         string                     `json:"name"`
	Namespace    string                     `json:"namespace"`
	ResourceType string                     `json:"resourceType"`
	State        string                     `json:"state"`
	Resources    *DashboardSandboxResources `extensions:"x-nullable" json:"resources"`
}

type DashboardSandboxesResponse struct {
	Meta           DashboardMeta      `json:"meta"`
	PaginationInfo PaginationInfo     `json:"paginationInfo"`
	Result         []DashboardSandbox `json:"result"`
}

type DashboardTrendPoint struct {
	Start time.Time      `json:"start"`
	End   time.Time      `json:"end"`
	Hours DashboardHours `json:"bookedSessionHours"`
}

type DashboardTrendsResult struct {
	Interval            string                `json:"interval"`
	SessionHoursBasis   string                `json:"sessionHoursBasis"`
	Points              []DashboardTrendPoint `extensions:"x-nullable" json:"points"`
	ExcludedSessionRows *int                  `extensions:"x-nullable" json:"excludedSessionRows"`
}

type DashboardTrendsResponse struct {
	Meta   DashboardMeta         `json:"meta"`
	Result DashboardTrendsResult `json:"result"`
}
