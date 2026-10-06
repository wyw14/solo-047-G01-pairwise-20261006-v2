// Package inventory is the single source of truth for the physical asset
// catalog: landing sites, cable segments (primary/protection fibers) and the
// declared primary/protection paths that tie them into a line.
//
// Every configuration change is validated here (missing endpoints, unknown
// sites, duplicate segments and stale/duplicate generations are rejected) and
// advances a monotonic catalog revision. Switchovers, OTDR scans and the
// integrity page all read and pin against this one catalog so a trace or a
// switchover can always be traced back to an exact, complete layout.
package inventory

import (
	"errors"
	"fmt"
	"time"
)

// ValidationError marks a rejected catalog configuration. API handlers map it
// to 422 Unprocessable Entity rather than a generic 409.
type ValidationError struct {
	reason string
}

func (e *ValidationError) Error() string { return e.reason }

func validationError(format string, args ...any) error {
	return &ValidationError{reason: fmt.Sprintf(format, args...)}
}

// NewValidationError builds a rejected-configuration error. Other packages
// (e.g. OTDR launch checks that read the catalog) use it to signal 422.
func NewValidationError(format string, args ...any) error {
	return validationError(format, args...)
}

// ErrNotFound is returned when an asset is missing from the catalog.
var ErrNotFound = errors.New("asset not found in catalog")

// ErrStale is returned when a declaration carries a non-zero expected revision
// that no longer matches the current catalog revision.
var ErrStale = errors.New("catalog revision is stale")

type Site struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CableSegment is one physical fiber run of a span between two landing sites.
// A span has at most one segment on the primary path and one on the
// protection path.
type CableSegment struct {
	ID        string    `json:"id"`
	SpanID    string    `json:"span_id"`
	Path      string    `json:"path"`
	FromSite  string    `json:"from_site"`
	ToSite    string    `json:"to_site"`
	LengthKM  float64   `json:"length_km"`
	Revision  uint64    `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RoutePath is the declaration that a span is a complete line carrying both a
// primary and a protection path between two end sites.
type RoutePath struct {
	ID              string    `json:"id"`
	SpanID          string    `json:"span_id"`
	RouteID         string    `json:"route_id"`
	PrimarySiteA    string    `json:"primary_site_a"`
	PrimarySiteZ    string    `json:"primary_site_z"`
	ProtectionSiteA string    `json:"protection_site_a"`
	ProtectionSiteZ string    `json:"protection_site_z"`
	Revision        uint64    `json:"revision"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (s Site) validate() error {
	if s.ID == "" || s.Name == "" {
		return validationError("site id and name are required")
	}
	return nil
}

func (c CableSegment) validate() error {
	if c.ID == "" {
		return validationError("cable segment id is required")
	}
	if c.SpanID == "" {
		return validationError("cable segment %s span id is required", c.ID)
	}
	if c.Path != "primary" && c.Path != "protection" {
		return validationError("cable segment %s path must be primary or protection", c.ID)
	}
	if c.FromSite == "" || c.ToSite == "" {
		return validationError("cable segment %s is missing an endpoint", c.ID)
	}
	if c.FromSite == c.ToSite {
		return validationError("cable segment %s endpoints must differ", c.ID)
	}
	if c.LengthKM <= 0 {
		return validationError("cable segment %s length must be positive", c.ID)
	}
	return nil
}

func (p RoutePath) validate() error {
	if p.ID == "" {
		return validationError("route path declaration id is required")
	}
	if p.SpanID == "" || p.RouteID == "" {
		return validationError("route path %s span id and route id are required", p.ID)
	}
	if p.PrimarySiteA == "" || p.PrimarySiteZ == "" {
		return validationError("route path %s primary endpoints are required", p.ID)
	}
	if p.ProtectionSiteA == "" || p.ProtectionSiteZ == "" {
		return validationError("route path %s protection endpoints are required", p.ID)
	}
	if p.PrimarySiteA == p.PrimarySiteZ || p.ProtectionSiteA == p.ProtectionSiteZ {
		return validationError("route path %s endpoints must differ", p.ID)
	}
	return nil
}

// Snapshot is an immutable, revision-stamped copy of the whole catalog. It is
// the value switchovers, scans and the integrity page consume. SpanRevisions
// maps each span to the global revision at which its physical layout last
// changed; pins compare against that, not against the whole-catalog revision.
type Snapshot struct {
	Revision      uint64            `json:"revision"`
	SpanRevisions map[string]uint64 `json:"span_revisions"`
	Sites         []Site            `json:"sites"`
	Segments      []CableSegment    `json:"cable_segments"`
	RoutePaths    []RoutePath       `json:"route_paths"`
	UpdatedAt     time.Time         `json:"updated_at"`
}
