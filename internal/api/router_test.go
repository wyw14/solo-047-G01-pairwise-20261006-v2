package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/wyw14/cry-103/internal/catalog"
	"github.com/wyw14/cry-103/internal/feed"
	"github.com/wyw14/cry-103/internal/incident"
	"github.com/wyw14/cry-103/internal/landing"
	"github.com/wyw14/cry-103/internal/otdr"
	"github.com/wyw14/cry-103/internal/platform"
	"github.com/wyw14/cry-103/internal/repair"
	"github.com/wyw14/cry-103/internal/repeater"
	"github.com/wyw14/cry-103/internal/route"
	"github.com/wyw14/cry-103/internal/span"
	"github.com/wyw14/cry-103/internal/store"
	"github.com/wyw14/cry-103/internal/switchover"
	"github.com/wyw14/cry-103/internal/telemetry"
	"github.com/wyw14/cry-103/internal/topology"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	clock := fixedClock{now: time.Date(2026, 8, 24, 3, 0, 0, 0, time.UTC)}
	ids := platform.UUIDGenerator{}
	registry := topology.NewRegistry()
	if _, err := registry.Register("S3", topology.PathPrimary, clock.Now()); err != nil {
		t.Fatal(err)
	}
	directory := catalog.New()
	seedAppliedAt := clock.Now()
	if _, err := directory.Apply(catalog.Configuration{
		Generation: 1,
		AppliedAt:  seedAppliedAt,
		Stations: []catalog.Station{
			{ID: "east", Name: "East Landing", UpdatedAt: seedAppliedAt},
			{ID: "west", Name: "West Landing", UpdatedAt: seedAppliedAt},
		},
		Segments: []catalog.Segment{
			{ID: "SG-1", SpanID: "S3", From: "east", To: "west", Sequence: 1, LengthKM: 126},
		},
		Paths: []catalog.PathBinding{
			{RouteID: "R-S3", SpanID: "S3", Role: catalog.RolePrimary, SegmentIDs: []string{"SG-1"}},
			{RouteID: "R-S3-P", SpanID: "S3", Role: catalog.RoleProtection, SegmentIDs: []string{"SG-1"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	target, err := span.New("S3", "S3", 126, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	path, err := route.New("R-S3", "S3", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	drainer := route.NewDrainer(0)
	activator := route.NewActivator(registry)
	receipts := landing.NewReceiptRegistry()
	permitService := repair.NewPermitService(feed.NewIsolationService(), receipts, ids, clock.Now)
	circuit, err := feed.NewCircuit("B", "east", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	permitService.RegisterCircuit(circuit)
	repeaterAsset, err := repeater.New("R07", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := incident.NewRepeaterLifecycle()
	projector := repeater.NewHealthProjector(lifecycle, 85)
	projector.Register(repeaterAsset)
	repository := telemetry.NewRepository()
	acceptance := repair.NewAcceptanceService(switchover.NewRestorer(drainer, activator, registry, directory, ids, clock.Now), clock.Now)
	acceptance.Register("S3", target, path)
	services := &Services{
		Clock: clock, IDs: ids, Journal: store.NewJournal(filepath.Join(t.TempDir(), "events.jsonl")),
		Spans: map[string]*span.Span{"S3": target}, Routes: map[string]*route.Route{"R-S3": path},
		Repeaters: map[string]*repeater.Repeater{"R07": repeaterAsset}, Topology: registry, Catalog: directory,
		OTDR:        otdr.NewSessionService(directory, registry, landing.NewScanner(registry, 0), ids, clock.Now),
		Switchovers: switchover.NewSessionService(drainer, activator, directory, ids, clock.Now, time.Second),
		Permits:     permitService, Receipts: receipts, Acceptance: acceptance,
		Telemetry: telemetry.NewIngestor(repository, clock.Now, projector), TelemetryRepository: repository,
		Admission: incident.NewAdmissionService(telemetry.NewMuteManager(clock.Now)),
	}
	return NewServer(services)
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

func TestHealthAndScanHTTPFlow(t *testing.T) {
	server := testServer(t)
	health := httptest.NewRecorder()
	server.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", health.Code, health.Body.String())
	}

	body, _ := json.Marshal(ScanRequest{StationID: "east", LengthKM: 126, Chunks: 3})
	scan := httptest.NewRecorder()
	server.Handler().ServeHTTP(scan, httptest.NewRequest(http.MethodPost, "/api/spans/S3/otdr-scans", bytes.NewReader(body)))
	if scan.Code != http.StatusCreated {
		t.Fatalf("scan status=%d body=%s", scan.Code, scan.Body.String())
	}
	var trace otdr.Trace
	if err := json.Unmarshal(scan.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.SpanID != "S3" || len(trace.Chunks) != 3 {
		t.Fatalf("unexpected trace: %+v", trace)
	}
	if trace.CatalogGeneration != 1 || trace.StationID != "east" {
		t.Fatalf("trace must be pinned to catalog generation and endpoint station: %+v", trace)
	}

	status := httptest.NewRecorder()
	server.Handler().ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", status.Code, status.Body.String())
	}
}

func TestScanRejectsStationOutsideCatalog(t *testing.T) {
	server := testServer(t)
	body, _ := json.Marshal(ScanRequest{StationID: "north", LengthKM: 126, Chunks: 1})
	scan := httptest.NewRecorder()
	server.Handler().ServeHTTP(scan, httptest.NewRequest(http.MethodPost, "/api/spans/S3/otdr-scans", bytes.NewReader(body)))
	if scan.Code != http.StatusConflict {
		t.Fatalf("scan from non-endpoint station must be rejected, status=%d body=%s", scan.Code, scan.Body.String())
	}
}

func TestCatalogEndpointsAndRejections(t *testing.T) {
	server := testServer(t)

	current := httptest.NewRecorder()
	server.Handler().ServeHTTP(current, httptest.NewRequest(http.MethodGet, "/api/catalog", nil))
	if current.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", current.Code, current.Body.String())
	}
	var snapshot catalog.Snapshot
	if err := json.Unmarshal(current.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Generation != 1 || len(snapshot.Paths) != 2 {
		t.Fatalf("unexpected catalog snapshot: %+v", snapshot)
	}

	line := httptest.NewRecorder()
	server.Handler().ServeHTTP(line, httptest.NewRequest(http.MethodGet, "/api/catalog/lines/S3", nil))
	if line.Code != http.StatusOK {
		t.Fatalf("line status=%d body=%s", line.Code, line.Body.String())
	}

	missingEndpoint := CatalogConfigurationRequest{
		Generation: 2,
		Stations:   []catalog.Station{{ID: "east", Name: "East"}, {ID: "west", Name: "West"}},
		Segments:   []catalog.Segment{{ID: "SG-1", SpanID: "S3", From: "east", To: "", Sequence: 1, LengthKM: 126}},
		Paths: []catalog.PathBinding{
			{RouteID: "R-S3", SpanID: "S3", Role: catalog.RolePrimary, SegmentIDs: []string{"SG-1"}},
		},
	}
	body, _ := json.Marshal(missingEndpoint)
	rejected := httptest.NewRecorder()
	server.Handler().ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/api/catalog/configurations", bytes.NewReader(body)))
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing-endpoint config must be 422, got %d body=%s", rejected.Code, rejected.Body.String())
	}

	duplicate := CatalogConfigurationRequest{
		Generation: 1,
		Stations:   []catalog.Station{{ID: "east", Name: "East"}, {ID: "west", Name: "West"}},
		Segments:   []catalog.Segment{{ID: "SG-1", SpanID: "S3", From: "east", To: "west", Sequence: 1, LengthKM: 126}},
		Paths: []catalog.PathBinding{
			{RouteID: "R-S3", SpanID: "S3", Role: catalog.RolePrimary, SegmentIDs: []string{"SG-1"}},
			{RouteID: "R-S3-P", SpanID: "S3", Role: catalog.RoleProtection, SegmentIDs: []string{"SG-1"}},
		},
	}
	body, _ = json.Marshal(duplicate)
	conflict := httptest.NewRecorder()
	server.Handler().ServeHTTP(conflict, httptest.NewRequest(http.MethodPost, "/api/catalog/configurations", bytes.NewReader(body)))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("duplicate generation must be 409, got %d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestSwitchoverIsPinnedToCatalog(t *testing.T) {
	server := testServer(t)
	body, _ := json.Marshal(SwitchoverRequest{Reason: "fiber cut"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/routes/R-S3/switchovers", bytes.NewReader(body)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("switchover status=%d body=%s", response.Code, response.Body.String())
	}
	var operation switchover.Snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.CatalogGeneration != 1 || operation.SpanID != "S3" {
		t.Fatalf("switchover must record catalog lineage: %+v", operation)
	}
}

func TestIntegrityPageReflectsCatalog(t *testing.T) {
	server := testServer(t)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	var status SystemStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Catalog == nil || status.Catalog.Generation != 1 {
		t.Fatalf("integrity page must embed the catalog snapshot: %+v", status.Catalog)
	}
	for _, finding := range status.Integrity {
		if finding.Severity == "critical" {
			t.Fatalf("seeded topology must be catalog-complete, got critical finding: %+v", finding)
		}
	}
}
