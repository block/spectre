import { egress } from "spectre";
import type { FetchForecastRequest } from "forecasts";

// In dependency mode, the weather sample fetches forecasts through egress.
egress<FetchForecastRequest>("http", "GET forecasts.example/v1/forecasts/{location}");
