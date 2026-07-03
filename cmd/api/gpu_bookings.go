package main

import (
	"context"
	"fmt"
	"net/http"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/gpuconfig"
	"sandbox-backend-service/pkg/utils"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

var activeBookingStatuses = []string{"scheduled", "ready", "active", "shutting_down"}

// Weekly quota counts completed sessions as consumed quota.
// Expired is excluded because it is used for both no-show expiry and ready-booking reset/cleanup.
var weeklyQuotaBookingStatuses = []string{"scheduled", "ready", "active", "shutting_down", "completed"}

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

func bookingStatusSQLList(statuses []string) string {
	return "'" + strings.Join(statuses, "','") + "'"
}

func activeStatusSQLList() string {
	return bookingStatusSQLList(activeBookingStatuses)
}

func weeklyQuotaStatusSQLList() string {
	return bookingStatusSQLList(weeklyQuotaBookingStatuses)
}

func bookingDurationHours(profile, category string, slotKeys []string, slotStart, slotEnd time.Time) float64 {
	if len(slotKeys) > 0 {
		total := 0.0
		for _, key := range slotKeys {
			tpl, ok := gpuconfig.GetGPUSlotTemplate(profile, category, key)
			if !ok {
				total = 0
				break
			}
			total += tpl.DurationHours
		}
		if total > 0 {
			return total
		}
	}
	d := slotEnd.Sub(slotStart).Hours()
	if d < 0 {
		return -d
	}
	return d
}

func bookingDurationLabel(profile, category string, slotKeys []string, slotStart, slotEnd time.Time) string {
	return fmt.Sprintf("%.0f Hours", bookingDurationHours(profile, category, slotKeys, slotStart, slotEnd))
}

func normalizedSlotKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func maxContinuousSlotSelectionMessage(limit int) string {
	limitText := strconv.Itoa(limit)
	switch limit {
	case 1:
		limitText = "one"
	case 2:
		limitText = "two"
	}

	slotWord := "slots"
	if limit == 1 {
		slotWord = "slot"
	}

	return fmt.Sprintf("You can select a maximum of %s continuous %s. Please reduce your selection to %s %s or fewer.", limitText, slotWord, limitText, slotWord)
}

func orderedTemplatesByStart(slotDate time.Time, templates []gpuconfig.SlotTemplate) ([]gpuconfig.SlotTemplate, error) {
	type item struct {
		slot      gpuconfig.SlotTemplate
		startTime time.Time
	}
	items := make([]item, 0, len(templates))
	for _, t := range templates {
		startAt, err := parseClockIST(slotDate, t.StartTime)
		if err != nil {
			return nil, err
		}
		items = append(items, item{slot: t, startTime: startAt})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].startTime.Before(items[j].startTime)
	})
	ordered := make([]gpuconfig.SlotTemplate, 0, len(items))
	for _, it := range items {
		ordered = append(ordered, it.slot)
	}
	return ordered, nil
}

func orderedContiguousSlotKeys(keys []string, ordered []gpuconfig.SlotTemplate) ([]string, bool) {
	if len(keys) == 0 {
		return nil, false
	}
	indexByKey := make(map[string]int, len(ordered))
	for i, s := range ordered {
		indexByKey[s.Key] = i
	}

	selected := make(map[int]struct{}, len(keys))
	minIndex := len(ordered)
	maxIndex := -1
	for _, key := range keys {
		nextIndex, ok := indexByKey[key]
		if !ok {
			return nil, false
		}
		if _, exists := selected[nextIndex]; exists {
			return nil, false
		}
		selected[nextIndex] = struct{}{}
		if nextIndex < minIndex {
			minIndex = nextIndex
		}
		if nextIndex > maxIndex {
			maxIndex = nextIndex
		}
	}
	if maxIndex-minIndex+1 != len(keys) {
		return nil, false
	}

	out := make([]string, 0, len(keys))
	for i := minIndex; i <= maxIndex; i++ {
		if _, ok := selected[i]; !ok {
			return nil, false
		}
		out = append(out, ordered[i].Key)
	}
	return out, true
}

// createGPUBooking godoc
// @Summary      Create slot booking
// @Description  Creates a CPU or GPU booking from code-defined categories and slot templates selected by SLOT_CONFIG_PROFILE.
// @Description
// @Description  New bookings are inserted as `scheduled`. Lifecycle automation moves `scheduled` bookings to `ready` at slot start, then to `active` once the Kubeflow Notebook reports ready replicas, then to `shutting_down` near slot end, and finally to `completed` at slot end.
// @Description
// @Description  Booking limits are enforced as two separate rules.
// @Description  MaxActiveBookings counts scheduled, ready, active, and shutting_down bookings for the same user and category. When reached, the API returns 400 with "Active booking limit exceeded".
// @Description  MaxBookingsPerWeek counts scheduled, ready, active, shutting_down, and completed bookings for the same user, category, and selected week. When reached, the API returns 400 with "Weekly booking limit exceeded".
// @Description  Cancelled bookings do not count toward either limit. Expired bookings do not count toward weekly quota because expired can mean either no-show expiry after the booking became ready, or reset/cleanup of a stuck ready booking. Scheduled bookings reset before resources are ready become cancelled, not expired.
// @Description  The previous "You already have an upcoming booking" restriction has been removed. Users may create multiple future bookings within MaxActiveBookings, MaxBookingsPerWeek, slot availability, and duplicate booking rules.
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
	req.SlotDate = strings.TrimSpace(req.SlotDate)
	req.SlotKeys = normalizedSlotKeys(req.SlotKeys)
	if err := normalizeAndValidateRuntimeAssets(&req); err != nil {
		sendError(w, logger, http.StatusBadRequest, err.Error())
		return
	}

	profile := app.env.NotebookConfig.SlotConfigProfile
	category, ok := gpuconfig.GetGPUCategory(profile, req.Category)
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "Invalid category")
		return
	}
	if !category.IsBookable {
		sendError(w, logger, http.StatusBadRequest, "Category does not use bookings or slots")
		return
	}
	if len(req.SlotKeys) == 0 {
		sendError(w, logger, http.StatusBadRequest, "slotKeys is required")
		return
	}

	if errorMessage, gotError := getErrorMessageForNotebookName(req.NotebookName); gotError {
		sendError(w, logger, http.StatusUnprocessableEntity, errorMessage)
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
	if len(req.SlotKeys) > category.MaxContiguousSlotSelectionAllowed {
		sendError(w, logger, http.StatusBadRequest, maxContinuousSlotSelectionMessage(category.MaxContiguousSlotSelectionAllowed))
		return
	}
	orderedTemplates, err := orderedTemplatesByStart(slotDate, category.Slots)
	if err != nil {
		sendError(w, logger, http.StatusBadRequest, "Invalid slot configuration for category")
		return
	}
	orderedSlotKeys, ok := orderedContiguousSlotKeys(req.SlotKeys, orderedTemplates)
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "Selected slots must be contiguous")
		return
	}
	req.SlotKeys = orderedSlotKeys
	templateByKey := make(map[string]gpuconfig.SlotTemplate, len(category.Slots))
	for _, s := range category.Slots {
		templateByKey[s.Key] = s
	}
	firstTemplate, ok := templateByKey[req.SlotKeys[0]]
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "Invalid slot key for category")
		return
	}
	lastTemplate, ok := templateByKey[req.SlotKeys[len(req.SlotKeys)-1]]
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "Invalid slot key for category")
		return
	}
	slotStart, err := parseClockIST(slotDate, firstTemplate.StartTime)
	if err != nil {
		sendError(w, logger, http.StatusBadRequest, "Invalid slot start time configuration")
		return
	}
	slotEnd, err := parseClockIST(slotDate, lastTemplate.EndTime)
	if err != nil {
		sendError(w, logger, http.StatusBadRequest, "Invalid slot end time configuration")
		return
	}
	if lastTemplate.SpansMidnight || !slotEnd.After(slotStart) {
		slotEnd = slotEnd.Add(24 * time.Hour)
	}
	nowIST := time.Now().In(istLocation())
	if slotDate.Before(time.Date(nowIST.Year(), nowIST.Month(), nowIST.Day(), 0, 0, 0, 0, istLocation())) {
		sendError(w, logger, http.StatusBadRequest, "Cannot book a slot in the past")
		return
	}
	// Same calendar day (or future): allow only if the slot has not ended yet.
	// - Before slotStart: early booking for a later window today (or any future date).
	// - slotStart <= now < slotEnd: mid-window booking (e.g. 16:32 for 16:00–20:00).
	// - now >= slotEnd: reject (e.g. at 16:32, 08:00–12:00 and 12:00–16:00 are closed).
	allowBeforeSlot := nowIST.Before(slotStart)
	allowMidSlot := !nowIST.Before(slotStart) && nowIST.Before(slotEnd)
	if !allowBeforeSlot && !allowMidSlot {
		sendError(w, logger, http.StatusBadRequest, "Cannot book a slot that has already ended")
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

	if req.GitAccessToken != nil || req.GitTokenSecretName != nil {
		if err := app.waitForNamespace(ctx, logger, namespace); err != nil {
			logger.Error("namespace not ready for git token setup", "error", err, "namespace", namespace)
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.GitAccessToken != nil {
		secretName := gitAccessTokenSecretName(req.NotebookName)
		if err := app.createOrUpdateGitAccessTokenSecret(ctx, namespace, secretName, *req.GitAccessToken); err != nil {
			logger.Error("failed to create git access token secret", "error", err, "namespace", namespace, "secret", secretName)
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
		req.GitTokenSecretName = &secretName
	}
	if req.GitTokenSecretName != nil {
		if err := app.verifyGitTokenSecret(ctx, namespace, *req.GitTokenSecretName); err != nil {
			logger.Warn("failed to verify git token secret", "error", err, "namespace", namespace, "secret", *req.GitTokenSecretName)
			sendError(w, logger, http.StatusBadRequest, err.Error())
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

	slotCountQuery := fmt.Sprintf(`
		SELECT COUNT(1)
		FROM bookings
		WHERE category_name = $1
		  AND slot_date = $2
		  AND status IN (%s)
		  AND (
		    slot_keys && $3::varchar[]
		    OR (slot_keys = '{}'::varchar[] AND slot_key = ANY($3::varchar[]))
		  )
	`, activeStatusSQLList())
	for _, selectedKey := range req.SlotKeys {
		var slotBooked int
		err = tx.QueryRow(ctx, slotCountQuery, req.Category, slotDate.Format("2006-01-02"), []string{selectedKey}).Scan(&slotBooked)
		if err != nil {
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
		if slotBooked >= category.MaxConcurrentUsers {
			sendError(w, logger, http.StatusConflict, fmt.Sprintf("Selected slot %s is full", selectedKey))
			return
		}
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
	`, weeklyQuotaStatusSQLList())
	err = tx.QueryRow(ctx, weekCountQuery, userInfo.Sub, req.Category, slotDate.Format("2006-01-02")).Scan(&weekCount)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	if weekCount >= category.MaxBookingsPerWeek {
		sendError(w, logger, http.StatusBadRequest, "Weekly booking limit exceeded")
		return
	}

	insertQuery := `
		INSERT INTO bookings (
			user_id, category_name, resource_type, slot_key, slot_keys, notebook_name, slot_date, slot_start, slot_end, status,
			file_url, git_url, git_token_secret_name
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, 'scheduled',
			$10, $11, $12
		) RETURNING id
	`
	var bookingID int64
	err = tx.QueryRow(
		ctx,
		insertQuery,
		userInfo.Sub,
		req.Category,
		category.ResourceType,
		req.SlotKeys[0],
		req.SlotKeys,
		req.NotebookName,
		slotDate.Format("2006-01-02"),
		slotStart,
		slotEnd,
		req.FileURL,
		req.GitURL,
		req.GitTokenSecretName,
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
		SlotKeys:     req.SlotKeys,
		SlotStart:    slotStart.Format(time.RFC3339),
		SlotEnd:      slotEnd.Format(time.RFC3339),
		NotebookName: req.NotebookName,
		ResourceType: category.ResourceType,
	})
}

// listGPUBookings godoc
// @Summary      List user bookings
// @Description  Lists CPU and GPU slot bookings for the current user with optional status filter and pagination.
// @Description  The `status` filter accepts comma-separated lifecycle states: `scheduled`, `ready`, `active`, `shutting_down`, `completed`, `cancelled`, `expired`, or `all` to disable filtering.
// @Description  `notebookUrl` is returned only when the booking is `active` and the linked notebook resource has been applied.
// @Tags         bookings
// @Produce      json
// @Param        status  query   string  false  "Comma-separated status filter, or 'all' to omit filtering"
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
	whereClause := "WHERE b.user_id = $1"
	query := `
		SELECT b.id, b.notebook_name, b.category_name, b.slot_key, b.slot_keys, b.status,
		       b.slot_date, b.slot_start, b.slot_end, b.created_at,
		       n.namespace, n.name, n.image_name, n.events[array_upper(n.events, 1)]::text AS notebook_latest_event
		FROM bookings b
		LEFT JOIN LATERAL (
			SELECT namespace, name, image_name, events
			FROM notebooks
			WHERE events[array_upper(events, 1)] <> 'deleted'
			  AND (
			    id = b.notebook_id
			    OR booking_id = b.id
			  )
			ORDER BY id DESC
			LIMIT 1
		) n ON true
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
		// Frontends often send status=all to mean "no filter". The DB has no status
		// value "all"; applying it would match zero rows.
		if len(cleaned) == 1 && cleaned[0] == "all" {
			cleaned = nil
		}
		if len(cleaned) > 0 {
			// Cast to varchar[] to avoid any inference issues (status is VARCHAR).
			whereClause += fmt.Sprintf(" AND b.status = ANY($%d::varchar[])", len(args)+1)
			args = append(args, cleaned)
		}
	}

	var totalCount int
	countQuery := "SELECT COUNT(*) FROM bookings b " + whereClause
	if err := app.pgPool.Pool.QueryRow(r.Context(), countQuery, args...).Scan(&totalCount); err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to list bookings")
		return
	}

	listArgs := append(append([]any{}, args...), limit, offset)
	query += " " + whereClause
	// Newest bookings first so recent active/completed/cancelled items are visible
	// on the first page when clients request status=all with a small limit.
	query += fmt.Sprintf(" ORDER BY b.created_at DESC, b.id DESC LIMIT $%d OFFSET $%d", len(listArgs)-1, len(listArgs))

	rows, err := app.pgPool.Pool.Query(r.Context(), query, listArgs...)
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
			slotKeys     []string
			status       string
			slotDate     time.Time
			slotStart    time.Time
			slotEnd      time.Time
			createdAt    time.Time
			nbNamespace  *string
			nbName       *string
			nbImageName  *string
			nbLatest     *string
		)
		if err := rows.Scan(
			&id, &notebookName, &categoryName, &slotKey, &slotKeys, &status,
			&slotDate, &slotStart, &slotEnd, &createdAt,
			&nbNamespace, &nbName, &nbImageName, &nbLatest,
		); err != nil {
			sendError(w, logger, http.StatusInternalServerError, "Failed to list bookings")
			return
		}
		if len(slotKeys) == 0 {
			slotKeys = []string{slotKey}
		}

		displayName := categoryName
		gpuMemory := ""
		resourceType := ""
		duration := bookingDurationLabel(profile, categoryName, slotKeys, slotStart, slotEnd)
		if c, ok := gpuconfig.GetGPUCategory(profile, categoryName); ok {
			displayName = c.DisplayName
			gpuMemory = c.GPUMemory
			resourceType = c.ResourceType
		}

		notebookURL := ""
		if status == "active" &&
			nbNamespace != nil &&
			nbName != nil &&
			nbLatest != nil &&
			*nbLatest == string(constants.StatusNotebookApplied) {
			notebookURL = generateNotebookURL(app.env.NotebookConfig.KubeFlowURL, *nbNamespace, *nbName, nbImageName, app.env.NotebookConfig.DisableInit)
		}

		bookings = append(bookings, BookingListItem{
			ID:           id,
			NotebookName: notebookName,
			NotebookURL:  notebookURL,
			Category:     categoryName,
			ResourceType: resourceType,
			DisplayName:  displayName,
			GPUMemory:    gpuMemory,
			Status:       status,
			SlotDate:     slotDate.Format("2006-01-02"),
			SlotKeys:     slotKeys,
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

	page := (offset / limit) + 1
	totalPages := 0
	if totalCount > 0 {
		totalPages = (totalCount + limit - 1) / limit
	}

	sendResponseJson(w, logger, http.StatusOK, BookingsListResponse{
		Type:  "dx:controlPlane:success",
		Title: "Success",
		PaginationInfo: PaginationInfo{
			Page:        page,
			Size:        limit,
			TotalCount:  totalCount,
			TotalPages:  totalPages,
			HasNext:     offset+limit < totalCount,
			HasPrevious: offset > 0 && totalCount > 0,
		},
		Result: bookings,
	})
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
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}

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
	if !category.IsBookable {
		sendError(w, logger, http.StatusBadRequest, "Category does not use bookings or slots")
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
			SELECT sk.slot_key, COUNT(1), BOOL_OR(b.user_id = $3)
			FROM bookings b,
			     LATERAL unnest(
			       CASE
			         WHEN array_length(b.slot_keys, 1) IS NULL OR array_length(b.slot_keys, 1) = 0
			           THEN ARRAY[b.slot_key]
			         ELSE b.slot_keys
			       END
			     ) AS sk(slot_key)
			WHERE category_name = $1
			  AND slot_date = $2
			  AND status IN (%s)
			GROUP BY sk.slot_key
		`, activeStatusSQLList()),
		categoryName,
		slotDate.Format("2006-01-02"),
		userInfo.Sub,
	)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to calculate slot availability")
		return
	}
	defer rows.Close()

	bookedBySlot := map[string]int{}
	alreadyBookedBySlot := map[string]bool{}
	for rows.Next() {
		var key string
		var count int
		var alreadyBooked bool
		if err := rows.Scan(&key, &count, &alreadyBooked); err != nil {
			sendError(w, logger, http.StatusInternalServerError, "Failed to calculate slot availability")
			return
		}
		bookedBySlot[key] = count
		alreadyBookedBySlot[key] = alreadyBooked
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
			AlreadyBooked:  alreadyBookedBySlot[s.Key],
		})
	}
	sort.SliceStable(slots, func(i, j int) bool {
		return slots[i].StartTime < slots[j].StartTime
	})

	sendResponseJson(w, logger, http.StatusOK, AvailableSlotsResponse{
		Date:         slotDate.Format("2006-01-02"),
		Category:     categoryName,
		ResourceType: category.ResourceType,
		Slots:        slots,
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
	if !category.IsBookable {
		sendError(w, logger, http.StatusBadRequest, "Category does not use bookings or slots")
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
			SELECT b.slot_date, COUNT(1)
			FROM bookings b,
			     LATERAL unnest(
			       CASE
			         WHEN array_length(b.slot_keys, 1) IS NULL OR array_length(b.slot_keys, 1) = 0
			           THEN ARRAY[b.slot_key]
			         ELSE b.slot_keys
			       END
			     ) AS sk(slot_key)
			WHERE category_name = $1
			  AND slot_date >= $2
			  AND slot_date < $3
			  AND status IN (%s)
			GROUP BY b.slot_date
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
	nowIST := time.Now().In(istLocation())
	todayStart := time.Date(nowIST.Year(), nowIST.Month(), nowIST.Day(), 0, 0, 0, 0, istLocation())
	maxBookingDate := nowIST.AddDate(0, 0, category.AdvanceBookingDays)
	maxBookingDayStart := time.Date(maxBookingDate.Year(), maxBookingDate.Month(), maxBookingDate.Day(), 0, 0, 0, 0, istLocation())
	minBookingDate := nowIST.AddDate(0, 0, category.MinAdvanceBookingDays)
	minBookingDayStart := time.Date(minBookingDate.Year(), minBookingDate.Month(), minBookingDate.Day(), 0, 0, 0, 0, istLocation())
	for d := monthStart; d.Before(monthEnd); d = d.AddDate(0, 0, 1) {
		ds := d.Format("2006-01-02")
		// Keep calendar response aligned with booking creation constraints.
		// A day outside the allowed booking window should not be shown as available.
		if d.Before(todayStart) || d.Before(minBookingDayStart) || d.After(maxBookingDayStart) {
			days = append(days, CalendarDay{
				Date:            ds,
				HasAvailability: false,
				SlotsAvailable:  0,
				SlotsTotal:      slotsPerDay,
			})
			continue
		}
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
		Month:        monthStart.Format("2006-01"),
		Category:     categoryName,
		ResourceType: category.ResourceType,
		Days:         days,
	})
}

// cancelGPUBooking godoc
// @Summary      Cancel scheduled booking
// @Description  Cancels a `scheduled` booking for the current user before resources are ready.
// @Description  Cancel changes `scheduled` to `cancelled`. It does not operate on `ready`, `active`, or `shutting_down`; use terminate for those states.
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

// extendGPUBooking godoc
// @Summary      Extend active booking by one slot
// @Description  Extends an `active` booking to the next contiguous slot if that slot has capacity, the category has not reached its contiguous-slot limit, and the booking has not already been extended.
// @Description  The booking remains `active`; `slot_keys`, `slot_end`, `shutdown_warning_sent_at`, and `extension_used` are updated so lifecycle timing follows the new end time.
// @Tags         bookings
// @Produce      json
// @Param        id  path  int  true  "Booking ID"
// @Success      200  {object}  ExtendBookingResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      409  {object}  Error409
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/bookings/{id}/extend [patch]
func (app *application) extendGPUBooking(w http.ResponseWriter, r *http.Request) {
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
		sendError(w, logger, http.StatusInternalServerError, "Failed to extend booking")
		return
	}
	defer tx.Rollback(ctx)

	var (
		status        string
		category      string
		slotDate      time.Time
		slotStart     time.Time
		slotEnd       time.Time
		slotKey       string
		slotKeys      []string
		extensionUsed bool
	)
	err = tx.QueryRow(ctx, `
		SELECT status, category_name, slot_date, slot_start, slot_end, slot_key, slot_keys, extension_used
		FROM bookings
		WHERE id = $1 AND user_id = $2
		FOR UPDATE
	`, bookingID, userInfo.Sub).Scan(&status, &category, &slotDate, &slotStart, &slotEnd, &slotKey, &slotKeys, &extensionUsed)
	if err != nil {
		if err == pgx.ErrNoRows {
			sendError(w, logger, http.StatusNotFound, "Booking not found")
			return
		}
		sendError(w, logger, http.StatusInternalServerError, "Failed to extend booking")
		return
	}
	if status != "active" {
		sendError(w, logger, http.StatusBadRequest, "Only active bookings can be extended")
		return
	}
	if extensionUsed {
		sendError(w, logger, http.StatusBadRequest, "You can only extend at max once")
		return
	}
	if len(slotKeys) == 0 {
		slotKeys = []string{slotKey}
	}

	profile := app.env.NotebookConfig.SlotConfigProfile
	cfgCategory, ok := gpuconfig.GetGPUCategory(profile, category)
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "Invalid category")
		return
	}
	if len(slotKeys) >= cfgCategory.MaxContiguousSlotSelectionAllowed {
		sendError(w, logger, http.StatusBadRequest, "Maximum contiguous slot selection already reached")
		return
	}
	orderedTemplates, err := orderedTemplatesByStart(slotDate.In(istLocation()), cfgCategory.Slots)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Invalid slot configuration")
		return
	}
	indexByKey := make(map[string]int, len(orderedTemplates))
	for i, s := range orderedTemplates {
		indexByKey[s.Key] = i
	}
	lastKey := slotKeys[len(slotKeys)-1]
	lastIndex, ok := indexByKey[lastKey]
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "Invalid slot sequence for booking")
		return
	}
	if lastIndex+1 >= len(orderedTemplates) {
		sendError(w, logger, http.StatusBadRequest, "No next contiguous slot available")
		return
	}
	nextTemplate := orderedTemplates[lastIndex+1]
	nextKey := nextTemplate.Key

	var slotBooked int
	slotCountQuery := fmt.Sprintf(`
		SELECT COUNT(1)
		FROM bookings
		WHERE category_name = $1
		  AND slot_date = $2
		  AND status IN (%s)
		  AND id <> $3
		  AND (
		    slot_keys && $4::varchar[]
		    OR (slot_keys = '{}'::varchar[] AND slot_key = ANY($4::varchar[]))
		  )
	`, activeStatusSQLList())
	err = tx.QueryRow(ctx, slotCountQuery, category, slotDate.Format("2006-01-02"), bookingID, []string{nextKey}).Scan(&slotBooked)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to extend booking")
		return
	}
	if slotBooked >= cfgCategory.MaxConcurrentUsers {
		sendError(w, logger, http.StatusConflict, "Next slot is full")
		return
	}

	nextSlotStart, err := parseClockIST(slotDate.In(istLocation()), nextTemplate.StartTime)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Invalid slot configuration")
		return
	}
	newSlotEnd, err := parseClockIST(slotDate.In(istLocation()), nextTemplate.EndTime)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Invalid slot configuration")
		return
	}
	// Match create-booking semantics for midnight-spanning slots.
	if nextTemplate.SpansMidnight || !newSlotEnd.After(nextSlotStart) {
		newSlotEnd = newSlotEnd.Add(24 * time.Hour)
	}
	updatedSlotKeys := append(append([]string{}, slotKeys...), nextKey)

	_, err = tx.Exec(ctx, `
		UPDATE bookings
		SET slot_keys = $1,
		    slot_end = $2,
		    shutdown_warning_sent_at = NULL,
		    extension_used = true
		WHERE id = $3
		  AND user_id = $4
		  AND status = 'active'
	`, updatedSlotKeys, newSlotEnd, bookingID, userInfo.Sub)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to extend booking")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to extend booking")
		return
	}

	sendResponseJson(w, logger, http.StatusOK, ExtendBookingResponse{
		BookingID: bookingID,
		Status:    "active",
		SlotDate:  slotDate.Format("2006-01-02"),
		SlotKeys:  updatedSlotKeys,
		SlotStart: slotStart.Format(time.RFC3339),
		SlotEnd:   newSlotEnd.Format(time.RFC3339),
		Duration:  bookingDurationLabel(profile, category, updatedSlotKeys, slotStart, newSlotEnd),
	})
}

// resetGPUBooking godoc
// @Summary      Reset stuck booking
// @Description  Resets only `scheduled` or `ready` bookings that are stuck or need cleanup.
// @Description  `scheduled` resets become `cancelled` and unlink any notebook id. `ready` resets become `expired`, set session and cleanup timestamps, unlink the notebook, and best-effort delete the linked Notebook/PVC.
// @Description  Reset does not apply to `active`, `shutting_down`, `completed`, `cancelled`, or already `expired` bookings.
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
			  AND n.events[array_upper(n.events, 1)] <> 'deleted'
		`, *notebookID).Scan(&nbID, &nbName, &nbNS, &nbPVC); err == nil {
			nbFound = true
		}
	}
	if !nbFound {
		if err := tx.QueryRow(ctx, `
			SELECT n.id, n.name, n.namespace, n.pvc_name
			FROM notebooks n
			WHERE n.booking_id = $1
			  AND n.events[array_upper(n.events, 1)] <> 'deleted'
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
		_ = app.deletePlatformTokenSecret(context.Background(), nbNS, nbName)
		_ = app.deleteNotebookFromK8s(context.Background(), logger, nbNS, nbName)
		_ = app.deletePVCFromK8s(context.Background(), logger, nbNS, nbPVC)

		// Soft-delete linked notebook metadata.
		_, _ = app.pgPool.Pool.Exec(context.Background(), `
			UPDATE notebooks
			SET events = array_append(events, 'deleted'),
			    name = name || '__deleted__' || id::text || '__' || extract(epoch from now())::bigint::text,
			    pvc_name = pvc_name || '__deleted__' || id::text || '__' || extract(epoch from now())::bigint::text,
			    booking_id = NULL
			WHERE id = $1
			  AND events[array_upper(events, 1)] <> 'deleted'
		`, nbID)
	}

	sendResponse(w, logger, http.StatusOK, fmt.Sprintf("Booking reset successfully (%s -> %s)", dbStatus, targetStatus))
}

// terminateGPUBooking godoc
// @Summary      End booking session early
// @Description  Ends a `ready`, `active`, or `shutting_down` booking early and marks it `completed`.
// @Description  Terminate sets session and cleanup timestamps, unlinks the notebook, and best-effort deletes the linked Notebook/PVC. If the booking is already `completed`, the endpoint returns success without changing it.
// @Description  Use cancel or reset for `scheduled` bookings; terminate does not apply to `cancelled` or `expired` bookings.
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
			  AND n.events[array_upper(n.events, 1)] <> 'deleted'
		`, *notebookID).Scan(&nbID, &nbName, &nbNS, &nbPVC); err == nil {
			nbFound = true
		}
	}
	if !nbFound {
		if err := tx.QueryRow(ctx, `
			SELECT n.id, n.name, n.namespace, n.pvc_name
			FROM notebooks n
			WHERE n.booking_id = $1
			  AND n.events[array_upper(n.events, 1)] <> 'deleted'
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
		_ = app.deletePlatformTokenSecret(context.Background(), nbNS, nbName)
		_ = app.deleteNotebookFromK8s(context.Background(), logger, nbNS, nbName)
		_ = app.deletePVCFromK8s(context.Background(), logger, nbNS, nbPVC)
		_, _ = app.pgPool.Pool.Exec(context.Background(), `
			UPDATE notebooks
			SET events = array_append(events, 'deleted'),
			    name = name || '__deleted__' || id::text || '__' || extract(epoch from now())::bigint::text,
			    pvc_name = pvc_name || '__deleted__' || id::text || '__' || extract(epoch from now())::bigint::text,
			    booking_id = NULL
			WHERE id = $1
			  AND events[array_upper(events, 1)] <> 'deleted'
		`, nbID)
	}

	sendResponse(w, logger, http.StatusOK, "Booking terminated successfully")
}
