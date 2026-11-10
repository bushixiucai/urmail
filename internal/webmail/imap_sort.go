package webmail

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// MaxSortScan bounds how many summaries we fetch for client-side sort and
// thread fallbacks. Folders larger than this keep server-side behavior only.
const MaxSortScan = 2000

// uidSort runs UID SORT when the server advertises SORT and returns the
// ordered UID sequence.
func (c *imapConn) uidSort(ctx context.Context, criteria, key string) (_ []uint32, resultErr error) {
	defer func() { c.finishIMAPError(ctx, resultErr) }()
	finish := c.guardIO(ctx)
	defer finish()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tag := c.nextTag()
	if criteria == "" {
		criteria = "ALL"
	}
	// criteria originates from BuildSearchCriteria (quoted literals only).
	if _, err := io.WriteString(c.conn, tag+" UID SORT ("+key+") UTF-8 "+criteria+"\r\n"); err != nil {
		return nil, err
	}
	var uids []uint32
	for {
		line, err := c.readLogicalLine()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, "* SORT ") {
			for _, f := range strings.Fields(line[len("* SORT "):]) {
				if n, err := parseUIDField(f); err == nil {
					uids = append(uids, n)
				}
			}
			continue
		}
		if strings.HasPrefix(line, tag+" OK") {
			return uids, nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return nil, imapCommandRejected(line)
		}
	}
}

// imapCommandRejected means a command completed with a tagged error and the
// connection remains synchronized.
type imapCommandRejected string

func (e imapCommandRejected) Error() string { return string(e) }

// uidThread runs UID THREAD REFERENCES when the server advertises
// THREAD=REFERENCES and returns uid -> thread root plus the number of
// members in each thread. The server reports every member of a thread, so
// the counts are folder-wide and not limited to the loaded page.
func (c *imapConn) uidThread(ctx context.Context, criteria string) (_ map[uint32]uint32, _ map[uint32]int, resultErr error) {
	defer func() { c.finishIMAPError(ctx, resultErr) }()
	finish := c.guardIO(ctx)
	defer finish()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	tag := c.nextTag()
	if criteria == "" {
		criteria = "ALL"
	}
	if _, err := io.WriteString(c.conn, tag+" UID THREAD REFERENCES UTF-8 "+criteria+"\r\n"); err != nil {
		return nil, nil, err
	}
	var lines []string
	for {
		line, err := c.readLogicalLine()
		if err != nil {
			return nil, nil, err
		}
		if strings.HasPrefix(line, "* THREAD") {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "* THREAD")))
			continue
		}
		if strings.HasPrefix(line, tag+" OK") {
			return parseThreadRoots(strings.Join(lines, " "))
		}
		if strings.HasPrefix(line, tag+" ") {
			return nil, nil, imapCommandRejected(line)
		}
	}
}

func parseUIDField(s string) (uint32, error) {
	var n uint32
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}
