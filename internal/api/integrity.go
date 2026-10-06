package api

import (
	"github.com/wyw14/cry-103/internal/inventory"
	"github.com/wyw14/cry-103/internal/route"
	"github.com/wyw14/cry-103/internal/span"
	"github.com/wyw14/cry-103/internal/topology"
)

// IntegrityFinding is kept for backwards-compatible JSON on the system status
// payload; its shape mirrors inventory.Finding.
type IntegrityFinding = inventory.Finding

// auditIntegrity is the API-layer projection onto the single inventory audit
// engine. Switchovers, OTDR scans and this view all derive truth from the same
// catalog revision.
func auditIntegrity(catalog inventory.Snapshot, spans []span.Snapshot, routes []route.Snapshot, topologies []topology.Snapshot) []IntegrityFinding {
	spanOperands := make([]inventory.SpanOperand, 0, len(spans))
	for _, item := range spans {
		spanOperands = append(spanOperands, inventory.SpanOperand{
			ID: item.ID, LengthKM: item.LengthKM, State: string(item.State),
		})
	}
	routeOperands := make([]inventory.RouteOperand, 0, len(routes))
	for _, item := range routes {
		routeOperands = append(routeOperands, inventory.RouteOperand{
			ID: item.ID, SpanID: item.SpanID, ActivePath: item.ActivePath, TrafficBPS: item.TrafficBPS,
		})
	}
	return inventory.Audit(catalog, inventory.Operands{
		Spans: spanOperands, Routes: routeOperands, Topologies: topologies,
	})
}
