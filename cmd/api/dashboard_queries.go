package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sandbox-backend-service/pkg/constants"
)

type dashboardNotebook struct {
	ID, UserID, Name, Namespace, ResourceType, CPU, Memory string
	GPUType                                                *string
	GPURequest, GPULimit                                   int64
	LatestEvent                                            constants.Events
}

type dashboardSession struct {
	UserID, ResourceType, Status string
	Started, Ended               *time.Time
	SlotStart, SlotEnd           time.Time
}

type dashboardData struct {
	ObservedAt   time.Time
	Notebooks    []dashboardNotebook
	Sessions     []dashboardSession
	Owners       map[string]*string
	HistoryReady bool
}

type dashboardStore interface {
	Load(context.Context, dashboardQuery, string) (dashboardData, error)
}

type postgresDashboardStore struct{ pool *pgxpool.Pool }

const dashboardNotebookTypeSQL = `CASE WHEN GREATEST(COALESCE(gpu_request,0),COALESCE(gpu_limit,0)) > 0 THEN 'gpu' ELSE 'cpu' END`

func (s postgresDashboardStore) Load(ctx context.Context, q dashboardQuery, eventTimezone string) (dashboardData, error) {
	d := dashboardData{ObservedAt: time.Now().UTC(), Owners: map[string]*string{}, HistoryReady: eventTimezone != ""}
	if s.pool == nil {
		return d, fmt.Errorf("missing postgres pool")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return d, err
	}
	defer tx.Rollback(ctx)
	if q.Kind == "users" {
		rows, err := tx.Query(ctx, `WITH owners AS (
			SELECT user_id FROM notebooks WHERE ($1='all' OR `+dashboardNotebookTypeSQL+`=$1)
			UNION SELECT user_id FROM bookings WHERE resource_type IN ('cpu','gpu') AND ($1='all' OR resource_type=$1)
		) SELECT o.user_id::text,p.email FROM owners o LEFT JOIN profiles p ON p.user_id=o.user_id
		WHERE strpos(lower(COALESCE(p.email,'')),lower($2))>0 OR strpos(lower(o.user_id::text),lower($2))>0`, q.ResourceType, q.Search)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			var id string
			var email *string
			if err := rows.Scan(&id, &email); err != nil {
				rows.Close()
				return d, err
			}
			d.Owners[id] = email
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return d, err
		}
	}
	if q.Kind != "trends" {
		rows, err := tx.Query(ctx, `SELECT id::text,user_id::text,name,namespace,`+dashboardNotebookTypeSQL+`,
			cpu_request::text,memory_request,gpu_type,COALESCE(gpu_request,0),COALESCE(gpu_limit,0),COALESCE(events[array_upper(events,1)],'')
			FROM notebooks WHERE COALESCE(events[array_upper(events,1)],'')<>'deleted'
			AND ($1='all' OR `+dashboardNotebookTypeSQL+`=$1) AND ($2='' OR user_id::text=$2)`, q.ResourceType, q.UserID)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			var n dashboardNotebook
			if err := rows.Scan(&n.ID, &n.UserID, &n.Name, &n.Namespace, &n.ResourceType, &n.CPU, &n.Memory, &n.GPUType, &n.GPURequest, &n.GPULimit, &n.LatestEvent); err != nil {
				rows.Close()
				return d, err
			}
			if q.Kind == "users" {
				if _, ok := d.Owners[n.UserID]; !ok {
					continue
				}
			}
			d.Notebooks = append(d.Notebooks, n)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return d, err
		}
	}
	if q.Window != nil && d.HistoryReady {
		// Slots are IST wall clocks; event timestamps use the deployment's explicitly
		// verified legacy zone. Include invalid sessions by planned-slot overlap so
		// incomplete history is reported instead of quietly disappearing.
		rows, err := tx.Query(ctx, `WITH sessions AS (
			SELECT user_id,resource_type,status,
				session_started_at AT TIME ZONE $2 AS started,
				session_ended_at AT TIME ZONE $2 AS ended,
				slot_start AT TIME ZONE 'Asia/Kolkata' AS slot_start,
				slot_end AT TIME ZONE 'Asia/Kolkata' AS slot_end
			FROM bookings WHERE resource_type IN ('cpu','gpu') AND ($1='all' OR resource_type=$1)
			AND session_started_at IS NOT NULL
		) SELECT user_id::text,resource_type,status,started,ended,slot_start,slot_end FROM sessions
		WHERE (started<$4 AND COALESCE(ended,LEAST(slot_end,$5))>$3)
		OR (slot_start<$4 AND slot_end>$3)`, q.ResourceType, eventTimezone, q.Window.From, q.Window.To, q.Now)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			var b dashboardSession
			if err := rows.Scan(&b.UserID, &b.ResourceType, &b.Status, &b.Started, &b.Ended, &b.SlotStart, &b.SlotEnd); err != nil {
				rows.Close()
				return d, err
			}
			if q.Kind == "users" {
				if _, ok := d.Owners[b.UserID]; !ok {
					continue
				}
			}
			d.Sessions = append(d.Sessions, b)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return d, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return d, err
	}
	return d, nil
}
