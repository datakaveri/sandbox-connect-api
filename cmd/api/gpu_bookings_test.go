package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/gpuconfig"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSlotWindowPreservesISTWallClock(t *testing.T) {
	for _, tc := range []struct {
		date, start, end, wantEnd string
		spansMidnight             bool
	}{
		{"2026-09-29", "00:00", "04:00", "2026-09-29T04:00:00+05:30", false},
		{"2026-09-30", "20:00", "00:00", "2026-10-01T00:00:00+05:30", true},
		{"2026-12-31", "20:00", "00:00", "2027-01-01T00:00:00+05:30", false},
		{"2028-02-28", "20:00", "00:00", "2028-02-29T00:00:00+05:30", true},
	} {
		t.Run(tc.date+"/"+tc.start, func(t *testing.T) {
			// PostgreSQL timestamp-without-time-zone values are decoded in UTC.
			date, err := time.Parse("2006-01-02", tc.date)
			if err != nil {
				t.Fatal(err)
			}
			start, end, err := slotWindow(date, gpuconfig.SlotTemplate{
				StartTime: tc.start, EndTime: tc.end, SpansMidnight: tc.spansMidnight,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := start.Format(time.RFC3339); got != tc.date+"T"+tc.start+":00+05:30" {
				t.Fatalf("start = %s", got)
			}
			if got := end.Format(time.RFC3339); got != tc.wantEnd {
				t.Fatalf("end = %s, want %s", got, tc.wantEnd)
			}
		})
	}
}

// Run with TEST_BOOKING_DATABASE_URL pointing to a disposable PostgreSQL database.
// Every run creates and removes its own schema, using the repository's real DDL.
func bookingIntegrationApp(t *testing.T) *application {
	t.Helper()
	url := os.Getenv("TEST_BOOKING_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_BOOKING_DATABASE_URL to run booking PostgreSQL regression tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "booking_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close(ctx)
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ddl, err := os.ReadFile("../../db.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(ddl)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO profiles (user_id, email) VALUES ($1, 'test@example.com')`, bookingTestUser); err != nil {
		t.Fatal(err)
	}
	app := testJupyterLiteApp("")
	app.pgPool = &db.PgPool{Pool: pool}
	app.registrySecret.SecretType = "none"
	return app
}

const bookingTestUser = "00000000-0000-0000-0000-000000000001"

func seedTestBooking(t *testing.T, app *application, category, date, start, end, status, user string, keys []string) int64 {
	t.Helper()
	startAt, err := time.Parse("2006-01-02 15:04", start)
	if err != nil {
		t.Fatal(err)
	}
	endAt, err := time.Parse("2006-01-02 15:04", end)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = app.pgPool.Pool.QueryRow(context.Background(), `
		INSERT INTO bookings (user_id, category_name, resource_type, slot_date, slot_key, slot_keys,
		                      slot_start, slot_end, status, notebook_name, shutdown_warning_sent_at)
		VALUES ($1, $2, 'cpu', $3, $4, $5, $6, $7, $8, $9, NOW()) RETURNING id
	`, user, category, date, keys[0], keys, startAt, endAt, status, "test-"+uuid.NewString()).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func extendTestBooking(app *application, id int64) *httptest.ResponseRecorder {
	req := requestWithUserContext(http.MethodPatch, fmt.Sprintf("/v1/bookings/%d/extend", id), nil)
	req.SetPathValue("id", fmt.Sprint(id))
	rec := httptest.NewRecorder()
	app.extendGPUBooking(rec, req)
	return rec
}

func TestBookingExtensionIntegration(t *testing.T) {
	app := bookingIntegrationApp(t)
	for _, category := range []string{"cpu_basic", "basic"} {
		for _, tc := range []struct {
			name, date, start, end, nextEnd, nextClock string
		}{
			{"same day", "2026-09-29", "2026-09-29 12:00", "2026-09-29 16:00", "2026-09-29 20:00", "16:00"},
			{"extend into midnight", "2026-09-29", "2026-09-29 16:00", "2026-09-29 20:00", "2026-09-30 00:00", "20:00"},
			{"midnight", "2026-09-29", "2026-09-29 20:00", "2026-09-30 00:00", "2026-09-30 04:00", "00:00"},
			{"month rollover", "2026-09-30", "2026-09-30 20:00", "2026-10-01 00:00", "2026-10-01 04:00", "00:00"},
			{"year rollover", "2026-12-31", "2026-12-31 20:00", "2027-01-01 00:00", "2027-01-01 04:00", "00:00"},
			{"leap day", "2028-02-28", "2028-02-28 20:00", "2028-02-29 00:00", "2028-02-29 04:00", "00:00"},
		} {
			t.Run(category+"/"+tc.name, func(t *testing.T) {
				if _, err := app.pgPool.Pool.Exec(context.Background(), "DELETE FROM bookings"); err != nil {
					t.Fatal(err)
				}
				keys := []string{category + "_" + tc.start[11:]}
				id := seedTestBooking(t, app, category, tc.date, tc.start, tc.end, "active", bookingTestUser, keys)
				rec := extendTestBooking(app, id)
				if rec.Code != http.StatusOK {
					t.Fatalf("extend = %d: %s", rec.Code, rec.Body.String())
				}
				var body ExtendBookingResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				wantKeys := append(keys, category+"_"+tc.nextClock)
				wantEnd := strings.Replace(tc.nextEnd, " ", "T", 1) + ":00+05:30"
				if body.Status != "active" || body.SlotDate != tc.date || body.SlotEnd != wantEnd || body.Duration != "8 Hours" || !reflect.DeepEqual(body.SlotKeys, wantKeys) {
					t.Fatalf("unexpected extension response: %+v", body)
				}
				var date, start, end, status string
				var used, warningCleared bool
				var storedKeys []string
				err := app.pgPool.Pool.QueryRow(context.Background(), `
					SELECT to_char(slot_date, 'YYYY-MM-DD'), to_char(slot_start, 'YYYY-MM-DD HH24:MI'),
					       to_char(slot_end, 'YYYY-MM-DD HH24:MI'), status, extension_used,
					       shutdown_warning_sent_at IS NULL, slot_keys FROM bookings WHERE id = $1
				`, id).Scan(&date, &start, &end, &status, &used, &warningCleared, &storedKeys)
				if err != nil {
					t.Fatal(err)
				}
				if date != tc.date || start != tc.start || end != tc.nextEnd || status != "active" || !used || !warningCleared || !reflect.DeepEqual(storedKeys, wantKeys) {
					t.Fatalf("unexpected stored extension: %s %s %s %s used=%v cleared=%v keys=%v", date, start, end, status, used, warningCleared, storedKeys)
				}
				if rec := extendTestBooking(app, id); rec.Code != http.StatusBadRequest {
					t.Fatalf("second extension = %d, want 400", rec.Code)
				}
			})
		}
	}
}

func TestBookingExtensionRestrictionsIntegration(t *testing.T) {
	app := bookingIntegrationApp(t)
	for _, tc := range []struct {
		name, status, start, message string
		keys                         []string
		legacy, gap                  bool
		wantStatus                   int
	}{
		{"inactive", "scheduled", "20:00", "Only active bookings", []string{"cpu_basic_20:00"}, false, false, http.StatusBadRequest},
		{"contiguous limit", "active", "16:00", "Maximum contiguous slot selection", []string{"cpu_basic_16:00", "cpu_basic_20:00"}, false, false, http.StatusBadRequest},
		{"missing midnight slot", "active", "20:00", "No next contiguous slot", []string{"cpu_basic_20:00"}, false, true, http.StatusBadRequest},
		{"legacy single slot", "active", "20:00", "", []string{"cpu_basic_20:00"}, true, false, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := app.pgPool.Pool.Exec(context.Background(), "DELETE FROM bookings"); err != nil {
				t.Fatal(err)
			}
			app.env.NotebookConfig.SlotConfigProfile = "production"
			if tc.gap {
				category, _ := gpuconfig.GetGPUCategory("production", "cpu_basic")
				var slots []gpuconfig.SlotTemplate
				for _, slot := range category.Slots {
					if slot.StartTime != "00:00" {
						slots = append(slots, slot)
					}
				}
				category.Slots = slots
				const profile = "test_midnight_gap"
				gpuconfig.GPUSlotConfigs[profile] = gpuconfig.GPUSlotConfigProfile{Categories: []gpuconfig.GPUCategory{*category}}
				t.Cleanup(func() { delete(gpuconfig.GPUSlotConfigs, profile) })
				app.env.NotebookConfig.SlotConfigProfile = profile
			}
			id := seedTestBooking(t, app, "cpu_basic", "2026-09-29", "2026-09-29 "+tc.start, "2026-09-30 00:00", tc.status, bookingTestUser, tc.keys)
			if tc.legacy {
				if _, err := app.pgPool.Pool.Exec(context.Background(), `UPDATE bookings SET slot_keys='{}' WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
			}
			rec := extendTestBooking(app, id)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.message) {
				t.Fatalf("extend = %d: %s, want %d containing %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.message)
			}
		})
	}
}

func TestMidnightExtensionCapacityIntegration(t *testing.T) {
	app := bookingIntegrationApp(t)
	for _, tc := range []struct {
		name, occupiedStart, occupiedEnd string
		wantStatus                       int
	}{
		{"next day full", "2026-09-30 00:00", "2026-09-30 04:00", http.StatusConflict},
		{"original day full", "2026-09-29 00:00", "2026-09-29 04:00", http.StatusOK},
		{"adjacent next slot full", "2026-09-30 04:00", "2026-09-30 08:00", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := app.pgPool.Pool.Exec(context.Background(), "DELETE FROM bookings"); err != nil {
				t.Fatal(err)
			}
			id := seedTestBooking(t, app, "cpu_basic", "2026-09-29", "2026-09-29 20:00", "2026-09-30 00:00", "active", bookingTestUser, []string{"cpu_basic_20:00"})
			category, _ := gpuconfig.GetGPUCategory("production", "cpu_basic")
			for i := 0; i < category.MaxConcurrentUsers; i++ {
				seedTestBooking(t, app, "cpu_basic", tc.occupiedStart[:10], tc.occupiedStart, tc.occupiedEnd, "scheduled", uuid.NewString(), []string{"cpu_basic_" + tc.occupiedStart[11:]})
			}
			if rec := extendTestBooking(app, id); rec.Code != tc.wantStatus {
				t.Fatalf("extend = %d: %s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			if tc.wantStatus == http.StatusConflict {
				var end string
				var used bool
				if err := app.pgPool.Pool.QueryRow(context.Background(), `SELECT to_char(slot_end, 'YYYY-MM-DD HH24:MI'), extension_used FROM bookings WHERE id=$1`, id).Scan(&end, &used); err != nil {
					t.Fatal(err)
				}
				if end != "2026-09-30 00:00" || used {
					t.Fatalf("failed extension changed booking: end=%s used=%v", end, used)
				}
			}
		})
	}
}

func TestMidnightExtensionAvailabilityIntegration(t *testing.T) {
	app := bookingIntegrationApp(t)
	// Choose the next month boundary within the production advance booking window.
	now := time.Now().In(istLocation())
	nextDay := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, istLocation())
	if nextDay.Sub(now) > 29*24*time.Hour {
		nextDay = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, istLocation())
	}
	date := nextDay.AddDate(0, 0, -1).Format("2006-01-02")
	nextDate := nextDay.Format("2006-01-02")
	category, _ := gpuconfig.GetGPUCategory("production", "cpu_basic")
	for i := 0; i < category.MaxConcurrentUsers; i++ {
		user := uuid.NewString()
		if i == 0 {
			user = bookingTestUser
		}
		seedTestBooking(t, app, "cpu_basic", date, date+" 20:00", nextDate+" 04:00", "active", user, []string{"cpu_basic_20:00", "cpu_basic_00:00"})
	}
	// Terminal bookings do not use capacity, including older single-key rows.
	terminalID := seedTestBooking(t, app, "cpu_basic", nextDate, nextDate+" 00:00", nextDate+" 04:00", "cancelled", uuid.NewString(), []string{"cpu_basic_00:00"})
	if _, err := app.pgPool.Pool.Exec(context.Background(), `UPDATE bookings SET slot_keys='{}' WHERE id=$1`, terminalID); err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{date, nextDate} {
		rec := httptest.NewRecorder()
		app.listGPUAvailableSlots(rec, requestWithUserContext(http.MethodGet, "/v1/slots/available?category=cpu_basic&date="+day, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("availability = %d: %s", rec.Code, rec.Body.String())
		}
		var body AvailableSlotsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, slot := range body.Slots {
			occupied := (day == date && slot.Key == "cpu_basic_20:00") || (day == nextDate && slot.Key == "cpu_basic_00:00")
			wantBooked := 0
			if occupied {
				wantBooked = category.MaxConcurrentUsers
			}
			if slot.BookedSlots != wantBooked || slot.AlreadyBooked != occupied || slot.AvailableSlots != category.MaxConcurrentUsers-wantBooked {
				t.Fatalf("%s: unexpected availability %+v", day, slot)
			}
		}
		// Calendar must attribute each reserved slot to the day it actually starts.
		rec = httptest.NewRecorder()
		app.listGPUCalendarSlots(rec, requestWithUserContext(http.MethodGet, "/v1/slots/calendar?category=cpu_basic&month="+day[:7], nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("calendar = %d: %s", rec.Code, rec.Body.String())
		}
		var calendar CalendarResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &calendar); err != nil {
			t.Fatal(err)
		}
		for _, d := range calendar.Days {
			if d.Date == day && d.SlotsAvailable != (len(category.Slots)-1)*category.MaxConcurrentUsers {
				t.Fatalf("unexpected calendar availability: %+v", d)
			}
		}
	}
	// New bookings also enforce capacity occupied by previous-day extensions.
	payload := fmt.Sprintf(`{"category":"cpu_basic","slotDate":%q,"slotKeys":["cpu_basic_00:00"],"notebookName":"new-notebook"}`, nextDate)
	rec := httptest.NewRecorder()
	app.createGPUBooking(rec, requestWithUserContext(http.MethodPost, "/v1/bookings", []byte(payload)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "is full") {
		t.Fatalf("create in extended slot = %d: %s", rec.Code, rec.Body.String())
	}
}
