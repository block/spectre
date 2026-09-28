import * as spectre from "spectre";

const unorderedStrings = (reference, candidate) => {
  const sorted = (values) => values === undefined ? [] : values.sort();
  return spectre.deepEqual(sorted(reference), sorted(candidate));
};

spectre.field("spectre.sample.v1.User.roles", unorderedStrings);
spectre.field("spectre.sample.v1.ListUsersResponse.generated_at", () => true);
