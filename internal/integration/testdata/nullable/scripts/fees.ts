import { ingress } from "spectre";
import type { Fee } from "fees";

ingress.http("GET /fee").match<Fee>();
