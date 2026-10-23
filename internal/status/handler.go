// Package status serves the SPECTRE status endpoint, reporting whether the
// candidate is quarantined and why, aggregated across the proxies.
package status

import (
	"encoding/json"
	"net/http"
	"time"

	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/proxy"
)

// Path is the HTTP path that serves the SPECTRE status report.
const Path = "/spectre/v1/status"

// Reporter reports one source's candidate quarantine state.
type Reporter interface {
	QuarantineStatus() proxy.QuarantineStatus
}

// Source names a quarantine Reporter, e.g. the ingress or egress proxy.
type Source struct {
	Name     string
	Reporter Reporter
}

// Report is the JSON body served at Path.
type Report struct {
	// Quarantined is true when any source has quarantined the candidate.
	Quarantined bool `json:"quarantined"`
	// HealthyForSeconds is how long the candidate has been healthy, measured on
	// the mirror's clock. It is null while quarantined.
	HealthyForSeconds Option[int64] `json:"healthy_for_seconds"`
	// Sources is the per-source quarantine state.
	Sources []SourceStatus `json:"sources"`
}

// SourceStatus is one source's quarantine state in a Report.
type SourceStatus struct {
	Name        string `json:"name"`
	Quarantined bool   `json:"quarantined"`
	Reason      string `json:"reason,omitempty"`
	// Since is when quarantine began, as RFC 3339, empty while healthy.
	Since string `json:"since,omitempty"`
}

// Handler serves the aggregated SPECTRE status at Path as JSON.
type Handler struct {
	started time.Time
	sources []Source
}

// NewHandler serves the quarantine state of sources at Path. Quarantine lasts
// for the process, so a healthy candidate has been healthy since started.
func NewHandler(started time.Time, sources ...Source) *Handler {
	return &Handler{started: started, sources: sources}
}

// ServeHTTP serves the status report at Path and 404s every other path.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != Path {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := json.Marshal(h.report())
	if err != nil {
		http.Error(writer, "encode status", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(body) //nolint:errcheck // Headers are sent, so a failed write cannot be reported.
}

// report aggregates every source's quarantine state.
func (h *Handler) report() Report {
	report := Report{Sources: make([]SourceStatus, 0, len(h.sources))}
	for _, source := range h.sources {
		status := sourceStatus(source.Name, source.Reporter.QuarantineStatus())
		if status.Quarantined {
			report.Quarantined = true
		}
		report.Sources = append(report.Sources, status)
	}
	if !report.Quarantined {
		report.HealthyForSeconds = Some(int64(time.Since(h.started) / time.Second))
	}
	return report
}

// sourceStatus renders one source's quarantine state for the report.
func sourceStatus(name string, quarantine proxy.QuarantineStatus) SourceStatus {
	status := SourceStatus{Name: name}
	if quarantine.Reason != nil {
		status.Quarantined = true
		status.Reason = quarantine.Reason.Error()
	}
	if !quarantine.Since.IsZero() {
		status.Since = quarantine.Since.UTC().Format(time.RFC3339)
	}
	return status
}
