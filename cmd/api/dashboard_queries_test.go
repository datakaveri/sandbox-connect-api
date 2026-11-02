package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/apimachinery/pkg/runtime"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	"sandbox-backend-service/pkg/k8s"
)

// Opt-in integration test. Always creates and removes its own schema; no
// deployment database or credentials are inferred from application config.
func TestDashboardPostgresQueries(t *testing.T) {
	dsn := os.Getenv("SANDBOX_DASHBOARD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SANDBOX_DASHBOARD_TEST_DATABASE_URL to an isolated PostgreSQL test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := pgx.Identifier{"dashboard_test_" + uuid.New().String()}.Sanitize()
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		cleanupConn, err := pgx.Connect(cleanupCtx, dsn)
		if err != nil {
			t.Errorf("connect for schema cleanup: %v", err)
			return
		}
		defer cleanupConn.Close(cleanupCtx)
		if _, err = cleanupConn.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("clean isolated schema: %v", err)
		}
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	bootstrap, err := os.ReadFile(filepath.Join("..", "..", "db.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(bootstrap)); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO profiles(user_id,email) VALUES
		('00000000-0000-4000-8000-000000000001','a@example.com'),
		('00000000-0000-4000-8000-000000000003','unused@example.com');
		INSERT INTO notebooks(user_id,name,namespace,storage_size,pvc_name,cpu_request,cpu_limit,memory_request,memory_limit,gpu_type,gpu_request,gpu_limit,events) VALUES
		('00000000-0000-4000-8000-000000000001','cpu','user-a','10Gi','cpu-pvc',0.25,1,'512Mi','1Gi',NULL,0,0,ARRAY['notebook-applied']),
		('00000000-0000-4000-8000-000000000001','gpu','user-a','10Gi','gpu-pvc',2,3,'8Gi','10Gi','nvidia.com/gpu',1,1,ARRAY['notebook-applied']),
		('00000000-0000-4000-8000-000000000002','deleted','user-b','10Gi','deleted-pvc',1,1,'1Gi','1Gi',NULL,0,0,ARRAY['notebook-applied','deleted']);
		INSERT INTO bookings(user_id,category_name,resource_type,slot_key,notebook_name,slot_date,slot_start,slot_end,status,session_started_at,session_ended_at) VALUES
		('00000000-0000-4000-8000-000000000001','cpu_basic','cpu','cpu','cpu','2026-10-12','2026-10-12 22:00','2026-10-13 02:00','completed','2026-10-12 17:00','2026-10-12 20:00'),
		('00000000-0000-4000-8000-000000000001','basic','gpu','gpu','gpu','2026-10-13','2026-10-13 22:00','2026-10-14 01:00','active','2026-10-13 17:30',NULL),
		('00000000-0000-4000-8000-000000000002','cpu_basic','cpu','invalid','old','2026-10-13','2026-10-13 22:00','2026-10-14 01:00','completed','2026-10-13 17:30',NULL),
		('00000000-0000-4000-8000-000000000002','cpu_basic','cpu','reset','reset','2026-10-13','2026-10-13 22:00','2026-10-14 01:00','expired',NULL,'2026-10-13 17:30'),
		('00000000-0000-4000-8000-000000000002','cpu_basic','cpu','late','late','2026-09-01','2026-09-01 00:00','2026-09-01 01:00','completed','2026-10-12 18:30','2026-10-12 19:30');
	`)
	if err != nil {
		t.Fatal(err)
	}
	store := postgresDashboardStore{pool: pool}
	q := dashboardTestQuery()
	q.Kind = "users"
	data, err := store.Load(ctx, q, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Owners) != 2 || len(data.Notebooks) != 2 || len(data.Sessions) != 4 {
		t.Fatalf("owners/current/session scope: owners=%#v notebooks=%#v sessions=%#v", data.Owners, data.Notebooks, data.Sessions)
	}
	view := buildDashboardView(q, data, dashboardRuntimeSnapshot{}, "cluster")
	if view.Stats.Period.Hours.CPU != 3 || view.Stats.Period.Hours.GPU != 2 || view.Stats.Period.ExcludedSessionRows != 1 {
		t.Fatalf("actual query normalization/overlap: %#v", view.Stats.Period)
	}
	if data.Owners["00000000-0000-4000-8000-000000000002"] != nil {
		t.Fatal("missing profile must not omit owner or fabricate email")
	}
	q.ResourceType = "gpu"
	data, err = store.Load(ctx, q, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Owners) != 1 || len(data.Notebooks) != 1 || len(data.Sessions) != 1 {
		t.Fatalf("GPU scope: %#v", data)
	}
	q.ResourceType = "all"
	q.Search = "%"
	data, err = store.Load(ctx, q, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Owners) != 0 || len(data.Notebooks) != 0 || len(data.Sessions) != 0 {
		t.Fatal("search must treat SQL wildcards literally")
	}
	q.Search = "a@example.com"
	data, err = store.Load(ctx, q, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Owners) != 1 || len(data.Notebooks) != 2 || len(data.Sessions) != 2 {
		t.Fatal("search must scope both current rows and history")
	}
	q.Search = ""
	q.Kind = "sandboxes"
	q.Window = nil
	q.UserID = "00000000-0000-4000-8000-000000000002"
	data, err = store.Load(ctx, q, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Notebooks) != 0 || len(data.Sessions) != 0 || data.HistoryReady {
		t.Fatal("deleted inventory or unverified history leaked")
	}

	// Exercise real DB reads through each handler. Optional response artifacts can
	// be validated against the standalone OpenAPI with a standard JSON Schema tool.
	for _, scenario := range []string{"healthy", "outage", "unverified-history"} {
		app := &application{env: ApiEnv{KeycloakClientID: "frontend", DashboardEventTimezone: "UTC"}, dashboardStore: store}
		if scenario != "outage" {
			client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[k8sschema.GroupVersionResource]string{dashboardNotebookGVR: "NotebookList"},
				dashboardTestNotebook("cpu", "0.25", "512Mi", 0), dashboardTestNotebook("gpu", "2", "8Gi", 1))
			app.k8sClient = &k8s.K8sClient{Dynamic: client}
		}
		if scenario == "unverified-history" {
			app.env.DashboardEventTimezone = ""
		}
		mux := http.NewServeMux()
		app.registerDashboardRoutes(mux)
		for _, kind := range []string{"summary", "trends", "users", "sandboxes"} {
			path := dashboardBasePath + "/" + kind
			req := requestWithLoggerContext(http.MethodGet, path, nil)
			req = req.WithContext(context.WithValue(req.Context(), UserContextKey, UserInfo{ClientID: "frontend", Roles: []string{"cos_admin"}}))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s/%s: %d %s", scenario, kind, rec.Code, rec.Body.String())
			}
			if dir := os.Getenv("SANDBOX_DASHBOARD_TEST_RESPONSES_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, scenario+"-"+kind+".json"), rec.Body.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
