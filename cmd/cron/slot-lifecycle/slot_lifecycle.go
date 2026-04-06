package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/gpuconfig"
	"sandbox-backend-service/pkg/k8s"

	"github.com/jackc/pgx/v5"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	notebookGVR = schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}
	pvcGVR = schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "persistentvolumeclaims",
	}
)

type lifecycleRunSummary struct {
	scheduledSelected      int
	scheduledProcessed     int
	scheduledReady         int
	scheduledErrors        int
	readySelected          int
	readyBecameActive      int
	readyExpiredNoShow     int
	readyWaitingGrace      int
	readyUnknownCategory   int
	readyParseErrors       int
	readyK8sCheckErrors    int
	readyK8sDeleteErrors   int
	activeSelected         int
	activeToShuttingDown   int
	activeWaitingWindow    int
	activeUnknownCategory  int
	activeSlotEndLookupErr int
	activeParseErrors      int
	endSelected            int
	endCompleted           int
	endWaitingSlotEnd      int
	endParseErrors         int
	endStopErrors          int
	cleanupSelected        int
	cleanupCompleted       int
	cleanupWaitingGrace    int
	cleanupUnknownCategory int
	cleanupParseErrors     int
	cleanupDeleteErrors    int
}

func runSlotLifecycle(pool *db.PgPool, k8sClient *k8s.K8sClient, cfg CronEnv) error {
	logger := slog.Default().With("service", "slot-lifecycle")
	summary := &lifecycleRunSummary{}

	// v1 transitions in order; each step is best-effort and idempotent.
	if err := stepScheduledToReady(pool, logger, cfg, summary); err != nil {
		return err
	}
	if err := stepReadyToActiveOrNoShow(pool, k8sClient, logger, cfg, summary); err != nil {
		return err
	}
	if err := stepActiveToShuttingDown(pool, logger, cfg, summary); err != nil {
		return err
	}
	if err := stepSlotEndStopAndComplete(pool, k8sClient, logger, cfg, summary); err != nil {
		return err
	}
	if err := stepCleanupCompleted(pool, k8sClient, logger, cfg, summary); err != nil {
		return err
	}

	logger.Info(
		"slot lifecycle run complete",
		"scheduled_selected", summary.scheduledSelected,
		"scheduled_processed", summary.scheduledProcessed,
		"scheduled_to_ready", summary.scheduledReady,
		"scheduled_errors", summary.scheduledErrors,
		"ready_selected", summary.readySelected,
		"ready_to_active", summary.readyBecameActive,
		"ready_to_expired_no_show", summary.readyExpiredNoShow,
		"ready_waiting_grace", summary.readyWaitingGrace,
		"ready_unknown_category", summary.readyUnknownCategory,
		"ready_parse_errors", summary.readyParseErrors,
		"ready_k8s_check_errors", summary.readyK8sCheckErrors,
		"ready_k8s_delete_errors", summary.readyK8sDeleteErrors,
		"active_selected", summary.activeSelected,
		"active_to_shutting_down", summary.activeToShuttingDown,
		"active_waiting_window", summary.activeWaitingWindow,
		"active_unknown_category", summary.activeUnknownCategory,
		"active_slot_end_lookup_errors", summary.activeSlotEndLookupErr,
		"active_parse_errors", summary.activeParseErrors,
		"slot_end_selected", summary.endSelected,
		"slot_end_to_completed", summary.endCompleted,
		"slot_end_waiting", summary.endWaitingSlotEnd,
		"slot_end_parse_errors", summary.endParseErrors,
		"slot_end_stop_errors", summary.endStopErrors,
		"cleanup_selected", summary.cleanupSelected,
		"cleanup_completed", summary.cleanupCompleted,
		"cleanup_waiting_grace", summary.cleanupWaitingGrace,
		"cleanup_unknown_category", summary.cleanupUnknownCategory,
		"cleanup_parse_errors", summary.cleanupParseErrors,
		"cleanup_delete_errors", summary.cleanupDeleteErrors,
	)
	return nil
}

func istLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.FixedZone("IST", 5*3600+1800)
	}
	return loc
}

func parseISTWallClock(layout, value string) (time.Time, error) {
	return time.ParseInLocation(layout, value, istLocation())
}

func nowIST() time.Time {
	return time.Now().In(istLocation())
}

func stepScheduledToReady(pool *db.PgPool, logger *slog.Logger, cfg CronEnv, summary *lifecycleRunSummary) error {
	ctx := context.Background()
	nowQuery := `NOW() AT TIME ZONE 'Asia/Kolkata'`
	selected := 0
	processed := 0
	readyTransitions := 0
	failures := 0

	// Select only (not updating yet) to keep transaction scope per booking.
	query := fmt.Sprintf(`
		SELECT id, user_id, category_name, slot_key, notebook_name, slot_date, slot_start, slot_end
		FROM bookings
		WHERE status = 'scheduled'
		  AND slot_start <= %s
		ORDER BY slot_start ASC
		LIMIT %d
	`, nowQuery, cfg.BatchSize)

	rows, err := pool.Pool.Query(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()

	var bookings []GPUBookingRow
	for rows.Next() {
		var b GPUBookingRow
		if err := rows.Scan(
			&b.ID,
			&b.UserID,
			&b.Category,
			&b.SlotKey,
			&b.Notebook,
			&b.SlotDate,
			&b.SlotStart,
			&b.SlotEnd,
		); err != nil {
			return err
		}
		selected++
		bookings = append(bookings, b)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, b := range bookings {
		processed++
		if err := processBookingToReady(pool, logger, cfg, b); err != nil {
			failures++
			logger.Error("failed processing booking scheduled->ready", "booking_id", b.ID, "error", err)
			continue
		}
		readyTransitions++
	}
	logger.Info(
		"slot lifecycle step complete",
		"step", "scheduled_to_ready",
		"selected", selected,
		"processed", processed,
		"transitioned_to_ready", readyTransitions,
		"errors", failures,
	)
	summary.scheduledSelected += selected
	summary.scheduledProcessed += processed
	summary.scheduledReady += readyTransitions
	summary.scheduledErrors += failures

	return nil
}

func processBookingToReady(pool *db.PgPool, logger *slog.Logger, cfg CronEnv, b GPUBookingRow) error {
	ctx := context.Background()

	category, ok := gpuconfig.GetGPUCategory(cfg.SlotConfigProfile, b.Category)
	if !ok {
		_, _ = pool.Pool.Exec(ctx, `UPDATE bookings SET status='cancelled' WHERE id=$1`, b.ID)
		return fmt.Errorf("unknown gpu category: %s", b.Category)
	}

	tx, err := pool.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pvcName := b.Notebook + "-pvc"

	insertQuery := `
		INSERT INTO notebooks (
			user_id, name, namespace, pvc_name, storage_size,
			cpu_request, cpu_limit, memory_request, memory_limit,
			gpu_type, gpu_request, gpu_limit, instance_type, template_name,
			booking_id
		)
		VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13, $14,
			$15
		)
		ON CONFLICT (name, namespace) DO NOTHING
		RETURNING id
	`

	var notebookID int64
	err = tx.QueryRow(
		ctx,
		insertQuery,
		b.UserID,
		b.Notebook,
		b.UserID, // namespace == user_id
		pvcName,
		category.StorageSize,
		category.CPURequest,
		category.CPULimit,
		category.MemoryRequest,
		category.MemoryLimit,
		category.GPUType,
		category.GPURequest,
		category.GPULimit,
		category.InstanceType,
		nil,
		b.ID,
	).Scan(&notebookID)

	if err != nil {
		// ON CONFLICT DO NOTHING returns no row; fetch existing notebook and link.
		if err == pgx.ErrNoRows {
			logger.Warn("notebook already exists for booking, linking instead", "booking_id", b.ID)
			if err2 := tx.QueryRow(
				ctx,
				`SELECT id FROM notebooks WHERE namespace=$1 AND name=$2`,
				b.UserID, b.Notebook,
			).Scan(&notebookID); err2 != nil {
				return err2
			}
			if _, err2 := tx.Exec(
				ctx,
				`UPDATE notebooks
				 SET booking_id=$1
				 WHERE id=$2 AND booking_id IS NULL`,
				b.ID, notebookID,
			); err2 != nil {
				return err2
			}
		} else {
			return err
		}
	}

	// Idempotent update: only scheduled -> ready.
	if _, err := tx.Exec(ctx,
		`UPDATE bookings
		 SET notebook_id=$1, status='ready'
		 WHERE id=$2 AND status='scheduled'`,
		notebookID, b.ID,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func stepReadyToActiveOrNoShow(pool *db.PgPool, k8sClient *k8s.K8sClient, logger *slog.Logger, cfg CronEnv, summary *lifecycleRunSummary) error {
	ctx := context.Background()
	batch := cfg.BatchSize
	selected := 0
	becameActive := 0
	expiredNoShow := 0
	waitingNoShowGrace := 0
	unknownCategory := 0
	parseErrors := 0
	k8sCheckErrors := 0
	k8sDeleteErrors := 0

	// Include pvc_name so we can delete quickly on no-show.
	query := `
		SELECT gb.id, gb.notebook_id, gb.user_id, gb.category_name,
		       n.name, n.namespace, n.pvc_name,
		       to_char(gb.slot_start,'YYYY-MM-DD HH24:MI:SS') as slot_start_str
		FROM bookings gb
		JOIN notebooks n ON n.id = gb.notebook_id
		WHERE gb.status='ready'
		  AND gb.session_started_at IS NULL
		LIMIT $1
	`

	rows, err := pool.Pool.Query(ctx, query, batch)
	if err != nil {
		return err
	}
	defer rows.Close()

	now := nowIST()

	for rows.Next() {
		selected++
		var (
			bookingID   int64
			notebookID  int64
			userID      string
			category    string
			notebookName string
			namespace   string
			pvcName     string
			slotStartStr string
		)
		if err := rows.Scan(
			&bookingID, &notebookID, &userID, &category,
			&notebookName, &namespace, &pvcName,
			&slotStartStr,
		); err != nil {
			return err
		}

		cat, ok := gpuconfig.GetGPUCategory(cfg.SlotConfigProfile, category)
		if !ok {
			unknownCategory++
			logger.Warn("unknown gpu category in booking", "booking_id", bookingID, "category", category)
			continue
		}

		slotStart, err := parseISTWallClock("2006-01-02 15:04:05", slotStartStr)
		if err != nil {
			parseErrors++
			logger.Warn("failed parsing slot_start", "booking_id", bookingID, "error", err)
			continue
		}

		running, err := isNotebookRunning(k8sClient, namespace, notebookName)
		if err != nil {
			k8sCheckErrors++
			logger.Error("failed checking notebook running", "booking_id", bookingID, "error", err)
			continue
		}

		if running {
			_, _ = pool.Pool.Exec(ctx,
				`UPDATE bookings
				 SET status='active', session_started_at=NOW()
				 WHERE id=$1 AND status='ready' AND session_started_at IS NULL`,
				bookingID,
			)
			becameActive++
			continue
		}

		// No-show if slot_start + grace <= now.
		noShowAt := slotStart.Add(time.Duration(cat.NoShowGraceMins) * time.Minute)
		if now.Before(noShowAt) {
			waitingNoShowGrace++
			continue
		}

		// Delete notebook + PVC (best-effort), then mark booking expired.
		if err := deleteNotebookAndPVC(k8sClient, namespace, notebookName, pvcName); err != nil {
			k8sDeleteErrors++
			logger.Error("failed deleting resources for no-show", "booking_id", bookingID, "error", err)
		}
		_, _ = pool.Pool.Exec(ctx,
			`UPDATE bookings
			 SET status='expired',
			     session_ended_at=NOW(),
			     cleanup_completed_at=NOW(),
			     notebook_id=NULL
			 WHERE id=$1 AND status='ready'`,
			bookingID,
		)
		_, _ = pool.Pool.Exec(ctx, `DELETE FROM notebooks WHERE id=$1`, notebookID)
		expiredNoShow++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	logger.Info(
		"slot lifecycle step complete",
		"step", "ready_to_active_or_noshow",
		"selected", selected,
		"became_active", becameActive,
		"expired_no_show", expiredNoShow,
		"waiting_grace", waitingNoShowGrace,
		"unknown_category", unknownCategory,
		"parse_errors", parseErrors,
		"k8s_check_errors", k8sCheckErrors,
		"k8s_delete_errors", k8sDeleteErrors,
	)
	summary.readySelected += selected
	summary.readyBecameActive += becameActive
	summary.readyExpiredNoShow += expiredNoShow
	summary.readyWaitingGrace += waitingNoShowGrace
	summary.readyUnknownCategory += unknownCategory
	summary.readyParseErrors += parseErrors
	summary.readyK8sCheckErrors += k8sCheckErrors
	summary.readyK8sDeleteErrors += k8sDeleteErrors
	return nil
}

func stepActiveToShuttingDown(pool *db.PgPool, logger *slog.Logger, cfg CronEnv, summary *lifecycleRunSummary) error {
	ctx := context.Background()
	selected := 0
	shuttingDownTransitions := 0
	waitingWarningWindow := 0
	unknownCategory := 0
	parseErrors := 0
	slotEndLookupErrors := 0

	// Include shutdown warning mins indirectly via category config in Go.
	query := `
		SELECT gb.id, gb.category_name
		FROM bookings gb
		WHERE gb.status='active'
		  AND gb.shutdown_warning_sent_at IS NULL
		LIMIT $1
	`

	rows, err := pool.Pool.Query(ctx, query, cfg.BatchSize)
	if err != nil {
		return err
	}
	defer rows.Close()

	now := nowIST()
	for rows.Next() {
		selected++
		var (
			bookingID   int64
			category    string
		)
		if err := rows.Scan(&bookingID, &category); err != nil {
			return err
		}
		cat, ok := gpuconfig.GetGPUCategory(cfg.SlotConfigProfile, category)
		if !ok {
			unknownCategory++
			continue
		}

		// Fetch slot_end (per category) for time-based decision.
		var slotEndStr string
		if err := pool.Pool.QueryRow(ctx,
			`SELECT to_char(slot_end,'YYYY-MM-DD HH24:MI:SS')
			 FROM bookings WHERE id=$1`,
			bookingID,
		).Scan(&slotEndStr); err != nil {
			slotEndLookupErrors++
			continue
		}
		slotEnd, err := parseISTWallClock("2006-01-02 15:04:05", slotEndStr)
		if err != nil {
			parseErrors++
			continue
		}

		warnAt := slotEnd.Add(-time.Duration(cat.PreShutdownWarningMins) * time.Minute)
		if now.Before(warnAt) {
			waitingWarningWindow++
			continue
		}

		_, _ = pool.Pool.Exec(ctx,
			`UPDATE bookings
			 SET status='shutting_down', shutdown_warning_sent_at=NOW()
			 WHERE id=$1 AND status='active' AND shutdown_warning_sent_at IS NULL`,
			bookingID,
		)
		shuttingDownTransitions++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	logger.Info(
		"slot lifecycle step complete",
		"step", "active_to_shutting_down",
		"selected", selected,
		"transitioned_to_shutting_down", shuttingDownTransitions,
		"waiting_warning_window", waitingWarningWindow,
		"unknown_category", unknownCategory,
		"slot_end_lookup_errors", slotEndLookupErrors,
		"parse_errors", parseErrors,
	)
	summary.activeSelected += selected
	summary.activeToShuttingDown += shuttingDownTransitions
	summary.activeWaitingWindow += waitingWarningWindow
	summary.activeUnknownCategory += unknownCategory
	summary.activeSlotEndLookupErr += slotEndLookupErrors
	summary.activeParseErrors += parseErrors
	return nil
}

func stepSlotEndStopAndComplete(pool *db.PgPool, k8sClient *k8s.K8sClient, logger *slog.Logger, cfg CronEnv, summary *lifecycleRunSummary) error {
	ctx := context.Background()
	selected := 0
	completedTransitions := 0
	waitingSlotEnd := 0
	parseErrors := 0
	stopErrors := 0

	query := `
		SELECT gb.id, gb.category_name,
		       n.name, n.namespace, n.pvc_name,
		       to_char(gb.slot_end,'YYYY-MM-DD HH24:MI:SS') as slot_end_str
		FROM bookings gb
		JOIN notebooks n ON n.id = gb.notebook_id
		WHERE gb.status IN ('active','shutting_down')
		  AND gb.session_ended_at IS NULL
		LIMIT $1
	`

	rows, err := pool.Pool.Query(ctx, query, cfg.BatchSize)
	if err != nil {
		return err
	}
	defer rows.Close()

	now := nowIST()

	for rows.Next() {
		selected++
		var (
			bookingID    int64
			category     string
			notebookName string
			namespace    string
			pvcName      string
			slotEndStr   string
		)
		if err := rows.Scan(&bookingID, &category, &notebookName, &namespace, &pvcName, &slotEndStr); err != nil {
			return err
		}

		slotEnd, err := parseISTWallClock("2006-01-02 15:04:05", slotEndStr)
		if err != nil {
			parseErrors++
			continue
		}
		if now.Before(slotEnd) {
			waitingSlotEnd++
			continue
		}

		// Stop notebook; mark complete regardless of stop call success.
		if err := addStoppedAnnotation(k8sClient, namespace, notebookName); err != nil {
			stopErrors++
			logger.Error("failed adding stopped annotation", "booking_id", bookingID, "error", err)
		}

		_, _ = pool.Pool.Exec(ctx,
			`UPDATE bookings
			 SET status='completed',
			     session_ended_at=NOW()
			 WHERE id=$1 AND status IN ('active','shutting_down') AND session_ended_at IS NULL`,
			bookingID,
		)
		completedTransitions++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	logger.Info(
		"slot lifecycle step complete",
		"step", "slot_end_stop_and_complete",
		"selected", selected,
		"transitioned_to_completed", completedTransitions,
		"waiting_slot_end", waitingSlotEnd,
		"parse_errors", parseErrors,
		"stop_errors", stopErrors,
	)
	summary.endSelected += selected
	summary.endCompleted += completedTransitions
	summary.endWaitingSlotEnd += waitingSlotEnd
	summary.endParseErrors += parseErrors
	summary.endStopErrors += stopErrors
	return nil
}

func stepCleanupCompleted(pool *db.PgPool, k8sClient *k8s.K8sClient, logger *slog.Logger, cfg CronEnv, summary *lifecycleRunSummary) error {
	ctx := context.Background()
	selected := 0
	cleanupCompleted := 0
	waitingGrace := 0
	unknownCategory := 0
	parseErrors := 0
	deleteErrors := 0

	query := `
		SELECT gb.id, gb.notebook_id, gb.category_name,
		       n.name, n.namespace, n.pvc_name,
		       to_char(gb.slot_end,'YYYY-MM-DD HH24:MI:SS') as slot_end_str
		FROM bookings gb
		JOIN notebooks n ON n.id = gb.notebook_id
		WHERE gb.status='completed'
		  AND gb.cleanup_completed_at IS NULL
		LIMIT $1
	`

	rows, err := pool.Pool.Query(ctx, query, cfg.BatchSize)
	if err != nil {
		return err
	}
	defer rows.Close()

	now := nowIST()
	for rows.Next() {
		selected++
		var (
			bookingID    int64
			notebookID   int64
			category     string
			notebookName string
			namespace    string
			pvcName      string
			slotEndStr   string
		)
		if err := rows.Scan(&bookingID, &notebookID, &category, &notebookName, &namespace, &pvcName, &slotEndStr); err != nil {
			return err
		}

		cat, ok := gpuconfig.GetGPUCategory(cfg.SlotConfigProfile, category)
		if !ok {
			unknownCategory++
			continue
		}

		slotEnd, err := parseISTWallClock("2006-01-02 15:04:05", slotEndStr)
		if err != nil {
			parseErrors++
			continue
		}
		cleanupAt := slotEnd.Add(time.Duration(cat.ShutdownGracePeriodMins) * time.Minute)
		if now.Before(cleanupAt) {
			waitingGrace++
			continue
		}

		if err := deleteNotebookAndPVC(k8sClient, namespace, notebookName, pvcName); err != nil {
			deleteErrors++
			logger.Error("failed deleting resources for cleanup", "booking_id", bookingID, "error", err)
		}
		_, _ = pool.Pool.Exec(ctx,
			`UPDATE bookings
			 SET cleanup_completed_at=NOW()
			 WHERE id=$1 AND status='completed' AND cleanup_completed_at IS NULL`,
			bookingID,
		)
		_, _ = pool.Pool.Exec(ctx, `DELETE FROM notebooks WHERE id=$1`, notebookID)
		cleanupCompleted++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	logger.Info(
		"slot lifecycle step complete",
		"step", "cleanup_completed",
		"selected", selected,
		"cleanup_completed", cleanupCompleted,
		"waiting_grace", waitingGrace,
		"unknown_category", unknownCategory,
		"parse_errors", parseErrors,
		"delete_errors", deleteErrors,
	)
	summary.cleanupSelected += selected
	summary.cleanupCompleted += cleanupCompleted
	summary.cleanupWaitingGrace += waitingGrace
	summary.cleanupUnknownCategory += unknownCategory
	summary.cleanupParseErrors += parseErrors
	summary.cleanupDeleteErrors += deleteErrors
	return nil
}

func isNotebookRunning(k8sClient *k8s.K8sClient, namespace, notebookName string) (bool, error) {
	obj, err := k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(context.Background(), notebookName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	// If kubeflow-resource-stopped annotation is present, consider it not running.
	annotations, found, err := unstructured.NestedMap(obj.Object, "metadata", "annotations")
	if err == nil && found {
		if v, ok := annotations["kubeflow-resource-stopped"]; ok && v != "" {
			return false, nil
		}
	}

	statusMap, found, err := unstructured.NestedMap(obj.Object, "status")
	if err != nil || !found {
		return false, nil
	}

	readyReplicas, ok := statusMap["readyReplicas"]
	if !ok {
		return false, nil
	}

	switch t := readyReplicas.(type) {
	case int64:
		return t > 0, nil
	case int:
		return t > 0, nil
	case float64:
		return t > 0, nil
	default:
		return false, nil
	}
}

func addStoppedAnnotation(k8sClient *k8s.K8sClient, namespace, notebookName string) error {
	obj, err := k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(context.Background(), notebookName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	annotations, found, err := unstructured.NestedMap(obj.Object, "metadata", "annotations")
	if err != nil {
		return err
	}
	if !found {
		annotations = map[string]any{}
	}

	annotations["kubeflow-resource-stopped"] = time.Now().UTC().Format(time.RFC3339)
	if err := unstructured.SetNestedMap(obj.Object, annotations, "metadata", "annotations"); err != nil {
		return err
	}

	_, err = k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Update(context.Background(), obj, metav1.UpdateOptions{})
	return err
}

func deleteNotebookAndPVC(k8sClient *k8s.K8sClient, namespace, notebookName, pvcName string) error {
	ctx := context.Background()

	// Best-effort deletes; kubernetes may already have removed them.
	if err := k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Delete(ctx, notebookName, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	if err := k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Delete(ctx, pvcName, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}

