package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/cry-103/internal/inventory"
)

type catalogSiteRequest struct {
	Name             string `json:"name"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

type catalogSegmentRequest struct {
	SpanID           string  `json:"span_id"`
	Path             string  `json:"path"`
	FromSite         string  `json:"from_site"`
	ToSite           string  `json:"to_site"`
	LengthKM         float64 `json:"length_km"`
	ExpectedRevision uint64  `json:"expected_revision"`
}

type catalogRoutePathRequest struct {
	SpanID           string `json:"span_id"`
	RouteID          string `json:"route_id"`
	PrimarySiteA     string `json:"primary_site_a"`
	PrimarySiteZ     string `json:"primary_site_z"`
	ProtectionSiteA  string `json:"protection_site_a"`
	ProtectionSiteZ  string `json:"protection_site_z"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

// writeOperationalError maps catalog validation failures to 422 while keeping
// genuine runtime conflicts (scan in flight, cross-connect break-before-make,
// stale revision) at 409.
func writeOperationalError(writer http.ResponseWriter, err error) {
	var validation *inventory.ValidationError
	if errors.As(err, &validation) {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeError(writer, http.StatusConflict, err)
}

// writeCatalogError maps catalog validation failures to 422 and optimistic
// concurrency conflicts to 409.
func writeCatalogError(writer http.ResponseWriter, err error) {
	var validation *inventory.ValidationError
	if errors.As(err, &validation) {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if errors.Is(err, inventory.ErrStale) {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeError(writer, http.StatusBadRequest, err)
}

// persistCatalog records the new catalog revision in the journal for
// traceability. A physical layout change does not silently re-pin a live line:
// integrity flags the drift and an explicit span re-sync acknowledges the new
// physical reality before switchovers/scans may resume.
func (s *Server) persistCatalog(snapshot inventory.Snapshot) {
	_ = s.services.Persist("inventory-catalog", "catalog", snapshot)
}

func (s *Server) resyncSpan(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	topologySnapshot, synced, err := s.services.InventoryGuard.Sync(spanID)
	if err != nil {
		writeOperationalError(writer, err)
		return
	}
	if !synced {
		writeError(writer, http.StatusConflict, fmt.Errorf("span %s has no activated topology to re-sync", spanID))
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"span_id": spanID, "catalog_revision": topologySnapshot.CatalogRevision,
		"topology_generation": topologySnapshot.Generation, "active_path": topologySnapshot.ActivePath,
	})
}

func (s *Server) getInventory(writer http.ResponseWriter, _ *http.Request) {
	view := s.services.InventoryView()
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) getSpanIntegrity(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	catalog := s.services.Catalog.Snapshot()
	findings := inventory.Audit(catalog, s.services.inventoryOperands())
	related := map[string]bool{spanID: true}
	for _, declaration := range catalog.RoutePaths {
		if declaration.SpanID == spanID {
			related[declaration.RouteID] = true
		}
	}
	s.services.mu.RLock()
	for _, target := range s.services.Routes {
		if target.Snapshot().SpanID == spanID {
			related[target.Snapshot().ID] = true
		}
	}
	s.services.mu.RUnlock()
	filtered := make([]inventory.Finding, 0, len(findings))
	for _, finding := range findings {
		if related[finding.AssetID] {
			filtered = append(filtered, finding)
		}
	}
	response := map[string]any{
		"span_id":          spanID,
		"catalog_revision": catalog.Revision,
		"layout_revision":  catalog.SpanRevisions[spanID],
		"pinned_revision":  uint64(0),
		"cable_segments":   s.services.Catalog.SegmentsFor(spanID),
		"integrity":        filtered,
	}
	if topologySnapshot, err := s.services.Topology.Snapshot(spanID); err == nil {
		response["pinned_revision"] = topologySnapshot.CatalogRevision
		response["topology_generation"] = topologySnapshot.Generation
		response["active_path"] = topologySnapshot.ActivePath
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) putSite(writer http.ResponseWriter, request *http.Request) {
	siteID := chi.URLParam(request, "id")
	var input catalogSiteRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	site, snapshot, err := s.services.Catalog.RegisterSite(
		inventory.Site{ID: siteID, Name: input.Name}, input.ExpectedRevision)
	if err != nil {
		writeCatalogError(writer, err)
		return
	}
	s.persistCatalog(snapshot)
	writeJSON(writer, http.StatusOK, map[string]any{"site": site, "catalog_revision": snapshot.Revision})
}

func (s *Server) putSegment(writer http.ResponseWriter, request *http.Request) {
	segmentID := chi.URLParam(request, "id")
	var input catalogSegmentRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	segment, snapshot, err := s.services.Catalog.RegisterSegment(inventory.CableSegment{
		ID: segmentID, SpanID: input.SpanID, Path: input.Path,
		FromSite: input.FromSite, ToSite: input.ToSite, LengthKM: input.LengthKM,
	}, input.ExpectedRevision)
	if err != nil {
		writeCatalogError(writer, err)
		return
	}
	s.persistCatalog(snapshot)
	writeJSON(writer, http.StatusOK, map[string]any{"cable_segment": segment, "catalog_revision": snapshot.Revision})
}

func (s *Server) putRoutePath(writer http.ResponseWriter, request *http.Request) {
	declarationID := chi.URLParam(request, "id")
	var input catalogRoutePathRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	declaration, snapshot, err := s.services.Catalog.DeclareRoutePath(inventory.RoutePath{
		ID: declarationID, SpanID: input.SpanID, RouteID: input.RouteID,
		PrimarySiteA: input.PrimarySiteA, PrimarySiteZ: input.PrimarySiteZ,
		ProtectionSiteA: input.ProtectionSiteA, ProtectionSiteZ: input.ProtectionSiteZ,
	}, input.ExpectedRevision)
	if err != nil {
		writeCatalogError(writer, err)
		return
	}
	s.persistCatalog(snapshot)
	writeJSON(writer, http.StatusOK, map[string]any{"route_path": declaration, "catalog_revision": snapshot.Revision})
}
