package topology

import (
	"errors"
	"fmt"
	"time"
)

func (r *Registry) Register(spanID string, path Path, catalogRevision uint64, at time.Time) (Snapshot, error) {
	snapshot := Snapshot{
		SpanID: spanID, Generation: 1, CatalogRevision: catalogRevision, ActivePath: path, ChangedAt: at.UTC(),
		PrimaryOpen: path != PathPrimary, ProtectOpen: path != PathProtection,
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.spans[spanID]; exists {
		return Snapshot{}, fmt.Errorf("topology for span %s already exists", spanID)
	}
	r.spans[spanID] = snapshot
	return snapshot, nil
}

func (r *Registry) Snapshot(spanID string) (Snapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot, exists := r.spans[spanID]
	if !exists {
		return Snapshot{}, fmt.Errorf("topology for span %s not found", spanID)
	}
	return snapshot, nil
}

func (r *Registry) Generation(spanID string) (uint64, error) {
	snapshot, err := r.Snapshot(spanID)
	if err != nil {
		return 0, err
	}
	return snapshot.Generation, nil
}

func (r *Registry) Revision(spanID string) (uint64, error) {
	snapshot, err := r.Snapshot(spanID)
	if err != nil {
		return 0, err
	}
	return snapshot.CatalogRevision, nil
}

func (r *Registry) Matches(spanID string, generation uint64) bool {
	snapshot, err := r.Snapshot(spanID)
	return err == nil && snapshot.Generation == generation
}

// Repin re-pins an existing topology to a newer asset catalog revision after
// the declared cable layout changed. Cross-connect switch state is preserved
// but the topology generation advances, so in-flight pinned scans fail fast.
func (r *Registry) Repin(spanID string, catalogRevision uint64, at time.Time) (Snapshot, error) {
	if catalogRevision == 0 {
		return Snapshot{}, errors.New("catalog revision is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, exists := r.spans[spanID]
	if !exists {
		return Snapshot{}, fmt.Errorf("topology for span %s not found", spanID)
	}
	if catalogRevision < snapshot.CatalogRevision {
		return Snapshot{}, fmt.Errorf("catalog revision %d is older than pinned revision %d", catalogRevision, snapshot.CatalogRevision)
	}
	if catalogRevision == snapshot.CatalogRevision {
		return snapshot, nil
	}
	snapshot.CatalogRevision = catalogRevision
	snapshot.Generation++
	snapshot.ChangedAt = at.UTC()
	r.spans[spanID] = snapshot
	return snapshot, nil
}

// Pinned reports whether the span's operational topology still reflects the
// supplied asset catalog revision.
func (r *Registry) Pinned(spanID string, catalogRevision uint64) bool {
	snapshot, err := r.Snapshot(spanID)
	return err == nil && snapshot.CatalogRevision == catalogRevision
}
