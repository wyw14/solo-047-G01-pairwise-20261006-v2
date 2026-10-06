package catalog

import (
	"fmt"
	"sort"
)

// LineView is the resolved physical topology of one line (span): its ordered
// segments and both primary/protection bindings with line endpoints.
type LineView struct {
	SpanID            string
	Endpoints         [2]string
	Segments          []Segment
	Primary           PathBinding
	Protection        PathBinding
	CatalogGeneration uint64
}

// Station looks up a station by id.
func (c *Catalog) Station(id string) (Station, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, station := range c.current.Stations {
		if station.ID == id {
			return station, true
		}
	}
	return Station{}, false
}

// Segment looks up a cable segment by id.
func (c *Catalog) Segment(id string) (Segment, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, segment := range c.current.Segments {
		if segment.ID == id {
			return segment, true
		}
	}
	return Segment{}, false
}

// ResolveRoute returns the physical line that a route carries traffic over,
// including both path bindings and the catalog generation they come from.
func (c *Catalog) ResolveRoute(routeID string) (LineView, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.current.Generation == 0 {
		return LineView{}, ErrEmpty
	}
	var spanID string
	for _, path := range c.current.Paths {
		if path.RouteID == routeID {
			spanID = path.SpanID
			break
		}
	}
	if spanID == "" {
		return LineView{}, fmt.Errorf("route %s is not bound in asset catalog", routeID)
	}
	return c.lineLocked(spanID)
}

// Line returns the resolved physical topology for a span.
func (c *Catalog) Line(spanID string) (LineView, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.current.Generation == 0 {
		return LineView{}, ErrEmpty
	}
	return c.lineLocked(spanID)
}

func (c *Catalog) lineLocked(spanID string) (LineView, error) {
	view := LineView{SpanID: spanID, CatalogGeneration: c.current.Generation}
	found := false
	for _, segment := range c.current.Segments {
		if segment.SpanID == spanID {
			view.Segments = append(view.Segments, segment)
			found = true
		}
	}
	if !found {
		return LineView{}, fmt.Errorf("span %s is not present in asset catalog", spanID)
	}
	sort.Slice(view.Segments, func(i, j int) bool { return view.Segments[i].Sequence < view.Segments[j].Sequence })
	view.Endpoints = [2]string{view.Segments[0].From, view.Segments[len(view.Segments)-1].To}
	for _, path := range c.current.Paths {
		if path.SpanID != spanID {
			continue
		}
		switch path.Role {
		case RolePrimary:
			view.Primary = path
		case RoleProtection:
			view.Protection = path
		}
	}
	if view.Primary.RouteID == "" || view.Protection.RouteID == "" {
		return LineView{}, fmt.Errorf("span %s catalog line is missing a primary/protection binding", spanID)
	}
	return view, nil
}

// SpanIDs lists every span covered by the catalog.
func (c *Catalog) SpanIDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := make(map[string]bool)
	var result []string
	for _, segment := range c.current.Segments {
		if !seen[segment.SpanID] {
			seen[segment.SpanID] = true
			result = append(result, segment.SpanID)
		}
	}
	sort.Strings(result)
	return result
}

// ServesEndpoint reports whether a station is an endpoint of the line the
// route runs over. OTDR scans must originate from such a station.
func (c *Catalog) ServesEndpoint(routeID, stationID string) bool {
	view, err := c.ResolveRoute(routeID)
	if err != nil {
		return false
	}
	return view.Endpoints[0] == stationID || view.Endpoints[1] == stationID
}

// EndpointOfSpan reports whether a station is one of the physical endpoints of
// the line (span) in the current catalog revision.
func (c *Catalog) EndpointOfSpan(spanID, stationID string) bool {
	view, err := c.Line(spanID)
	if err != nil {
		return false
	}
	return view.Endpoints[0] == stationID || view.Endpoints[1] == stationID
}

// Generation returns the current catalog generation (0 when empty).
func (c *Catalog) Generation() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.current.Generation
}
