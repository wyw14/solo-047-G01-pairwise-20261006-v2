package otdr

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/wyw14/cry-103/internal/catalog"
	"github.com/wyw14/cry-103/internal/landing"
	"github.com/wyw14/cry-103/internal/platform"
	"github.com/wyw14/cry-103/internal/topology"
)

type SessionService struct {
	mu       sync.RWMutex
	catalog  *catalog.Catalog
	topology *topology.Registry
	scanner  *landing.Scanner
	ids      platform.IDGenerator
	now      func() time.Time
	traces   map[string]Trace
}

func NewSessionService(directory *catalog.Catalog, registry *topology.Registry, scanner *landing.Scanner, ids platform.IDGenerator, now func() time.Time) *SessionService {
	return &SessionService{
		catalog: directory, topology: registry, scanner: scanner, ids: ids, now: now,
		traces: make(map[string]Trace),
	}
}

func (s *SessionService) Run(ctx context.Context, spanID, stationID string, lengthKM float64, chunks int) (Trace, error) {
	// Every scan is pinned to the same traceable catalog revision as the rest
	// of the system: the span must exist in the directory and the scan station
	// must be one of its physical endpoints.
	line, err := s.catalog.Line(spanID)
	if err != nil {
		return Trace{}, err
	}
	if stationID == "" {
		return Trace{}, fmt.Errorf("scan station is required")
	}
	if line.Endpoints[0] != stationID && line.Endpoints[1] != stationID {
		return Trace{}, fmt.Errorf("station %s is not an endpoint of span %s in catalog generation %d",
			stationID, spanID, line.CatalogGeneration)
	}
	snapshot, err := s.topology.Snapshot(spanID)
	if err != nil {
		return Trace{}, err
	}
	startedAt := s.now()
	trace := Trace{
		ID: s.ids.New("scan"), SpanID: spanID, StationID: stationID,
		Generation: snapshot.Generation, CatalogGeneration: line.CatalogGeneration,
		Path: snapshot.ActivePath, StartedAt: startedAt.UTC(),
	}
	parts, err := s.scanner.ScanPinned(ctx, snapshot, stationID, chunks)
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
	if trace.CatalogGeneration != s.catalog.Generation() {
		return Trace{}, fmt.Errorf("asset catalog changed during OTDR scan")
	}
	s.mu.Lock()
	s.traces[trace.ID] = trace
	s.mu.Unlock()
	return trace, nil
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
