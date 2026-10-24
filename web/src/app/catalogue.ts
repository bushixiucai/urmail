/** Adapt the backend's identifier placeholders without changing the shared catalog. */
export function adaptCatalogue(
  catalogue: Record<string, string>,
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(catalogue).map(([key, message]) => [
      key,
      message.replace(/(?<!\{)\{([A-Za-z_][A-Za-z0-9_]*)\}(?!\})/g, "{{$1}}"),
    ]),
  );
}
