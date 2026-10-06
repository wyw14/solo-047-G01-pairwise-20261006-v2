package otdr

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/wyw14/cry-103/internal/inventory"
	"github.com/wyw14/cry-103/internal/landing"
	"github.com/wyw14/cry-103/internal/platform"
)

type SessionService struct {
	mu      sync.RWMutex
	guard   *inventory.Guard
	scanner *landing.Scanner
	ids     platform.IDGenerator
	now     func() time.Time
	traces  map[string]Trace
}

func NewSessionService(guard *inventory.Guard, scanner *landing.Scanner, ids platform.IDGenerator, now func() time.Time) *SessionService {
	return &SessionService{
		guard: guard, scanner: scanner, ids: ids, now: now, traces: make(map[string]Trace),
	}
}

func (s *SessionService) Run(ctx context.Context, spanID, stationID string, lengthKM float64, chunks int) (Trace, error) {
	topologySnapshot, catalogSnapshot, err := s.guard.PinnedSnapshot(spanID)
	if err != nil {
		return Trace{}, err
	}
	if err := requireLaunchEndpoint(catalogSnapshot, spanID, string(topologySnapshot.ActivePath), stationID); err != nil {
		return Trace{}, err
	}
	startedAt := s.now()
	trace := Trace{
		ID: s.ids.New("scan"), SpanID: spanID, StationID: stationID,
		CatalogRevision: topologySnapshot.CatalogRevision,
		Generation:      topologySnapshot.Generation,
		Path:            topologySnapshot.ActivePath, StartedAt: startedAt.UTC(),
	}
	parts, err := s.scanner.ScanPinned(ctx, topologySnapshot, stationID, chunks)
	if err != nil {
		return Trace{}, fmt.Errorf("collect OTDR chunks: %w", err)
	}
	trace, err = assemble(trace, parts, lengthKM, s.now())
	if err != nil {
		return Trace{}, err
	}
	if len(trace.Generations()) != 1 {
		return Trace{}, fmt.Errorf("OTDR trace contains multiple topology generations")
	}
	s.mu.Lock()
	s.traces[trace.ID] = trace
	s.mu.Unlock()
	return trace, nil
}

// requireLaunchEndpoint ensures an OTDR scan is launched from a landing site
// that is actually an endpoint of the scanned physical path in the catalog,
// rather than an arbitrary or unknown station.
func requireLaunchEndpoint(catalog inventory.Snapshot, spanID, path, stationID string) error {
	if stationID == "" {
		return inventory.NewValidationError("OTDR launch station is required")
	}
	for _, segment := range catalog.Segments {
		if segment.SpanID != spanID || segment.Path != path {
			continue
		}
		if segment.FromSite == stationID || segment.ToSite == stationID {
			return nil
		}
	}
	return inventory.NewValidationError("station %s is not a catalog endpoint of span %s %s path", stationID, spanID, path)
}

func (s *SessionService) Get(id string) (Trace, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	trace, exists := s.traces[id]
	return trace, exists
}

func (s *SessionService) List() []Trace {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Trace, 0, len(s.traces))
	for _, trace := range s.traces {
		result = append(result, trace)
	}
	return result
}
