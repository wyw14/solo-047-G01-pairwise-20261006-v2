package catalog

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validConfiguration(generation uint64) Configuration {
	return Configuration{
		Generation: generation,
		AppliedAt:  time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC),
		Stations: []Station{
			{ID: "east", Name: "East Landing"},
			{ID: "mid", Name: "Mid Repeater"},
			{ID: "west", Name: "West Landing"},
		},
		Segments: []Segment{
			{ID: "SG-1", SpanID: "S3", From: "east", To: "mid", Sequence: 1, LengthKM: 60.2},
			{ID: "SG-2", SpanID: "S3", From: "mid", To: "west", Sequence: 2, LengthKM: 66.2},
		},
		Paths: []PathBinding{
			{RouteID: "R-S3", SpanID: "S3", Role: RolePrimary, SegmentIDs: []string{"SG-1", "SG-2"}},
			{RouteID: "R-S3P", SpanID: "S3", Role: RoleProtection, SegmentIDs: []string{"SG-1", "SG-2"}},
		},
	}
}

func TestApplyAcceptsCompleteLineAndResolvesIt(t *testing.T) {
	directory := New()
	snapshot, err := directory.Apply(validConfiguration(1))
	if err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	if snapshot.Generation != 1 || len(snapshot.Segments) != 2 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	view, err := directory.ResolveRoute("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if view.Endpoints != [2]string{"east", "west"} {
		t.Fatalf("unexpected endpoints: %v", view.Endpoints)
	}
	if view.Primary.RouteID != "R-S3" || view.Protection.RouteID != "R-S3P" {
		t.Fatalf("unexpected bindings: %+v", view)
	}
	if view.CatalogGeneration != 1 {
		t.Fatalf("line view must carry catalog generation, got %d", view.CatalogGeneration)
	}
	if !directory.ServesEndpoint("R-S3", "east") || directory.ServesEndpoint("R-S3", "north") {
		t.Fatal("endpoint lookup is wrong")
	}
}

func TestRejectMissingEndpoint(t *testing.T) {
	directory := New()
	config := validConfiguration(1)
	config.Segments[0].To = ""
	if _, err := directory.Apply(config); err == nil || !strings.Contains(err.Error(), "missing an endpoint") {
		t.Fatalf("expected missing endpoint error, got %v", err)
	}
}

func TestRejectUnknownEndpointStation(t *testing.T) {
	directory := New()
	config := validConfiguration(1)
	config.Segments[1].To = "north"
	if _, err := directory.Apply(config); err == nil || !strings.Contains(err.Error(), "unknown endpoint station") {
		t.Fatalf("expected unknown endpoint error, got %v", err)
	}
}

func TestRejectBrokenSegmentChain(t *testing.T) {
	directory := New()
	config := validConfiguration(1)
	config.Segments[0].To = "west"
	config.Segments[1].From = "mid"
	if _, err := directory.Apply(config); err == nil || !strings.Contains(err.Error(), "chain breaks") {
		t.Fatalf("expected chain break error, got %v", err)
	}
}

func TestRejectDuplicatePathGeneration(t *testing.T) {
	directory := New()
	config := validConfiguration(1)
	config.Paths = append(config.Paths, PathBinding{
		RouteID: "R-S3-ALT", SpanID: "S3", Role: RolePrimary, SegmentIDs: []string{"SG-1", "SG-2"},
	})
	_, err := directory.Apply(config)
	if err == nil || !strings.Contains(err.Error(), "duplicate primary path generation") {
		t.Fatalf("expected duplicate path generation error, got %v", err)
	}
}

func TestRejectMissingProtectionPath(t *testing.T) {
	directory := New()
	config := validConfiguration(1)
	config.Paths = config.Paths[:1]
	if _, err := directory.Apply(config); err == nil || !strings.Contains(err.Error(), "protection path") {
		t.Fatalf("expected missing protection error, got %v", err)
	}
}

func TestRejectDuplicateAndSkippedGenerations(t *testing.T) {
	directory := New()
	if _, err := directory.Apply(validConfiguration(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.Apply(validConfiguration(1)); err == nil ||
		!strings.Contains(err.Error(), "generation must advance") {
		t.Fatalf("duplicate generation must be rejected, got %v", err)
	}
	if _, err := directory.Apply(validConfiguration(3)); err == nil ||
		!strings.Contains(err.Error(), "generation must advance") {
		t.Fatalf("skipped generation must be rejected, got %v", err)
	}
	next := validConfiguration(2)
	next.Stations = append(next.Stations, Station{ID: "north", Name: "North Landing"})
	snapshot, err := directory.Apply(next)
	if err != nil {
		t.Fatalf("sequential generation should be accepted: %v", err)
	}
	history := directory.History()
	if len(history) != 2 || !snapshot.AppliedAt.Equal(history[1].AppliedAt) {
		t.Fatalf("history must retain every revision: %+v", history)
	}
	at, err := directory.At(1)
	if err != nil || len(at.Stations) != 3 {
		t.Fatalf("revision lookup failed: %v %+v", err, at)
	}
}

func TestEmptyCatalogReportsError(t *testing.T) {
	directory := New()
	if _, err := directory.Current(); !errors.Is(err, ErrEmpty) {
		t.Fatalf("expected ErrEmpty, got %v", err)
	}
	if _, err := directory.ResolveRoute("R-S3"); !errors.Is(err, ErrEmpty) {
		t.Fatalf("expected ErrEmpty, got %v", err)
	}
}

func TestAppliedConfigurationIsImmutable(t *testing.T) {
	directory := New()
	config := validConfiguration(1)
	if _, err := directory.Apply(config); err != nil {
		t.Fatal(err)
	}
	config.Segments[0].From = "west"
	config.Paths[0].SegmentIDs[0] = "SG-2"
	snapshot, err := directory.Current()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Segments[0].From != "east" || snapshot.Paths[0].SegmentIDs[0] != "SG-1" {
		t.Fatal("catalog snapshot must not be mutable through the caller's input")
	}
}
