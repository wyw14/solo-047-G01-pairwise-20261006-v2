package inventory

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Catalog is the authoritative, append-validated asset catalog. All mutators
// advance Revision by exactly one and reject invalid or stale configurations
// before touching stored state. SpanRevisions tracks the global revision at
// which each span's physical layout last changed, so an unrelated edit (e.g.
// adding another line's site) does not invalidate a span's pin.
type Catalog struct {
	mu            sync.RWMutex
	revision      uint64
	updatedAt     time.Time
	sites         map[string]Site
	segments      map[string]CableSegment
	routePaths    map[string]RoutePath
	spanRevisions map[string]uint64
	now           func() time.Time
}

func NewCatalog(now func() time.Time) *Catalog {
	return &Catalog{
		revision:      0,
		sites:         make(map[string]Site),
		segments:      make(map[string]CableSegment),
		routePaths:    make(map[string]RoutePath),
		spanRevisions: make(map[string]uint64),
		now:           now,
	}
}

func (c *Catalog) checkRevision(expected uint64) error {
	if expected != 0 && expected != c.revision {
		return fmt.Errorf("%w: expected %d, current %d", ErrStale, expected, c.revision)
	}
	return nil
}

func (c *Catalog) touchLocked() time.Time {
	at := c.now().UTC()
	c.revision++
	c.updatedAt = at
	return at
}

// RegisterSite adds or replaces a landing site definition.
func (c *Catalog) RegisterSite(site Site, expectedRevision uint64) (Site, Snapshot, error) {
	if err := site.validate(); err != nil {
		return Site{}, Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkRevision(expectedRevision); err != nil {
		return Site{}, Snapshot{}, err
	}
	at := c.touchLocked()
	site.UpdatedAt = at
	c.sites[site.ID] = site
	return site, c.snapshotLocked(), nil
}

// RegisterSegment adds or replaces a cable segment. It rejects segments with
// missing endpoints, unknown sites, and a second segment that would occupy the
// same span+path slot ("duplicate generation" of a fiber path).
func (c *Catalog) RegisterSegment(segment CableSegment, expectedRevision uint64) (CableSegment, Snapshot, error) {
	if err := segment.validate(); err != nil {
		return CableSegment{}, Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkRevision(expectedRevision); err != nil {
		return CableSegment{}, Snapshot{}, err
	}
	if _, ok := c.sites[segment.FromSite]; !ok {
		return CableSegment{}, Snapshot{}, validationError("cable segment %s endpoint site %s is not registered", segment.ID, segment.FromSite)
	}
	if _, ok := c.sites[segment.ToSite]; !ok {
		return CableSegment{}, Snapshot{}, validationError("cable segment %s endpoint site %s is not registered", segment.ID, segment.ToSite)
	}
	if err := c.checkSegmentSlotLocked(segment); err != nil {
		return CableSegment{}, Snapshot{}, err
	}
	if err := c.declarationsRemainIntactLocked(segment); err != nil {
		return CableSegment{}, Snapshot{}, err
	}
	at := c.touchLocked()
	segment.UpdatedAt = at
	segment.Revision = c.revision
	c.segments[segment.ID] = segment
	c.spanRevisions[segment.SpanID] = c.revision
	return segment, c.snapshotLocked(), nil
}

// checkSegmentSlotLocked enforces one physical segment per span path. A
// replacement under the same segment id is allowed (re-cabling); a different
// segment id claiming an occupied span+path is a duplicate generation.
func (c *Catalog) checkSegmentSlotLocked(incoming CableSegment) error {
	for _, existing := range c.segments {
		if existing.SpanID == incoming.SpanID && existing.Path == incoming.Path && existing.ID != incoming.ID {
			return validationError(
				"span %s %s path already carries segment %s; duplicate segment generation %s",
				incoming.SpanID, incoming.Path, existing.ID, incoming.ID)
		}
	}
	return nil
}

// DeclareRoutePath records the primary/protection path declaration for a span
// and validates that each declared path has a contiguous set of segments
// joining the declared end sites.
func (c *Catalog) DeclareRoutePath(declaration RoutePath, expectedRevision uint64) (RoutePath, Snapshot, error) {
	if err := declaration.validate(); err != nil {
		return RoutePath{}, Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkRevision(expectedRevision); err != nil {
		return RoutePath{}, Snapshot{}, err
	}
	for _, siteID := range []string{
		declaration.PrimarySiteA, declaration.PrimarySiteZ,
		declaration.ProtectionSiteA, declaration.ProtectionSiteZ,
	} {
		if _, ok := c.sites[siteID]; !ok {
			return RoutePath{}, Snapshot{}, validationError("route path %s endpoint site %s is not registered", declaration.ID, siteID)
		}
	}
	if existing, ok := c.routePaths[declaration.ID]; ok && existing.SpanID != declaration.SpanID {
		return RoutePath{}, Snapshot{}, validationError("route path %s is already bound to span %s", declaration.ID, existing.SpanID)
	}
	if err := c.checkSpanUniqueLocked(declaration); err != nil {
		return RoutePath{}, Snapshot{}, err
	}
	if err := checkPathContiguousLocked(declaration.SpanID, "primary",
		declaration.PrimarySiteA, declaration.PrimarySiteZ, c.segments); err != nil {
		return RoutePath{}, Snapshot{}, err
	}
	if err := checkPathContiguousLocked(declaration.SpanID, "protection",
		declaration.ProtectionSiteA, declaration.ProtectionSiteZ, c.segments); err != nil {
		return RoutePath{}, Snapshot{}, err
	}
	at := c.touchLocked()
	declaration.UpdatedAt = at
	declaration.Revision = c.revision
	c.routePaths[declaration.ID] = declaration
	c.spanRevisions[declaration.SpanID] = c.revision
	return declaration, c.snapshotLocked(), nil
}

// declarationsRemainIntactLocked re-validates every existing route-path
// declaration that touches the incoming segment's span, using a temporary map
// that includes the incoming segment. This stops a segment replacement from
// silently breaking a previously complete path.
func (c *Catalog) declarationsRemainIntactLocked(incoming CableSegment) error {
	provisional := make(map[string]CableSegment, len(c.segments)+1)
	for id, segment := range c.segments {
		provisional[id] = segment
	}
	provisional[incoming.ID] = incoming
	for _, declaration := range c.routePaths {
		if declaration.SpanID != incoming.SpanID {
			continue
		}
		if err := checkPathContiguousLocked(declaration.SpanID, "primary",
			declaration.PrimarySiteA, declaration.PrimarySiteZ, provisional); err != nil {
			return err
		}
		if err := checkPathContiguousLocked(declaration.SpanID, "protection",
			declaration.ProtectionSiteA, declaration.ProtectionSiteZ, provisional); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) checkSpanUniqueLocked(incoming RoutePath) error {
	for _, existing := range c.routePaths {
		if existing.SpanID == incoming.SpanID && existing.ID != incoming.ID {
			return validationError("span %s already has route path declaration %s; duplicate path generation",
				incoming.SpanID, existing.ID)
		}
	}
	return nil
}

// checkPathContiguousLocked walks the undirected segment graph for one span
// path and requires the declared end sites to be joined by at least one
// segment, with every intermediate site having degree two (no dangling ends).
func checkPathContiguousLocked(spanID, path, siteA, siteZ string, segments map[string]CableSegment) error {
	degree := make(map[string]int)
	adjacency := make(map[string]map[string]bool)
	var count int
	for _, segment := range segments {
		if segment.SpanID != spanID || segment.Path != path {
			continue
		}
		count++
		degree[segment.FromSite]++
		degree[segment.ToSite]++
		if adjacency[segment.FromSite] == nil {
			adjacency[segment.FromSite] = make(map[string]bool)
		}
		if adjacency[segment.ToSite] == nil {
			adjacency[segment.ToSite] = make(map[string]bool)
		}
		adjacency[segment.FromSite][segment.ToSite] = true
		adjacency[segment.ToSite][segment.FromSite] = true
	}
	if count == 0 {
		return validationError("span %s %s path has no cable segment", spanID, path)
	}
	if degree[siteA] == 0 || degree[siteZ] == 0 {
		return validationError("span %s %s path is missing an endpoint segment", spanID, path)
	}
	for site, links := range degree {
		if (site == siteA || site == siteZ) && links != 1 {
			return validationError("span %s %s path endpoint %s must terminate exactly one segment", spanID, path, site)
		}
		if site != siteA && site != siteZ && links != 2 {
			return validationError("span %s %s path is broken at intermediate site %s", spanID, path, site)
		}
	}
	return nil
}

func (c *Catalog) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshotLocked()
}

func (c *Catalog) snapshotLocked() Snapshot {
	snapshot := Snapshot{
		Revision: c.revision, UpdatedAt: c.updatedAt,
		SpanRevisions: make(map[string]uint64, len(c.spanRevisions)),
		Sites:         make([]Site, 0, len(c.sites)),
		Segments:      make([]CableSegment, 0, len(c.segments)),
		RoutePaths:    make([]RoutePath, 0, len(c.routePaths)),
	}
	for spanID, revision := range c.spanRevisions {
		snapshot.SpanRevisions[spanID] = revision
	}
	for _, site := range c.sites {
		snapshot.Sites = append(snapshot.Sites, site)
	}
	for _, segment := range c.segments {
		snapshot.Segments = append(snapshot.Segments, segment)
	}
	for _, declaration := range c.routePaths {
		snapshot.RoutePaths = append(snapshot.RoutePaths, declaration)
	}
	sort.Slice(snapshot.Sites, func(i, j int) bool { return snapshot.Sites[i].ID < snapshot.Sites[j].ID })
	sort.Slice(snapshot.Segments, func(i, j int) bool { return snapshot.Segments[i].ID < snapshot.Segments[j].ID })
	sort.Slice(snapshot.RoutePaths, func(i, j int) bool { return snapshot.RoutePaths[i].ID < snapshot.RoutePaths[j].ID })
	return snapshot
}

func (c *Catalog) Revision() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.revision
}

// SpanRevision returns the global revision at which a span's physical layout
// (its cable segments or path declaration) last changed.
func (c *Catalog) SpanRevision(spanID string) (uint64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	revision, ok := c.spanRevisions[spanID]
	return revision, ok
}

// Site / segment / declaration lookups used by consumers and the integrity audit.
func (c *Catalog) Site(id string) (Site, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	site, ok := c.sites[id]
	return site, ok
}

func (c *Catalog) Segment(id string) (CableSegment, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	segment, ok := c.segments[id]
	return segment, ok
}

func (c *Catalog) RouteDeclaration(id string) (RoutePath, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	declaration, ok := c.routePaths[id]
	return declaration, ok
}

// SegmentsFor returns the catalog segments registered for a span.
func (c *Catalog) SegmentsFor(spanID string) []CableSegment {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var result []CableSegment
	for _, segment := range c.segments {
		if segment.SpanID == spanID {
			result = append(result, segment)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		return result[i].ID < result[j].ID
	})
	return result
}
