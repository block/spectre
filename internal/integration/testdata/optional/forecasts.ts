import { egress, ingress } from "spectre";
import type { FetchForecastRequest } from "forecasts";
import type { GetForecastV2Response } from "weather";

ingress.http("GET /v2/forecast").match<GetForecastV2Response>();
// The candidate keeps its own forecasts, so it may leave out the reference's fetches.
egress.http("GET forecasts.example/v1/forecasts/{location}").optional().match<FetchForecastRequest>();
// The candidate must still make every fetch the reference makes here.
egress.http("GET required.example/v1/forecasts/{location}").match<FetchForecastRequest>();
