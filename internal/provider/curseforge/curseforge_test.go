package curseforge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every test in this file points the client at an httptest server and uses the
// dummy key below. Nothing here may reach api.curseforge.com: a real request
// would either spend the developer's quota or, worse, send their key to a host
// that a typo in the base URL chose.

const testAPIKey = "test-key"

// ─── the stub ────────────────────────────────────────────────────────────────

// cfRequest is one recorded request, kept whole so a test can assert on the
// query the client built and on every header it set.
type cfRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
}

// fakeCF records what the client asked for. The recording exists so a test can
// prove a request was never made, which no error message can establish: the
// interesting bugs in this client are the requests that should not happen.
type fakeCF struct {
	*httptest.Server

	mu       sync.Mutex
	requests []cfRequest
	respond  func(w http.ResponseWriter, r *http.Request)
}

func newFakeCF(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) *fakeCF {
	t.Helper()

	f := &fakeCF{respond: respond}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCF) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, cfRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Header: r.Header.Clone(),
	})
	respond := f.respond
	f.mu.Unlock()

	if respond == nil {
		http.NotFound(w, r)
		return
	}
	respond(w, r)
}

// set swaps the handler mid-test, so a client that remembered a failure is
// caught remembering one.
func (f *fakeCF) set(respond func(w http.ResponseWriter, r *http.Request)) {
	f.mu.Lock()
	f.respond = respond
	f.mu.Unlock()
}

func (f *fakeCF) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeCF) recorded() []cfRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cfRequest(nil), f.requests...)
}

// only returns the single request that was made, naming the others if there
// were any.
func (f *fakeCF) only(t *testing.T) cfRequest {
	t.Helper()
	reqs := f.recorded()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request, got %d:%s", len(reqs), describe(reqs))
	}
	return reqs[0]
}

func describe(reqs []cfRequest) string {
	var b strings.Builder
	for _, r := range reqs {
		fmt.Fprintf(&b, "\n  %s %s?%s", r.Method, r.Path, r.Query.Encode())
	}
	return b.String()
}

// clientFor builds a client aimed at f, reusing the server's own HTTP client
// so no keep-alive state is shared between tests.
func clientFor(f *fakeCF, mutate ...func(*Options)) *Client {
	opts := Options{
		BaseURL: f.URL + "/v1",
		APIKey:  testAPIKey,
		HTTP:    f.Client(),
	}
	for _, m := range mutate {
		m(&opts)
	}
	return New(opts)
}

// ─── fixtures ────────────────────────────────────────────────────────────────

// One project with three published files, deliberately not ordered by
// usefulness: the newest file targets an older Minecraft version, so a client
// that only sorts by date installs a jar the game cannot load.
const (
	cfModID = 306612

	// Built for 26.1 and published last.
	cfFileWrongMC = 4712345
	// Built for 26.3, published before cfFileWrongMC.
	cfFileRight = 4712346
	// Built for 26.2.
	cfFileOld = 4712347

	// CurseForge publishes digests in mixed case and callers compare them
	// case-insensitively, so the client normalises on the way in.
	cfSHA1Upper = "9E1F0C2D8A7B6C5D4E3F2A1B0C9D8E7F6A5B4C3D2"
	cfSHA1Right = "aa11bb22cc33dd44ee55ff6677889900aabbccdd"
	cfSHA1Old   = "1122334455667788990011223344556677889900"
	cfSHA1Wrong = "99887766554433221100ffeeddccbbaa99887766"
	cfSHA512    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	// A legacy MD5. It must never end up in SHA1: a download verified against
	// an MD5 would be compared as though it were a SHA-1 and never match.
	cfMD5 = "DEADBEEFDEADBEEFDEADBEEFDEADBEEF"
)

func cfFileJSON(id int64, name, date string, versions []string, sha1 string) string {
	v := make([]string, 0, len(versions))
	for _, g := range versions {
		v = append(v, fmt.Sprintf("%q", g))
	}
	return fmt.Sprintf(`{
	  "id": %d,
	  "gameId": 432,
	  "modId": %d,
	  "displayName": %q,
	  "fileName": %q,
	  "fileDate": %q,
	  "fileLength": 534210,
	  "downloadCount": 1234,
	  "downloadUrl": "https://edge.forgecdn.net/files/4712/%d/%s",
	  "gameVersions": [%s],
	  "releaseType": 1,
	  "fileFingerprints": [
	    {"algorithm": 1, "value": %q},
	    {"algorithm": 4, "value": %q},
	    {"algorithm": 2, "value": %q}
	  ],
	  "dependencies": [{"modId": %d, "relationType": 3}]
	}`, id, cfModID, name, name, date, id, name, strings.Join(v, ","), sha1, cfSHA512, cfMD5, cfModID)
}

const cfRightVersions = "26.3, Fabric"

func cfRightBody() string {
	return cfFileJSON(cfFileRight, "sodium-fabric-0.6.1.jar", "2025-01-05T10:00:00Z", []string{"26.3", "Fabric"}, cfSHA1Right)
}

// cfFilesBody is the /files listing in the order a server may plausibly return
// it: unsorted, so a caller that forgets to sort is caught.
func cfFilesBody() string {
	return "[" + strings.Join([]string{
		cfFileJSON(cfFileOld, "sodium-fabric-0.6.0.jar", "2025-03-01T10:00:00Z", []string{"26.2", "Fabric"}, cfSHA1Old),
		cfRightBody(),
		cfFileJSON(cfFileWrongMC, "sodium-fabric-0.6.2.jar", "2025-06-01T10:00:00Z", []string{"26.1", "Fabric"}, cfSHA1Wrong),
	}, ",") + "]"
}

func cfModBody() string {
	return fmt.Sprintf(`{
	  "id": %d,
	  "gameId": 432,
	  "name": "Sodium",
	  "slug": "sodium",
	  "summary": "A modern rendering engine",
	  "status": 4,
	  "downloadCount": 987654,
	  "isFeatured": true,
	  "categories": [{"id": 4, "name": "Fabric", "slug": "fabric"}],
	  "gameVersions": ["26.3", "Fabric"],
	  "latestFiles": [%s]
	}`, cfModID, cfRightBody())
}

// cfRouter serves every endpoint the client knows, so a header test can walk
// all of them without a bespoke stub per case.
func cfRouter(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/games/432/versions"):
		fmt.Fprint(w, `[{"id": 4711, "name": "26.3", "slug": "26.3", "gameId": 432}]`)
	case strings.HasPrefix(r.URL.Path, "/v1/games/432/categories"):
		fmt.Fprint(w, `[{"id": 7, "name": "Rift", "slug": "rift"}]`)
	case strings.HasSuffix(r.URL.Path, "/files"):
		fmt.Fprint(w, cfFilesBody())
	case strings.Contains(r.URL.Path, "/search"):
		fmt.Fprint(w, `[{"id": 306612, "name": "Sodium", "slug": "sodium"}]`)
	case r.URL.Path == "/v1/files/4712346":
		fmt.Fprint(w, cfRightBody())
	default:
		fmt.Fprint(w, cfModBody())
	}
}

// ─── headers and the key ─────────────────────────────────────────────────────

// TestEveryEndpointSendsKeyAcceptAndUserAgent pins the three headers CurseForge
// rejects a request without. The key is the difference between working and a
// 403, and it must never be dropped on one endpoint just because that endpoint
// is new.
func TestEveryEndpointSendsKeyAcceptAndUserAgent(t *testing.T) {
	f := newFakeCF(t, cfRouter)
	ctx := context.Background()

	calls := []struct {
		name string
		call func(*Client) error
	}{
		{"Mod", func(c *Client) error { _, err := c.Mod(ctx, cfModID); return err }},
		{"Files", func(c *Client) error { _, err := c.Files(ctx, cfModID); return err }},
		{"ModFile", func(c *Client) error { _, err := c.ModFile(ctx, cfFileRight); return err }},
		{"Search", func(c *Client) error { _, err := c.Search(ctx, "sodium", 5); return err }},
		{"GameVersionID", func(c *Client) error { _, err := c.GameVersionID(ctx, "26.3"); return err }},
		{"LoaderID", func(c *Client) error { _, err := c.LoaderID(ctx, "rift"); return err }},
	}

	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh client per endpoint: the file cache would otherwise
			// answer the last two calls without a request, and this test would
			// pass while asserting nothing about their headers.
			before := f.count()
			if err := tc.call(clientFor(f)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			reqs := f.recorded()
			if len(reqs) != before+1 {
				t.Fatalf("%s made %d request(s), want 1:%s", tc.name, len(reqs)-before, describe(reqs[before:]))
			}
			got := reqs[len(reqs)-1]
			if got.Method != http.MethodGet {
				t.Errorf("method = %s, want GET", got.Method)
			}
			if got.Header.Get("x-api-key") != testAPIKey {
				t.Errorf("x-api-key = %q, want %q", got.Header.Get("x-api-key"), testAPIKey)
			}
			if got.Header.Get("Accept") != "application/json" {
				t.Errorf("Accept = %q, want application/json", got.Header.Get("Accept"))
			}
			if got.Header.Get("User-Agent") != UserAgent {
				t.Errorf("User-Agent = %q, want %q", got.Header.Get("User-Agent"), UserAgent)
			}
		})
	}
}

// TestWithoutKeyNoRequestIsMade covers the state a user is in before they run
// `config set curseforge.apiKey`. The client must refuse locally: an
// unauthenticated request comes back as a bare 403, which reads like a wrong
// key rather than a missing one.
func TestWithoutKeyNoRequestIsMade(t *testing.T) {
	f := newFakeCF(t, cfRouter)
	ctx := context.Background()

	for _, key := range []string{"", "   ", "\t\n"} {
		t.Run(fmt.Sprintf("key=%q", key), func(t *testing.T) {
			c := clientFor(f, func(o *Options) { o.APIKey = key })
			if c.HasKey() {
				t.Fatalf("HasKey() = true for %q", key)
			}
			calls := map[string]func() error{
				"Mod":           func() error { _, err := c.Mod(ctx, cfModID); return err },
				"Files":         func() error { _, err := c.Files(ctx, cfModID); return err },
				"ModFile":       func() error { _, err := c.ModFile(ctx, cfFileRight); return err },
				"Search":        func() error { _, err := c.Search(ctx, "sodium", 5); return err },
				"GameVersionID": func() error { _, err := c.GameVersionID(ctx, "26.3"); return err },
				"LoaderID":      func() error { _, err := c.LoaderID(ctx, "rift"); return err },
			}
			before := f.count()
			for name, call := range calls {
				if err := call(); !errors.Is(err, ErrNoAPIKey) {
					t.Errorf("%s: error = %v, want ErrNoAPIKey", name, err)
				}
			}
			if after := f.count(); after != before {
				t.Fatalf("%d request(s) left the client without a key", after-before)
			}
		})
	}

	if !clientFor(f).HasKey() {
		t.Fatal("HasKey() = false for a configured key")
	}
}

// TestNewNormalisesBaseURL pins the two things Options can change silently. A
// trailing slash would otherwise produce "//v1/mods/…", which some proxies
// answer with a redirect that drops the key header.
func TestNewNormalisesBaseURL(t *testing.T) {
	f := newFakeCF(t, cfRouter)
	c := clientFor(f, func(o *Options) {
		o.BaseURL = f.URL + "/v1/"
		o.UserAgent = "modharbor-test/9"
	})

	if _, err := c.Mod(context.Background(), cfModID); err != nil {
		t.Fatalf("Mod: %v", err)
	}
	got := f.only(t)
	if got.Path != "/v1/mods/306612" {
		t.Fatalf("path = %q, want /v1/mods/306612", got.Path)
	}
	if got.Header.Get("User-Agent") != "modharbor-test/9" {
		t.Fatalf("User-Agent = %q, want the configured value", got.Header.Get("User-Agent"))
	}
}

// ─── errors ──────────────────────────────────────────────────────────────────

// TestNotFoundNamesTheRequestedEndpoint covers the 404 a deleted project or
// withdrawn file produces. "Not found" alone tells a user nothing about which
// mod vanished, and modharbor hits this endpoint with a project id taken from
// launcher metadata — which is exactly how a stale id presents.
func TestNotFoundNamesTheRequestedEndpoint(t *testing.T) {
	calls := []struct {
		name     string
		call     func(*Client) error
		wantPath string
	}{
		{"Mod", func(c *Client) error { _, err := c.Mod(context.Background(), cfModID); return err }, "/mods/306612"},
		{"Files", func(c *Client) error { _, err := c.Files(context.Background(), cfModID); return err }, "/mods/306612"},
		{"ModFile", func(c *Client) error {
			_, err := c.ModFile(context.Background(), cfFileRight)
			return err
		}, "/files/4712346"},
		{"Search", func(c *Client) error {
			_, err := c.Search(context.Background(), "sodium", 5)
			return err
		}, "/mods/search"},
	}

	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
			err := tc.call(clientFor(f))
			if err == nil {
				t.Fatal("expected an error")
			}
			// A caller branches on this to say "this mod was deleted" rather
			// than "the network is down".
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("error %v does not wrap ErrNotFound", err)
			}
			if !strings.Contains(err.Error(), tc.wantPath) {
				t.Fatalf("error %q does not name %q", err, tc.wantPath)
			}
			var cfErr *Error
			if errors.As(err, &cfErr) {
				t.Errorf("a 404 produced both ErrNotFound and *Error: %v", err)
			}
		})
	}
}

// TestErrorResponsesCarryStatusAndCause covers every other failure. The
// message has to carry the cause as well as the status: a user who sees only
// "500" cannot tell whether to retry or to check their plan.
func TestErrorResponsesCarryStatusAndCause(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantMsg []string
	}{
		{
			name:    "missing key",
			status:  http.StatusUnauthorized,
			body:    `{"error":"Unauthorized","errorMessage":"missing api key"}`,
			wantMsg: []string{"401", "invalid or missing API key"},
		},
		{
			name:    "forbidden",
			status:  http.StatusForbidden,
			body:    `{"error":"Forbidden"}`,
			wantMsg: []string{"403", "invalid or missing API key"},
		},
		{
			name:    "server error with a message",
			status:  http.StatusInternalServerError,
			body:    `{"message":"index rebuild in progress"}`,
			wantMsg: []string{"500", "index rebuild in progress"},
		},
		{
			name:    "server error with plain text",
			status:  http.StatusBadGateway,
			body:    "  upstream connect error\n",
			wantMsg: []string{"502", "upstream connect error"},
		},
		{
			name:    "server error with an empty body",
			status:  http.StatusServiceUnavailable,
			body:    "",
			wantMsg: []string{"503"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			c := clientFor(f)
			ctx := context.Background()

			calls := map[string]func() error{
				"Mod":     func() error { _, err := c.Mod(ctx, cfModID); return err },
				"Files":   func() error { _, err := c.Files(ctx, cfModID); return err },
				"ModFile": func() error { _, err := c.ModFile(ctx, cfFileRight); return err },
				"Search":  func() error { _, err := c.Search(ctx, "sodium", 5); return err },
			}
			for name, call := range calls {
				err := call()
				if err == nil {
					t.Errorf("%s: expected an error for %d", name, tc.status)
					continue
				}
				for _, want := range tc.wantMsg {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("%s: message %q does not mention %q", name, err, want)
					}
				}
				if errors.Is(err, ErrNotFound) {
					t.Errorf("%s: status %d was reported as a missing project", name, tc.status)
				}
				// The project id is not in the message for every status, so
				// the exact request URL is what a caller has to log. Assert it
				// is populated rather than hoping the message grows one day.
				var cfErr *Error
				if !errors.As(err, &cfErr) {
					t.Errorf("%s: error %v is not a *Error", name, err)
					continue
				}
				if cfErr.StatusCode != tc.status {
					t.Errorf("%s: StatusCode = %d, want %d", name, cfErr.StatusCode, tc.status)
				}
				if !strings.Contains(cfErr.URL, "/v1/") {
					t.Errorf("%s: URL = %q, want the endpoint that was requested", name, cfErr.URL)
				}
			}
		})
	}
}

// TestMalformedObjectBodies covers the replies a proxy, a captive portal or a
// truncated connection produce. Decoding one into a zero struct hands the
// resolver an empty project that looks like a real answer, and the user is then
// told the mod does not exist when in fact modharbor could not read the reply.
func TestMalformedObjectBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "truncated json", body: `{"id": 306612`},
		{name: "empty body", body: ""},
		{name: "html error page", body: "<html><body>502 Bad Gateway</body></html>"},
		{name: "array where an object belongs", body: `[]`},
		{name: "wrong field type", body: `{"id": "not-a-number"}`},
		{name: "trailing garbage", body: `{"id": 306612} oops`},
		// The one body that used to decode cleanly into a zero project, which
		// no error message could tell apart from a mod that publishes nothing.
		{name: "null", body: `null`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
			c := clientFor(f)
			ctx := context.Background()

			mod, err := c.Mod(ctx, cfModID)
			file, ferr := c.ModFile(ctx, cfFileRight)

			for name, e := range map[string]error{"Mod": err, "ModFile": ferr} {
				if e == nil {
					t.Errorf("%s: body %q decoded without an error", name, tc.body)
				}
			}
			// A failed decode must not also hand back a value that a caller
			// could mistake for a result.
			if mod != nil || file != nil {
				t.Fatalf("a failed decode returned data: %v %v", mod, file)
			}
		})
	}
}

// TestMalformedListBodies is the same defence for the two endpoints that
// decode into a slice, where the dangerous body is the mirror image: an object
// where an array belongs would otherwise decode into an empty listing that
// reads as "this mod publishes nothing".
func TestMalformedListBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "truncated json", body: `[{"id": 306612`},
		{name: "empty body", body: ""},
		{name: "html error page", body: "<html><body>502 Bad Gateway</body></html>"},
		{name: "object where an array belongs", body: `{"data": []}`},
		{name: "scalar element", body: `["sodium"]`},
		{name: "null", body: `null`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
			c := clientFor(f)
			ctx := context.Background()

			files, err := c.Files(ctx, cfModID)
			hits, serr := c.Search(ctx, "sodium", 5)

			for name, e := range map[string]error{"Files": err, "Search": serr} {
				if e == nil {
					t.Errorf("%s: body %q decoded without an error", name, tc.body)
				}
			}
			if files != nil || hits != nil {
				t.Fatalf("a failed decode returned data: %v %v", files, hits)
			}
		})
	}
}

// TestNullBodyIsNotAnEmptyListing pins the distinction B26 exists for.
//
// A `null` body is a failure and an empty array is an answer. Both used to come
// back as a zero value with no error, so a mod with hundreds of files and a mod
// that publishes none were indistinguishable — and the migration silently
// skipped the first. Every endpoint has to say which of the two it received,
// and has to name the endpoint, because "nothing" is otherwise all a caller
// has to go on.
func TestNullBodyIsNotAnEmptyListing(t *testing.T) {
	tests := []struct {
		name     string
		call     func(*Client) error
		wantPath string
	}{
		{"Mod", func(c *Client) error { _, err := c.Mod(context.Background(), cfModID); return err }, "/v1/mods/306612"},
		{"ModFile", func(c *Client) error {
			_, err := c.ModFile(context.Background(), cfFileRight)
			return err
		}, "/v1/files/4712346"},
		{"Files", func(c *Client) error { _, err := c.Files(context.Background(), cfModID); return err }, "/v1/mods/306612/files"},
		{"Search", func(c *Client) error {
			_, err := c.Search(context.Background(), "sodium", 5)
			return err
		}, "/v1/mods/search"},
		{"GameVersionID", func(c *Client) error {
			_, err := c.GameVersionID(context.Background(), "26.3")
			return err
		}, "/v1/games/432/versions"},
		{"LoaderID", func(c *Client) error { _, err := c.LoaderID(context.Background(), "rift"); return err }, "/v1/games/432/categories"},
	}

	for _, tc := range tests {
		t.Run(tc.name+" answers null", func(t *testing.T) {
			f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "null") })
			err := tc.call(clientFor(f))
			if err == nil {
				t.Fatal("a null body was reported as an answer")
			}
			// A caller branches on this to say "the API answered nothing",
			// which is not the same as "there is nothing to install".
			if !errors.Is(err, ErrNullResponse) {
				t.Fatalf("error %v does not wrap ErrNullResponse", err)
			}
			if !strings.Contains(err.Error(), "null") {
				t.Errorf("message %q does not say the body was null", err)
			}
			if !strings.Contains(err.Error(), tc.wantPath) {
				t.Errorf("message %q does not name the endpoint %q", err, tc.wantPath)
			}
		})
	}

	// The other half: an empty array is a real answer and has to stay one.
	t.Run("an empty listing is still an answer", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "[]") })
		c := clientFor(f)

		files, err := c.Files(context.Background(), cfModID)
		if err != nil {
			t.Fatalf("an empty file list was reported as an error: %v", err)
		}
		if len(files) != 0 {
			t.Fatalf("expected no files, got %+v", files)
		}
	})
}

// TestRateLimitIsNotRetried pins the 429 behaviour so a change to it is a
// deliberate one. CurseForge's limit is per key and clears in seconds, so a
// retry loop here would turn one throttled call into the burst that gets the
// user blocked. The stub sends Retry-After: 60, which any backoff would wait
// for; the recorded request count is the assertion.
func TestRateLimitIsNotRetried(t *testing.T) {
	f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"Rate limit exceeded","errorMessage":"Too many requests"}`)
	})
	c := clientFor(f)

	started := time.Now()
	_, err := c.Search(context.Background(), "sodium", 5)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a 429 must not be reported as success")
	}
	if n := f.count(); n != 1 {
		t.Fatalf("%d requests were made, want exactly 1", n)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("the call took %s, so Retry-After is now honoured", elapsed)
	}

	var cfErr *Error
	if !errors.As(err, &cfErr) {
		t.Fatalf("error %v is not a *Error", err)
	}
	if cfErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", cfErr.StatusCode)
	}
	if !strings.Contains(err.Error(), "Too many requests") {
		t.Errorf("message %q does not carry the server's explanation", err)
	}
}

// TestTransportErrorsAreReturned covers the network layer itself. A refused
// connection has to surface as an error naming the URL, never as an empty
// result that reads like "this mod publishes nothing".
func TestTransportErrorsAreReturned(t *testing.T) {
	f := newFakeCF(t, nil)
	dead := f.URL
	f.Close()

	_, err := clientFor(f).Mod(context.Background(), cfModID)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if !strings.Contains(err.Error(), dead) {
		t.Fatalf("error %q does not name the URL that failed", err)
	}
}

// TestZeroOptionsProduceAUsableClient covers the construction the app performs
// when a user has configured nothing but a key. The default timeout is what
// stops one hung connection from blocking the CLI indefinitely, and the default
// root is what makes the client reach CurseForge at all. Nothing here dials:
// that host is the real API.
func TestZeroOptionsProduceAUsableClient(t *testing.T) {
	c := New(Options{APIKey: testAPIKey})
	if c.baseURL != DefaultBaseURL {
		t.Errorf("base URL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
	if !strings.HasPrefix(DefaultBaseURL, "https://") {
		t.Errorf("the default API root is not https: %q", DefaultBaseURL)
	}
	if c.http == nil || c.http.Timeout != 60*time.Second {
		t.Errorf("http timeout = %v, want a bounded 60s", c.http)
	}
	if c.userAgent != UserAgent {
		t.Errorf("User-Agent = %q, want %q", c.userAgent, UserAgent)
	}

	// The missing-key guard has to run before the dial: a client with no key
	// that reaches the network gets a 403 the user cannot act on.
	if _, err := New(Options{}).Files(context.Background(), cfModID); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("a keyless client returned %v, want ErrNoAPIKey", err)
	}
}

// TestRequestAndBodyFailuresAreErrors covers the two ways the transport layer
// fails around do(). A base URL the HTTP client cannot parse is a typo in a
// config file, and a body that ends early is a proxy or a dropped connection;
// neither may come back as a decoded value.
func TestRequestAndBodyFailuresAreErrors(t *testing.T) {
	t.Run("a base url the http client cannot parse", func(t *testing.T) {
		f := newFakeCF(t, cfRouter)
		c := clientFor(f, func(o *Options) { o.BaseURL = "://not-a-url" })

		if _, err := c.Mod(context.Background(), cfModID); err == nil {
			t.Fatal("an unparseable base URL produced no error")
		}
		if n := f.count(); n != 0 {
			t.Fatalf("%d request(s) were made", n)
		}
	})

	// The headers promise more than arrives. Reading must fail rather than
	// decode the prefix: a half-decoded project is the same silent zero value
	// as an empty one.
	t.Run("a body that ends early", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "4096")
			fmt.Fprint(w, `{"id": 306612, "name": "So`)
		})

		mod, err := clientFor(f).Mod(context.Background(), cfModID)
		if err == nil {
			t.Fatalf("a truncated body decoded to %+v", mod)
		}
		if !strings.Contains(err.Error(), "unexpected EOF") {
			t.Fatalf("error %q does not report a short read", err)
		}
		if mod != nil {
			t.Fatalf("a truncated body returned %+v", mod)
		}
	})
}

// ─── the file cache ──────────────────────────────────────────────────────────

// TestFileCacheServesRepeatLookups covers the fetch-once guarantee. Launching
// a game resolves dozens of mods, and the same file is asked for by both the
// project listing and the file id, so an uncached client would double the
// request budget and get throttled on a large pack.
func TestFileCacheServesRepeatLookups(t *testing.T) {
	t.Run("the same id twice", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, cfRightBody()) })
		c := clientFor(f)
		ctx := context.Background()

		first, err := c.ModFile(ctx, cfFileRight)
		if err != nil {
			t.Fatalf("ModFile: %v", err)
		}
		second, err := c.ModFile(ctx, cfFileRight)
		if err != nil {
			t.Fatalf("ModFile again: %v", err)
		}
		if first != second {
			t.Error("the cache handed back two different values for one id")
		}
		if n := f.count(); n != 1 {
			t.Fatalf("%d requests, want 1", n)
		}
	})

	t.Run("the listing primes the cache", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/files") {
				fmt.Fprint(w, cfFilesBody())
				return
			}
			http.NotFound(w, r)
		})
		c := clientFor(f)
		ctx := context.Background()

		files, err := c.Files(ctx, cfModID)
		if err != nil {
			t.Fatalf("Files: %v", err)
		}
		if len(files) != 3 {
			t.Fatalf("expected 3 files, got %d", len(files))
		}
		got, err := c.ModFile(ctx, cfFileRight)
		if err != nil {
			t.Fatalf("the listing did not prime the cache: %v", err)
		}
		if got.SHA1 != cfSHA1Right {
			t.Errorf("sha1 = %q, want %q", got.SHA1, cfSHA1Right)
		}
		if n := f.count(); n != 1 {
			t.Fatalf("%d requests, want the listing alone", n)
		}
	})

	t.Run("latestFiles prime the cache", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, cfModBody()) })
		c := clientFor(f)
		ctx := context.Background()

		if _, err := c.Mod(ctx, cfModID); err != nil {
			t.Fatalf("Mod: %v", err)
		}
		if _, err := c.ModFile(ctx, cfFileRight); err != nil {
			t.Fatalf("the project did not prime the cache: %v", err)
		}
		if n := f.count(); n != 1 {
			t.Fatalf("%d requests, want the project fetch alone", n)
		}
	})

	// A 404 must not be remembered: CurseForge answers 404 while it rebuilds
	// an index, and a remembered miss would make one hiccup permanent.
	t.Run("failures are not cached", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
		c := clientFor(f)
		ctx := context.Background()

		if _, err := c.ModFile(ctx, cfFileRight); !errors.Is(err, ErrNotFound) {
			t.Fatalf("first lookup: %v, want ErrNotFound", err)
		}
		f.set(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, cfRightBody()) })

		got, err := c.ModFile(ctx, cfFileRight)
		if err != nil {
			t.Fatalf("the second lookup reused a cached failure: %v", err)
		}
		if got.SHA1 != cfSHA1Right {
			t.Errorf("sha1 = %q, want %q", got.SHA1, cfSHA1Right)
		}
	})
}

// TestConcurrentLookupsAreSafe exercises the cache under the race detector.
// The resolver walks a pack's mods in parallel, so the id → file map is
// written and read from several goroutines; an unguarded map write corrupts
// the process rather than failing an assertion.
func TestConcurrentLookupsAreSafe(t *testing.T) {
	f := newFakeCF(t, cfRouter)
	c := clientFor(f)
	ctx := context.Background()

	const workers = 8
	var wg sync.WaitGroup
	failures := make(chan error, workers*3)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := c.Files(ctx, cfModID); err != nil {
				failures <- fmt.Errorf("worker %d Files: %w", i, err)
				return
			}
			if _, err := c.Mod(ctx, cfModID); err != nil {
				failures <- fmt.Errorf("worker %d Mod: %w", i, err)
				return
			}
			file, err := c.ModFile(ctx, cfFileRight)
			if err != nil {
				failures <- fmt.Errorf("worker %d ModFile: %w", i, err)
				return
			}
			if file.SHA1 != cfSHA1Right {
				failures <- fmt.Errorf("worker %d saw sha1 %q", i, file.SHA1)
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	// Concurrent misses legitimately fetch more than once; what must not happen
	// is fewer requests than the ones that were needed.
	if n := f.count(); n == 0 {
		t.Fatal("no request was made")
	}
}

// ─── endpoint shape ──────────────────────────────────────────────────────────

// TestQueryParameters pins the wire format. These values are CurseForge's own
// contract, not modharbor's: classId 6 means "mods" and gameId 432 means
// Minecraft, and a loader id leaking into classId would return projects for the
// wrong class.
func TestQueryParameters(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		call      func(*Client) error
		wantPath  string
		wantQuery map[string]string
	}{
		{
			name: "Files",
			body: cfFilesBody(),
			call: func(c *Client) error {
				_, err := c.Files(context.Background(), cfModID)
				return err
			},
			wantPath:  "/v1/mods/306612/files",
			wantQuery: map[string]string{"modId": "306612", "index": "0", "pageSize": "100"},
		},
		{
			name: "Search",
			body: `[]`,
			call: func(c *Client) error {
				_, err := c.Search(context.Background(), "sodium fabric", 7)
				return err
			},
			wantPath: "/v1/mods/search",
			wantQuery: map[string]string{
				"searchFilter": "sodium fabric", "index": "0", "pageSize": "7",
				"classId": "6", "gameId": "432",
			},
		},
		{
			name: "GameVersionID",
			body: `[{"id": 4711, "name": "26.3", "slug": "26.3", "gameId": 432}]`,
			call: func(c *Client) error {
				_, err := c.GameVersionID(context.Background(), "26.3")
				return err
			},
			wantPath:  "/v1/games/432/versions",
			wantQuery: map[string]string{"gameId": "432"},
		},
		{
			name: "LoaderID fallback",
			body: `[{"id": 7, "name": "Rift", "slug": "rift"}]`,
			call: func(c *Client) error {
				_, err := c.LoaderID(context.Background(), "rift")
				return err
			},
			wantPath:  "/v1/games/432/categories",
			wantQuery: map[string]string{"gameId": "432"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
			if err := tc.call(clientFor(f)); err != nil {
				t.Fatalf("call: %v", err)
			}
			got := f.only(t)
			if got.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", got.Path, tc.wantPath)
			}
			if len(got.Query) != len(tc.wantQuery) {
				t.Errorf("query = %v, want exactly %v", got.Query, tc.wantQuery)
			}
			for k, v := range tc.wantQuery {
				if got.Query.Get(k) != v {
					t.Errorf("query %s = %q, want %q", k, got.Query.Get(k), v)
				}
			}
		})
	}
}

// TestSearchMapsResults covers the shape a search returns. The ids and slugs are
// what a caller shows the user and later feeds back into an install, so a
// dropped field becomes a mod that cannot be fetched afterwards.
func TestSearchMapsResults(t *testing.T) {
	body := `[
	  {"id": 306612, "gameId": 432, "name": "Sodium", "slug": "sodium", "summary": "Rendering", "downloadCount": 900, "isFeatured": true, "latestFiles": []},
	  {"id": 238222, "gameId": 432, "name": "Lithium", "slug": "lithium", "summary": "Optimisations", "downloadCount": 800, "isFeatured": false, "latestFiles": []}
	]`
	f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
	c := clientFor(f)

	hits, err := c.Search(context.Background(), "so", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 results, got %d", len(hits))
	}
	if hits[0].ID != 306612 || hits[0].Slug != "sodium" || !hits[0].IsFeatured {
		t.Errorf("first hit mis-mapped: %+v", hits[0])
	}
	if hits[1].ID != 238222 || hits[1].Name != "Lithium" || hits[1].IsFeatured {
		t.Errorf("second hit mis-mapped: %+v", hits[1])
	}
	if hits[0].GameID != GameIDMinecraft {
		t.Errorf("gameId = %d, want %d", hits[0].GameID, GameIDMinecraft)
	}

	// No hits is a legitimate answer, not an error: the user typed a name that
	// does not exist and deserves an empty list, not a failure.
	f.set(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) })
	none, err := c.Search(context.Background(), "nothing matches this", 5)
	if err != nil {
		t.Fatalf("an empty result set must not be an error: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no results, got %+v", none)
	}
}

// TestModMapsProjectAndPrimesFiles covers the project fetch a caller makes when
// it holds a numeric id, as launcher metadata does. Its latestFiles must come
// back digested, or the caller would have to re-fetch every file by id.
func TestModMapsProjectAndPrimesFiles(t *testing.T) {
	f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, cfModBody()) })
	c := clientFor(f)

	mod, err := c.Mod(context.Background(), cfModID)
	if err != nil {
		t.Fatalf("Mod: %v", err)
	}
	if mod.ID != cfModID || mod.Slug != "sodium" || mod.Name != "Sodium" {
		t.Fatalf("project mis-mapped: %+v", mod)
	}
	if mod.GameID != GameIDMinecraft {
		t.Errorf("gameId = %d, want %d", mod.GameID, GameIDMinecraft)
	}
	if len(mod.Categories) != 1 || mod.Categories[0].Name != "Fabric" {
		t.Errorf("categories mis-mapped: %+v", mod.Categories)
	}
	if len(mod.LatestFiles) != 1 {
		t.Fatalf("expected 1 latest file, got %d", len(mod.LatestFiles))
	}

	got := mod.LatestFiles[0]
	if got.SHA1 != cfSHA1Right {
		t.Errorf("sha1 = %q, want %q", got.SHA1, cfSHA1Right)
	}
	if !got.Supports("26.3") {
		t.Error("a 26.3 file does not report support for 26.3")
	}
	if got.DownloadURL == nil || *got.DownloadURL == "" {
		t.Errorf("downloadUrl lost: %+v", got)
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0].RelationType != RelationRequired {
		t.Errorf("dependencies mis-mapped: %+v", got.Dependencies)
	}
}

// TestPicksNewestFileForGameVersion is the mapping that decides what a user
// actually runs. The listing offers a 26.1 build published after the 26.3 one,
// so choosing by date alone installs a jar 26.3 refuses to load.
func TestPicksNewestFileForGameVersion(t *testing.T) {
	f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/files") {
			fmt.Fprint(w, cfFilesBody())
			return
		}
		fmt.Fprint(w, cfModBody())
	})
	c := clientFor(f)

	files, err := c.Files(context.Background(), cfModID)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}

	// This is the selection a caller performs with the exported helpers, so
	// the test states the rule the whole provider rests on: newest first, then
	// the first that supports the game version.
	newestFirst := Newest(files)
	var picked *ModFile
	for _, file := range newestFirst {
		if file.Supports("26.3") {
			picked = &file
			break
		}
	}
	if picked == nil {
		t.Fatalf("no file was selected for 26.3 out of %d", len(files))
	}
	if picked.ID != cfFileRight {
		t.Fatalf("selected file %d (%s), want the 26.3 build %d",
			picked.ID, picked.FileName, cfFileRight)
	}
	if picked.SHA1 != cfSHA1Right {
		t.Errorf("selected file sha1 = %q, want %q", picked.SHA1, cfSHA1Right)
	}

	// The newest entry in the listing is the 26.1 build and must be rejected
	// for 26.3 — that rejection is the whole reason Supports exists.
	if newestFirst[0].ID != cfFileWrongMC {
		t.Fatalf("ordering changed: first is %d, want %d", newestFirst[0].ID, cfFileWrongMC)
	}
	if newestFirst[0].Supports("26.3") {
		t.Error("a build for 26.1 was accepted for 26.3")
	}
}

// TestSupportsRejectsOtherGameVersions covers the version filter on its own,
// including the case-insensitive match a caller relies on when the version
// comes from a directory name such as "26.3-fabric-mod".
func TestSupportsRejectsOtherGameVersions(t *testing.T) {
	file := ModFile{GameVersions: []string{"26.3", "Fabric"}, FileName: "sodium-fabric-0.6.1.jar"}

	if !file.Supports("26.3") {
		t.Error("a file built for 26.3 was rejected for 26.3")
	}
	if file.Supports("26.4") {
		t.Error("a file built for 26.3 was accepted for 26.4")
	}
	if file.Supports("1.21.4") {
		t.Error("a 26.3 file was accepted for an unrelated Minecraft version")
	}
	// A file that names no version at all is evidence of nothing, so it is
	// rejected: installing a build whose target is unknown risks the exact
	// crash the version check exists to prevent. A caller that genuinely wants
	// "unknown means compatible" has to inspect GameVersions itself.
	if (ModFile{FileName: "unlabelled.jar"}).Supports("26.3") {
		t.Error("a file with no version list was accepted")
	}
	// An empty version means "no constraint", which is what the migration path
	// uses for an instance that declares none.
	if !file.Supports("") {
		t.Error("an empty version must not reject a file")
	}
}

// TestSupportsTakesNoLoader pins the shape of the version filter. The loader is
// a project property and asking a file about it was the B25 defect: the
// argument was accepted and discarded, so a Forge build passed a Fabric check
// and the resolver's loader veto quietly stopped applying. The signature itself
// is the guarantee now — a caller cannot pass a loader and read the answer as
// "this is a Fabric build", and the compiler stops the ones who try. What
// replaces it is tested by TestSupportsLoaderUsesProjectCategories.
func TestSupportsTakesNoLoader(t *testing.T) {
	// The loader name still sits in the version list, where CurseForge puts
	// it, and the version check must not be turned into a loader check by
	// accident: it is the project's categories that answer that.
	file := ModFile{GameVersions: []string{"26.3", "Forge"}, FileName: "some-mod-forge.jar"}
	if !file.Supports("26.3") {
		t.Error("a file built for 26.3 was rejected for 26.3")
	}
	if file.Supports("26.2") {
		t.Error("a Forge file was accepted for an unrelated Minecraft version")
	}

	// A caller asking about an instance that declares no version is asking
	// nothing, so the answer is yes and the loader question stays unanswered.
	if !file.Supports("") {
		t.Error("an empty version must not reject a file")
	}
}

// TestSupportsLoaderUsesProjectCategories replaces TestSupportsCannotCheckTheLoader.
//
// That test pinned the defect rather than the contract: it asserted that
// ModFile.Supports keeps answering yes for a loader it never looked at, on the
// grounds that "the caller must filter by project category" — while nothing in
// the API made any caller do that, and the resolver's loader veto, the one
// mechanism that stops modharbor installing a Forge jar into a Fabric instance,
// depended on this function and got nothing. The fix keeps the premise the
// test recorded (the loader is not on the file) and puts the check where the
// data actually is: the project's categories.
func TestSupportsLoaderUsesProjectCategories(t *testing.T) {
	// A Forge-only project is the shape that made the old filter dangerous: its
	// files say nothing about the loader, and only the categories do.
	forgeOnly := Mod{Categories: []Category{{ID: 1, Name: "Forge", Slug: "forge"}}}

	if forgeOnly.SupportsLoader(LoaderFabric) {
		t.Error("a Forge-only project passed a Fabric check")
	}
	if !forgeOnly.SupportsLoader(LoaderForge) {
		t.Error("a Forge project was rejected for Forge")
	}
	// Loader names arrive from user config in whatever case, and an instance
	// directory named "26.3-fabric-mod" supplies them that way.
	if !forgeOnly.SupportsLoader("forge") {
		t.Error("a lower-case loader name was not matched")
	}
	if forgeOnly.SupportsLoader("  NeoForge  ") {
		t.Error("padding was not trimmed before matching")
	}
	// No loader named means no constraint, matching the empty game version.
	if !forgeOnly.SupportsLoader("") {
		t.Error("an empty loader must not reject a project")
	}
	// A loader CurseForge has never heard of is a loader this client cannot
	// vouch for, so it must not be answered yes.
	if forgeOnly.SupportsLoader("rift") {
		t.Error("an unknown loader was reported as supported")
	}
}

// TestLoadersReadsTheProjectCategories covers the identification of a loader
// among a project's categories. The payload mixes loaders with class entries
// ("Library", "Utility") and platform entries, so a name that is not a loader
// has to be dropped rather than reported as one.
func TestLoadersReadsTheProjectCategories(t *testing.T) {
	tests := []struct {
		name string
		cats []Category
		want []string
	}{
		{
			name: "one loader among unrelated categories",
			cats: []Category{
				{ID: 1, Name: "Forge", Slug: "forge"},
				{ID: 6, Name: "NeoForge", Slug: "neoforge"},
				{ID: 999, Name: "Utility", Slug: "utility"},
				{ID: 1000, Name: "Library", Slug: "library"},
			},
			want: []string{LoaderForge, LoaderNeoForge},
		},
		{
			// A project that files itself under several loaders publishes
			// several, and reporting only the first would veto the rest.
			name: "the same loader twice is one answer",
			cats: []Category{
				{ID: 4, Name: "Fabric", Slug: "fabric"},
				{ID: 4, Name: "Fabric", Slug: "fabric"},
			},
			want: []string{LoaderFabric},
		},
		{
			// A loader added after this table was written still answers by
			// name, which is why the name is checked before the id.
			name: "an id the table has never seen",
			cats: []Category{{ID: 31337, Name: "Rift", Slug: "rift"}},
			want: nil,
		},
		{
			// Category ids are only stable while CurseForge keeps its
			// numbering, so the id is the fallback rather than the first test.
			name: "an id that matches with no usable name",
			cats: []Category{{ID: 5, Name: "", Slug: ""}},
			want: []string{LoaderQuilt},
		},
		{name: "no categories at all", cats: nil, want: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Mod{Categories: tc.cats}.Loaders()
			if len(got) != len(tc.want) {
				t.Fatalf("loaders = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("loaders = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestForgeOnlyProjectIsVetoedForAFabricInstance is the whole B25 story as a
// caller experiences it: pick what to install for an instance, using only what
// this package exports.
//
// The fixture is deliberate. The project publishes no loader on its files at
// all, the newest file is built for the wrong Minecraft version, and the
// second newest is the right one. A Fabric instance must end up with nothing,
// because installing the Forge build is what the veto exists to prevent; a Forge
// instance must get the 26.3 build.
func TestForgeOnlyProjectIsVetoedForAFabricInstance(t *testing.T) {
	f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/files") {
			fmt.Fprint(w, `[
			  {"id": 1, "modId": 42, "fileName": "some-mod-1.0.0.jar", "fileDate": "2026-06-01T00:00:00Z",
			   "gameVersions": ["26.1", "Forge"], "fileFingerprints": []},
			  {"id": 2, "modId": 42, "fileName": "some-mod-1.1.0.jar", "fileDate": "2025-01-05T00:00:00Z",
			   "gameVersions": ["26.3", "Forge"], "fileFingerprints": []}
			]`)
			return
		}
		fmt.Fprint(w, `{"id": 42, "name": "Some Mod", "slug": "some-mod",
		  "categories": [{"id": 1, "name": "Forge", "slug": "forge"}], "latestFiles": []}`)
	})
	c := clientFor(f)

	mod, err := c.Mod(context.Background(), 42)
	if err != nil {
		t.Fatalf("Mod: %v", err)
	}
	files, err := c.Files(context.Background(), 42)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}

	pick := func(loader string) *ModFile {
		for _, file := range Newest(files) {
			if mod.SupportsLoader(loader) && file.Supports("26.3") {
				return &file
			}
		}
		return nil
	}

	if got := pick(LoaderFabric); got != nil {
		t.Fatalf("a Forge-only project was installed into a Fabric instance: %+v", got)
	}
	got := pick(LoaderForge)
	if got == nil {
		t.Fatalf("the Forge instance got no file out of %d", len(files))
	}
	if got.ID != 2 || got.FileName != "some-mod-1.1.0.jar" {
		t.Fatalf("chose file %d (%s), want the 26.3 build", got.ID, got.FileName)
	}
}

// ─── digest hydration ────────────────────────────────────────────────────────

// TestFingerprintHydration covers the translation from CurseForge's numeric
// fingerprint list into the digests a caller verifies downloads against.
func TestFingerprintHydration(t *testing.T) {
	t.Run("digests are normalised to lower case", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, cfFileJSON(cfFileRight, "sodium-fabric-0.6.1.jar", "2025-01-05T10:00:00Z", []string{"26.3", "Fabric"}, cfSHA1Upper))
		})
		file, err := clientFor(f).ModFile(context.Background(), cfFileRight)
		if err != nil {
			t.Fatalf("ModFile: %v", err)
		}
		if file.SHA1 != strings.ToLower(cfSHA1Upper) {
			t.Errorf("sha1 = %q, want the digest lowercased", file.SHA1)
		}
		if file.SHA512 != strings.ToLower(cfSHA512) {
			t.Errorf("sha512 = %q, want the digest lowercased", file.SHA512)
		}
		if !file.Supports("26.3") {
			t.Error("the game versions were lost")
		}
	})

	// An MD5 sitting in the fingerprint list must never be mistaken for a
	// SHA-1: a caller would verify a 40-character digest against it and
	// reject every single file it ever saw.
	t.Run("an md5 is not a sha1", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"id": 1, "modId": 1, "fileName": "legacy.jar",
			  "fileFingerprints": [{"algorithm": 2, "value": %q}, {"algorithm": 1, "value": %q}]}`,
				cfMD5, cfSHA1Right)
		})
		file, err := clientFor(f).ModFile(context.Background(), 1)
		if err != nil {
			t.Fatalf("ModFile: %v", err)
		}
		if strings.EqualFold(file.SHA1, cfMD5) {
			t.Fatal("an MD5 fingerprint was published as the SHA-1")
		}
		if file.SHA1 != cfSHA1Right {
			t.Errorf("sha1 = %q, want %q", file.SHA1, cfSHA1Right)
		}
		if file.SHA512 != "" {
			t.Errorf("sha512 = %q, want empty", file.SHA512)
		}
	})

}

// TestSortableGameVersionsDecodeBothShapes covers B23.
//
// CurseForge ships sortableGameVersions as a JSON array of numbers on some
// endpoints and as an array of strings on others. Declared as []string, one
// numeric file anywhere in a listing failed the decode of the whole response,
// which took every readable sibling down with it: the project reported as
// publishing nothing when it published hundreds of files. Both shapes now
// decode, and neither of them costs the caller the id.
func TestSortableGameVersionsDecodeBothShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []VersionID
	}{
		{name: "numbers", body: `[9001, 9002]`, want: []VersionID{"9001", "9002"}},
		{name: "strings", body: `["9001", "9002"]`, want: []VersionID{"9001", "9002"}},
		// A real listing can mix the two, and the client has to keep going
		// rather than pick a side and fail the whole response.
		{name: "mixed", body: `[9001, "9002"]`, want: []VersionID{"9001", "9002"}},
		// A null element says nothing about the file, so it becomes no id at
		// all rather than an id of 0, which is a real CurseForge category.
		{name: "a null element", body: `[9001, null]`, want: []VersionID{"9001", ""}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"id": 4, "modId": 1, "fileName": "numeric.jar",
				  "gameVersions": ["26.3"], "sortableGameVersions": %s, "fileFingerprints": []}`, tc.body)
			})
			file, err := clientFor(f).ModFile(context.Background(), 4)
			if err != nil {
				t.Fatalf("ModFile: %v", err)
			}
			if len(file.SortableGameVersions) != len(tc.want) {
				t.Fatalf("ids = %v, want %v", file.SortableGameVersions, tc.want)
			}
			for i := range tc.want {
				if file.SortableGameVersions[i] != tc.want[i] {
					t.Fatalf("ids = %v, want %v", file.SortableGameVersions, tc.want)
				}
			}
			// The named list is untouched by the tolerant decoding.
			if !file.Supports("26.3") {
				t.Error("the named version was lost")
			}
		})
	}

	// The damage the failure caused was not one file, it was the listing it
	// appeared in: the readable files beside it were dropped as well.
	t.Run("one numeric file no longer poisons the listing", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/files") {
				fmt.Fprint(w, `[{"id": 5, "modId": 1, "fileName": "fine.jar", "gameVersions": ["26.3"],
				  "fileFingerprints": []}, {"id": 4, "modId": 1, "fileName": "numeric.jar",
				  "sortableGameVersions": [9001], "fileFingerprints": []}]`)
				return
			}
			// The id 9001 is not a Minecraft version the table knows, so no
			// extra request is needed to prove the readable file survived.
			fmt.Fprint(w, `[]`)
		})

		files, err := clientFor(f).Files(context.Background(), 1)
		if err != nil {
			t.Fatalf("Files: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("expected both files, got %d: %+v", len(files), files)
		}
		if files[0].FileName != "fine.jar" || !files[0].Supports("26.3") {
			t.Errorf("the readable file was lost: %+v", files[0])
		}
		if files[1].FileName != "numeric.jar" || len(files[1].SortableGameVersions) != 1 {
			t.Errorf("the numeric file was mangled: %+v", files[1])
		}
	})

	// Tolerance has to stop where nonsense begins, or a typo in the API's
	// shape would be read as a version id.
	t.Run("an entry that is neither a number nor a string is an error", func(t *testing.T) {
		for _, body := range []string{`[true]`, `[{"id": 1}]`, `[[1]]`} {
			f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"id": 4, "modId": 1, "sortableGameVersions": %s}`, body)
			})
			if _, err := clientFor(f).ModFile(context.Background(), 4); err == nil {
				t.Errorf("sortableGameVersions %s decoded without an error", body)
			}
		}
	})
}

// TestSortableIDsResolveToVersionNames covers B24.
//
// sortableGameVersions holds ids. Copying them into the named list made every
// entry something that can never equal a Minecraft version — "9001" is not
// "26.3" — so a file that published only ids was rejected for every release
// and the project read as publishing nothing compatible. The ids are resolved
// through CurseForge's version table instead, and the answer is now correct in
// both directions.
func TestSortableIDsResolveToVersionNames(t *testing.T) {
	// 4711 is 26.3; 4 is the Fabric loader category, which the version table
	// does not know about and which the project owns anyway.
	const payload = `{"id": 2, "modId": 1, "fileName": "sortable.jar",
	  "sortableGameVersions": [4711, 4, "4712"], "fileFingerprints": []}`

	f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1"+gameVersionsPath {
			fmt.Fprint(w, `[
			  {"id": 4711, "name": "26.3", "slug": "26.3", "gameId": 432},
			  {"id": 4712, "name": "26.4", "slug": "26.4", "gameId": 432}
			]`)
			return
		}
		fmt.Fprint(w, payload)
	})

	file, err := clientFor(f).ModFile(context.Background(), 2)
	if err != nil {
		t.Fatalf("ModFile: %v", err)
	}

	// A file that genuinely supports 26.3 says so...
	if !file.Supports("26.3") {
		t.Errorf("game versions = %v, want 26.3 resolved from its id", file.GameVersions)
	}
	// ...and a release it never named is still refused. A fix that answered
	// yes to everything would pass the assertion above on its own.
	if file.Supports("26.2") {
		t.Errorf("a version the file never named was accepted: %v", file.GameVersions)
	}
	// Every id the table can resolve is resolved, so a file that targets two
	// releases answers for both rather than only the first.
	if !file.Supports("26.4") {
		t.Errorf("the second id was dropped: game versions = %v", file.GameVersions)
	}
	// The loader category id resolves to nothing, because the version table
	// only knows Minecraft versions. Losing it costs no check: the loader is
	// read from the project's categories.
	for _, v := range file.GameVersions {
		if v == "4" || v == "Fabric" {
			t.Errorf("a loader id leaked into the version names: %v", file.GameVersions)
		}
	}
	// Nothing is thrown away: the ids stay on the file, in the shape the API
	// published them, because they are the only stable identity a version has.
	if len(file.SortableGameVersions) != 3 {
		t.Errorf("ids = %v, want all three preserved", file.SortableGameVersions)
	}
	if file.SortableGameVersions[0] != "4711" {
		t.Errorf("ids = %v, want the raw ids unchanged", file.SortableGameVersions)
	}
}

// TestVersionListIsFetchedOnlyWhenItIsNeeded pins the cost of resolving ids.
//
// The version list is a request, and a migration asks for hundreds of files.
// A client that fetched it eagerly would double the request budget of a whole
// pack; one that fetched it per file would multiply it by the pack size.
func TestVersionListIsFetchedOnlyWhenItIsNeeded(t *testing.T) {
	serve := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1"+gameVersionsPath {
			fmt.Fprint(w, `[{"id": 9001, "name": "26.3", "slug": "26.3", "gameId": 432}]`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/files") {
			fmt.Fprint(w, `[{"id": 5, "modId": 1, "fileName": "named.jar",
			  "gameVersions": ["26.3"], "fileFingerprints": []},
			  {"id": 4, "modId": 1, "fileName": "ids.jar",
			  "sortableGameVersions": [9001], "fileFingerprints": []}]`)
			return
		}
		fmt.Fprint(w, `{"id": 1, "modId": 1, "latestFiles": [{"id": 5, "fileName": "named.jar",
		  "gameVersions": ["26.3"], "fileFingerprints": []}]}`)
	}

	// Names only: this case is about what a well-formed listing costs, so the
	// fixture must contain no id-bearing file. A listing that does contain one
	// legitimately spends a request resolving it, which is the next subtest.
	t.Run("a listing with names costs nothing extra", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/files") {
				fmt.Fprint(w, `[{"id": 5, "modId": 1, "fileName": "a.jar",
				  "gameVersions": ["26.3"], "fileFingerprints": []},
				  {"id": 6, "modId": 1, "fileName": "b.jar",
				  "gameVersions": ["1.20.1"], "fileFingerprints": []}]`)
				return
			}
			fmt.Fprint(w, `{"id": 1, "modId": 1, "latestFiles": []}`)
		})
		if _, err := clientFor(f).Files(context.Background(), 1); err != nil {
			t.Fatalf("Files: %v", err)
		}
		if got := f.only(t); got.Path != "/v1/mods/1/files" {
			t.Fatalf("path = %q, want the listing alone", got.Path)
		}
	})

	t.Run("a listing with ids asks once, however many files", func(t *testing.T) {
		f := newFakeCF(t, serve)
		c := clientFor(f)
		ctx := context.Background()

		files, err := c.Files(ctx, 1)
		if err != nil {
			t.Fatalf("Files: %v", err)
		}
		if len(files) != 2 || !files[1].Supports("26.3") {
			t.Fatalf("the id-bearing file was not resolved: %+v", files)
		}
		// A second listing must not ask again.
		if _, err := c.Files(ctx, 1); err != nil {
			t.Fatalf("Files again: %v", err)
		}
		if _, err := c.GameVersionID(ctx, "26.3"); err != nil {
			t.Fatalf("GameVersionID: %v", err)
		}

		var versionLists int
		for _, req := range f.recorded() {
			if req.Path == "/v1"+gameVersionsPath {
				versionLists++
			}
		}
		if versionLists != 1 {
			t.Fatalf("the version list was fetched %d times, want 1", versionLists)
		}
	})

	// A version list that could not be read must not be remembered: CurseForge
	// answers 500 while it rebuilds an index, and pinning that would leave the
	// client blind to every release for the rest of the run.
	t.Run("a failure is not remembered", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1"+gameVersionsPath {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"id": 4, "modId": 1, "fileName": "ids.jar",
			  "sortableGameVersions": [9001], "fileFingerprints": []}`)
		})
		c := clientFor(f)
		ctx := context.Background()

		if _, err := c.ModFile(ctx, 4); err == nil {
			t.Fatal("a file whose versions could not be read was reported as read")
		}
		f.set(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1"+gameVersionsPath {
				fmt.Fprint(w, `[{"id": 9001, "name": "26.3", "slug": "26.3", "gameId": 432}]`)
				return
			}
			fmt.Fprint(w, `{"id": 4, "modId": 1, "fileName": "ids.jar",
			  "sortableGameVersions": [9001], "fileFingerprints": []}`)
		})
		file, err := c.ModFile(ctx, 4)
		if err != nil {
			t.Fatalf("the retry did not ask again: %v", err)
		}
		if !file.Supports("26.3") {
			t.Errorf("game versions = %v, want 26.3 resolved after the retry", file.GameVersions)
		}
	})
}

// TestNamedVersionsWinOverSortableIDs covers a file that publishes both lists.
// The names win: the ids are a second encoding of the same list, and preferring
// them would replace every readable version with an id that matches nothing.
func TestNamedVersionsWinOverSortableIDs(t *testing.T) {
	f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1"+gameVersionsPath {
			t.Error("the version list was fetched for a file that published names")
			fmt.Fprint(w, `[{"id": 9001, "name": "26.1", "slug": "26.1", "gameId": 432}]`)
			return
		}
		fmt.Fprint(w, `{"id": 3, "modId": 1, "fileName": "both.jar",
		  "gameVersions": ["26.3"], "sortableGameVersions": [9001], "fileFingerprints": []}`)
	})

	file, err := clientFor(f).ModFile(context.Background(), 3)
	if err != nil {
		t.Fatalf("ModFile: %v", err)
	}
	if len(file.GameVersions) != 1 || file.GameVersions[0] != "26.3" {
		t.Fatalf("game versions = %v, want [26.3]", file.GameVersions)
	}
	if !file.Supports("26.3") {
		t.Error("the named version was discarded")
	}
	if file.Supports("26.1") {
		t.Error("the id list overrode the names CurseForge published")
	}
}

// TestNewestOrdersAndCopies covers the ordering helper on the inputs a real
// listing produces.
func TestNewestOrdersAndCopies(t *testing.T) {
	files := []ModFile{
		{ID: 1, FileName: "old.jar", FileDate: "2024-01-01T00:00:00Z"},
		{ID: 2, FileName: "newest.jar", FileDate: "2026-01-01T00:00:00Z"},
		{ID: 3, FileName: "middle.jar", FileDate: "2025-01-01T00:00:00Z"},
		// CurseForge omits fileDate on files published through its legacy
		// pipeline. Such a file must never sort first, because "first" is how
		// a caller chooses what to install.
		{ID: 4, FileName: "undated.jar", FileDate: ""},
		{ID: 5, FileName: "garbage-date.jar", FileDate: "not a timestamp"},
	}
	original := append([]ModFile(nil), files...)

	got := Newest(files)
	for i, want := range []int64{2, 3, 1, 4, 5} {
		if got[i].ID != want {
			t.Fatalf("position %d is %d (%s), want %d", i, got[i].ID, got[i].FileName, want)
		}
	}
	// The caller's slice is reused by resolvers that keep their own ordering;
	// reordering it in place would silently change what they hand back later.
	for i := range files {
		if files[i].ID != original[i].ID {
			t.Fatalf("Newest reordered the caller's slice: %+v", files)
		}
	}
	if len(Newest(nil)) != 0 {
		t.Error("an empty listing must stay empty")
	}
	if n := len(Newest([]ModFile{{ID: 9}})); n != 1 {
		t.Errorf("a single-file listing came back with %d entries", n)
	}
}

// ─── id lookups ──────────────────────────────────────────────────────────────

// TestGameVersionID pins the version → id resolution a caller needs before it
// can ask the rest of the API about a specific Minecraft version. A version the
// API does not know must be an error: id 0 is a real id in the listings, so
// returning it would query about a different game version entirely.
func TestGameVersionID(t *testing.T) {
	body := `[
	  {"id": 4700, "name": "26.1", "slug": "26.1", "gameId": 432},
	  {"id": 4711, "name": "26.3", "slug": "26.3", "gameId": 432},
	  {"id": 4712, "name": "26.3-pre2", "slug": "26.3-pre2", "gameId": 432}
	]`
	f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
	c := clientFor(f)
	ctx := context.Background()

	id, err := c.GameVersionID(ctx, "26.3")
	if err != nil {
		t.Fatalf("GameVersionID: %v", err)
	}
	if id != 4711 {
		t.Fatalf("id = %d, want 4711", id)
	}

	// Padding from a hand-edited config file must not decide the lookup.
	if id, err = c.GameVersionID(ctx, "  26.3  "); err != nil || id != 4711 {
		t.Fatalf("whitespace-padded version: id=%d err=%v", id, err)
	}

	// An exact name only: "26.3" must not match "26.3-pre2", or a user on the
	// release is handed a prerelease's file list.
	id, err = c.GameVersionID(ctx, "26.4")
	if err == nil {
		t.Fatalf("an unknown version resolved to id %d", id)
	}
	if id != 0 {
		t.Errorf("id = %d on failure, want 0", id)
	}
	if !strings.Contains(err.Error(), "26.4") {
		t.Errorf("error %q does not name the version it could not resolve", err)
	}
	if !strings.Contains(err.Error(), "curseforge") {
		t.Errorf("error %q does not say which provider failed", err)
	}
}

// TestLoaderID pins the loader → category mapping. The four known loaders come
// from a table, so a lookup costs no request at all; moving them behind the API
// would add a round trip to every instance modharbor resolves.
func TestLoaderID(t *testing.T) {
	t.Run("known loaders cost no request", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
			t.Error("a hard-coded loader was looked up over the network")
			http.NotFound(w, r)
		})
		c := clientFor(f)
		want := map[string]int64{
			LoaderFabric:   4,
			LoaderForge:    1,
			LoaderNeoForge: 6,
			LoaderQuilt:    5,
		}
		for loader, id := range want {
			// Loader names arrive from user config in whatever case.
			got, err := c.LoaderID(context.Background(), strings.ToLower(loader))
			if err != nil {
				t.Errorf("LoaderID(%q): %v", loader, err)
				continue
			}
			if got != id {
				t.Errorf("LoaderID(%q) = %d, want %d", loader, got, id)
			}
		}
		if n := f.count(); n != 0 {
			t.Fatalf("%d request(s) for %d known loaders", n, len(want))
		}
	})

	t.Run("an unlisted loader falls back to the categories", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[
			  {"id": 1, "name": "Forge", "slug": "forge"},
			  {"id": 7, "name": "Rift", "slug": "rift"}
			]`)
		})
		id, err := clientFor(f).LoaderID(context.Background(), "rift")
		if err != nil {
			t.Fatalf("LoaderID: %v", err)
		}
		if id != 7 {
			t.Fatalf("id = %d, want 7", id)
		}
		if got := f.only(t); got.Path != "/v1/games/432/categories" {
			t.Fatalf("path = %q, want the categories endpoint", got.Path)
		}
	})

	t.Run("a loader CurseForge does not know is an error", func(t *testing.T) {
		f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) })
		c := clientFor(f)

		id, err := c.LoaderID(context.Background(), "bukkit")
		if err == nil {
			t.Fatalf("an unknown loader resolved to id %d", id)
		}
		if id != 0 {
			t.Errorf("id = %d on failure, want 0", id)
		}
		if !strings.Contains(err.Error(), "bukkit") {
			t.Errorf("error %q does not name the loader", err)
		}
	})
}

// ─── downloads ───────────────────────────────────────────────────────────────

// TestDownloadBodyScopesTheAPIKey covers the only requests this client makes
// that are not JSON calls. The key belongs to the API host and nowhere else:
// CurseForge hands out CDN URLs on other domains, and sending the key along to
// those publishes it to whoever runs the host.
func TestDownloadBodyScopesTheAPIKey(t *testing.T) {
	const jar = "PK\x03\x04 not really a jar"
	serve := func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, jar) }

	apiSrv := newFakeCF(t, serve)
	// Stands in for edge.forgecdn.net: same bytes, unrelated host.
	cdnSrv := newFakeCF(t, serve)
	c := clientFor(apiSrv)
	ctx := context.Background()

	body, err := c.DownloadBody(ctx, apiSrv.URL+"/v1/files/4712346/download")
	if err != nil {
		t.Fatalf("API download: %v", err)
	}
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read the API body: %v", err)
	}
	body.Close()
	if string(got) != jar {
		t.Fatalf("body = %q", got)
	}
	// The header is read back out of the stub's request log, which it takes
	// under its own lock; the handler runs on another goroutine.
	apiHeader := apiSrv.only(t).Header
	if apiHeader.Get("x-api-key") != testAPIKey {
		t.Errorf("the API host was asked without the key")
	}
	if apiHeader.Get("User-Agent") != UserAgent {
		t.Errorf("User-Agent = %q", apiHeader.Get("User-Agent"))
	}

	if body, err = c.DownloadBody(ctx, cdnSrv.URL+"/files/4712/346/sodium.jar"); err != nil {
		t.Fatalf("CDN download: %v", err)
	}
	body.Close()
	cdnHeader := cdnSrv.only(t).Header
	if cdnHeader.Get("x-api-key") != "" {
		t.Errorf("the API key was sent to a third-party host: %q", cdnHeader.Get("x-api-key"))
	}
	if cdnHeader.Get("User-Agent") != UserAgent {
		t.Errorf("User-Agent = %q", cdnHeader.Get("User-Agent"))
	}

	// Without a key the header is omitted rather than sent empty: an empty
	// x-api-key is rejected differently from an absent one, and it would show
	// up in a CDN's access log as a broken credential.
	keyless := clientFor(apiSrv, func(o *Options) { o.APIKey = "" })
	if body, err = keyless.DownloadBody(ctx, apiSrv.URL+"/v1/files/1/download"); err != nil {
		t.Fatalf("keyless download: %v", err)
	}
	body.Close()
	keylessHeader := apiSrv.recorded()[len(apiSrv.recorded())-1].Header
	if keylessHeader.Get("x-api-key") != "" {
		t.Errorf("an empty key was sent as a header: %q", keylessHeader.Get("x-api-key"))
	}
}

// TestDownloadBodyErrors covers the download path's failures. It has no message
// from the server to work with, so it must at least say what the status was
// and which URL produced it — the difference between "Sodium will not install"
// and "this URL 403s".
func TestDownloadBodyErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantStatus int
		wantText   string
	}{
		{name: "gone", status: http.StatusNotFound, wantStatus: 404, wantText: "Not Found"},
		{name: "forbidden", status: http.StatusForbidden, wantStatus: 403, wantText: "Forbidden"},
		{name: "server error", status: http.StatusInternalServerError, wantStatus: 500, wantText: "Internal Server Error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCF(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) })
			url := f.URL + "/v1/files/4712346/download"

			body, err := clientFor(f).DownloadBody(context.Background(), url)
			if err == nil {
				body.Close()
				t.Fatalf("status %d was reported as success", tc.status)
			}
			var cfErr *Error
			if !errors.As(err, &cfErr) {
				t.Fatalf("error %v is not a *Error", err)
			}
			if cfErr.StatusCode != tc.wantStatus {
				t.Errorf("StatusCode = %d, want %d", cfErr.StatusCode, tc.wantStatus)
			}
			if cfErr.URL != url {
				t.Errorf("URL = %q, want %q", cfErr.URL, url)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("message %q does not explain the status", err)
			}
		})
	}

	t.Run("a malformed url never leaves the process", func(t *testing.T) {
		f := newFakeCF(t, nil)
		body, err := clientFor(f).DownloadBody(context.Background(), "://not-a-url")
		if err == nil {
			body.Close()
			t.Fatal("expected an error")
		}
	})

	t.Run("a refused connection is reported", func(t *testing.T) {
		f := newFakeCF(t, nil)
		dead := f.URL
		f.Close()

		if _, err := clientFor(f).DownloadBody(context.Background(), dead+"/v1/files/1/download"); err == nil {
			t.Fatal("expected a transport error")
		}
	})
}
