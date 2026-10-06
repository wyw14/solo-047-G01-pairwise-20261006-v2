// Package catalog maintains the authoritative, traceable asset directory for
// physical cable infrastructure: stations, cable segments, primary/protection
// path bindings and configuration generations. Every operational workflow
// (switchovers, OTDR scans, integrity audits) reads the same catalog snapshot so
// a line's physical topology can be confirmed end to end.
package catalog

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Role identifies which route role a path binding serves.
type Role string

const (
	RolePrimary    Role = "primary"
	RoleProtection Role = "protection"
)

// Station is an endpoint or intermediate landing site for cable segments.
type Station struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Segment is one cable segment (光缆段) linking two stations in sequence order.
type Segment struct {
	ID       string  `json:"id"`
	SpanID   string  `json:"span_id"`
	From     string  `json:"from_station"`
	To       string  `json:"to_station"`
	Sequence int     `json:"sequence"`
	LengthKM float64 `json:"length_km"`
}

// PathBinding binds a route to an ordered list of segments for one role
// (primary or protection). Both endpoints of the served line are implicit in
// the segment chain.
type PathBinding struct {
	RouteID    string   `json:"route_id"`
	SpanID     string   `json:"span_id"`
	Role       Role     `json:"role"`
	SegmentIDs []string `json:"segment_ids"`
}

// Configuration is a full, versioned asset directory revision. A new revision
// must use exactly Generation = current + 1; duplicate or skipped generations
// are rejected, which keeps every operational record attributable to one
// immutable catalog generation.
type Configuration struct {
	Generation uint64        `json:"generation"`
	Stations   []Station     `json:"stations"`
	Segments   []Segment     `json:"segments"`
	Paths      []PathBinding `json:"paths"`
	AppliedAt  time.Time     `json:"applied_at"`
}

// Snapshot is an immutable view of the catalog handed out to consumers.
type Snapshot struct {
	Generation uint64        `json:"generation"`
	Stations   []Station     `json:"stations"`
	Segments   []Segment     `json:"segments"`
	Paths      []PathBinding `json:"paths"`
	AppliedAt  time.Time     `json:"applied_at"`
}

// Catalog holds the single authoritative directory.
type Catalog struct {
	mu      sync.RWMutex
	current Snapshot
	history []Snapshot
}

// New creates an empty catalog at generation 0 (no configuration applied yet).
func New() *Catalog {
	return &Catalog{}
}

// Apply validates and installs a new configuration revision. It returns an
// error and leaves the catalog untouched when the revision is invalid.
func (c *Catalog) Apply(config Configuration) (Snapshot, error) {
	if config.Generation == 0 {
		return Snapshot{}, errors.New("catalog configuration generation must be positive")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if config.Generation != c.current.Generation+1 {
		return Snapshot{}, fmt.Errorf("catalog generation must advance from %d to %d, got %d",
			c.current.Generation, c.current.Generation+1, config.Generation)
	}
	if err := validate(config); err != nil {
		return Snapshot{}, &ValidationError{msg: err.Error()}
	}
	snapshot := freeze(config)
	c.current = snapshot
	c.history = append(c.history, snapshot)
	return snapshot, nil
}

// Current returns the installed snapshot. Before the first Apply it reports
// generation 0 and an ErrEmpty error so callers cannot mistake emptiness for a
// real revision.
func (c *Catalog) Current() (Snapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.current.Generation == 0 {
		return Snapshot{}, ErrEmpty
	}
	return c.current, nil
}

// At returns the revision with the requested generation.
func (c *Catalog) At(generation uint64) (Snapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if generation == 0 || generation > uint64(len(c.history)) {
		return Snapshot{}, fmt.Errorf("catalog generation %d not found", generation)
	}
	return c.history[generation-1], nil
}

// History returns all applied revisions, oldest first.
func (c *Catalog) History() []Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]Snapshot, len(c.history))
	copy(result, c.history)
	return result
}

// ErrEmpty signals that no configuration has been applied yet.
var ErrEmpty = errors.New("asset catalog has no configuration")

// ValidationError marks a configuration whose content is malformed (missing
// endpoints, duplicate generations, broken chains, ...). Callers distinguish it
// from sequencing errors such as reusing an older generation.
type ValidationError struct {
	msg string
}

func (e *ValidationError) Error() string { return e.msg }

func freeze(config Configuration) Snapshot {
	applied := config.AppliedAt.UTC()
	stations := append([]Station(nil), config.Stations...)
	segments := append([]Segment(nil), config.Segments...)
	paths := make([]PathBinding, len(config.Paths))
	for i, path := range config.Paths {
		paths[i] = PathBinding{
			RouteID: path.RouteID, SpanID: path.SpanID, Role: path.Role,
			SegmentIDs: append([]string(nil), path.SegmentIDs...),
		}
	}
	return Snapshot{
		Generation: config.Generation, Stations: stations, Segments: segments,
		Paths: paths, AppliedAt: applied,
	}
}

// validate enforces physical completeness:
//   - stations have identities and are unique;
//   - every segment has two distinct, known endpoints and positive length;
//   - segment chains (per span, ordered by sequence) link end to end;
//   - each span carries exactly one primary and one protection binding;
//   - every binding references existing segments of its span in chain order
//     and covers both line endpoints.
func validate(config Configuration) error {
	stations := make(map[string]Station, len(config.Stations))
	for _, station := range config.Stations {
		if station.ID == "" || station.Name == "" {
			return errors.New("station id and name are required")
		}
		if _, duplicate := stations[station.ID]; duplicate {
			return fmt.Errorf("duplicate station %s", station.ID)
		}
		stations[station.ID] = station
	}

	segments := make(map[string]Segment, len(config.Segments))
	bySpan := make(map[string][]Segment)
	for _, segment := range config.Segments {
		if segment.ID == "" || segment.SpanID == "" {
			return errors.New("segment id and span id are required")
		}
		// 拒绝缺少端点的配置。
		if segment.From == "" || segment.To == "" {
			return fmt.Errorf("segment %s is missing an endpoint", segment.ID)
		}
		if segment.From == segment.To {
			return fmt.Errorf("segment %s must connect two different stations", segment.ID)
		}
		if _, ok := stations[segment.From]; !ok {
			return fmt.Errorf("segment %s references unknown endpoint station %s", segment.ID, segment.From)
		}
		if _, ok := stations[segment.To]; !ok {
			return fmt.Errorf("segment %s references unknown endpoint station %s", segment.ID, segment.To)
		}
		if segment.LengthKM <= 0 {
			return fmt.Errorf("segment %s length must be positive", segment.ID)
		}
		if _, duplicate := segments[segment.ID]; duplicate {
			return fmt.Errorf("duplicate segment %s", segment.ID)
		}
		segments[segment.ID] = segment
		bySpan[segment.SpanID] = append(bySpan[segment.SpanID], segment)
	}
	for spanID, items := range bySpan {
		sort.Slice(items, func(i, j int) bool { return items[i].Sequence < items[j].Sequence })
		seenSequence := make(map[int]bool, len(items))
		for index, segment := range items {
			if seenSequence[segment.Sequence] {
				return fmt.Errorf("duplicate segment sequence %d on span %s", segment.Sequence, spanID)
			}
			seenSequence[segment.Sequence] = true
			if index > 0 && items[index-1].To != segment.From {
				return fmt.Errorf(
					"span %s segment chain breaks between %s and %s: station %s is not linked to %s",
					spanID, items[index-1].ID, segment.ID, items[index-1].To, segment.From)
			}
		}
	}

	type bindingKey struct {
		spanID string
		role   Role
	}
	pathsBySpan := make(map[string][]PathBinding)
	seen := make(map[bindingKey]bool, len(config.Paths))
	for _, path := range config.Paths {
		if path.RouteID == "" || path.SpanID == "" {
			return errors.New("path route id and span id are required")
		}
		if path.Role != RolePrimary && path.Role != RoleProtection {
			return fmt.Errorf("path for route %s has invalid role %q", path.RouteID, path.Role)
		}
		key := bindingKey{spanID: path.SpanID, role: path.Role}
		// 拒绝重复代际：同一 span 同一主备角色只允许一份绑定。
		if seen[key] {
			return fmt.Errorf("duplicate %s path generation for span %s", path.Role, path.SpanID)
		}
		seen[key] = true
		if len(path.SegmentIDs) == 0 {
			return fmt.Errorf("%s path for span %s has no segments", path.Role, path.SpanID)
		}
		for _, segmentID := range path.SegmentIDs {
			segment, ok := segments[segmentID]
			if !ok {
				return fmt.Errorf("%s path for span %s references missing segment %s", path.Role, path.SpanID, segmentID)
			}
			if segment.SpanID != path.SpanID {
				return fmt.Errorf("segment %s does not belong to span %s", segmentID, path.SpanID)
			}
		}
		pathsBySpan[path.SpanID] = append(pathsBySpan[path.SpanID], path)
	}
	for spanID, bindings := range pathsBySpan {
		if !seen[bindingKey{spanID: spanID, role: RolePrimary}] {
			return fmt.Errorf("span %s is missing its primary path", spanID)
		}
		if !seen[bindingKey{spanID: spanID, role: RoleProtection}] {
			return fmt.Errorf("span %s is missing its protection path", spanID)
		}
		chain, ok := bySpan[spanID]
		if !ok {
			return fmt.Errorf("span %s has path bindings but no segments", spanID)
		}
		for _, binding := range bindings {
			if err := validateBinding(binding, chain); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBinding(binding PathBinding, chain []Segment) error {
	ordered := append([]Segment(nil), chain...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Sequence < ordered[j].Sequence })
	byID := make(map[string]Segment, len(ordered))
	for _, segment := range ordered {
		byID[segment.ID] = segment
	}
	for index, segmentID := range binding.SegmentIDs {
		segment := byID[segmentID]
		if index == 0 {
			continue
		}
		previous := byID[binding.SegmentIDs[index-1]]
		if previous.To != segment.From {
			return fmt.Errorf("%s path for span %s is discontinuous at segment %s",
				binding.Role, binding.SpanID, segment.ID)
		}
	}
	return nil
}
