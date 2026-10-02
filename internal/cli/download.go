package cli

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
	"github.com/MohammadMD1383/modharbor/internal/ui"
)

// fetchClient downloads mod files. Modrinth's CDN benefits from keep-alive
// connections when pulling dozens of jars.
var fetchClient = &http.Client{
	Timeout: 10 * time.Minute,
	Transport: &http.Transport{
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	},
}

// transfer describes a download the caller wants shown to the user.
//
// It carries only what the caller knows before the request goes out;
// fetchFile owns the rest of the lifecycle. A nil *transfer means "draw
// nothing", which is the normal case under --json, --quiet and any
// non-terminal stderr.
type transfer struct {
	// Label leads the bar. installProjects passes the mod name, because
	// "downloading 40 MB" tells a user far less than which of thirty jars
	// they are waiting for.
	Label string
	// Total is the expected size in bytes. fetchFile prefers the server's
	// Content-Length when there is one, so treat this as a hint, not a
	// contract.
	Total int64
}

// progressEnabled reports whether a download bar may be drawn.
//
// Progress is written to stderr so it can never contaminate stdout, but it is
// suppressed for --json anyway: a flag promising machine-readable output should
// not also paint a bar, and a user who wants the bar can drop the flag. It is
// suppressed for --quiet because that flag turns off decorative output.
//
// ui.Progress additionally disables itself when stderr is not a terminal, so
// redirecting to a file yields neither the bar nor any escape sequences.
func progressEnabled() bool { return !flagJSON && !flagQuiet }

// newTransfer builds a transfer, or nil when no bar should be drawn.
func newTransfer(label string, total int64) *transfer {
	if !progressEnabled() {
		return nil
	}
	return &transfer{Label: label, Total: total}
}

// countingReader advances a progress bar as the body streams in, so the bar
// moves while bytes are arriving instead of snapping to 100% at the end.
type countingReader struct {
	src io.Reader
	bar *ui.Progress
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	if n > 0 && c.bar != nil {
		c.bar.Add(int64(n))
	}
	return n, err
}

// transferTotal picks the byte count for the progress bar.
//
// Content-Length wins over the size the catalogue advertised, because it is
// what the server is actually sending; a stale size would otherwise stop the
// bar short of 100%.
func transferTotal(tr *transfer, resp *http.Response) int64 {
	if resp != nil && resp.ContentLength > 0 {
		return resp.ContentLength
	}
	if tr != nil {
		return tr.Total
	}
	return 0
}

// fetchFile downloads url into dest, verifying sha512 when provided.
//
// The download lands in a temporary file and is renamed into place only after
// the digest matches, so a failed or truncated download never leaves a broken
// jar where Minecraft would try to load it.
//
// tr may be nil, in which case the transfer happens silently. When it is not,
// the bar is cleared on every return path — including a checksum mismatch —
// so a failed download cannot leave progress stuck on screen.
func fetchFile(ctx context.Context, url, dest, wantSHA512 string, tr *transfer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", modrinth.UserAgent)

	resp, err := fetchClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".modharbor-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// Start the bar only once the request has succeeded, so a connection
	// failure never paints a bar it cannot finish.
	var body io.Reader = resp.Body
	if tr != nil {
		bar := ui.NewProgress(tr.Label, transferTotal(tr, resp))
		// Done("") erases the line and prints nothing, which is what is wanted
		// on both success and failure. It also no-ops when stderr is not a
		// terminal.
		defer bar.Done("")
		body = countingReader{src: resp.Body, bar: bar}
	}

	h := sha512.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if want := normaliseDigest(wantSHA512); want != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("checksum mismatch for %s (expected %s, got %s)",
				filepath.Base(dest), want, got)
		}
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

// sortVersionsByPreference orders versions newest-first while preferring
// releases over betas over alphas within the allowed channel.
func sortVersionsByPreference(vers []modrinth.Version, channel modrinth.Channel) {
	rank := func(t string) int {
		switch channel {
		case modrinth.ChannelAlpha:
			switch t {
			case "release":
				return 0
			case "beta":
				return 1
			default:
				return 2
			}
		case modrinth.ChannelBeta:
			if t == "release" {
				return 0
			}
			return 1
		default:
			return 0
		}
	}
	sort.SliceStable(vers, func(i, j int) bool {
		ri, rj := rank(vers[i].VersionType), rank(vers[j].VersionType)
		if ri != rj {
			return ri < rj
		}
		return vers[i].DatePublished.After(vers[j].DatePublished)
	})
}

func normaliseDigest(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(h, "sha512:")
	return h
}
