package nsd

import "strings"

// cleanPathPosix is a minimal filepath.Clean for absolute POSIX paths used in
// rendered configuration. It avoids importing path/filepath so the package
// keeps a single, platform-independent render behavior.
func cleanPathPosix(p string) string {
	if p == "" {
		return "."
	}
	absolute := strings.HasPrefix(p, "/")
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 && out[len(out)-1] != ".." {
				out = out[:len(out)-1]
				continue
			}
			if !absolute {
				out = append(out, "..")
			}
		default:
			out = append(out, part)
		}
	}
	joined := strings.Join(out, "/")
	if absolute {
		return "/" + joined
	}
	if joined == "" {
		return "."
	}
	return joined
}
