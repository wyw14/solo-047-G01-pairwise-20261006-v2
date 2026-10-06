package switchover

import (
	"context"
	"fmt"
	"time"

	"github.com/wyw14/cry-103/internal/platform"
	"github.com/wyw14/cry-103/internal/route"
)

type SessionService struct {
	drainer      *route.Drainer
	activator    *route.Activator
	ids          platform.IDGenerator
	now          func() time.Time
	drainTimeout time.Duration
}

func NewSessionService(drainer *route.Drainer, activator *route.Activator, ids platform.IDGenerator, now func() time.Time, drainTimeout time.Duration) *SessionService {
	return &SessionService{drainer: drainer, activator: activator, ids: ids, now: now, drainTimeout: drainTimeout}
}

func (s *SessionService) Failover(ctx context.Context, target *route.Route) (Snapshot, error) {
	spanID := target.Snapshot().SpanID
	// Refuse to fail over a line that is not pinned to the current asset
	// catalog revision; the operation record keeps both identifiers for audit.
	topologySnapshot, _, err := s.activator.Guard().PinnedSnapshot(spanID)
	if err != nil {
		return Snapshot{}, err
	}
	operation := NewOperation(s.ids.New("switch"), target.Snapshot().ID,
		topologySnapshot.CatalogRevision, topologySnapshot.Generation, s.now())
	operation.record(StateDrainingPrimary, "primary-drain", "draining failed primary path", s.now())
	drainTimeout := s.drainTimeout
	if drainTimeout <= 0 {
		drainTimeout = s.drainer.Delay()
	}
	drainCtx, cancelDrain := context.WithTimeout(ctx, drainTimeout)
	_, drainErr := s.drainer.Drain(drainCtx, target, s.now())
	cancelDrain()
	if drainErr != nil {
		operation.record(StateDrainingPrimary, "primary-drain", drainErr.Error(), s.now())
	}
	operation.record(StateActivating, "protection-activate", "activating protection path", s.now())
	if _, err := s.activator.Protection(ctx, target, s.now()); err != nil {
		result := operation.record(StateFailed, "protection-activate", err.Error(), s.now())
		return result, fmt.Errorf("activate protection: %w", err)
	}
	message := "protection path carries traffic"
	if drainErr != nil {
		message = "protection path carries traffic after primary drain timeout"
	}
	return operation.record(StateProtected, "complete", message, s.now()), nil
}
