package inventory

import (
	"fmt"
	"time"

	"github.com/wyw14/cry-103/internal/topology"
)

// Guard binds the asset catalog to the operational cross-connect topology. It
// is the single chokepoint switchovers and OTDR scans pass through: an
// operation may only proceed while the live topology is pinned to the current
// revision of that span's physical layout. Edits to unrelated spans or sites
// do not invalidate the pin.
type Guard struct {
	catalog  *Catalog
	topology *topology.Registry
	now      func() time.Time
}

func NewGuard(catalog *Catalog, registry *topology.Registry, now func() time.Time) *Guard {
	return &Guard{catalog: catalog, topology: registry, now: now}
}

func (g *Guard) Catalog() *Catalog            { return g.catalog }
func (g *Guard) Topology() *topology.Registry { return g.topology }

func (g *Guard) hasDeclaration(spanID string) bool {
	for _, declaration := range g.catalog.Snapshot().RoutePaths {
		if declaration.SpanID == spanID {
			return true
		}
	}
	return false
}

// spanRevision returns the layout revision a span must be pinned to.
func (g *Guard) spanRevision(spanID string) (uint64, error) {
	revision, ok := g.catalog.SpanRevision(spanID)
	if !ok {
		return 0, validationError("span %s has no cable layout in the asset catalog", spanID)
	}
	return revision, nil
}

// Activate registers operational topology for a span against the current
// revision of its physical layout. Used when a declared line is brought under
// management.
func (g *Guard) Activate(spanID string, active topology.Path) (topology.Snapshot, error) {
	if !g.hasDeclaration(spanID) {
		return topology.Snapshot{}, validationError("span %s has no primary/protection route declaration", spanID)
	}
	revision, err := g.spanRevision(spanID)
	if err != nil {
		return topology.Snapshot{}, err
	}
	return g.topology.Register(spanID, active, revision, g.now())
}

// Sync re-pins a span's operational topology after its layout changed. Spans
// without live topology are left untouched (they activate on demand).
func (g *Guard) Sync(spanID string) (topology.Snapshot, bool, error) {
	if _, err := g.topology.Snapshot(spanID); err != nil {
		return topology.Snapshot{}, false, nil
	}
	revision, err := g.spanRevision(spanID)
	if err != nil {
		return topology.Snapshot{}, false, err
	}
	snapshot, err := g.topology.Repin(spanID, revision, g.now())
	if err != nil {
		return topology.Snapshot{}, false, err
	}
	return snapshot, true, nil
}

// PinnedSnapshot returns the live topology for a span only when it is pinned
// to the current revision of that span's physical layout. Drift means the
// layout changed under a live line and the operation must wait for a re-sync.
func (g *Guard) PinnedSnapshot(spanID string) (topology.Snapshot, Snapshot, error) {
	catalogSnapshot := g.catalog.Snapshot()
	snapshot, err := g.topology.Snapshot(spanID)
	if err != nil {
		return topology.Snapshot{}, catalogSnapshot, err
	}
	expected, err := g.spanRevision(spanID)
	if err != nil {
		return topology.Snapshot{}, catalogSnapshot, err
	}
	if snapshot.CatalogRevision != expected {
		return topology.Snapshot{}, catalogSnapshot, fmt.Errorf(
			"span %s operational topology is pinned to catalog revision %d, its layout is now at revision %d; re-sync required",
			spanID, snapshot.CatalogRevision, expected)
	}
	if !g.hasDeclaration(spanID) {
		return topology.Snapshot{}, catalogSnapshot,
			validationError("span %s has no primary/protection route declaration", spanID)
	}
	return snapshot, catalogSnapshot, nil
}

// Pin returns the (catalog layout revision, topology generation) pair an
// operation records for traceability.
func (g *Guard) Pin(spanID string) (catalogRevision, generation uint64, err error) {
	snapshot, _, err := g.PinnedSnapshot(spanID)
	if err != nil {
		return 0, 0, err
	}
	return snapshot.CatalogRevision, snapshot.Generation, nil
}
