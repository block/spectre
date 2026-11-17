//go:build integration

package integration_test

import (
	"net/http"
	"testing"
)

func TestUnmatchedCandidateEgressQuarantinesCandidate(t *testing.T) {
	m := startMirror(t,
		scripts(`
			import { egress } from "spectre";
			import type { FetchForecastRequest } from "forecasts";

			egress.http("GET forecasts.example/v1/forecasts/{location}").match<FetchForecastRequest>();
		`),
		reference(sampleService()),
		candidate(sampleService()),
		dependency(forecastsHost, forecastProvider),
	)

	// A dependency call the reference never made gets a 502 without reaching the provider.
	m.candidateEgress.get(forecastsHost, "/v1/forecasts/paris").expectStatus(http.StatusBadGateway)
	m.expectQuarantine(unmatchedEgress("GET forecasts.example/v1/forecasts/{location}", "no matching reference request within 500ms"))
	m.candidateEgress.get(forecastsHost, "/v1/forecasts/london").expectStatus(http.StatusServiceUnavailable)
	m.dependency(forecastsHost).expectReceived("/v1/forecasts/paris", 0)
}

func TestCandidateLeavingOutEgressCallIsQuarantined(t *testing.T) {
	m := startMirror(t,
		scripts(`
			import { egress, ingress } from "spectre";
			import type { FetchForecastRequest } from "forecasts";
			import type { GetForecastV2Response } from "weather";

			ingress.http("GET /v2/forecast").match<GetForecastV2Response>();
			egress.http("GET forecasts.example/v1/forecasts/{location}").match<FetchForecastRequest>();
		`),
		reference(sampleService()),
		candidate(sampleService(ownForecasts())),
		dependency(forecastsHost, forecastProvider),
	)

	m.ingress.get("/v2/forecast?location=london").expectStatus(http.StatusOK)
	m.expectQuarantine(unmatchedEgress("GET forecasts.example/v1/forecasts/{location}", "no matching candidate request within 500ms"))
	m.dependency(forecastsHost).expectReceived("/v1/forecasts/london", 1)
}

func TestCandidateMayLeaveOutOptionalEgressCalls(t *testing.T) {
	m := startMirror(t,
		scripts(`
			import { egress, ingress } from "spectre";
			import type { FetchForecastRequest } from "forecasts";
			import type { GetForecastV2Response } from "weather";

			ingress.http("GET /v2/forecast").match<GetForecastV2Response>();
			egress.http("GET forecasts.example/v1/forecasts/{location}").optional().match<FetchForecastRequest>();
			egress.http("GET required.example/v1/forecasts/{location}").match<FetchForecastRequest>();
		`),
		reference(sampleService()),
		candidate(sampleService(ownForecasts())),
		dependency(forecastsHost, forecastProvider),
		dependency("required.example", forecastProvider),
	)

	// The reference fetches london's forecast, which the candidate reads from its own data.
	m.ingress.get("/v2/forecast?location=london").expectStatus(http.StatusOK)
	m.expectCalls(calls{"GET /v2/forecast": 1})
	m.dependency(forecastsHost).expectReceived("/v1/forecasts/london", 1)

	// A required fetch the candidate leaves out quarantines it. Its window ends after
	// the optional fetch's, and only the first reason is kept, so the optional one never quarantined.
	m.referenceEgress.get("required.example", "/v1/forecasts/london").expectStatus(http.StatusOK)
	m.dependency("required.example").expectReceived("/v1/forecasts/london", 1)
	m.expectQuarantine(unmatchedEgress("GET required.example/v1/forecasts/{location}", "no matching candidate request within 500ms"))
}

func TestUnmatchedCandidateCallToOptionalEndpointQuarantines(t *testing.T) {
	m := startMirror(t,
		scripts(`
			import { egress } from "spectre";
			import type { FetchForecastRequest } from "forecasts";

			egress.http("GET forecasts.example/v1/forecasts/{location}").optional().match<FetchForecastRequest>();
		`),
		reference(sampleService()),
		candidate(sampleService()),
		dependency(forecastsHost, forecastProvider),
	)

	// Optional only lets the candidate make fewer calls, not calls the reference never made.
	m.candidateEgress.get(forecastsHost, "/v1/forecasts/paris").expectStatus(http.StatusBadGateway)
	m.expectQuarantine(unmatchedEgress("GET forecasts.example/v1/forecasts/{location}", "no matching reference request within 500ms"))
	m.dependency(forecastsHost).expectReceived("/v1/forecasts/paris", 0)
}
