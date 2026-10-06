package switchover

import (
	"context"
	"fmt"
	"time"

	"github.com/wyw14/cry-103/internal/platform"
	"github.com/wyw14/cry-103/internal/route"
	"github.com/wyw14/cry-103/internal/topology"
)

type Restorer struct {
	drainer   *route.Drainer
	activator *route.Activator
	ids       platform.IDGenerator
	now       func() time.Time
}

func NewRestorer(drainer *route.Drainer, activator *route.Activator, ids platform.IDGenerator, now func() time.Time) *Restorer {
	return &Restorer{drainer: drainer, activator: activator, ids: ids, now: now}
}

func (r *Restorer) Restore(ctx context.Context, target *route.Route) (Snapshot, error) {
	spanID := target.Snapshot().SpanID
	// Pin to the catalog before touching any cross-connect, so restore cannot
	// run against a line whose physical layout changed underneath it.
	topologySnapshot, _, err := r.activator.Guard().PinnedSnapshot(spanID)
	if err != nil {
		return Snapshot{}, err
	}
	operation := NewOperation(r.ids.New("restore"), target.Snapshot().ID,
		topologySnapshot.CatalogRevision, topologySnapshot.Generation, r.now())
	operation.record(StateRestoring, "protection-drain", "draining protection traffic", r.now())
	if _, err := r.drainer.Drain(ctx, target, r.now()); err != nil {
		result := operation.record(StateFailed, "protection-drain", err.Error(), r.now())
		return result, err
	}
	registry := r.activator.Guard().Topology()
	if _, err := registry.Open(spanID, topology.PathProtection, r.now()); err != nil {
		result := operation.record(StateFailed, "protection-open", err.Error(), r.now())
		return result, err
	}
	operation.record(StateRestoring, "primary-connect", "connecting recovered primary path", r.now())
	if _, err := r.activator.Primary(ctx, target, r.now()); err != nil {
		result := operation.record(StateFailed, "primary-connect", err.Error(), r.now())
		return result, fmt.Errorf("connect primary: %w", err)
	}
	return operation.record(StateCompleted, "complete", "primary route restored", r.now()), nil
}
