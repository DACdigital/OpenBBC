// Rule pack: urql / @urql/core / @urql/next.
//
// Same gql extraction as apollo — delegates to the shared scanner.

import { extractGql } from "./apollo.mjs";
import { depPresent } from "./_util.mjs";

export const meta = {
  id: "urql",
  client: "urql",
};

export function detect(pkg) {
  return depPresent(pkg, "urql") ||
         depPresent(pkg, "@urql/core") ||
         depPresent(pkg, "@urql/next") ||
         depPresent(pkg, "@urql/preact");
}

export function extract(filePath, rawText) {
  return extractGql(filePath, rawText, "urql");
}
