// Match the server's new-password policy without trimming or normalizing.
export function validNewPassword(password: string): boolean {
  return (
    Array.from(password).length >= 12 &&
    new TextEncoder().encode(password).byteLength <= 256 &&
    !/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(
      password,
    )
  );
}
