package exporters

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"

	"github.com/matanbaruch/cursor-admin-api-exporter/pkg/client"
)

type CursorExporter struct {
	client              *client.CursorClient
	teamMembersExporter *TeamMembersExporter
	dailyUsageExporter  *DailyUsageExporter
	spendingExporter    *SpendingExporter
	usageEventsExporter *UsageEventsExporter

	snapshotMu sync.RWMutex
	snapshot   cursorMetricsSnapshot

	scrapeDuration       prometheus.Histogram
	refreshDuration      prometheus.Histogram
	scrapeErrors         prometheus.Counter
	lastRefreshTimestamp *prometheus.Desc
}

type cursorMetricsSnapshot struct {
	teamMembers []client.TeamMember
	dailyUsage  []client.DailyUsage
	spending    []client.SpendingData
	usageEvents []client.UsageEvent

	hasTeamMembers bool
	hasDailyUsage  bool
	hasSpending    bool
	hasUsageEvents bool

	lastRefresh time.Time
}

func NewCursorExporter(baseURL, token string) *CursorExporter {
	cursorClient := client.NewCursorClient(baseURL, token)

	return &CursorExporter{
		client:              cursorClient,
		teamMembersExporter: NewTeamMembersExporter(cursorClient),
		dailyUsageExporter:  NewDailyUsageExporter(cursorClient),
		spendingExporter:    NewSpendingExporter(cursorClient),
		usageEventsExporter: NewUsageEventsExporter(cursorClient),

		scrapeDuration: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name: "cursor_exporter_scrape_duration_seconds",
				Help: "Time spent serving cached Cursor metrics",
			},
		),

		refreshDuration: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name: "cursor_exporter_refresh_duration_seconds",
				Help: "Time spent refreshing Cursor Admin API data",
			},
		),

		scrapeErrors: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "cursor_exporter_scrape_errors_total",
				Help: "Total number of Cursor API collection errors",
			},
		),

		lastRefreshTimestamp: prometheus.NewDesc(
			"cursor_exporter_last_refresh_timestamp_seconds",
			"Unix timestamp of the last successful Cursor API refresh",
			nil,
			nil,
		),
	}
}

func (e *CursorExporter) Start(ctx context.Context, refreshInterval time.Duration) {
	if refreshInterval <= 0 {
		refreshInterval = 5 * time.Minute
	}

	go e.runRefreshLoop(ctx, refreshInterval)
}

func (e *CursorExporter) runRefreshLoop(ctx context.Context, refreshInterval time.Duration) {
	e.refreshCursorMetrics()

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.refreshCursorMetrics()
		}
	}
}

func (e *CursorExporter) Describe(ch chan<- *prometheus.Desc) {
	e.teamMembersExporter.Describe(ch)
	e.dailyUsageExporter.Describe(ch)
	e.spendingExporter.Describe(ch)
	e.usageEventsExporter.Describe(ch)
	e.scrapeDuration.Describe(ch)
	e.refreshDuration.Describe(ch)
	e.scrapeErrors.Describe(ch)
	ch <- e.lastRefreshTimestamp
}

func (e *CursorExporter) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	defer func() {
		duration := time.Since(start)
		e.scrapeDuration.Observe(duration.Seconds())
		e.scrapeDuration.Collect(ch)
		e.refreshDuration.Collect(ch)
		e.scrapeErrors.Collect(ch)
		logrus.WithField("total_duration", duration).Debug("Completed Cursor metrics collection")
	}()

	logrus.Debug("Starting Cursor metrics collection")
	snapshot := e.currentSnapshot()
	if !snapshot.lastRefresh.IsZero() {
		ch <- prometheus.MustNewConstMetric(
			e.lastRefreshTimestamp,
			prometheus.GaugeValue,
			float64(snapshot.lastRefresh.Unix()),
		)
	}

	if !snapshot.hasMetrics() {
		logrus.Debug("No cached Cursor metrics available yet")
		return
	}

	if snapshot.hasTeamMembers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logrus.WithField("panic", r).Error("Panic during team members collection")
					e.scrapeErrors.Inc()
				}
			}()
			logrus.Debug("Starting cached team members collection")
			e.teamMembersExporter.CollectMembers(ch, snapshot.teamMembers)
			logrus.Debug("Completed cached team members collection")
		}()
	}

	if snapshot.hasDailyUsage {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logrus.WithField("panic", r).Error("Panic during daily usage collection")
					e.scrapeErrors.Inc()
				}
			}()
			logrus.Debug("Starting cached daily usage collection")
			e.dailyUsageExporter.CollectUsage(ch, snapshot.dailyUsage)
			logrus.Debug("Completed cached daily usage collection")
		}()
	}

	if snapshot.hasSpending {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logrus.WithField("panic", r).Error("Panic during spending collection")
					e.scrapeErrors.Inc()
				}
			}()
			logrus.Debug("Starting cached spending collection")
			e.spendingExporter.CollectSpending(ch, snapshot.spending)
			logrus.Debug("Completed cached spending collection")
		}()
	}

	if snapshot.hasUsageEvents {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logrus.WithField("panic", r).Error("Panic during usage events collection")
					e.scrapeErrors.Inc()
				}
			}()
			logrus.Debug("Starting cached usage events collection")
			e.usageEventsExporter.CollectEvents(ch, snapshot.usageEvents)
			logrus.Debug("Completed cached usage events collection")
		}()
	}
}

func (e *CursorExporter) currentSnapshot() cursorMetricsSnapshot {
	e.snapshotMu.RLock()
	defer e.snapshotMu.RUnlock()

	return e.snapshot
}

func (e *CursorExporter) storeSnapshot(snapshot cursorMetricsSnapshot) {
	e.snapshotMu.Lock()
	defer e.snapshotMu.Unlock()

	e.snapshot = snapshot
}

func (s cursorMetricsSnapshot) hasMetrics() bool {
	return s.hasTeamMembers || s.hasDailyUsage || s.hasSpending || s.hasUsageEvents
}

func (e *CursorExporter) refreshCursorMetrics() {
	start := time.Now()
	defer func() {
		duration := time.Since(start)
		e.refreshDuration.Observe(duration.Seconds())
		if r := recover(); r != nil {
			logrus.WithField("panic", r).Error("Panic during Cursor API refresh")
			e.scrapeErrors.Inc()
		}
	}()

	logrus.Debug("Starting Cursor API refresh")
	next := e.currentSnapshot()
	successes := 0

	if members, err := e.client.GetTeamMembers(); err != nil {
		e.recordRefreshError("team_members", err)
	} else {
		next.teamMembers = members
		next.hasTeamMembers = true
		successes++
	}

	endDate := time.Now().Format("2006-01-02")
	startDate := time.Now().AddDate(0, 0, -30).Format("2006-01-02")

	if usage, err := e.client.GetDailyUsage(startDate, endDate); err != nil {
		e.recordRefreshError("daily_usage", err)
	} else {
		next.dailyUsage = usage
		next.hasDailyUsage = true
		successes++
	}

	if spending, err := e.client.GetSpending(1000, 0); err != nil {
		e.recordRefreshError("spending", err)
	} else {
		next.spending = spending
		next.hasSpending = true
		successes++
	}

	if events, err := e.client.GetUsageEvents("", usageEventsPageSize, 0, startDate, endDate); err != nil {
		e.recordRefreshError("usage_events", err)
	} else {
		next.usageEvents = events
		next.hasUsageEvents = true
		successes++
	}

	if successes == 0 {
		logrus.Warn("Cursor API refresh produced no successful metrics")
		return
	}

	next.lastRefresh = time.Now()
	e.storeSnapshot(next)
	logrus.WithFields(logrus.Fields{
		"duration":  time.Since(start),
		"successes": successes,
	}).Debug("Completed Cursor API refresh")
}

func (e *CursorExporter) recordRefreshError(collector string, err error) {
	logrus.WithError(err).WithField("collector", collector).Error("Failed to refresh Cursor metrics")
	e.scrapeErrors.Inc()
}
