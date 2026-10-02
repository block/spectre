// Raw weather JSON omits zero-valued ordinary scalars and empty lists.
declare module "weather" {
  export interface GetForecastRequest {
    location: string;
  }

  export interface GetForecastResponse {
    forecast?: Forecast;
    success?: boolean;
    message?: string;
  }

  export interface GetForecastV2Request {
    location: string;
  }

  export interface GetForecastV2Response {
    forecast?: Forecast;
    alerts?: Alert[];
    success?: boolean;
    message?: string;
  }

  export interface GetStatusRequest {}

  export interface GetStatusResponse {
    status?: string;
    revision?: string;
    message?: string;
  }

  export interface Forecast {
    location?: string;
    days?: DailyForecast[];
  }

  export interface DailyForecast {
    date?: string;
    high?: Temperature;
    low?: Temperature;
    precipitation_percent?: number;
    wind?: Wind;
  }

  export interface Temperature {
    degrees?: number;
    unit?: string;
  }

  export interface Wind {
    speed_kph?: number;
    direction?: string;
  }

  export interface Alert {
    id?: string;
    severity?: string;
    headline?: string;
  }
}
