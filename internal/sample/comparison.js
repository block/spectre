import * as spectre from "spectre";

const unorderedStrings = (reference, candidate) => {
  const sorted = (values) => values === undefined ? [] : values.slice().sort();
  return JSON.stringify(sorted(reference)) === JSON.stringify(sorted(candidate));
};

spectre.field("spectre.sample.v1.User.roles", unorderedStrings);
spectre.field("spectre.sample.v1.ListUsersResponse.generated_at", () => true);
