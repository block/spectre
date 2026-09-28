import * as spectre from "spectre";

spectre.field("spectre.sample.v1.User.roles", (roles) => roles === undefined ? undefined : roles.sort());
spectre.field("spectre.sample.v1.ListUsersResponse.generated_at", () => undefined);
