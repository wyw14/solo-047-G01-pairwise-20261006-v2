package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func do(t *testing.T, server *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(method, path, reader))
	return recorder
}

func TestInventoryRejectsSegmentWithoutEndpoints(t *testing.T) {
	server := testServer(t)
	response := do(t, server, http.MethodPut, "/api/inventory/cable-segments/BAD", map[string]any{
		"span_id": "S9", "path": "primary", "from_site": "east", "to_site": "", "length_km": 10,
	})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing endpoint status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestInventoryRejectsDuplicateGeneration(t *testing.T) {
	server := testServer(t)
	// The seeded catalog already carries a primary segment SEG-S3-P for S3.
	response := do(t, server, http.MethodPut, "/api/inventory/cable-segments/DUP", map[string]any{
		"span_id": "S3", "path": "primary", "from_site": "east", "to_site": "west", "length_km": 126,
	})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate generation status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestInventoryStaleRevisionConflicts(t *testing.T) {
	server := testServer(t)
	ok := do(t, server, http.MethodPut, "/api/inventory/sites/east", map[string]any{"name": "East"})
	if ok.Code != http.StatusOK {
		t.Fatalf("site upsert status=%d body=%s", ok.Code, ok.Body.String())
	}
	stale := do(t, server, http.MethodPut, "/api/inventory/sites/west", map[string]any{
		"name": "West", "expected_revision": 999,
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale revision status=%d body=%s", stale.Code, stale.Body.String())
	}
}

func TestOTDRScanRejectsNonCatalogStation(t *testing.T) {
	server := testServer(t)
	bad := do(t, server, http.MethodPost, "/api/spans/S3/otdr-scans", ScanRequest{
		StationID: "north", LengthKM: 126, Chunks: 2,
	})
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("non-endpoint station status=%d body=%s", bad.Code, bad.Body.String())
	}

	good := do(t, server, http.MethodPost, "/api/spans/S3/otdr-scans", ScanRequest{
		StationID: "east", LengthKM: 126, Chunks: 2,
	})
	if good.Code != http.StatusCreated {
		t.Fatalf("valid scan status=%d body=%s", good.Code, good.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(good.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if revision, _ := payload["catalog_revision"].(float64); revision < 1 {
		t.Fatalf("trace must pin a catalog revision, got %v", payload["catalog_revision"])
	}
}

func TestSwitchoverPinsCatalogRevision(t *testing.T) {
	server := testServer(t)
	response := do(t, server, http.MethodPost, "/api/routes/R-S3/switchovers", SwitchoverRequest{Reason: "cut"})
	if response.Code != http.StatusAccepted {
		t.Fatalf("switchover status=%d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if revision, _ := payload["catalog_revision"].(float64); revision < 1 {
		t.Fatalf("switchover must record catalog revision, got %v", payload["catalog_revision"])
	}
	if generation, _ := payload["topology_generation"].(float64); generation < 1 {
		t.Fatalf("switchover must record topology generation, got %v", payload["topology_generation"])
	}
}

func TestCatalogDriftBlocksSwitchoverUntilResync(t *testing.T) {
	server := testServer(t)

	// An unrelated site edit must not invalidate the S3 pin.
	site := do(t, server, http.MethodPut, "/api/inventory/sites/north", map[string]any{"name": "North"})
	if site.Code != http.StatusOK {
		t.Fatalf("unrelated site status=%d body=%s", site.Code, site.Body.String())
	}
	scan := do(t, server, http.MethodPost, "/api/spans/S3/otdr-scans", ScanRequest{
		StationID: "east", LengthKM: 126, Chunks: 1,
	})
	if scan.Code != http.StatusCreated {
		t.Fatalf("unrelated catalog edit must not drift S3, status=%d body=%s", scan.Code, scan.Body.String())
	}

	// Re-cable the primary segment (same id, valid complete path) advances the
	// S3 layout revision and must block operations until an explicit re-sync.
	recable := do(t, server, http.MethodPut, "/api/inventory/cable-segments/SEG-S3-P", map[string]any{
		"span_id": "S3", "path": "primary", "from_site": "east", "to_site": "west", "length_km": 110,
	})
	if recable.Code != http.StatusOK {
		t.Fatalf("recabling status=%d body=%s", recable.Code, recable.Body.String())
	}
	blocked := do(t, server, http.MethodPost, "/api/routes/R-S3/switchovers", SwitchoverRequest{Reason: "cut"})
	if blocked.Code != http.StatusConflict {
		t.Fatalf("switchover on drifted line status=%d body=%s", blocked.Code, blocked.Body.String())
	}

	integrity := do(t, server, http.MethodGet, "/api/spans/S3/integrity", nil)
	var view map[string]any
	if err := json.Unmarshal(integrity.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view["pinned_revision"].(float64) == view["layout_revision"].(float64) {
		t.Fatal("integrity page must show the drifted pin before re-sync")
	}

	resync := do(t, server, http.MethodPost, "/api/spans/S3/resync", nil)
	if resync.Code != http.StatusOK {
		t.Fatalf("resync status=%d body=%s", resync.Code, resync.Body.String())
	}
	switched := do(t, server, http.MethodPost, "/api/routes/R-S3/switchovers", SwitchoverRequest{Reason: "cut"})
	if switched.Code != http.StatusAccepted {
		t.Fatalf("switchover after resync status=%d body=%s", switched.Code, switched.Body.String())
	}
}

func TestInventoryAndSpanIntegrityShareCatalog(t *testing.T) {
	server := testServer(t)
	inventory := do(t, server, http.MethodGet, "/api/inventory", nil)
	if inventory.Code != http.StatusOK {
		t.Fatalf("inventory status=%d body=%s", inventory.Code, inventory.Body.String())
	}
	var catalog map[string]any
	if err := json.Unmarshal(inventory.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog["revision"].(float64) < 1 {
		t.Fatal("inventory page must expose the catalog revision")
	}
	segments, _ := catalog["cable_segments"].([]any)
	if len(segments) != 2 {
		t.Fatalf("expected primary+protection segments, got %d", len(segments))
	}

	integrity := do(t, server, http.MethodGet, "/api/spans/S3/integrity", nil)
	if integrity.Code != http.StatusOK {
		t.Fatalf("span integrity status=%d body=%s", integrity.Code, integrity.Body.String())
	}
	var view map[string]any
	if err := json.Unmarshal(integrity.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view["pinned_revision"].(float64) != catalog["revision"].(float64) {
		t.Fatalf("integrity page pinned revision %v must equal catalog revision %v",
			view["pinned_revision"], catalog["revision"])
	}
	spanSegments, _ := view["cable_segments"].([]any)
	if len(spanSegments) != 2 {
		t.Fatalf("expected both cable segments on the span integrity page, got %d", len(spanSegments))
	}
}
