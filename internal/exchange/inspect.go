package exchange

import (
	"encoding/json"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

const (
	ServiceStatusPresent     = "present"
	ServiceStatusMissing     = "missing_from_service"
	ServiceStatusChanged     = "changed_in_service"
	MissingStatusPackage     = "missing_in_package"
	MissingStatusStillAbsent = "missing_in_package_and_service"
	MissingStatusResolved    = "missing_in_package_but_present_in_service"
)

type MemberStatus struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	Version       int    `json:"version,omitempty"`
	PackageDigest string `json:"package_digest"`
	ServiceStatus string `json:"service_status"`
	CurrentDigest string `json:"current_digest,omitempty"`
}

type MissingStatusItem struct {
	MissingReference
	Status string `json:"status"`
}

type Inspection struct {
	Valid                  bool                `json:"valid"`
	Format                 string              `json:"format"`
	PackageID              string              `json:"package_id"`
	Status                 string              `json:"status"`
	Complete               bool                `json:"complete"`
	ContentDigest          string              `json:"content_digest"`
	Issues                 []string            `json:"issues"`
	Members                []MemberStatus      `json:"members"`
	MissingReferences      []MissingStatusItem `json:"missing_references"`
	MissingInPackage       int                 `json:"missing_in_package"`
	MissingFromServiceNow  int                 `json:"missing_from_service_now"`
	PresentInService       int                 `json:"present_in_service"`
	ChangedInService       int                 `json:"changed_in_service"`
	ServiceClosureComplete bool                `json:"service_closure_complete"`
	PackageClosureComplete bool                `json:"package_closure_complete"`
}

func currentRevisionDigest(history []geology.Revision, version int) (string, bool) {
	if !revisionExists(history, version) {
		return "", false
	}
	digest, err := digestValue(history[version-1])
	if err != nil {
		return "", false
	}
	return digest, true
}

// Inspect validates the package independently, then compares its immutable
// members with the current service snapshot. Current extra revisions are
// ignored: they do not mutate content already exported.
func Inspect(e Envelope, data Dataset) Inspection {
	valid, issues := Validate(e)
	inspection := Inspection{
		Valid: valid, Format: e.Format, PackageID: e.PackageID, Status: e.Status, Complete: e.Complete,
		ContentDigest: e.ContentDigest, Issues: nonNilIssues(issues), Members: []MemberStatus{}, MissingReferences: []MissingStatusItem{},
	}

	for _, member := range e.Manifest.Members {
		if member.Kind == KindProfileHistory {
			history, exists := data.Histories[member.ID]
			containerStatus := ServiceStatusMissing
			containerCurrent := ""
			if exists {
				if digest, err := digestValue(history); err == nil {
					containerCurrent = digest
					if digest == member.ContentDigest {
						containerStatus = ServiceStatusPresent
					} else {
						containerStatus = ServiceStatusChanged
					}
				}
			}

			var summary HistorySummary
			if err := json.Unmarshal(member.Summary, &summary); err == nil {
				for _, revisionSummary := range summary.Revisions {
					item := MemberStatus{Kind: KindProfileRevision, ID: member.ID, Version: revisionSummary.Version, PackageDigest: revisionSummary.Digest, ServiceStatus: ServiceStatusMissing}
					if exists {
						if current, ok := currentRevisionDigest(history, revisionSummary.Version); ok {
							item.CurrentDigest = current
							if current == revisionSummary.Digest {
								item.ServiceStatus = ServiceStatusPresent
							} else {
								item.ServiceStatus = ServiceStatusChanged
							}
						}
					}
					countServiceStatus(&inspection, item.ServiceStatus)
					inspection.Members = append(inspection.Members, item)
				}
			}
			countServiceStatus(&inspection, containerStatus)
			inspection.Members = append(inspection.Members, MemberStatus{Kind: KindProfileHistory, ID: member.ID, PackageDigest: member.ContentDigest, ServiceStatus: containerStatus, CurrentDigest: containerCurrent})
			continue
		}
		if member.Kind == KindComparison {
			result, exists := data.Comparisons[member.ID]
			item := MemberStatus{Kind: KindComparison, ID: member.ID, PackageDigest: member.ContentDigest, ServiceStatus: ServiceStatusMissing}
			if exists {
				if digest, err := digestValue(result); err == nil {
					item.CurrentDigest = digest
					if digest == member.ContentDigest {
						item.ServiceStatus = ServiceStatusPresent
					} else {
						item.ServiceStatus = ServiceStatusChanged
					}
				}
			}
			countServiceStatus(&inspection, item.ServiceStatus)
			inspection.Members = append(inspection.Members, item)
		}
	}

	for _, missing := range e.Manifest.MissingReferences {
		item := MissingStatusItem{MissingReference: missing, Status: MissingStatusStillAbsent}
		if referenceNowPresent(missing, data) {
			item.Status = MissingStatusResolved
		} else {
			inspection.MissingFromServiceNow++
		}
		inspection.MissingInPackage++
		inspection.MissingReferences = append(inspection.MissingReferences, item)
	}
	inspection.ServiceClosureComplete = inspection.MissingFromServiceNow == 0
	inspection.PackageClosureComplete = inspection.MissingInPackage == 0
	return inspection
}

func nonNilIssues(issues []string) []string {
	if issues == nil {
		return []string{}
	}
	return issues
}

func countServiceStatus(i *Inspection, status string) {
	switch status {
	case ServiceStatusPresent:
		i.PresentInService++
	case ServiceStatusChanged:
		i.ChangedInService++
	case ServiceStatusMissing:
		i.MissingFromServiceNow++
	}
}

func referenceNowPresent(m MissingReference, data Dataset) bool {
	switch m.Kind {
	case KindProfileHistory:
		_, ok := data.Histories[m.ID]
		return ok
	case KindProfileRevision:
		history, ok := data.Histories[m.ID]
		return ok && revisionExists(history, m.Version)
	case KindComparison:
		_, ok := data.Comparisons[m.ID]
		return ok
	default:
		return false
	}
}
