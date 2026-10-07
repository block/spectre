import { field, ingress } from "spectre";
import type { GetForecastResponse, GetForecastV2Response, GetStatusResponse } from "weather";

ingress.match<GetForecastResponse>("http", "GET /api/v1/forecast");
ingress.match<GetForecastV2Response>("http", "GET /v2/forecast");
ingress.match<GetStatusResponse>("http", "GET /_status");

field<GetForecastV2Response, "alerts">((alerts) =>
  alerts?.sort((a, b) => (a.id ?? "").localeCompare(b.id ?? "")),
);
// Each deployment reports its own revision.
field<GetStatusResponse, "revision">(() => undefined);
