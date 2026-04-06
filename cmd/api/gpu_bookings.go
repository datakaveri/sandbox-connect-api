package main

import (
	"context"
	"fmt"
	"net/http"
	"sandbox-backend-service/pkg/gpuconfig"
	"sandbox-backend-service/pkg/utils"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

var activeBookingStatuses = []string{"scheduled", "ready", "active", "shutting_down"}

func istLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.FixedZone("IST", 5*3600+1800)
	}
	return loc
}

func parseSlotDateIST(slotDate string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", slotDate, istLocation())
}

func parseClockIST(slotDate time.Time, hhmm string) (time.Time, error) {
	t, err := time.ParseInLocation("15:04", hhmm, istLocation())
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(slotDate.Year(), slotDate.Month(), slotDate.Day(), t.Hour(), t.Minute(), 0, 0, istLocation()), nil
}

func activeStatusSQLList() string {
	return "'" + strings.Join(activeBookingStatuses, "','") + "'"
}

// createGPUBooking godoc
// @Summary      Create slot booking
// @Description  Creates a CPU or GPU booking from code-defined categories and slot templates (API_GPU_SLOT_CONFIG_PROFILE)
// @Tags         bookings
// @Accept       json
// @Produce      json
// @Param        booking  body  CreateBookingRequest  true  "Booking request"
// @Success      201  {object}  CreateBookingResponse
// @Failure      400  {object}  Error400
// @Failure      403  {object}  Error403
// @Failure      404  {object}  Error404
// @Failure      409  {object}  Error409
// @Failure      422  {object}  Error422
// @Failure      429  {object}  Error429
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/bookings [post]
func (app *application) createGPUBooking(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}

	req, err := utils.DecodeAndValidate[CreateBookingRequest](r.Body, logger)
	if err != nil {
		sendError(w, logger, http.StatusUnprocessableEntity, "Invalid body")
		return
	}
	req.Category = strings.TrimSpace(strings.ToLower(req.Category))
	req.SlotKey = strings.TrimSpace(req.SlotKey)
	req.SlotDate = strings.TrimSpace(req.SlotDate)

	if errorMessage, gotError := getErrorMessageForNotebookName(req.NotebookName); gotError {
		sendError(w, logger, http.StatusUnprocessableEntity, errorMessage)
		return
	}

	profile := app.env.NotebookConfig.SlotConfigProfile
	category, ok := gpuconfig.GetGPUCategory(profile, req.Category)
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "Invalid category")
		return
	}
	slotTemplate, ok := gpuconfig.GetGPUSlotTemplate(profile, req.Category, req.SlotKey)
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "Invalid slot key for category")
		return
	}

	// Category-aware access control:
	// - If RequiredRole is set (e.g. GPU categories), enforce role.
	// - If RequiresCredits is set, enforce profile credit gating.
	if category.RequiredRole != "" && !contains(userInfo.Roles, category.RequiredRole) {
		sendError(w, logger, http.StatusForbidden, fmt.Sprintf("Required role '%s' is missing for this category", category.RequiredRole))
		return
	}
	SetAuditSandboxType(r, strings.ToLower(category.ResourceType))

	slotDate, err := parseSlotDateIST(req.SlotDate)
	if err != nil {
		sendError(w, logger, http.StatusBadRequest, "slotDate must be in YYYY-MM-DD format")
		return
	}
	slotStart, err := parseClockIST(slotDate, slotTemplate.StartTime)
	if err != nil {
		sendError(w, logger, http.StatusBadRequest, "Invalid slot start time configuration")
		return
	}
	slotEnd, err := parseClockIST(slotDate, slotTemplate.EndTime)
	if err != nil {
		sendError(w, logger, http.StatusBadRequest, "Invalid slot end time configuration")
		return
	}
	if slotTemplate.SpansMidnight || !slotEnd.After(slotStart) {
		slotEnd = slotEnd.Add(24 * time.Hour)
	}
	nowIST := time.Now().In(istLocation())
	if slotDate.Before(time.Date(nowIST.Year(), nowIST.Month(), nowIST.Day(), 0, 0, 0, 0, istLocation())) {
		sendError(w, logger, http.StatusBadRequest, "Cannot book a slot in the past")
		return
	}
	if !slotStart.After(nowIST) {
		sendError(w, logger, http.StatusBadRequest, "Cannot book a slot that already started")
		return
	}

	maxBookingDate := nowIST.AddDate(0, 0, category.AdvanceBookingDays)
	if slotDate.After(time.Date(maxBookingDate.Year(), maxBookingDate.Month(), maxBookingDate.Day(), 0, 0, 0, 0, istLocation())) {
		sendError(w, logger, http.StatusBadRequest, "slotDate exceeds advance booking window")
		return
	}
	minBookingDate := nowIST.AddDate(0, 0, category.MinAdvanceBookingDays)
	if slotDate.Before(time.Date(minBookingDate.Year(), minBookingDate.Month(), minBookingDate.Day(), 0, 0, 0, 0, istLocation())) {
		sendError(w, logger, http.StatusBadRequest, "slotDate does not satisfy minimum advance booking rule")
		return
	}

	ctx := r.Context()
	namespace := userInfo.Sub

	// Ensure profile and namespace prerequisites are present for all booking categories.
	var profileExists bool
	checkProfileQuery := `SELECT EXISTS(SELECT 1 FROM profiles WHERE user_id = $1)`
	err = app.pgPool.Pool.QueryRow(ctx, checkProfileQuery, userInfo.Sub).Scan(&profileExists)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !profileExists {
		if err := app.createKubeflowProfile(ctx, logger, userInfo.Sub, userInfo.Email); err != nil && !k8serrors.IsAlreadyExists(err) {
			logger.Error("failed to create kubeflow profile for booking flow", "error", err, "user_id", userInfo.Sub)
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
		_, err = app.pgPool.Pool.Exec(ctx, `
			INSERT INTO profiles (user_id, email)
			VALUES ($1, $2)
			ON CONFLICT (user_id) DO NOTHING
		`, userInfo.Sub, userInfo.Email)
		if err != nil {
			logger.Error("failed to create profile record for booking flow", "error", err, "user_id", userInfo.Sub)
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	// Booking flow must ensure registry pull secret as well, otherwise
	// scheduled->ready->worker path can hit ImagePullBackOff in user namespace.
	if app.registrySecret.SecretType != "none" {
		if err := app.waitForNamespace(ctx, logger, namespace); err != nil {
			logger.Error("namespace not ready for booking flow", "error", err, "namespace", namespace)
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
		if err := app.ensureRegistrySecret(ctx, logger, namespace); err != nil {
			logger.Error("failed to ensure registry secret for booking flow", "error", err, "namespace", namespace)
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	tx, err := app.pgPool.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback(ctx)

	if category.RequiresCredits {
		var canCreateCategory bool
		lockQuery := `SELECT can_create_gpu_notebook FROM profiles WHERE user_id = $1 FOR UPDATE NOWAIT`
		err = tx.QueryRow(ctx, lockQuery, userInfo.Sub).Scan(&canCreateCategory)
		if err != nil {
			if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "55P03" {
				sendError(w, logger, http.StatusTooManyRequests, "Another operation is in progress for your account, please try again shortly.")
				return
			}
			if err == pgx.ErrNoRows {
				sendError(w, logger, http.StatusForbidden, "Profile not found. Please create profile first.")
				return
			}
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
		if !canCreateCategory {
			sendError(w, logger, http.StatusForbidden, "Credit limit exceeded for this category.")
			return
		}
	}

	var slotBooked int
	slotCountQuery := fmt.Sprintf(`
		SELECT COUNT(1)
		FROM bookings
		WHERE category_name = $1
		  AND slot_key = $2
		  AND slot_date = $3
		  AND status IN (%s)
	`, activeStatusSQLList())
	err = tx.QueryRow(ctx, slotCountQuery, req.Category, req.SlotKey, slotDate.Format("2006-01-02")).Scan(&slotBooked)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	if slotBooked >= category.MaxConcurrentUsers {
		sendError(w, logger, http.StatusConflict, "Selected slot is full")
		return
	}

	var userActiveCount int
	userActiveCountQuery := fmt.Sprintf(`
		SELECT COUNT(1)
		FROM bookings
		WHERE user_id = $1
		  AND category_name = $2
		  AND status IN (%s)
	`, activeStatusSQLList())
	err = tx.QueryRow(ctx, userActiveCountQuery, userInfo.Sub, req.Category).Scan(&userActiveCount)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	if userActiveCount >= category.MaxActiveBookings {
		sendError(w, logger, http.StatusBadRequest, "Active booking limit exceeded")
		return
	}

	var weekCount int
	weekCountQuery := fmt.Sprintf(`
		SELECT COUNT(1)
		FROM bookings
		WHERE user_id = $1
		  AND category_name = $2
		  AND status IN (%s)
		  AND slot_date >= date_trunc('week', $3::date)::date
		  AND slot_date < (date_trunc('week', $3::date) + interval '7 days')::date
	`, activeStatusSQLList())
	err = tx.QueryRow(ctx, weekCountQuery, userInfo.Sub, req.Category, slotDate.Format("2006-01-02")).Scan(&weekCount)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	if weekCount >= category.MaxBookingsPerWeek {
		sendError(w, logger, http.StatusBadRequest, "Weekly booking limit exceeded")
		return
	}

	var hasUpcoming int
	upcomingQuery := fmt.Sprintf(`
		SELECT COUNT(1)
		FROM bookings
		WHERE user_id = $1
		  AND status IN (%s)
		  AND slot_start > $2
	`, activeStatusSQLList())
	err = tx.QueryRow(ctx, upcomingQuery, userInfo.Sub, nowIST).Scan(&hasUpcoming)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	if hasUpcoming > 0 {
		sendError(w, logger, http.StatusBadRequest, "You already have an upcoming booking")
		return
	}

	insertQuery := `
		INSERT INTO bookings (
			user_id, category_name, resource_type, slot_key, notebook_name, slot_date, slot_start, slot_end, status
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, 'scheduled'
		) RETURNING id
	`
	var bookingID int64
	err = tx.QueryRow(
		ctx,
		insertQuery,
		userInfo.Sub,
		req.Category,
		category.ResourceType,
		req.SlotKey,
		req.NotebookName,
		slotDate.Format("2006-01-02"),
		slotStart,
		slotEnd,
	).Scan(&bookingID)
	if err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
			sendError(w, logger, http.StatusConflict, "Duplicate booking for selected slot or notebook name")
			return
		}
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}

	sendResponseJson(w, logger, http.StatusCreated, CreateBookingResponse{
		BookingID:    bookingID,
		Status:       "scheduled",
		SlotDate:     slotDate.Format("2006-01-02"),
		SlotStart:    slotStart.Format(time.RFC3339),
		SlotEnd:      slotEnd.Format(time.RFC3339),
		NotebookName: req.NotebookName,
		ResourceType: category.ResourceType,
	})
}

// listGPUBookings godoc
// @Summary      List user bookings
// @Description  Lists CPU and GPU slot bookings for the current user with optional status filter and pagination
// @Tags         bookings
// @Produce      json
// @Param        status  query   string  false  "Comma-separated status filter"
// @Param        limit   query   int     false  "Max rows (default 10, max 50)"
// @Param        offset  query   int     false  "Pagination offset"
// @Success      200  {object}  BookingsListResponse
// @Failure      401  {object}  Error401
// @Failure      429  {object}  Error429
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/bookings [get]
func (app *application) listGPUBookings(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}

	limit := 10
	if limitRaw := r.URL.Query().Get("limit"); limitRaw != "" {
		if parsed, err := strconv.Atoi(limitRaw); err == nil && parsed > 0 {
			if parsed > 50 {
				limit = 50
			} else {
				limit = parsed
			}
		}
	}
	offset := 0
	if offsetRaw := r.URL.Query().Get("offset"); offsetRaw != "" {
		if parsed, err := strconv.Atoi(offsetRaw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// Build placeholders carefully: when `status` filter is present, we must not
	// shift the placeholders for LIMIT/OFFSET.
	//
	// Expected mapping:
	//   $1 = user_id
	//   optional $2 = status[] (if status filter provided)
	//   then $n-1 = limit, $n = offset
	args := []any{userInfo.Sub}
	query := `
		SELECT id, notebook_name, category_name, slot_key, status, slot_date, slot_start, slot_end, created_at
		FROM bookings
		WHERE user_id = $1
	`
	if statusFilter := strings.TrimSpace(r.URL.Query().Get("status")); statusFilter != "" {
		statuses := strings.Split(statusFilter, ",")
		cleaned := make([]string, 0, len(statuses))
		for _, s := range statuses {
			s = strings.TrimSpace(strings.ToLower(s))
			if s != "" {
				cleaned = append(cleaned, s)
			}
		}
		if len(cleaned) > 0 {
			// Cast to varchar[] to avoid any inference issues (status is VARCHAR).
			query += fmt.Sprintf(" AND status = ANY($%d::varchar[])", len(args)+1)
			args = append(args, cleaned)
		}
	}

	args = append(args, limit, offset)
	query += fmt.Sprintf(" ORDER BY slot_start ASC LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := app.pgPool.Pool.Query(r.Context(), query, args...)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to list bookings")
		return
	}
	defer rows.Close()

	profile := app.env.NotebookConfig.SlotConfigProfile
	bookings := make([]BookingListItem, 0, limit)
	for rows.Next() {
		var (
			id           int64
			notebookName string
			categoryName string
			slotKey      string
			status       string
			slotDate     time.Time
			slotStart    time.Time
			slotEnd      time.Time
			createdAt    time.Time
		)
		if err := rows.Scan(&id, &notebookName, &categoryName, &slotKey, &status, &slotDate, &slotStart, &slotEnd, &createdAt); err != nil {
			sendError(w, logger, http.StatusInternalServerError, "Failed to list bookings")
			return
		}

		displayName := categoryName
		gpuMemory := ""
		resourceType := ""
		duration := ""
		if c, ok := gpuconfig.GetGPUCategory(profile, categoryName); ok {
			displayName = c.DisplayName
			gpuMemory = c.GPUMemory
			resourceType = c.ResourceType
		}
		if s, ok := gpuconfig.GetGPUSlotTemplate(profile, categoryName, slotKey); ok {
			duration = s.Label
		}
		if duration == "" {
			duration = fmt.Sprintf("%.0f Hours", slotEnd.Sub(slotStart).Hours())
		}

		bookings = append(bookings, BookingListItem{
			ID:           id,
			NotebookName: notebookName,
			Category:     categoryName,
			ResourceType: resourceType,
			DisplayName:  displayName,
			GPUMemory:    gpuMemory,
			Status:       status,
			SlotDate:     slotDate.Format("2006-01-02"),
			SlotStart:    slotStart.Format(time.RFC3339),
			SlotEnd:      slotEnd.Format(time.RFC3339),
			Duration:     duration,
			CreatedAt:    createdAt.Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to list bookings")
		return
	}

	nextOffset := -1
	if len(bookings) == limit {
		nextOffset = offset + limit
	}
	sendResponseJson(w, logger, http.StatusOK, BookingsListResponse{Bookings: bookings, NextOffset: nextOffset})
}

// listGPUAvailableSlots godoc
// @Summary      List available slots for date/category
// @Description  Returns slot-level availability for a category on a given date
// @Tags         bookings
// @Produce      json
// @Param        category  query  string  true  "Category name (CPU or GPU)"
// @Param        date      query  string  true  "Date in YYYY-MM-DD (IST)"
// @Success      200  {object}  AvailableSlotsResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      429  {object}  Error429
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/slots/available [get]
func (app *application) listGPUAvailableSlots(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	categoryName := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("category")))
	dateStr := strings.TrimSpace(r.URL.Query().Get("date"))
	if categoryName == "" || dateStr == "" {
		sendError(w, logger, http.StatusBadRequest, "category and date are required")
		return
	}

	profile := app.env.NotebookConfig.SlotConfigProfile
	category, ok := gpuconfig.GetGPUCategory(profile, categoryName)
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "Invalid category")
		return
	}
	slotDate, err := parseSlotDateIST(dateStr)
	if err != nil {
		sendError(w, logger, http.StatusBadRequest, "date must be in YYYY-MM-DD format")
		return
	}

	rows, err := app.pgPool.Pool.Query(
		r.Context(),
		fmt.Sprintf(`
			SELECT slot_key, COUNT(1)
			FROM bookings
			WHERE category_name = $1
			  AND slot_date = $2
			  AND status IN (%s)
			GROUP BY slot_key
		`, activeStatusSQLList()),
		categoryName,
		slotDate.Format("2006-01-02"),
	)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to calculate slot availability")
		return
	}
	defer rows.Close()

	bookedBySlot := map[string]int{}
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			sendError(w, logger, http.StatusInternalServerError, "Failed to calculate slot availability")
			return
		}
		bookedBySlot[key] = count
	}
	if err := rows.Err(); err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to calculate slot availability")
		return
	}

	slots := make([]AvailableSlot, 0, len(category.Slots))
	for _, s := range category.Slots {
		booked := bookedBySlot[s.Key]
		available := category.MaxConcurrentUsers - booked
		if available < 0 {
			available = 0
		}
		availability := "available"
		if available == 0 {
			availability = "full"
		} else if available*2 < category.MaxConcurrentUsers {
			availability = "limited"
		}
		slots = append(slots, AvailableSlot{
			Key:            s.Key,
			Label:          s.Label,
			StartTime:      s.StartTime,
			EndTime:        s.EndTime,
			TotalSlots:     category.MaxConcurrentUsers,
			BookedSlots:    booked,
			AvailableSlots: available,
			Availability:   availability,
		})
	}

	sendResponseJson(w, logger, http.StatusOK, AvailableSlotsResponse{
		Date:     slotDate.Format("2006-01-02"),
		Category: categoryName,
		ResourceType: category.ResourceType,
		Slots:    slots,
	})
}

// listGPUCalendarSlots godoc
// @Summary      List monthly availability summary
// @Description  Returns day-level availability totals for a category/month
// @Tags         bookings
// @Produce      json
// @Param        category  query  string  true  "Category name (CPU or GPU)"
// @Param        month     query  string  true  "Month in YYYY-MM"
// @Success      200  {object}  CalendarResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      429  {object}  Error429
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/slots/calendar [get]
func (app *application) listGPUCalendarSlots(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	categoryName := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("category")))
	monthStr := strings.TrimSpace(r.URL.Query().Get("month"))
	if categoryName == "" || monthStr == "" {
		sendError(w, logger, http.StatusBadRequest, "category and month are required")
		return
	}

	profile := app.env.NotebookConfig.SlotConfigProfile
	category, ok := gpuconfig.GetGPUCategory(profile, categoryName)
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "Invalid category")
		return
	}
	monthStart, err := time.ParseInLocation("2006-01", monthStr, istLocation())
	if err != nil {
		sendError(w, logger, http.StatusBadRequest, "month must be in YYYY-MM format")
		return
	}
	monthEnd := monthStart.AddDate(0, 1, 0)

	rows, err := app.pgPool.Pool.Query(
		r.Context(),
		fmt.Sprintf(`
			SELECT slot_date, COUNT(1)
			FROM bookings
			WHERE category_name = $1
			  AND slot_date >= $2
			  AND slot_date < $3
			  AND status IN (%s)
			GROUP BY slot_date
		`, activeStatusSQLList()),
		categoryName,
		monthStart.Format("2006-01-02"),
		monthEnd.Format("2006-01-02"),
	)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to calculate calendar availability")
		return
	}
	defer rows.Close()

	bookedByDate := map[string]int{}
	for rows.Next() {
		var d time.Time
		var c int
		if err := rows.Scan(&d, &c); err != nil {
			sendError(w, logger, http.StatusInternalServerError, "Failed to calculate calendar availability")
			return
		}
		bookedByDate[d.Format("2006-01-02")] = c
	}
	if err := rows.Err(); err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to calculate calendar availability")
		return
	}

	days := make([]CalendarDay, 0, monthEnd.Day())
	slotsPerDay := len(category.Slots) * category.MaxConcurrentUsers
	for d := monthStart; d.Before(monthEnd); d = d.AddDate(0, 0, 1) {
		ds := d.Format("2006-01-02")
		booked := bookedByDate[ds]
		available := slotsPerDay - booked
		if available < 0 {
			available = 0
		}
		days = append(days, CalendarDay{
			Date:            ds,
			HasAvailability: available > 0,
			SlotsAvailable:  available,
			SlotsTotal:      slotsPerDay,
		})
	}

	sendResponseJson(w, logger, http.StatusOK, CalendarResponse{
		Month:    monthStart.Format("2006-01"),
		Category: categoryName,
		ResourceType: category.ResourceType,
		Days:     days,
	})
}

// cancelGPUBooking godoc
// @Summary      Cancel scheduled booking
// @Description  Cancels a scheduled booking for the current user (scheduled only; use terminate for ready/active)
// @Tags         bookings
// @Produce      json
// @Param        id  path  int  true  "Booking ID"
// @Success      200  {object}  SwaggerMessageResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      429  {object}  Error429
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/bookings/{id}/cancel [patch]
func (app *application) cancelGPUBooking(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}

	bookingIDRaw := r.PathValue("id")
	bookingID, err := strconv.ParseInt(bookingIDRaw, 10, 64)
	if err != nil || bookingID <= 0 {
		sendError(w, logger, http.StatusBadRequest, "Invalid booking id")
		return
	}

	updateQuery := `
		UPDATE bookings
		SET status = 'cancelled'
		WHERE id = $1
		  AND user_id = $2
		  AND status = 'scheduled'
	`
	result, err := app.pgPool.Pool.Exec(r.Context(), updateQuery, bookingID, userInfo.Sub)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to cancel booking")
		return
	}
	if result.RowsAffected() == 0 {
		var exists bool
		checkQuery := `SELECT EXISTS(SELECT 1 FROM bookings WHERE id = $1 AND user_id = $2)`
		if err := app.pgPool.Pool.QueryRow(r.Context(), checkQuery, bookingID, userInfo.Sub).Scan(&exists); err != nil {
			sendError(w, logger, http.StatusInternalServerError, "Failed to cancel booking")
			return
		}
		if !exists {
			sendError(w, logger, http.StatusNotFound, "Booking not found")
			return
		}
		sendError(w, logger, http.StatusBadRequest, "Only scheduled bookings can be cancelled")
		return
	}

	sendResponse(w, logger, http.StatusOK, "Booking cancelled successfully")
}

// resetGPUBooking godoc
// @Summary      Reset stuck booking
// @Description  Marks a booking as cancelled/expired and best-effort deletes any linked notebook/PVC.
// @Tags         bookings
// @Produce      json
// @Param        id  path  int  true  "Booking ID"
// @Success      200  {object}  SwaggerMessageResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/bookings/{id}/reset [patch]
func (app *application) resetGPUBooking(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}

	bookingIDRaw := r.PathValue("id")
	bookingID, err := strconv.ParseInt(bookingIDRaw, 10, 64)
	if err != nil || bookingID <= 0 {
		sendError(w, logger, http.StatusBadRequest, "Invalid booking id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	tx, err := app.pgPool.Pool.Begin(ctx)
	if err != nil {
		logger.Error("resetGPUBooking: failed to begin transaction", "booking_id", bookingID, "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to reset booking")
		return
	}
	defer tx.Rollback(ctx)

	// Lock the bookings row only (no outer join locking in Postgres).
	var (
		dbStatus   string
		notebookID *int64
	)
	if err := tx.QueryRow(ctx, `
		SELECT status, notebook_id
		FROM bookings
		WHERE id = $1 AND user_id = $2
		FOR UPDATE
	`, bookingID, userInfo.Sub).Scan(&dbStatus, &notebookID); err != nil {
		if err == pgx.ErrNoRows {
			sendError(w, logger, http.StatusNotFound, "Booking not found")
			return
		}
		logger.Error("resetGPUBooking: failed to select booking row", "booking_id", bookingID, "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to reset booking")
		return
	}

	var (
		targetStatus    string
		sessionEndedNow bool
	)
	switch dbStatus {
	case "scheduled":
		targetStatus = "cancelled"
	case "ready":
		targetStatus = "expired"
		sessionEndedNow = true
	default:
		sendError(w, logger, http.StatusBadRequest, "Only scheduled/ready bookings can be reset")
		return
	}

	// Resolve notebook row before commit (tx is invalid after Commit).
	var (
		nbID    int64
		nbName  string
		nbNS    string
		nbPVC   string
		nbFound bool
	)
	if notebookID != nil {
		if err := tx.QueryRow(ctx, `
			SELECT n.id, n.name, n.namespace, n.pvc_name
			FROM notebooks n
			WHERE n.id = $1
		`, *notebookID).Scan(&nbID, &nbName, &nbNS, &nbPVC); err == nil {
			nbFound = true
		}
	}
	if !nbFound {
		if err := tx.QueryRow(ctx, `
			SELECT n.id, n.name, n.namespace, n.pvc_name
			FROM notebooks n
			WHERE n.booking_id = $1
			ORDER BY n.id DESC
			LIMIT 1
		`, bookingID).Scan(&nbID, &nbName, &nbNS, &nbPVC); err == nil {
			nbFound = true
		}
	}

	// Update booking state first.
	if sessionEndedNow {
		if _, err := tx.Exec(ctx, `
			UPDATE bookings
			SET status = $1,
			    session_ended_at = NOW(),
			    cleanup_completed_at = NOW(),
			    notebook_id = NULL
			WHERE id = $2 AND user_id = $3 AND status = $4
		`, targetStatus, bookingID, userInfo.Sub, dbStatus); err != nil {
			logger.Error("resetGPUBooking: failed to update ready->expired", "booking_id", bookingID, "error", err)
			sendError(w, logger, http.StatusInternalServerError, "Failed to reset booking")
			return
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE bookings
			SET status = $1,
			    notebook_id = NULL
			WHERE id = $2 AND user_id = $3 AND status = $4
		`, targetStatus, bookingID, userInfo.Sub, dbStatus); err != nil {
			logger.Error("resetGPUBooking: failed to update scheduled->cancelled", "booking_id", bookingID, "error", err)
			sendError(w, logger, http.StatusInternalServerError, "Failed to reset booking")
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("resetGPUBooking: failed to commit transaction", "booking_id", bookingID, "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to reset booking")
		return
	}

	if nbFound {
		// Best-effort K8s deletion (do not fail the API if K8s is already gone).
		_ = app.deleteNotebookFromK8s(context.Background(), logger, nbNS, nbName)
		_ = app.deletePVCFromK8s(context.Background(), logger, nbNS, nbPVC)

		// Remove the linked DB row.
		_, _ = app.pgPool.Pool.Exec(context.Background(), `DELETE FROM notebooks WHERE id=$1`, nbID)
	}

	sendResponse(w, logger, http.StatusOK, fmt.Sprintf("Booking reset successfully (%s -> %s)", dbStatus, targetStatus))
}

// terminateGPUBooking godoc
// @Summary      End booking session early
// @Description  Marks a ready, active, or shutting_down booking completed and best-effort deletes the linked notebook/PVC. Use cancel for scheduled only.
// @Tags         bookings
// @Produce      json
// @Param        id  path  int  true  "Booking ID"
// @Success      200  {object}  SwaggerMessageResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/bookings/{id}/terminate [patch]
func (app *application) terminateGPUBooking(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}

	bookingIDRaw := r.PathValue("id")
	bookingID, err := strconv.ParseInt(bookingIDRaw, 10, 64)
	if err != nil || bookingID <= 0 {
		sendError(w, logger, http.StatusBadRequest, "Invalid booking id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	tx, err := app.pgPool.Pool.Begin(ctx)
	if err != nil {
		logger.Error("terminateGPUBooking: failed to begin transaction", "booking_id", bookingID, "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to terminate booking")
		return
	}
	defer tx.Rollback(ctx)

	var (
		dbStatus   string
		notebookID *int64
	)
	if err := tx.QueryRow(ctx, `
		SELECT status, notebook_id
		FROM bookings
		WHERE id = $1 AND user_id = $2
		FOR UPDATE
	`, bookingID, userInfo.Sub).Scan(&dbStatus, &notebookID); err != nil {
		if err == pgx.ErrNoRows {
			sendError(w, logger, http.StatusNotFound, "Booking not found")
			return
		}
		logger.Error("terminateGPUBooking: failed to select booking row", "booking_id", bookingID, "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to terminate booking")
		return
	}

	if dbStatus == "completed" {
		sendResponse(w, logger, http.StatusOK, "Booking already terminated")
		return
	}

	switch dbStatus {
	case "ready", "active", "shutting_down":
	default:
		sendError(w, logger, http.StatusBadRequest,
			"Only ready, active, or shutting_down bookings can be terminated; use PATCH .../cancel for scheduled")
		return
	}

	var (
		nbID    int64
		nbName  string
		nbNS    string
		nbPVC   string
		nbFound bool
	)
	if notebookID != nil {
		if err := tx.QueryRow(ctx, `
			SELECT n.id, n.name, n.namespace, n.pvc_name
			FROM notebooks n
			WHERE n.id = $1
		`, *notebookID).Scan(&nbID, &nbName, &nbNS, &nbPVC); err == nil {
			nbFound = true
		}
	}
	if !nbFound {
		if err := tx.QueryRow(ctx, `
			SELECT n.id, n.name, n.namespace, n.pvc_name
			FROM notebooks n
			WHERE n.booking_id = $1
			ORDER BY n.id DESC
			LIMIT 1
		`, bookingID).Scan(&nbID, &nbName, &nbNS, &nbPVC); err == nil {
			nbFound = true
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE bookings
		SET status = 'completed',
		    session_ended_at = NOW(),
		    cleanup_completed_at = NOW(),
		    notebook_id = NULL
		WHERE id = $1 AND user_id = $2 AND status = $3
	`, bookingID, userInfo.Sub, dbStatus); err != nil {
		logger.Error("terminateGPUBooking: failed to update booking", "booking_id", bookingID, "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to terminate booking")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("terminateGPUBooking: failed to commit transaction", "booking_id", bookingID, "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to terminate booking")
		return
	}

	if nbFound {
		_ = app.deleteNotebookFromK8s(context.Background(), logger, nbNS, nbName)
		_ = app.deletePVCFromK8s(context.Background(), logger, nbNS, nbPVC)
		_, _ = app.pgPool.Pool.Exec(context.Background(), `DELETE FROM notebooks WHERE id=$1`, nbID)
	}

	sendResponse(w, logger, http.StatusOK, "Booking terminated successfully")
}
