//go:build integration

package integration_test

import (
	"net/http"
	"testing"

	"github.com/block/spectre/internal/integration/it"
	"github.com/block/spectre/internal/model"
)

func TestUnmatchedCandidateEgressQuarantinesCandidate(t *testing.T) {
	m := it.Start(t,
		it.Scripts(`
			import { egress } from "spectre";
			import type { FetchForecastRequest } from "forecasts";

			egress.http("GET forecasts.example/v1/forecasts/{location}").match<FetchForecastRequest>();
		`),
		it.Reference(sampleService()),
		it.Candidate(sampleService()),
		it.Dependency(forecastsHost, forecastProvider),
	)

	// A dependency call the reference never made gets a 502 without reaching the provider.
	m.CandidateEgress.Get(forecastsHost, "/v1/forecasts/paris").ExpectStatus(http.StatusBadGateway)
	m.ExpectQuarantine(it.UnmatchedEgress("GET forecasts.example/v1/forecasts/{location}", "no matching reference request within 500ms"))
	m.CandidateEgress.Get(forecastsHost, "/v1/forecasts/london").ExpectStatus(http.StatusServiceUnavailable)
	m.Dependency(forecastsHost).ExpectReceived("/v1/forecasts/paris", 0)
}

func TestCandidateEgressDifferingFromReferenceReportsDifferences(t *testing.T) {
	m := it.Start(t,
		it.Scripts(`
			import { egress } from "spectre";
			import type { FetchForecastRequest } from "forecasts";

			egress.http("GET forecasts.example/v1/forecasts/{location}").match<FetchForecastRequest>();
		`),
		it.Reference(sampleService()),
		it.Candidate(sampleService()),
		it.Dependency(forecastsHost, forecastProvider),
	)

	m.ReferenceEgress.Get(forecastsHost, "/v1/forecasts/london").ExpectStatus(http.StatusOK)
	// The only reference call of the request on the route differs, so the report names the path.
	m.CandidateEgress.Get(forecastsHost, "/v1/forecasts/paris").ExpectStatus(http.StatusBadGateway)
	m.ExpectQuarantine(it.DivergentEgress("GET forecasts.example/v1/forecasts/{location}",
		model.Difference{Path: "$.location", Change: model.ChangeChanged, Count: 1}))
	m.Dependency(forecastsHost).ExpectReceived("/v1/forecasts/paris", 0)
}

func TestCandidateEgressOutsideMirroredRequestQuarantinesCandidate(t *testing.T) {
	m := it.Start(t,
		it.Scripts(`
			import { egress } from "spectre";
			import type { FetchForecastRequest } from "forecasts";

			egress.http("GET forecasts.example/v1/forecasts/{location}").match<FetchForecastRequest>();
		`),
		it.Reference(sampleService()),
		it.Candidate(sampleService()),
		it.Dependency(forecastsHost, forecastProvider),
	)

	// The reference's call belongs to no mirrored request, so no candidate call is expected for it.
	m.ReferenceEgress.GetUnmirrored(forecastsHost, "/v1/forecasts/london").ExpectStatus(http.StatusOK)
	m.CandidateEgress.GetUnmirrored(forecastsHost, "/v1/forecasts/london").ExpectStatus(http.StatusBadGateway)
	m.ExpectQuarantine(it.UnpairedEgress("GET forecasts.example/v1/forecasts/{location}", "candidate request carries no SPECTRE request ID"))
	m.Dependency(forecastsHost).ExpectReceived("/v1/forecasts/london", 1)
}

func TestCandidateLeavingOutEgressCallIsQuarantined(t *testing.T) {
	m := it.Start(t,
		it.Scripts(`
			import { egress, ingress } from "spectre";
			import type { FetchForecastRequest } from "forecasts";
			import type { GetForecastV2Response } from "weather";

			ingress.http("GET /v2/forecast").match<GetForecastV2Response>();
			egress.http("GET forecasts.example/v1/forecasts/{location}").match<FetchForecastRequest>();
		`),
		it.Reference(sampleService()),
		it.Candidate(sampleService(ownForecasts())),
		it.Dependency(forecastsHost, forecastProvider),
	)

	m.Ingress.Get("/v2/forecast?location=london").ExpectStatus(http.StatusOK)
	m.ExpectQuarantine(it.UnmatchedEgress("GET forecasts.example/v1/forecasts/{location}", "no matching candidate request within 500ms"))
	m.Dependency(forecastsHost).ExpectReceived("/v1/forecasts/london", 1)
}

func TestCandidateMayLeaveOutOptionalEgressCalls(t *testing.T) {
	m := it.Start(t,
		it.Scripts(`
			import { egress, ingress } from "spectre";
			import type { FetchForecastRequest } from "forecasts";
			import type { GetForecastV2Response } from "weather";

			ingress.http("GET /v2/forecast").match<GetForecastV2Response>();
			egress.http("GET forecasts.example/v1/forecasts/{location}").optional().match<FetchForecastRequest>();
			egress.http("GET required.example/v1/forecasts/{location}").match<FetchForecastRequest>();
		`),
		it.Reference(sampleService()),
		it.Candidate(sampleService(ownForecasts())),
		it.Dependency(forecastsHost, forecastProvider),
		it.Dependency("required.example", forecastProvider),
	)

	// The reference fetches london's forecast, which the candidate reads from its own data.
	m.Ingress.Get("/v2/forecast?location=london").ExpectStatus(http.StatusOK)
	m.ExpectCalls(it.Calls{"GET /v2/forecast": 1})
	m.Dependency(forecastsHost).ExpectReceived("/v1/forecasts/london", 1)

	// A required fetch the candidate leaves out quarantines it. Its window ends after
	// the optional fetch's, and only the first reason is kept, so the optional one never quarantined.
	m.ReferenceEgress.Get("required.example", "/v1/forecasts/london").ExpectStatus(http.StatusOK)
	m.Dependency("required.example").ExpectReceived("/v1/forecasts/london", 1)
	m.ExpectQuarantine(it.UnmatchedEgress("GET required.example/v1/forecasts/{location}", "no matching candidate request within 500ms"))
}

func TestUnmatchedCandidateCallToOptionalEndpointQuarantines(t *testing.T) {
	m := it.Start(t,
		it.Scripts(`
			import { egress } from "spectre";
			import type { FetchForecastRequest } from "forecasts";

			egress.http("GET forecasts.example/v1/forecasts/{location}").optional().match<FetchForecastRequest>();
		`),
		it.Reference(sampleService()),
		it.Candidate(sampleService()),
		it.Dependency(forecastsHost, forecastProvider),
	)

	// Optional only lets the candidate make fewer calls, not calls the reference never made.
	m.CandidateEgress.Get(forecastsHost, "/v1/forecasts/paris").ExpectStatus(http.StatusBadGateway)
	m.ExpectQuarantine(it.UnmatchedEgress("GET forecasts.example/v1/forecasts/{location}", "no matching reference request within 500ms"))
	m.Dependency(forecastsHost).ExpectReceived("/v1/forecasts/paris", 0)
}
