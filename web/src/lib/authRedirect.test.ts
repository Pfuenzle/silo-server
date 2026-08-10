import { describe, expect, it } from "vitest";

import { buildLoginRetryHref, sanitizeAuthRedirect } from "./authRedirect";

describe("sanitizeAuthRedirect", () => {
  it.each(["https://evil.example", "//evil.example", "/\\evil.example", "/%5Cevil.example"])(
    "rejects external redirect form %s",
    (value) => {
      expect(sanitizeAuthRedirect(value)).toBeNull();
    },
  );

  it.each(["/%2Fevil.example", "/library\nnext"])(
    "rejects encoded separators and control characters in %s",
    (value) => {
      expect(sanitizeAuthRedirect(value)).toBeNull();
    },
  );

  it("preserves a safe same-origin path, query, and fragment", () => {
    const path = "/library?tab=new#recent";

    expect(sanitizeAuthRedirect(path)).toBe(path);
    expect(buildLoginRetryHref(path)).toBe("/login?redirect=%2Flibrary%3Ftab%3Dnew%23recent");
  });
});
