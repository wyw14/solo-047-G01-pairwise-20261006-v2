package inventory

import (
	"fmt"
	"sort"

	"github.com/wyw14/cry-103/internal/topology"
)

type Finding struct {
	Code     string `json:"code"`
	AssetID  string `json:"asset_id"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Operands are the runtime observations the audit correlates against the
// physical catalog. They are plain values so the catalog package stays free of
// a dependency on the span/route runtime packages.
type Operands struct {
	Spans      []SpanOperand
	Routes     []RouteOperand
	Topologies []topology.Snapshot
}

type SpanOperand struct {
	ID       string
	LengthKM float64
	State    string
}

type RouteOperand struct {
	ID         string
	SpanID     string
	ActivePath topology.Path
	TrafficBPS int64
}

const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
)

// Audit is the single integrity engine behind the completeness page. It first
// proves the physical catalog is complete (sites, both cable paths, route
// declarations), then reconciles that physical truth with live cross-connect
// topology, routes and span state.
func Audit(catalog Snapshot, ops Operands) []Finding {
	var findings []Finding
	findings = append(findings, auditCatalog(catalog)...)

	topologyBySpan := make(map[string]topology.Snapshot, len(ops.Topologies))
	for _, item := range ops.Topologies {
		topologyBySpan[item.SpanID] = item
	}
	spanByID := make(map[string]SpanOperand, len(ops.Spans))
	for _, item := range ops.Spans {
		spanByID[item.ID] = item
	}

	declaredSpans := make(map[string]bool)
	for _, declaration := range catalog.RoutePaths {
		declaredSpans[declaration.SpanID] = true
	}
	segmentSpans := make(map[string]bool)
	primaryLength := make(map[string]float64)
	for _, segment := range catalog.Segments {
		segmentSpans[segment.SpanID] = true
		if segment.Path == "primary" {
			primaryLength[segment.SpanID] += segment.LengthKM
		}
	}

	for spanID := range declaredSpans {
		crossConnect, topologyExists := topologyBySpan[spanID]
		layoutRevision := catalog.SpanRevisions[spanID]
		if !topologyExists {
			findings = append(findings, Finding{
				Code: "span-without-topology", AssetID: spanID, Severity: SeverityCritical,
				Message: "declared span has no registered physical topology",
			})
			continue
		}
		if crossConnect.CatalogRevision != layoutRevision {
			findings = append(findings, Finding{
				Code: "topology-catalog-drift", AssetID: spanID, Severity: SeverityCritical,
				Message: fmt.Sprintf("topology pinned to catalog revision %d but span layout is at revision %d",
					crossConnect.CatalogRevision, layoutRevision),
			})
		}
		if cable, exists := spanByID[spanID]; exists {
			if total := primaryLength[spanID]; total > 0 && cable.LengthKM > 0 &&
				abs(cable.LengthKM-total) > lengthToleranceKM {
				findings = append(findings, Finding{
					Code: "segment-length-mismatch", AssetID: spanID, Severity: SeverityWarning,
					Message: fmt.Sprintf("operational span length %.2fkm disagrees with primary catalog segment total %.2fkm",
						cable.LengthKM, total),
				})
			}
		}
	}

	// Segments exist for a span that was never declared as a primary/protection line.
	for spanID := range segmentSpans {
		if !declaredSpans[spanID] {
			findings = append(findings, Finding{
				Code: "span-without-route-declaration", AssetID: spanID, Severity: SeverityWarning,
				Message: "span has cable segments but no primary/protection route declaration",
			})
		}
	}

	for _, path := range ops.Routes {
		if !declaredSpans[path.SpanID] && !segmentSpans[path.SpanID] {
			findings = append(findings, Finding{
				Code: "route-without-span", AssetID: path.ID, Severity: SeverityCritical,
				Message: fmt.Sprintf("route %s references span %s missing from the asset catalog", path.ID, path.SpanID),
			})
			continue
		}
		crossConnect, topologyExists := topologyBySpan[path.SpanID]
		cable, spanExists := spanByID[path.SpanID]
		if !spanExists {
			findings = append(findings, Finding{
				Code: "route-without-span", AssetID: path.ID, Severity: SeverityCritical,
				Message: fmt.Sprintf("route %s references missing span %s", path.ID, path.SpanID),
			})
			continue
		}
		if !topologyExists {
			findings = append(findings, Finding{
				Code: "route-without-topology", AssetID: path.ID, Severity: SeverityCritical,
				Message: fmt.Sprintf("route %s has no cross-connect topology", path.ID),
			})
			continue
		}
		if !crossConnect.PrimaryOpen && !crossConnect.ProtectOpen {
			findings = append(findings, Finding{
				Code: "dual-active-cross-connect", AssetID: path.ID, Severity: SeverityCritical,
				Message: "primary and protection cross-connects are both closed",
			})
		}
		if path.ActivePath != crossConnect.ActivePath {
			findings = append(findings, Finding{
				Code: "route-topology-disagreement", AssetID: path.ID, Severity: SeverityWarning,
				Message: fmt.Sprintf("route reports %s while topology reports %s", path.ActivePath, crossConnect.ActivePath),
			})
		}
		if cable.State == "recovered" && path.ActivePath != topology.PathPrimary {
			findings = append(findings, Finding{
				Code: "recovered-span-on-protection", AssetID: cable.ID, Severity: SeverityWarning,
				Message: "recovered span still carries traffic on the protection path",
			})
		}
		if path.TrafficBPS > 0 && ((path.ActivePath == topology.PathPrimary && crossConnect.PrimaryOpen) ||
			(path.ActivePath == topology.PathProtection && crossConnect.ProtectOpen)) {
			findings = append(findings, Finding{
				Code: "traffic-on-open-cross-connect", AssetID: path.ID, Severity: SeverityCritical,
				Message: fmt.Sprintf("route reports traffic on open %s cross-connect", path.ActivePath),
			})
		}
	}

	sortFindings(findings)
	return findings
}

const lengthToleranceKM = 0.5

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// auditCatalog proves the physical layout itself is complete, independent of
// any runtime state.
func auditCatalog(catalog Snapshot) []Finding {
	var findings []Finding
	sites := make(map[string]bool, len(catalog.Sites))
	referencedSites := make(map[string]bool)
	for _, site := range catalog.Sites {
		sites[site.ID] = true
	}

	bySpan := make(map[string][]CableSegment)
	for _, segment := range catalog.Segments {
		if _, ok := sites[segment.FromSite]; !ok {
			findings = append(findings, Finding{
				Code: "segment-unknown-site", AssetID: segment.ID, Severity: SeverityCritical,
				Message: fmt.Sprintf("segment %s endpoint site %s is not registered", segment.ID, segment.FromSite),
			})
		}
		if _, ok := sites[segment.ToSite]; !ok {
			findings = append(findings, Finding{
				Code: "segment-unknown-site", AssetID: segment.ID, Severity: SeverityCritical,
				Message: fmt.Sprintf("segment %s endpoint site %s is not registered", segment.ID, segment.ToSite),
			})
		}
		referencedSites[segment.FromSite] = true
		referencedSites[segment.ToSite] = true
		bySpan[segment.SpanID] = append(bySpan[segment.SpanID], segment)
	}

	declarationBySpan := make(map[string]RoutePath)
	for _, declaration := range catalog.RoutePaths {
		declarationBySpan[declaration.SpanID] = declaration
		for _, siteID := range []string{
			declaration.PrimarySiteA, declaration.PrimarySiteZ,
			declaration.ProtectionSiteA, declaration.ProtectionSiteZ,
		} {
			referencedSites[siteID] = true
		}
	}

	// Every span that appears (segments or a declaration) must carry both paths.
	spanIDs := make(map[string]bool)
	for spanID := range bySpan {
		spanIDs[spanID] = true
	}
	for spanID := range declarationBySpan {
		spanIDs[spanID] = true
	}
	for spanID := range spanIDs {
		paths := make(map[string]bool)
		for _, segment := range bySpan[spanID] {
			paths[segment.Path] = true
		}
		if !paths["primary"] {
			findings = append(findings, Finding{
				Code: "span-missing-primary-cable", AssetID: spanID, Severity: SeverityCritical,
				Message: fmt.Sprintf("span %s physical topology is incomplete: primary cable segment missing", spanID),
			})
		}
		if !paths["protection"] {
			findings = append(findings, Finding{
				Code: "span-missing-protection-cable", AssetID: spanID, Severity: SeverityCritical,
				Message: fmt.Sprintf("span %s physical topology is incomplete: protection cable segment missing", spanID),
			})
		}
		if declaration, ok := declarationBySpan[spanID]; ok {
			findings = append(findings, auditDeclaration(declaration, bySpan[spanID])...)
		}
	}

	for _, site := range catalog.Sites {
		if !referencedSites[site.ID] {
			findings = append(findings, Finding{
				Code: "unused-site", AssetID: site.ID, Severity: SeverityWarning,
				Message: fmt.Sprintf("site %s is registered but used by no segment or route", site.ID),
			})
		}
	}

	sortFindings(findings)
	return findings
}

func auditDeclaration(declaration RoutePath, segments []CableSegment) []Finding {
	var findings []Finding
	primary := make(map[string]CableSegment)
	protection := make(map[string]CableSegment)
	for _, segment := range segments {
		switch segment.Path {
		case "primary":
			primary[segment.ID] = segment
		case "protection":
			protection[segment.ID] = segment
		}
	}
	if err := checkPathContiguousLocked(declaration.SpanID, "primary",
		declaration.PrimarySiteA, declaration.PrimarySiteZ, primary); err != nil {
		findings = append(findings, Finding{
			Code: "primary-path-disconnected", AssetID: declaration.SpanID, Severity: SeverityCritical,
			Message: err.Error(),
		})
	}
	if err := checkPathContiguousLocked(declaration.SpanID, "protection",
		declaration.ProtectionSiteA, declaration.ProtectionSiteZ, protection); err != nil {
		findings = append(findings, Finding{
			Code: "protection-path-disconnected", AssetID: declaration.SpanID, Severity: SeverityCritical,
			Message: err.Error(),
		})
	}
	return findings
}

func sortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return findings[i].Severity < findings[j].Severity
		}
		if findings[i].AssetID != findings[j].AssetID {
			return findings[i].AssetID < findings[j].AssetID
		}
		return findings[i].Code < findings[j].Code
	})
}
