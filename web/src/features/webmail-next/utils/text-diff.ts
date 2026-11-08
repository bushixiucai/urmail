export interface DiffChunk {
  type: "eq" | "del" | "add";
  value: string;
}

/**
 * Computes a lightweight token-based diff between oldText and newText.
 * Handles words, symbols, whitespace, and multi-line strings cleanly.
 */
export function computeTextDiff(oldText: string, newText: string): DiffChunk[] {
  if (oldText === newText) {
    return oldText ? [{ type: "eq", value: oldText }] : [];
  }
  if (!oldText) {
    return newText ? [{ type: "add", value: newText }] : [];
  }
  if (!newText) {
    return oldText ? [{ type: "del", value: oldText }] : [];
  }

  // Tokenize into words, individual non-whitespace punctuation, and whitespace sequences
  const tokenize = (str: string): string[] => {
    const tokens: string[] = [];
    const re = /[\p{L}\p{N}]+|[^\s\p{L}\p{N}]|\s+/gu;
    let match: RegExpExecArray | null;
    while ((match = re.exec(str)) !== null) {
      tokens.push(match[0]);
    }
    return tokens;
  };

  const oldTokens = tokenize(oldText);
  const newTokens = tokenize(newText);

  // Compute Longest Common Subsequence (LCS) matrix
  const m = oldTokens.length;
  const n = newTokens.length;

  // For very long texts (> 1000 tokens), fallback to line-level diff if needed,
  // but email body diffs are typically modest. Limit max matrix cells to 1,000,000.
  if (m * n > 1000000) {
    return [
      { type: "del", value: oldText },
      { type: "add", value: newText },
    ];
  }

  const dp: number[][] = Array.from({ length: m + 1 }, () =>
    new Array<number>(n + 1).fill(0),
  );

  for (let i = 0; i < m; i++) {
    for (let j = 0; j < n; j++) {
      if (oldTokens[i] === newTokens[j]) {
        dp[i + 1][j + 1] = dp[i][j] + 1;
      } else {
        dp[i + 1][j + 1] = Math.max(dp[i + 1][j], dp[i][j + 1]);
      }
    }
  }

  // Backtrack to extract diff tokens
  const rawChunks: DiffChunk[] = [];
  let i = m;
  let j = n;

  while (i > 0 || j > 0) {
    if (i > 0 && j > 0 && oldTokens[i - 1] === newTokens[j - 1]) {
      rawChunks.push({ type: "eq", value: oldTokens[i - 1] });
      i--;
      j--;
    } else if (j > 0 && (i === 0 || dp[i][j - 1] >= dp[i - 1][j])) {
      rawChunks.push({ type: "add", value: newTokens[j - 1] });
      j--;
    } else if (i > 0 && (j === 0 || dp[i][j - 1] < dp[i - 1][j])) {
      rawChunks.push({ type: "del", value: oldTokens[i - 1] });
      i--;
    }
  }

  rawChunks.reverse();

  // Merge consecutive chunks with the same type
  const merged: DiffChunk[] = [];
  for (const chunk of rawChunks) {
    if (merged.length > 0 && merged[merged.length - 1].type === chunk.type) {
      merged[merged.length - 1].value += chunk.value;
    } else {
      merged.push({ ...chunk });
    }
  }

  return merged;
}
