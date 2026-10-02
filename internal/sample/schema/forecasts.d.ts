declare module "forecasts" {
  import type { Forecast } from "weather";

  export interface FetchForecastRequest {
    location: string;
  }

  export interface FetchForecastResponse {
    forecast?: Forecast;
    message?: string;
  }
}
