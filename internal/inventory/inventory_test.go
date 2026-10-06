package inventory

import (
	"errors"
	"testing"
	"time"

	"github.com/wyw14/cry-103/internal/topology"
)

func fixedNow() time.Time { return time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC) }

func seedCompleteCatalog(t *testing.T, catalog *Catalog) {
	t.Helper()
	if _, _, err := catalog.RegisterSite(Site{ID: "east", Name: "East Landing"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.RegisterSite(Site{ID: "west", Name: "West Landing"}, 0); err != nil {
		t.Fatal(err)
	}
	register := func(id, path string) {
		if _, _, err := catalog.RegisterSegment(CableSegment{
			ID: id, SpanID: "S3", Path: path, FromSite: "east", ToSite: "west", LengthKM: 63.2,
		}, 0); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	register("SEG-S3-P", "primary")
	register("SEG-S3-B", "protection")
	if _, _, err := catalog.DeclareRoutePath(RoutePath{
		ID: "LINE-S3", SpanID: "S3", RouteID: "R-S3",
		PrimarySiteA: "east", PrimarySiteZ: "west",
		ProtectionSiteA: "east", ProtectionSiteZ: "west",
	}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterSiteRejectsMissingIdentity(t *testing.T) {
	catalog := NewCatalog(fixedNow)
	if _, _, err := catalog.RegisterSite(Site{ID: "east"}, 0); err == nil {
		t.Fatal("site without a name must be rejected")
	}
}

func TestRegisterSegmentRejectsMissingEndpoints(t *testing.T) {
	catalog := NewCatalog(fixedNow)
	if _, _, err := catalog.RegisterSite(Site{ID: "east", Name: "East"}, 0); err != nil {
		t.Fatal(err)
	}
	cases := []CableSegment{
		{ID: "x", SpanID: "S3", Path: "primary", FromSite: "", ToSite: "west", LengthKM: 10},
		{ID: "x", SpanID: "S3", Path: "primary", FromSite: "east", ToSite: "", LengthKM: 10},
		{ID: "x", SpanID: "S3", Path: "primary", FromSite: "east", ToSite: "east", LengthKM: 10},
		{ID: "x", SpanID: "S3", Path: "primary", FromSite: "east", ToSite: "west", LengthKM: 0},
		{ID: "x", SpanID: "S3", Path: "sideways", FromSite: "east", ToSite: "west", LengthKM: 10},
	}
	for index, segment := range cases {
		if _, _, err := catalog.RegisterSegment(segment, 0); err == nil {
			t.Fatalf("case %d must be rejected: %+v", index, segment)
		} else if !errors.Is(err, ErrNotFound) && !isValidation(err) {
			t.Fatalf("case %d expected validation error, got %v", index, err)
		}
	}
}

func TestRegisterSegmentRejectsUnknownSite(t *testing.T) {
	catalog := NewCatalog(fixedNow)
	if _, _, err := catalog.RegisterSite(Site{ID: "east", Name: "East"}, 0); err != nil {
		t.Fatal(err)
	}
	_, _, err := catalog.RegisterSegment(CableSegment{
		ID: "SEG", SpanID: "S3", Path: "primary", FromSite: "east", ToSite: "ghost", LengthKM: 10,
	}, 0)
	if err == nil || !isValidation(err) {
		t.Fatalf("unknown endpoint site must be rejected, got %v", err)
	}
}

func TestRegisterSegmentRejectsDuplicateGeneration(t *testing.T) {
	catalog := NewCatalog(fixedNow)
	seedCompleteCatalog(t, catalog)
	_, _, err := catalog.RegisterSegment(CableSegment{
		ID: "SEG-S3-P-DUP", SpanID: "S3", Path: "primary", FromSite: "east", ToSite: "west", LengthKM: 63.2,
	}, 0)
	if err == nil || !isValidation(err) {
		t.Fatalf("second primary segment for one span is a duplicate generation, got %v", err)
	}
	// Replacing the same segment id is a legitimate re-cable and must succeed.
	if _, _, err := catalog.RegisterSegment(CableSegment{
		ID: "SEG-S3-P", SpanID: "S3", Path: "primary", FromSite: "east", ToSite: "west", LengthKM: 64.0,
	}, 0); err != nil {
		t.Fatalf("same-id segment replacement must be allowed: %v", err)
	}
}

func TestStaleExpectedRevisionIsRejected(t *testing.T) {
	catalog := NewCatalog(fixedNow)
	if _, _, err := catalog.RegisterSite(Site{ID: "east", Name: "East"}, 0); err != nil {
		t.Fatal(err)
	}
	_, _, err := catalog.RegisterSite(Site{ID: "west", Name: "West"}, 7)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("expected stale revision error, got %v", err)
	}
	// Matching revision is accepted.
	current := catalog.Revision()
	if _, _, err := catalog.RegisterSite(Site{ID: "west", Name: "West"}, current); err != nil {
		t.Fatalf("matching revision must be accepted: %v", err)
	}
}

func TestDeclareRoutePathRequiresBothCompletePaths(t *testing.T) {
	catalog := NewCatalog(fixedNow)
	if _, _, err := catalog.RegisterSite(Site{ID: "east", Name: "East"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.RegisterSite(Site{ID: "west", Name: "West"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.RegisterSegment(CableSegment{
		ID: "SEG-P", SpanID: "S3", Path: "primary", FromSite: "east", ToSite: "west", LengthKM: 60,
	}, 0); err != nil {
		t.Fatal(err)
	}
	// Protection missing -> declaration rejected.
	_, _, err := catalog.DeclareRoutePath(RoutePath{
		ID: "LINE-S3", SpanID: "S3", RouteID: "R-S3",
		PrimarySiteA: "east", PrimarySiteZ: "west",
		ProtectionSiteA: "east", ProtectionSiteZ: "west",
	}, 0)
	if err == nil || !isValidation(err) {
		t.Fatalf("declaration without a protection cable must be rejected, got %v", err)
	}
}

func TestSegmentReplacementCannotBreakDeclaredPath(t *testing.T) {
	catalog := NewCatalog(fixedNow)
	seedCompleteCatalog(t, catalog)
	if _, _, err := catalog.RegisterSite(Site{ID: "north", Name: "North"}, 0); err != nil {
		t.Fatal(err)
	}
	// Repoint the primary segment away from the declared west endpoint.
	_, _, err := catalog.RegisterSegment(CableSegment{
		ID: "SEG-S3-P", SpanID: "S3", Path: "primary", FromSite: "east", ToSite: "north", LengthKM: 63.2,
	}, 0)
	if err == nil || !isValidation(err) {
		t.Fatalf("repointing a segment away from a declared endpoint must be rejected, got %v", err)
	}
}

func TestRevisionAdvancesOncePerChange(t *testing.T) {
	catalog := NewCatalog(fixedNow)
	before := catalog.Revision()
	_, snapshot, err := catalog.RegisterSite(Site{ID: "east", Name: "East"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != before+1 {
		t.Fatalf("revision = %d, want %d", snapshot.Revision, before+1)
	}
}

func TestAuditCompleteCatalogHasNoPhysicalFindings(t *testing.T) {
	catalog := NewCatalog(fixedNow)
	seedCompleteCatalog(t, catalog)
	findings := auditCatalog(catalog.Snapshot())
	for _, finding := range findings {
		if finding.Severity == SeverityCritical {
			t.Fatalf("complete catalog produced critical finding: %+v", finding)
		}
	}
}

func TestAuditFlagsMissingProtectionCable(t *testing.T) {
	catalog := NewCatalog(fixedNow)
	if _, _, err := catalog.RegisterSite(Site{ID: "east", Name: "East"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.RegisterSite(Site{ID: "west", Name: "West"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.RegisterSegment(CableSegment{
		ID: "SEG-P", SpanID: "S3", Path: "primary", FromSite: "east", ToSite: "west", LengthKM: 60,
	}, 0); err != nil {
		t.Fatal(err)
	}
	findings := auditCatalog(catalog.Snapshot())
	if !hasCode(findings, "span-missing-protection-cable") {
		t.Fatalf("expected missing protection finding, got %+v", findings)
	}
}

func TestAuditFlagsTopologyCatalogDrift(t *testing.T) {
	catalog := NewCatalog(fixedNow)
	seedCompleteCatalog(t, catalog)
	snapshot := catalog.Snapshot()
	findings := Audit(snapshot, Operands{
		Spans:  []SpanOperand{{ID: "S3", LengthKM: 126.4, State: "normal"}},
		Routes: []RouteOperand{{ID: "R-S3", SpanID: "S3", ActivePath: topology.PathPrimary, TrafficBPS: 1}},
		Topologies: []topology.Snapshot{{
			SpanID: "S3", Generation: 1, CatalogRevision: snapshot.Revision - 1,
			ActivePath: topology.PathPrimary,
		}},
	})
	if !hasCode(findings, "topology-catalog-drift") {
		t.Fatalf("expected catalog drift finding, got %+v", findings)
	}
}

func isValidation(err error) bool {
	var validation *ValidationError
	return errors.As(err, &validation)
}

func hasCode(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
