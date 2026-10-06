package route

import (
	"context"
	"fmt"
	"time"

	"github.com/wyw14/cry-103/internal/inventory"
	"github.com/wyw14/cry-103/internal/topology"
)

type Activator struct {
	guard *inventory.Guard
}

func NewActivator(guard *inventory.Guard) *Activator {
	return &Activator{guard: guard}
}

func (a *Activator) Guard() *inventory.Guard { return a.guard }

// requirePinned refuses a cross-connect change unless the span is declared in
// the asset catalog and its live topology is pinned to the current revision.
func (a *Activator) requirePinned(spanID string) (topology.Snapshot, error) {
	snapshot, _, err := a.guard.PinnedSnapshot(spanID)
	return snapshot, err
}

func (a *Activator) Protection(ctx context.Context, route *Route, at time.Time) (Snapshot, error) {
	select {
	case <-ctx.Done():
		return route.Snapshot(), fmt.Errorf("protection activation: %w", ctx.Err())
	default:
	}
	spanID := route.Snapshot().SpanID
	if _, err := a.requirePinned(spanID); err != nil {
		return route.Snapshot(), err
	}
	registry := a.guard.Topology()
	if _, err := registry.Open(spanID, topology.PathPrimary, at); err != nil {
		return route.Snapshot(), err
	}
	if _, err := registry.Connect(spanID, topology.PathProtection, at); err != nil {
		return route.Snapshot(), err
	}
	if _, err := registry.Switch(spanID, topology.PathProtection, at); err != nil {
		return route.Snapshot(), err
	}
	return route.update(StateProtectionActive, topology.PathProtection, 1_000_000, at), nil
}

func (a *Activator) Primary(ctx context.Context, route *Route, at time.Time) (Snapshot, error) {
	select {
	case <-ctx.Done():
		return route.Snapshot(), ctx.Err()
	default:
	}
	spanID := route.Snapshot().SpanID
	if _, err := a.requirePinned(spanID); err != nil {
		return route.Snapshot(), err
	}
	registry := a.guard.Topology()
	if _, err := registry.Connect(spanID, topology.PathPrimary, at); err != nil {
		return route.Snapshot(), err
	}
	if _, err := registry.Switch(spanID, topology.PathPrimary, at); err != nil {
		return route.Snapshot(), err
	}
	return route.update(StatePrimary, topology.PathPrimary, 1_000_000, at), nil
}
