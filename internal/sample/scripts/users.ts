import { field } from "spectre";
import type { ListUsersResponse, User } from "spectre.sample.v1";

field<User, "roles">((roles) => roles.sort());
field<ListUsersResponse, "generatedAt">(() => undefined);
