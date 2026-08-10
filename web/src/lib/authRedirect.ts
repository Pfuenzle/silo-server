export function sanitizeAuthRedirect(value: string | null | undefined): string | null {
  if (!value) {
    return null;
  }
  let decoded: string;
  try {
    decoded = decodeURIComponent(value);
  } catch {
    return null;
  }
  if (!decoded.startsWith("/")) {
    return null;
  }
  if (decoded.startsWith("//")) {
    return null;
  }
  for (const character of decoded) {
    const codePoint = character.charCodeAt(0);
    if (character === "\\" || codePoint <= 31 || codePoint === 127) {
      return null;
    }
  }
  return value;
}

export function buildLoginRetryHref(next: string | null | undefined): string {
  const safeNext = sanitizeAuthRedirect(next);
  return safeNext ? `/login?redirect=${encodeURIComponent(safeNext)}` : "/login";
}
