package realtime

import (
	"context"
	"log/slog"
	"time"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/requestctx"
)

type ExpiryWorker struct {
	Service  Service
	Interval time.Duration
	Logger   *slog.Logger
}

func NewExpiryWorker(service Service, logger *slog.Logger) ExpiryWorker {
	return ExpiryWorker{
		Service:  service,
		Interval: 5 * time.Second,
		Logger:   logger,
	}
}

func (w ExpiryWorker) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	_, _ = w.RunOnce(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := w.RunOnce(ctx); err != nil && w.Logger != nil {
				w.Logger.Warn("realtime expiry worker failed",
					slog.String("component", "realtime"),
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

func (w ExpiryWorker) RunOnce(ctx context.Context) (ExpiryResult, error) {
	req, err := requestctx.ResolveBootstrap(ctx, w.Service.DB, correlation.Normalize("corr_realtime_expiry_worker"))
	if err != nil {
		return ExpiryResult{}, err
	}
	now := time.Now().UTC()
	result := ExpiryResult{}

	notifications, err := w.Service.ExpireNotifications(ctx, req, now)
	if err != nil {
		return ExpiryResult{}, err
	}
	result.NotificationsExpired = notifications.NotificationsExpired

	leases, err := w.Service.ExpireLeases(ctx, req, now)
	if err != nil {
		return ExpiryResult{}, err
	}
	result.LeasesExpired = leases.LeasesExpired

	subscriptions, err := w.Service.ExpireSubscriptions(ctx, now)
	if err != nil {
		return ExpiryResult{}, err
	}
	result.SubscriptionsExpired = subscriptions.SubscriptionsExpired

	presence, err := w.Service.MarkStalePresence(ctx, req, now)
	if err != nil {
		return ExpiryResult{}, err
	}
	result.PresenceMarkedStale = presence.PresenceMarkedStale

	progress, err := w.Service.CloseTerminalProgressFeeds(ctx, req)
	if err != nil {
		return ExpiryResult{}, err
	}
	result.ProgressFeedsClosed = progress.ProgressFeedsClosed

	if w.Logger != nil && (result.NotificationsExpired > 0 || result.LeasesExpired > 0 || result.SubscriptionsExpired > 0 || result.PresenceMarkedStale > 0 || result.ProgressFeedsClosed > 0) {
		w.Logger.Info("realtime expiry worker applied transitions",
			slog.String("component", "realtime"),
			slog.Int("notifications_expired", result.NotificationsExpired),
			slog.Int("leases_expired", result.LeasesExpired),
			slog.Int("subscriptions_expired", result.SubscriptionsExpired),
			slog.Int("presence_marked_stale", result.PresenceMarkedStale),
			slog.Int("progress_feeds_closed", result.ProgressFeedsClosed),
		)
	}
	return result, nil
}
