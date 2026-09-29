import * as spectre from "spectre";

const service = "spectre.sample.v1.WeatherService";

spectre.endpoint("GET /api/v1/forecast", `${service}.GetForecast`);
spectre.endpoint("GET /v2/forecast", `${service}.GetForecastV2`);
spectre.endpoint("GET /_status", `${service}.GetStatus`);

spectre.field("spectre.sample.v1.GetForecastV2Response.alerts", (alerts) =>
  alerts === undefined ? undefined : alerts.sort((a, b) => (a.id ?? "").localeCompare(b.id ?? "")),
);
// Each deployment reports its own revision.
spectre.field("spectre.sample.v1.GetStatusResponse.revision", () => undefined);
