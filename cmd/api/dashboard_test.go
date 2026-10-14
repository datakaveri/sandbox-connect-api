package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/k8s"
)

type dashboardFakeStore struct {
	data  dashboardData
	err   error
	calls int
}

func (s *dashboardFakeStore) Load(_ context.Context, q dashboardQuery, _ string) (dashboardData, error) {
	s.calls++
	d := s.data
	d.ObservedAt = q.Now
	return d, s.err
}

func dashboardTestInstant(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		panic(err)
	}
	return t
}
func dashboardTimePtr(t time.Time) *time.Time { return &t }
func dashboardStringPtr(s string) *string     { return &s }

func dashboardTestQuery() dashboardQuery {
	q, err := parseDashboardQuery(url.Values{"from": {"2026-10-12T18:00:00Z"}, "to": {"2026-10-13T20:00:00Z"}}, "summary", dashboardTestInstant("2026-10-14T10:00:00Z"))
	if err != nil {
		panic(err)
	}
	return q
}

func dashboardTestNotebook(name, cpu, memory string, gpu int64) *unstructured.Unstructured {
	requests := map[string]any{"cpu": cpu, "memory": memory}
	if gpu > 0 {
		requests["nvidia.com/gpu"] = fmt.Sprint(gpu)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubeflow.org/v1beta1", "kind": "Notebook",
		"metadata": map[string]any{"name": name, "namespace": "user-a"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": name, "resources": map[string]any{"requests": requests}},
			map[string]any{"name": "sidecar", "resources": map[string]any{"requests": map[string]any{"cpu": "1", "memory": "1Gi"}}},
		}}}}, "status": map[string]any{"readyReplicas": int64(1)},
	}}
}

func TestDashboardQueryValidation(t *testing.T) {
	now := dashboardTestInstant("2026-10-14T10:00:00Z")
	q, err := parseDashboardQuery(url.Values{}, "summary", now)
	if err != nil || !q.Window.From.Equal(dashboardTestInstant("2026-10-07T18:30:00Z")) || q.Window.Timezone != "Asia/Kolkata" {
		t.Fatalf("default IST window: %#v %v", q, err)
	}
	tests := []struct{ kind, query string }{
		{"summary", "from=2026-10-01T00:00:00Z"},
		{"summary", "from=2026-10-01&to=2026-10-02"},
		{"summary", "from=2026-10-14T00:00:00Z&to=2026-10-15T00:00:00Z"},
		{"summary", "from=2026-09-01T00:00:00Z&to=2026-10-14T00:00:00Z"},
		{"summary", "from=2026-10-01T00:00:00Z&to=2026-10-01T00:00:00Z"},
		{"summary", "resourceType=browser"}, {"summary", "resourceType=cpu&resourceType=gpu"},
		{"summary", "timezone=UTC"}, {"summary", "from="}, {"users", "size=101"}, {"users", "page=-1"},
		{"users", "page=9223372036854775807&size=100"}, {"users", "sort=secret"}, {"users", "q=" + strings.Repeat("a", 101)},
		{"sandboxes", "from=2026-10-01T00:00:00Z&to=2026-10-02T00:00:00Z"}, {"sandboxes", "state=deleted"}, {"sandboxes", "userId=nope"},
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.query, func(t *testing.T) {
			values, _ := url.ParseQuery(tt.query)
			if _, err := parseDashboardQuery(values, tt.kind, now); err == nil {
				t.Fatal("expected invalid query to fail")
			}
		})
	}
	valid := url.Values{"from": {"2026-10-12T23:30:00+05:30"}, "to": {"2026-10-13T01:30:00+05:30"}}
	q, err = parseDashboardQuery(valid, "trends", now)
	if err != nil || !q.Window.From.Equal(dashboardTestInstant("2026-10-12T18:00:00Z")) {
		t.Fatalf("offset normalization: %#v %v", q, err)
	}
}

func TestDashboardConfigValidation(t *testing.T) {
	for _, tt := range []struct {
		env   ApiEnv
		valid bool
	}{
		{ApiEnv{}, true},
		{ApiEnv{DashboardEventTimezone: "UTC"}, true},
		{ApiEnv{DashboardEventTimezone: "Asia/Kolkata"}, true},
		{ApiEnv{DashboardEventTimezone: "not/a-timezone"}, false},
	} {
		if (validateDashboardConfig(tt.env) == nil) != tt.valid {
			t.Fatalf("unexpected dashboard config validation: %#v", tt.env)
		}
	}
}

func TestDashboardSessionHoursAndDailyBoundaries(t *testing.T) {
	q := dashboardTestQuery()
	start, end := dashboardTestInstant("2026-10-12T17:00:00Z"), dashboardTestInstant("2026-10-12T20:00:00Z")
	d := dashboardData{HistoryReady: true, Owners: map[string]*string{"a": nil, "b": nil}, Sessions: []dashboardSession{
		{UserID: "a", ResourceType: "cpu", Status: "completed", Started: &start, Ended: &end},
		{UserID: "b", ResourceType: "gpu", Status: "active", Started: dashboardTimePtr(dashboardTestInstant("2026-10-13T17:30:00Z")), SlotEnd: dashboardTestInstant("2026-10-13T19:30:00Z")},
		{UserID: "a", ResourceType: "cpu", Status: "completed", Started: &start, SlotStart: q.Window.From, SlotEnd: q.Window.To},
		{UserID: "b", ResourceType: "gpu", Status: "expired", SlotStart: q.Window.From, SlotEnd: q.Window.To},
		{UserID: "b", ResourceType: "gpu", Status: "completed", Started: &end, Ended: &start, SlotStart: q.Window.From, SlotEnd: q.Window.To},
	}}
	view := buildDashboardView(q, d, dashboardRuntimeSnapshot{Notebooks: map[dashboardNotebookKey]*unstructured.Unstructured{}}, "cluster")
	p := view.Stats.Period
	if p.Hours.CPU != 2 || p.Hours.GPU != 2 || p.Hours.Total != 4 || p.UsersWithSessions != 2 || p.ExcludedSessionRows != 2 {
		t.Fatalf("unexpected period %#v", p)
	}
	if len(view.Trends.Points) != 3 || view.Trends.Points[0].Hours.CPU != 0.5 || view.Trends.Points[1].Hours.CPU != 1.5 || view.Trends.Points[1].Hours.GPU != 1 || view.Trends.Points[2].Hours.GPU != 1 {
		t.Fatalf("IST clipping: %#v", view.Trends.Points)
	}
	var chart, user float64
	for _, point := range view.Trends.Points {
		chart += point.Hours.Total
	}
	for _, row := range view.Users {
		user += row.Stats.Period.Hours.Total
	}
	if chart != p.Hours.Total || user != p.Hours.Total {
		t.Fatal("summary, user and chart hours disagree")
	}
	if !view.Meta.Partial || view.Meta.Warnings[0].Code != "INCOMPLETE_SESSION_HISTORY" {
		t.Fatalf("missing history warning: %#v", view.Meta)
	}
	// Open sessions never grow beyond the planned slot or the observation instant.
	q.Now = dashboardTestInstant("2026-10-13T18:00:00Z")
	q.Window.To = q.Now
	view = buildDashboardView(q, d, dashboardRuntimeSnapshot{Notebooks: map[dashboardNotebookKey]*unstructured.Unstructured{}}, "cluster")
	if view.Stats.Period.Hours.GPU != 0.5 {
		t.Fatalf("open-session now cap: %#v", view.Stats.Period)
	}
}

func TestDashboardRuntimeAndResourceScope(t *testing.T) {
	q := dashboardTestQuery()
	d := dashboardData{HistoryReady: true, Owners: map[string]*string{"user-a": nil}, Notebooks: []dashboardNotebook{
		{ID: "1", UserID: "user-a", Name: "cpu", Namespace: "user-a", ResourceType: "cpu", CPU: "0.25", Memory: "512Mi", LatestEvent: constants.StatusNotebookApplied},
		{ID: "2", UserID: "user-a", Name: "gpu", Namespace: "user-a", ResourceType: "gpu", CPU: "2", Memory: "8Gi", GPUType: dashboardStringPtr("nvidia.com/gpu"), GPURequest: 1, GPULimit: 1, LatestEvent: constants.StatusNotebookApplied},
		{ID: "3", UserID: "user-a", Name: "deleted", ResourceType: "cpu", LatestEvent: "deleted"},
	}}
	snapshot := dashboardRuntimeSnapshot{Notebooks: map[dashboardNotebookKey]*unstructured.Unstructured{
		{"user-a", "cpu"}: dashboardTestNotebook("cpu", "250m", "512Mi", 0),
		{"user-a", "gpu"}: dashboardTestNotebook("gpu", "2", "8Gi", 1),
	}}
	view := buildDashboardView(q, d, snapshot, "cluster")
	c := view.Stats.Current
	if c.Sandboxes.Total != 2 || *c.Sandboxes.RunningCPU != 1 || *c.Sandboxes.RunningGPU != 1 || *c.RunningUsers != 1 {
		t.Fatalf("counts: %#v", c)
	}
	if c.RunningRequests == nil || c.RunningRequests.CPU != 2.25 || c.RunningRequests.Memory != 9126805504 || len(c.RunningRequests.GPU) != 1 || c.RunningRequests.GPU[0].Units != 1 {
		t.Fatalf("requests must exclude sidecar: %#v", c.RunningRequests)
	}
	// The worker writes extended-resource requests as integers in the Notebook CR.
	// They must reconcile as quantities just like the API's string form.
	gpuObj := snapshot.Notebooks[dashboardNotebookKey{"user-a", "gpu"}].DeepCopy()
	containers, _, _ := unstructured.NestedSlice(gpuObj.Object, "spec", "template", "spec", "containers")
	containers[0].(map[string]any)["resources"].(map[string]any)["requests"].(map[string]any)["nvidia.com/gpu"] = int64(1)
	if err := unstructured.SetNestedSlice(gpuObj.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		t.Fatal(err)
	}
	if !dashboardResourcesMatch(d.Notebooks[1], gpuObj) {
		t.Fatal("integer GPU requests from the worker must reconcile")
	}
	q.ResourceType = "gpu"
	view = buildDashboardView(q, d, snapshot, "cluster")
	if view.Stats.Current.Sandboxes.Total != 1 || view.Stats.Current.RunningRequests.CPU != 2 {
		t.Fatal("GPU scope must retain GPU notebook CPU demand")
	}
	q.ResourceType = "all"
	delete(snapshot.Notebooks, dashboardNotebookKey{"user-a", "gpu"})
	snapshot.ConfirmedMissing = map[dashboardNotebookKey]string{{"user-a", "gpu"}: "2"}
	view = buildDashboardView(q, d, snapshot, "cluster")
	if view.Stats.Current.Sandboxes.ByState["orphaned"] != 1 {
		t.Fatal("successful absence should be orphaned")
	}
	view = buildDashboardView(q, d, dashboardRuntimeSnapshot{}, "cluster")
	if view.Stats.Current.Sandboxes.ByState["unknown"] != 2 || view.Stats.Current.RunningRequests != nil || view.Stats.Current.RunningUsers != nil || view.Stats.Current.Sandboxes.RunningGPU != nil {
		t.Fatal("outage must yield unknown/null, not orphaned/zero")
	}
	if view.Stats.Period == nil {
		t.Fatal("Kubernetes outage must preserve session stats")
	}
	snapshot.Notebooks[dashboardNotebookKey{"user-a", "gpu"}] = dashboardTestNotebook("gpu", "3", "8Gi", 1)
	view = buildDashboardView(q, d, snapshot, "cluster")
	if view.Stats.Current.RunningRequests != nil || !view.Meta.Partial {
		t.Fatal("mismatched live allocation must be unavailable")
	}
	d.HistoryReady = false
	view = buildDashboardView(q, d, snapshot, "cluster")
	if view.Stats.Period != nil || view.Trends.Points != nil || view.Trends.ExcludedSessionRows != nil {
		t.Fatal("unverified legacy timezone must disable history")
	}
}

func TestDashboardRuntimeCacheAndFailure(t *testing.T) {
	obj := dashboardTestNotebook("cpu", "0.25", "512Mi", 0)
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{dashboardNotebookGVR: "NotebookList"}, obj)
	app := &application{k8sClient: &k8s.K8sClient{Dynamic: client}}
	now := dashboardTestInstant("2026-10-14T10:00:00Z")
	first := app.dashboardRuntime(context.Background(), now)
	if len(first.Notebooks) != 1 {
		t.Fatal("expected cluster-wide collection")
	}
	app.dashboardRuntime(context.Background(), now.Add(10*time.Second))
	if len(client.Actions()) != 1 {
		t.Fatal("cache should avoid another list")
	}
	client.PrependReactor("list", "notebooks", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("unavailable") })
	stale := app.dashboardRuntime(context.Background(), now.Add(40*time.Second))
	if !stale.Stale || len(stale.Notebooks) != 1 || !stale.ObservedAt.Equal(now) {
		t.Fatal("bounded stale sample should remain usable")
	}
	old := app.dashboardRuntime(context.Background(), now.Add(65*time.Second))
	if old.Notebooks != nil || old.ObservedAt == nil {
		t.Fatal("expired stale sample must be unavailable, retaining observation time")
	}
}

func TestDashboardWorkerContainerAndLegacyNames(t *testing.T) {
	q := dashboardTestQuery()
	n := dashboardNotebook{ID: "1", UserID: "user-a", Name: "cpu-demo", Namespace: "user-a", ResourceType: "cpu", CPU: "0.25", Memory: "512Mi", LatestEvent: constants.StatusNotebookApplied}
	for _, name := range []string{n.Name, "notebook"} {
		t.Run(name, func(t *testing.T) {
			obj := dashboardTestNotebook(n.Name, "0.25", "512Mi", 0)
			containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
			containers[0].(map[string]any)["name"] = name
			// Primary selection must not depend on its position in the template.
			containers[0], containers[1] = containers[1], containers[0]
			if err := unstructured.SetNestedSlice(obj.Object, containers, "spec", "template", "spec", "containers"); err != nil {
				t.Fatal(err)
			}
			view := buildDashboardView(q, dashboardData{HistoryReady: true, Notebooks: []dashboardNotebook{n}}, dashboardRuntimeSnapshot{Notebooks: map[dashboardNotebookKey]*unstructured.Unstructured{{n.Namespace, n.Name}: obj}}, "cluster")
			if requests := view.Stats.Current.RunningRequests; requests == nil || requests.CPU != 0.25 || requests.Memory != 536870912 {
				t.Fatalf("worker/legacy primary resources with a sidecar must reconcile: %#v", requests)
			}
		})
	}
}

func TestDashboardCanceledRefreshPreservesCacheAndAllowsRetry(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(fmt.Sprint("warm=", warm), func(t *testing.T) {
			client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{dashboardNotebookGVR: "NotebookList"}, dashboardTestNotebook("cpu", "0.25", "512Mi", 0))
			app := &application{k8sClient: &k8s.K8sClient{Dynamic: client}}
			now := dashboardTestInstant("2026-10-14T10:00:00Z")
			if warm {
				app.dashboardRuntime(context.Background(), now)
				now = now.Add(40 * time.Second)
			}
			before := app.dashboardCache.checkedAt
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			interrupted, calls := false, 0
			client.PrependReactor("list", "notebooks", func(clienttesting.Action) (bool, runtime.Object, error) {
				calls++
				if !interrupted {
					interrupted = true
					cancel()
					return true, nil, context.Canceled
				}
				return false, nil, nil
			})
			app.dashboardRuntime(ctx, now)
			if !app.dashboardCache.checkedAt.Equal(before) || app.dashboardCache.snapshot.Stale {
				t.Fatal("caller cancellation changed shared cache state or retry eligibility")
			}
			if warm && len(app.dashboardCache.snapshot.Notebooks) != 1 {
				t.Fatal("last good snapshot was discarded")
			}
			snapshot := app.dashboardRuntime(context.Background(), now.Add(time.Second))
			if calls != 2 || len(snapshot.Notebooks) != 1 || snapshot.Stale {
				t.Fatalf("healthy caller must retry immediately: attempts=%d snapshot=%#v", calls, snapshot)
			}
		})
	}
}

func TestDashboardNewNotebookRefreshesMissingCachedIdentity(t *testing.T) {
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{dashboardNotebookGVR: "NotebookList"})
	app := &application{k8sClient: &k8s.K8sClient{Dynamic: client}}
	q := dashboardTestQuery()
	app.dashboardRuntime(context.Background(), q.Now)
	n := dashboardNotebook{ID: "1", UserID: "user-a", Name: "new-cpu", Namespace: "user-a", ResourceType: "cpu", CPU: "0.25", Memory: "512Mi", LatestEvent: constants.StatusNotebookApplied}
	if _, err := client.Resource(dashboardNotebookGVR).Namespace(n.Namespace).Create(context.Background(), dashboardTestNotebook(n.Name, n.CPU, n.Memory, 0), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	q.Now = q.Now.Add(time.Second)
	d := dashboardData{HistoryReady: true, Notebooks: []dashboardNotebook{n}}
	old := app.dashboardRuntime(context.Background(), q.Now)
	view := buildDashboardView(q, d, old, "cluster")
	if view.Stats.Current.Sandboxes.ByState["unknown"] != 1 || view.Stats.Current.Sandboxes.ByState["orphaned"] != 0 || !view.Meta.Partial {
		t.Fatal("unconfirmed cached absence must not mean orphaned")
	}
	refreshed := app.dashboardRuntime(context.Background(), q.Now, n)
	view = buildDashboardView(q, d, refreshed, "cluster")
	if view.Stats.Current.Sandboxes.ByState["running"] != 1 || view.Stats.Current.RunningRequests == nil {
		t.Fatalf("newly ready sandbox must be discovered: %#v", view.Stats.Current)
	}
}

func TestDashboardConfirmedAbsenceIsCachedAndNameReuseIsRechecked(t *testing.T) {
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{dashboardNotebookGVR: "NotebookList"})
	app := &application{k8sClient: &k8s.K8sClient{Dynamic: client}}
	q := dashboardTestQuery()
	n := dashboardNotebook{ID: "1", UserID: "user-a", Name: "cpu", Namespace: "user-a", ResourceType: "cpu", CPU: "0.25", Memory: "512Mi", LatestEvent: constants.StatusNotebookApplied}
	snapshot := app.dashboardRuntime(context.Background(), q.Now, n)
	view := buildDashboardView(q, dashboardData{Notebooks: []dashboardNotebook{n}}, snapshot, "cluster")
	if view.Stats.Current.Sandboxes.ByState["orphaned"] != 1 {
		t.Fatal("confirmed absence must still identify a true orphan")
	}
	app.dashboardRuntime(context.Background(), q.Now.Add(time.Second), n)
	if len(client.Actions()) != 1 {
		t.Fatal("confirmed missing identity should honor the normal cache interval")
	}
	stale := snapshot
	stale.Stale = true
	view = buildDashboardView(q, dashboardData{Notebooks: []dashboardNotebook{n}}, stale, "cluster")
	if view.Stats.Current.Sandboxes.ByState["orphaned"] != 0 || view.Stats.Current.Sandboxes.ByState["unknown"] != 1 {
		t.Fatal("failed refresh cannot confirm current absence")
	}
	if _, err := client.Resource(dashboardNotebookGVR).Namespace(n.Namespace).Create(context.Background(), dashboardTestNotebook(n.Name, n.CPU, n.Memory, 0), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	n.ID = "2"
	snapshot = app.dashboardRuntime(context.Background(), q.Now.Add(2*time.Second), n)
	view = buildDashboardView(q, dashboardData{Notebooks: []dashboardNotebook{n}}, snapshot, "cluster")
	if view.Stats.Current.Sandboxes.ByState["running"] != 1 {
		t.Fatal("a new row reusing the name must not inherit the previous absence")
	}
}

func TestDashboardWaitingCallerCanCancelWithoutBlockingRefresh(t *testing.T) {
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{dashboardNotebookGVR: "NotebookList"}, dashboardTestNotebook("cpu", "0.25", "512Mi", 0))
	started, release := make(chan struct{}), make(chan struct{})
	client.PrependReactor("list", "notebooks", func(clienttesting.Action) (bool, runtime.Object, error) {
		close(started)
		<-release
		return false, nil, nil
	})
	app := &application{k8sClient: &k8s.K8sClient{Dynamic: client}}
	now := dashboardTestInstant("2026-10-14T10:00:00Z")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); app.dashboardRuntime(context.Background(), now) }()
	defer func() { close(release); wg.Wait() }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { app.dashboardRuntime(ctx, now); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled waiter blocked behind the shared refresh")
	}
}

func TestDashboardUsersSortAndPagination(t *testing.T) {
	users := []DashboardUser{{UserID: "b", Stats: newDashboardStats(true)}, {UserID: "a", Stats: newDashboardStats(true)}, {UserID: "c", Stats: newDashboardStats(false)}}
	users[0].Stats.Period.Hours.Total = 4
	users[1].Stats.Period.Hours.Total = 4
	sortDashboardUsers(users, "bookedSessionHours:desc")
	if users[0].UserID != "a" || users[1].UserID != "b" || users[2].UserID != "c" {
		t.Fatal("tie breaker or nulls-last failed")
	}
	info, start, end := dashboardPagination(3, dashboardQuery{Page: 2, Size: 2})
	if start != 2 || end != 3 || info.TotalPages != 2 || info.HasNext || !info.HasPrevious {
		t.Fatalf("pagination: %#v %d %d", info, start, end)
	}
	_, start, end = dashboardPagination(3, dashboardQuery{Page: 100, Size: 2})
	if start != 3 || end != 3 {
		t.Fatal("out-of-range page must be empty")
	}
}

func TestDashboardAuthenticatedRoutes(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	app := testJupyterLiteApp("")
	app.env.KeycloakClientID = "frontend"
	app.env.PlatformTokenNotebookClientID = "sandbox-notebook"
	app.env.KeycloakURL = "https://idp.example.com/auth"
	app.env.KeycloakRealm = "sandbox"
	app.env.KeycloakPublicKey = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	store := &dashboardFakeStore{data: dashboardData{HistoryReady: true, Owners: map[string]*string{}}}
	app.dashboardStore = store
	claims := func() JWTPayload {
		return JWTPayload{Sub: "00000000-0000-4000-8000-000000000001", Exp: time.Now().Add(time.Hour).Unix(), Iss: app.platformTokenIssuer(), Aud: jwt.ClaimStrings{"sandbox-notebook", "account"}, Azp: "frontend", Email: "admin@example.com", EmailVerified: true, RealmAccess: RealmAccess{Roles: []string{"cos_admin"}}}
	}
	tests := []struct {
		name   string
		mutate func(*JWTPayload)
		status int
	}{
		{"admin", func(*JWTPayload) {}, 200},
		{"frontend audience", func(p *JWTPayload) { p.Aud = jwt.ClaimStrings{"frontend"} }, 200},
		{"other audience", func(p *JWTPayload) { p.Aud = jwt.ClaimStrings{"other"} }, 200},
		{"missing audience", func(p *JWTPayload) { p.Aud = nil }, 200},
		{"ordinary", func(p *JWTPayload) { p.RealmAccess.Roles = nil }, 403},
		{"compute", func(p *JWTPayload) { p.RealmAccess.Roles = []string{"compute"} }, 403},
		{"lookalike role", func(p *JWTPayload) { p.RealmAccess.Roles = []string{"COS_ADMIN"} }, 403},
		{"delegated", func(p *JWTPayload) { p.Azp = "sandbox-notebook" }, 401},
		{"wrong client", func(p *JWTPayload) { p.Azp = "other"; p.Aud = jwt.ClaimStrings{"frontend"} }, 401},
		{"missing client", func(p *JWTPayload) { p.Azp = "" }, 401},
		{"wrong issuer", func(p *JWTPayload) { p.Iss = "https://wrong.example.com" }, 401},
		{"missing expiry", func(p *JWTPayload) { p.Exp = 0 }, 401},
		{"missing subject", func(p *JWTPayload) { p.Sub = "" }, 401},
		{"expired", func(p *JWTPayload) { p.Exp = time.Now().Add(-time.Hour).Unix() }, 401},
		{"unverified email", func(p *JWTPayload) { p.EmailVerified = false }, 401},
	}
	for _, mode := range []bool{true, false} {
		app.env.BookingsEnabled = mode
		for _, tt := range tests {
			t.Run(tt.name+"/mode="+map[bool]string{true: "booking", false: "direct"}[mode], func(t *testing.T) {
				app.rateLimiter = NewIPRateLimiter(100, 60)
				c := claims()
				tt.mutate(&c)
				token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, &c).SignedString(key)
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{"summary", "trends", "users", "sandboxes"} {
					before := store.calls
					req := httptest.NewRequest(http.MethodGet, dashboardBasePath+"/"+path, nil)
					req.Header.Set("Authorization", "Bearer "+token)
					rec := httptest.NewRecorder()
					app.router().ServeHTTP(rec, req)
					if rec.Code != tt.status {
						t.Fatalf("%s: got %d want %d: %s", path, rec.Code, tt.status, rec.Body.String())
					}
					if tt.status != 200 && store.calls != before {
						t.Fatal("denied request accessed dashboard DB")
					}
					if tt.status == 200 {
						if rec.Header().Get("Cache-Control") != "private, no-store" {
							t.Fatal("missing private no-store")
						}
						if strings.Contains(rec.Body.String(), "credit") {
							t.Fatal("unexpected credit data")
						}
					}
				}
			})
		}
	}
}

func TestDashboardHandlersEmptyQueriesAndDatabaseFailure(t *testing.T) {
	app := &application{env: ApiEnv{KeycloakClientID: "frontend"}}
	store := &dashboardFakeStore{data: dashboardData{Owners: map[string]*string{}}}
	app.dashboardStore = store
	for _, kind := range []string{"summary", "trends", "users", "sandboxes"} {
		req := requestWithLoggerContext(http.MethodGet, dashboardBasePath+"/"+kind, nil)
		ctx := context.WithValue(req.Context(), UserContextKey, UserInfo{ClientID: "frontend", Roles: []string{"cos_admin"}})
		rec := httptest.NewRecorder()
		mux := http.NewServeMux()
		app.registerDashboardRoutes(mux)
		mux.ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", kind, rec.Code, rec.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var meta DashboardMeta
		if err := json.Unmarshal(body["meta"], &meta); err != nil {
			t.Fatal(err)
		}
		if meta.ClusterID != "sandbox-primary" {
			t.Fatalf("%s must use the built-in single-cluster identity, got %q", kind, meta.ClusterID)
		}
		if kind == "summary" {
			var summary DashboardSummaryResult
			if err := json.Unmarshal(body["result"], &summary); err != nil {
				t.Fatal(err)
			}
			if summary.Cluster.ClusterID != "sandbox-primary" || summary.Cluster.Name != "Primary sandbox cluster" {
				t.Fatalf("unexpected built-in cluster labels: %#v", summary.Cluster)
			}
		}
		if kind == "users" || kind == "sandboxes" {
			if string(body["result"]) != "[]" {
				t.Fatal("empty lists must be arrays")
			}
		}
	}
	store.err = errors.New("database password or host should never reach response")
	req := requestWithLoggerContext(http.MethodGet, dashboardBasePath+"/summary", nil)
	rec := httptest.NewRecorder()
	app.dashboardSummary(rec, req)
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("unsafe DB failure: %d %s", rec.Code, rec.Body.String())
	}
	req = requestWithLoggerContext(http.MethodGet, dashboardBasePath+"/summary?size=10", nil)
	rec = httptest.NewRecorder()
	before := store.calls
	app.dashboardSummary(rec, req)
	if rec.Code != 400 || store.calls != before {
		t.Fatal("invalid query must fail before DB access")
	}
}

func TestDashboardEmptyStatsValues(t *testing.T) {
	stats := newDashboardStats(true)
	if !reflect.DeepEqual(stats.Current.RunningRequests.GPU, []DashboardGPUUnits{}) || math.IsNaN(stats.Period.Hours.Total) {
		t.Fatal("invalid empty DTO values")
	}
}

func TestDashboardRuntimePaginationDoesNotTreatPartialListAsAbsence(t *testing.T) {
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{dashboardNotebookGVR: "NotebookList"})
	calls := 0
	client.PrependReactor("list", "notebooks", func(a clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, &unstructured.UnstructuredList{Object: map[string]any{"metadata": map[string]any{"continue": "next"}}, Items: []unstructured.Unstructured{*dashboardTestNotebook("one", "1", "1Gi", 0)}}, nil
		}
		return true, nil, errors.New("second page unavailable")
	})
	app := &application{k8sClient: &k8s.K8sClient{Dynamic: client}}
	result := app.dashboardRuntime(context.Background(), time.Now())
	if calls != 2 || result.Notebooks != nil {
		t.Fatalf("partial list must not replace runtime snapshot: calls=%d result=%#v", calls, result)
	}
}
