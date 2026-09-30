import * as spectre from "spectre";

// In dependency mode, the weather sample fetches forecasts from this provider through egress.
spectre.egress(
  "GET forecasts.example/v1/forecasts/{location}",
  "spectre.sample.v1.ForecastProviderService.FetchForecast",
);
