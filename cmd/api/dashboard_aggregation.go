package main

import (
	"sort"
	"time"

	"sandbox-backend-service/pkg/constants"
)

func newDashboardStats(history bool) DashboardStats {
	users, cpu, gpu := 0, 0, 0
	states := map[string]int{}
	for _, state := range dashboardStates {
		states[state] = 0
	}
	s := DashboardStats{Current: DashboardCurrent{RunningUsers: &users,
		Sandboxes:       DashboardSandboxCounts{ByState: states, RunningCPU: &cpu, RunningGPU: &gpu},
		RunningRequests: &DashboardRequests{GPU: []DashboardGPUUnits{}}}}
	if history {
		s.Period = &DashboardPeriod{SessionHoursBasis: dashboardSessionBasis}
	}
	return s
}

func addDashboardRequests(total *DashboardRequests, r *DashboardSandboxResources) {
	total.CPU += r.CPU
	total.Memory += r.Memory
	for _, unit := range r.GPU {
		found := false
		for i := range total.GPU {
			if total.GPU[i].ResourceName == unit.ResourceName {
				total.GPU[i].Units += unit.Units
				found = true
				break
			}
		}
		if !found {
			total.GPU = append(total.GPU, unit)
		}
	}
	sort.Slice(total.GPU, func(i, j int) bool { return total.GPU[i].ResourceName < total.GPU[j].ResourceName })
}

func accumulateDashboardCurrent(s *DashboardStats, n DashboardSandbox, runningRequestValid bool) {
	c := &s.Current
	c.Sandboxes.Total++
	c.Sandboxes.ByState[n.State]++
	if n.ResourceType == "cpu" {
		c.Sandboxes.CPU++
	} else {
		c.Sandboxes.GPU++
	}
	if n.State == "unknown" {
		if n.ResourceType == "cpu" {
			c.Sandboxes.RunningCPU = nil
		} else {
			c.Sandboxes.RunningGPU = nil
		}
		c.RunningUsers = nil
		c.RunningRequests = nil
	}
	if n.State == "running" {
		if n.ResourceType == "cpu" && c.Sandboxes.RunningCPU != nil {
			*c.Sandboxes.RunningCPU++
		}
		if n.ResourceType == "gpu" && c.Sandboxes.RunningGPU != nil {
			*c.Sandboxes.RunningGPU++
		}
		if !runningRequestValid {
			c.RunningRequests = nil
		}
		if c.RunningRequests != nil {
			addDashboardRequests(c.RunningRequests, n.Resources)
		}
	}
}

func addDashboardHours(h *DashboardHours, kind string, seconds float64) {
	if kind == "gpu" {
		h.GPU += seconds / 3600
	} else {
		h.CPU += seconds / 3600
	}
	h.Total = h.CPU + h.GPU
}

func dashboardSessionOverlap(s dashboardSession, q dashboardQuery) (time.Time, time.Time, bool) {
	if s.Started == nil {
		return time.Time{}, time.Time{}, false
	}
	var end time.Time
	if s.Ended != nil {
		end = *s.Ended
	} else if s.Status == "active" || s.Status == "shutting_down" {
		end = s.SlotEnd
		if end.After(q.Now) {
			end = q.Now
		}
	} else {
		return time.Time{}, time.Time{}, true
	}
	if end.Before(*s.Started) {
		return time.Time{}, time.Time{}, true
	}
	start := *s.Started
	if start.Before(q.Window.From) {
		start = q.Window.From
	}
	if end.After(q.Window.To) {
		end = q.Window.To
	}
	return start, end, false
}

func dashboardDailyPoints(window *DashboardWindow) []DashboardTrendPoint {
	points := []DashboardTrendPoint{}
	for start := window.From; start.Before(window.To); {
		local := start.In(istLocation())
		end := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, istLocation()).AddDate(0, 0, 1).UTC()
		if end.After(window.To) {
			end = window.To
		}
		points = append(points, DashboardTrendPoint{Start: start, End: end})
		start = end
	}
	return points
}

func buildDashboardView(q dashboardQuery, d dashboardData, runtime dashboardRuntimeSnapshot, clusterID string) dashboardView {
	history := q.Window != nil && d.HistoryReady
	view := dashboardView{Meta: DashboardMeta{GeneratedAt: q.Now, ClusterID: clusterID, ResourceType: q.ResourceType, Window: q.Window,
		Sources: []DashboardSource{{Source: "postgres", Status: "ok", ObservedAt: &d.ObservedAt}}, Warnings: []DashboardWarning{}},
		Stats: newDashboardStats(history), Users: []DashboardUser{}, Sandboxes: []DashboardSandbox{},
		Trends: DashboardTrendsResult{Interval: "day", SessionHoursBasis: dashboardSessionBasis}}
	if q.Window != nil && !d.HistoryReady {
		view.Meta.warn("LEGACY_TIMESTAMP_UNRESOLVED", "Configure the verified legacy event timezone before using historical session totals")
	}
	if q.Kind != "trends" {
		status := "ok"
		if runtime.Notebooks == nil {
			status = "unavailable"
			view.Meta.warn("KUBERNETES_UNAVAILABLE", "Live runtime observation is unavailable; recorded session data remains available")
		} else if runtime.Stale || (runtime.ObservedAt != nil && q.Now.Sub(*runtime.ObservedAt) > 60*time.Second) {
			status = "stale"
			view.Meta.warn("STALE_RUNTIME_SNAPSHOT", "Serving the last successful runtime observation")
		}
		view.Meta.Sources = append(view.Meta.Sources, DashboardSource{Source: "kubernetes", Status: status, ObservedAt: runtime.ObservedAt})
	}
	users := map[string]*DashboardUser{}
	for id, email := range d.Owners {
		users[id] = &DashboardUser{UserID: id, Email: email, Stats: newDashboardStats(history)}
	}
	runningUsers := map[string]bool{}
	for _, n := range d.Notebooks {
		if n.LatestEvent == "deleted" || (q.ResourceType != "all" && n.ResourceType != q.ResourceType) || (q.UserID != "" && n.UserID != q.UserID) {
			continue
		}
		state := "opening"
		obj := runtime.Notebooks[dashboardNotebookKey{n.Namespace, n.Name}]
		if checkNotebookFailed(n.LatestEvent) {
			state = "failed"
		} else if n.LatestEvent == constants.StatusNotebookApplied {
			if runtime.Notebooks == nil {
				state = "unknown"
			} else if obj == nil && (runtime.Stale || n.ID == "" || runtime.ConfirmedMissing[dashboardNotebookKey{n.Namespace, n.Name}] != n.ID) {
				state = "unknown"
				view.Meta.warn("STALE_RUNTIME_SNAPSHOT", "The runtime snapshot cannot confirm this sandbox's absence yet")
			} else {
				var spec map[string]any
				if obj != nil {
					spec = obj.Object
				}
				state = string(determineNotebookState(n.LatestEvent, spec))
			}
		}
		resources, err := dashboardStoredResources(n)
		if err != nil {
			view.Meta.warn("ALLOCATION_INCOMPLETE", "Some stored resource quantities could not be parsed")
		}
		valid := resources != nil && dashboardResourcesMatch(n, obj)
		if state == "running" && !valid {
			view.Meta.warn("ALLOCATION_INCOMPLETE", "Stored requests do not fully reconcile with running Notebook container requests")
		}
		sandbox := DashboardSandbox{ID: n.ID, ClusterID: clusterID, UserID: n.UserID, Name: n.Name, Namespace: n.Namespace, ResourceType: n.ResourceType, State: state, Resources: resources}
		view.Sandboxes = append(view.Sandboxes, sandbox)
		accumulateDashboardCurrent(&view.Stats, sandbox, valid)
		if state == "running" {
			runningUsers[n.UserID] = true
		}
		if u := users[n.UserID]; u != nil {
			accumulateDashboardCurrent(&u.Stats, sandbox, valid)
			if state == "running" && u.Stats.Current.RunningUsers != nil {
				*u.Stats.Current.RunningUsers = 1
			}
		}
	}
	if view.Stats.Current.RunningUsers != nil {
		*view.Stats.Current.RunningUsers = len(runningUsers)
	}
	if history {
		view.Trends.Points = dashboardDailyPoints(q.Window)
		sessionUsers := map[string]bool{}
		for _, s := range d.Sessions {
			if (s.ResourceType != "cpu" && s.ResourceType != "gpu") || (q.ResourceType != "all" && s.ResourceType != q.ResourceType) {
				continue
			}
			start, end, invalid := dashboardSessionOverlap(s, q)
			if invalid {
				if s.SlotStart.Before(q.Window.To) && s.SlotEnd.After(q.Window.From) {
					view.Stats.Period.ExcludedSessionRows++
					if u := users[s.UserID]; u != nil {
						u.Stats.Period.ExcludedSessionRows++
					}
				}
				continue
			}
			if !end.After(start) {
				continue
			}
			seconds := end.Sub(start).Seconds()
			addDashboardHours(&view.Stats.Period.Hours, s.ResourceType, seconds)
			sessionUsers[s.UserID] = true
			if u := users[s.UserID]; u != nil {
				addDashboardHours(&u.Stats.Period.Hours, s.ResourceType, seconds)
				u.Stats.Period.UsersWithSessions = 1
			}
			for i := range view.Trends.Points {
				p := &view.Trends.Points[i]
				a, b := start, end
				if a.Before(p.Start) {
					a = p.Start
				}
				if b.After(p.End) {
					b = p.End
				}
				if b.After(a) {
					addDashboardHours(&p.Hours, s.ResourceType, b.Sub(a).Seconds())
				}
			}
		}
		view.Stats.Period.UsersWithSessions = len(sessionUsers)
		view.Trends.ExcludedSessionRows = &view.Stats.Period.ExcludedSessionRows
		if view.Stats.Period.ExcludedSessionRows > 0 {
			view.Meta.warn("INCOMPLETE_SESSION_HISTORY", "Invalid or incomplete recorded sessions were excluded")
		}
	}
	for _, u := range users {
		view.Users = append(view.Users, *u)
	}
	return view
}
