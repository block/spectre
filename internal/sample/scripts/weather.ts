import { field, ingress } from "spectre";
import type { GetForecastResponse, GetForecastV2Response, GetStatusResponse } from "weather";

ingress.http("GET /api/v1/forecast").match<GetForecastResponse>();
ingress.http("GET /v2/forecast").match<GetForecastV2Response>();
ingress.http("GET /_status").match<GetStatusResponse>();

field<GetForecastV2Response, "alerts">((alerts) =>
  alerts?.sort((a, b) => (a.id ?? "").localeCompare(b.id ?? "")),
);
// Each deployment reports its own revision.
field<GetStatusResponse, "revision">(() => undefined);
