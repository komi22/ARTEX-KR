// Package traffic implements the request-recording subsystem (docs §10): an
// embedded go-mitmproxy proxy whose addon writes every target HTTP exchange into
// a human-browsable file tree, with a sidecar SQLite index for paged queries.
// Full capture, plaintext (no redaction), target HTTP only.
package traffic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
	_ "modernc.org/sqlite"
)

const indexSchema = `
CREATE TABLE IF NOT EXISTS exchanges (
  id           TEXT PRIMARY KEY,
  ts           INTEGER,
  host         TEXT,
  method       TEXT,
  url_template TEXT,
  url          TEXT,
  status       INTEGER,
  content_type TEXT,
  req_len      INTEGER,
  resp_len     INTEGER,
  path         TEXT
);
CREATE INDEX IF NOT EXISTS idx_ex_host ON exchanges(host);
CREATE INDEX IF NOT EXISTS idx_ex_tmpl ON exchanges(host, url_template);
CREATE INDEX IF NOT EXISTS idx_ex_ts   ON exchanges(ts);

CREATE TABLE IF NOT EXISTS exchange_bodies (
  id        TEXT PRIMARY KEY,
  req_head  TEXT NOT NULL DEFAULT '',
  req_body  BLOB,
  req_blob  TEXT,
  resp_head TEXT NOT NULL DEFAULT '',
  resp_body BLOB,
  resp_blob TEXT
);

CREATE TABLE IF NOT EXISTS blob_refs (
  hash        TEXT NOT NULL,
  exchange_id TEXT NOT NULL,
  PRIMARY KEY (hash, exchange_id)
);
CREATE INDEX IF NOT EXISTS idx_blob_refs_ex ON blob_refs(exchange_id);
`

// ftsSchema is applied separately from indexSchema: a driver build without FTS5
// must degrade to "no full-text search" rather than take the whole recorder down.
// trigram (not the default unicode61) is required for two reasons this subsystem
// depends on: it matches arbitrary substrings — "ssw0r" finds "P@ssw0rd" — and it
// handles CJK, which unicode61 does not tokenize. Contentless (content=”) keeps
// only the index, since the text itself lives in exchange_bodies; contentless_delete
// lets rows be deleted without replaying the original text back in.
const ftsSchema = `CREATE VIRTUAL TABLE IF NOT EXISTS ex_fts USING fts5(
  content, tokenize='trigram', content='', contentless_delete=1
);`

const (
	// maxInlineBody is the cutoff between a body stored inline (SQLite column) and
	// one spilled to the content-addressed blob store.
	maxInlineBody = 256 * 1024
	// blobPreview is how much of a spilled body stays inline so readers can tell
	// what it is (JSON shape, SQL dump header, ZIP magic) without fetching it.
	blobPreview = 8 * 1024
	// maxIndexBody caps a single body's contribution to the full-text index. Text
	// is indexed in full below this; binary never reaches the index at all.
	maxIndexBody = 4 * 1024 * 1024
	// minTrigram is the shortest term the trigram tokenizer can match; shorter
	// queries fall back to metadata LIKE.
	minTrigram = 3
	// maxBlobRead caps one traffic_blob read so paging through a large body never
	// floods the agent's context.
	maxBlobRead = 8 * 1024
)

// TrafficSearchDescription is persisted into the tool catalog for new and
// upgraded installations. Keep it in the traffic package so the runtime tool
// and the catalog migration cannot drift apart.
const TrafficSearchDescription = "기록 프록시가 수집한 대상 트래픽을 검색합니다. host에는 호스트명, 호스트:포트 또는 전체 URL을 지정할 수 있습니다. URL 일부나 본문 키워드로 결과를 필터링할 수 있습니다. 포트를 지정하면 같은 IP의 다른 포트에서 수집한 트래픽은 제외합니다. body_contains는 수집한 요청과 응답의 헤더 및 본문을 검색하며, 3자 이상의 다국어 문자열과 일반 부분 문자열을 지원합니다. 응답 본문 없이 id, method, url, status, resp_len으로 구성된 간단한 목록만 반환합니다. 결과를 찾았다면 traffic_get으로 각 요청과 응답을 확인한 뒤, 실제로 취약점을 뒷받침하는 트래픽 ID만 bind_finding_traffic에 전달하세요. 기본값은 3건이며 페이지당 최대 10건까지 조회할 수 있습니다. 결과가 많으면 page를 0부터 늘려가며 확인하세요."

// Traffic runs the recording proxy and owns the file tree + index.
type Traffic struct {
	dir   string
	addr  string
	db    *sql.DB
	wmu   sync.Mutex // serializes record() vs DeleteHost (incl. blob GC)
	seq   atomic.Int64
	proxy *mproxy.Proxy
	// fts reports whether the full-text index is available. False on a driver
	// build without FTS5: recording and metadata search still work, body search
	// degrades to unsupported rather than erroring.
	fts bool
	// reaping tracks background reclamation of legacy trees, so shutdown and tests
	// can wait for it instead of racing it.
	reaping sync.WaitGroup
	// pass is the set of hosts whose MITM interception failed for a proxy/protocol
	// reason; connections to them are tunneled transparently (fail-open) so the
	// request still reaches the target — unrecorded — instead of being killed.
	pass sync.Map // hostname(string) -> struct{}
	// upstream is the global egress proxy every captured request is forwarded
	// through (nil = dial targets directly). Both the intercepted and the
	// transparent-passthrough paths honor it (go-mitmproxy's getUpstreamConn),
	// so no host escapes it. Hot-swappable at runtime via SetUpstreamProxy.
	upstream atomic.Pointer[url.URL]
}

// Open initializes the traffic tree, blob store and SQLite index under dir.
func Open(dir, addr string) (*Traffic, error) {
	for _, d := range []string{dir, filepath.Join(dir, "_index"), filepath.Join(dir, "_blobs"), filepath.Join(dir, "_ca")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "_index", "index.sqlite"))
	if err != nil {
		return nil, err
	}
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(indexSchema); err != nil {
		db.Close()
		return nil, err
	}
	t := &Traffic{dir: dir, addr: addr, db: db}
	if _, err := db.Exec(ftsSchema); err != nil {
		log.Printf("[traffic] 전체 텍스트 인덱싱을 사용할 수 없으며 텍스트 검색이 비활성화됩니다(메타데이터 검색은 영향을 받지 않음): %v", err)
	} else {
		t.fts = true
	}

	p, err := mproxy.NewProxy(&mproxy.Options{
		Addr:        addr,
		SslInsecure: true,
		CaRootPath:  filepath.Join(dir, "_ca"),
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	// Upstream selection. By default (no global egress proxy set) targets are
	// dialed DIRECTLY: go-mitmproxy's own default upstream uses
	// http.ProxyFromEnvironment, so an HTTP_PROXY/HTTPS_PROXY in the environment
	// (a VPN/system proxy) would make it forward target requests through that
	// external proxy — which can't reach the target → 502. Returning nil forces
	// a direct dial. When a global egress proxy IS configured (SetUpstreamProxy),
	// every captured request — intercepted AND transparently tunneled — is
	// forwarded through it instead, so no host leaks the real source IP.
	p.SetUpstreamProxy(func(*http.Request) (*url.URL, error) { return t.upstream.Load(), nil })
	// Fail-open: MITM every host by default, EXCEPT ones a prior request proved we
	// can't intercept without breaking (see maybePassthrough). Those are tunneled
	// transparently so the request still reaches the target instead of being killed.
	p.SetShouldInterceptRule(func(req *http.Request) bool {
		_, tunnel := t.pass.Load(hostOnly(req.Host))
		return !tunnel
	})
	p.AddAddon(&sink{t: t})
	t.proxy = p
	return t, nil
}

// hostOnly strips an optional :port, so passthrough keys match whether the host
// arrives as "example.com:443" (CONNECT) or "example.com" (request URL).
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// ProxyAddr returns the address workers should set as HTTP(S)_PROXY. A bare
// ":port" means "bind all interfaces" (legacy default), so map it to loopback.
func (t *Traffic) ProxyAddr() string {
	if strings.HasPrefix(t.addr, ":") {
		return "http://127.0.0.1" + t.addr
	}
	return "http://" + t.addr
}

// SetUpstreamProxy points every captured request at a global egress proxy
// (http/https/socks5, optional user:pass in the URL). An empty raw string clears
// it, restoring direct dialing. The change is atomic and takes effect on the next
// connection — no restart, no proxy rebuild. go-mitmproxy dials all three schemes
// itself, so socks5 works uniformly here regardless of the target tool.
func (t *Traffic) SetUpstreamProxy(raw string) error {
	if strings.TrimSpace(raw) == "" {
		t.upstream.Store(nil)
		return nil
	}
	u, err := ValidateProxyURL(raw)
	if err != nil {
		return err
	}
	t.upstream.Store(u)
	return nil
}

// ValidateProxyURL parses and checks a proxy URL (http/https/socks5, optional
// user:pass), returning the parsed URL. Exposed so callers can validate a global
// proxy before persisting it even when the traffic proxy itself is disabled.
func ValidateProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("프록시 주소 %q 확인: %w", raw, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	case "":
		return nil, fmt.Errorf("프록시 %q에 프로토콜이 부족합니다(http://, https:// 또는 Socks5:// 사용)", raw)
	default:
		return nil, fmt.Errorf("지원하지 않는 프록시 프로토콜 %q입니다. http, https 또는 socks5를 사용하세요", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("프록시 %q에 호스트 주소가 없습니다", raw)
	}
	return u, nil
}

// CACertPath returns the PEM CA cert clients must trust to verify HTTPS through
// the MITM proxy (go-mitmproxy writes it here on first start).
func (t *Traffic) CACertPath() string {
	return filepath.Join(t.dir, "_ca", "mitmproxy-ca-cert.pem")
}

// Start runs the proxy (blocking); run in a goroutine.
func (t *Traffic) Start() error { return t.proxy.Start() }

// Close waits for background tree reclamation to finish before closing the
// index, so shutdown never leaves a goroutine unlinking files out from under a
// removed data directory.
func (t *Traffic) Close() error {
	t.reaping.Wait()
	return t.db.Close()
}
func (t *Traffic) DB() *sql.DB { return t.db }

// sink is the go-mitmproxy addon that records completed exchanges.
type sink struct {
	mproxy.BaseAddon
	t *Traffic
}

func (s *sink) Response(f *mproxy.Flow) {
	if f.Request == nil || f.Response == nil {
		return
	}
	s.t.record(f)
}

// RequestError fires when a request through an established MITM tunnel fails. If
// the failure looks proxy/protocol-caused (h2 quirks, HEAD-with-body, protocol
// errors) — not a plain target-unreachable error — we flag the host for
// transparent passthrough so future requests to it succeed instead of dying.
func (s *sink) RequestError(f *mproxy.Flow, err error) { s.t.maybePassthrough(f, err) }

// maybePassthrough marks a host to be tunneled transparently on the next
// connection, but only for errors the proxy itself caused — a target that is
// simply down/filtered would fail without us too, and must stay MITM'd+recorded.
func (t *Traffic) maybePassthrough(f *mproxy.Flow, err error) {
	if err == nil || f == nil || f.Request == nil || f.Request.URL == nil || !proxyCausedErr(err) {
		return
	}
	host := f.Request.URL.Hostname()
	if host == "" {
		return
	}
	if _, loaded := t.pass.LoadOrStore(host, struct{}{}); !loaded {
		log.Printf("[트래픽] %s에 MITM 오류가 발생했습니다. 투명 전송으로 변경되었습니다(이후 호스트는 대상에 직접 연결되어 더 이상 기록되지 않지만 요청은 평소대로 계속됩니다): %v", host, err)
	}
}

// proxyCausedErr reports whether err indicates the interception layer (not the
// target) is at fault — HTTP/2 handling, HEAD-with-body, or a protocol violation.
func proxyCausedErr(err error) bool {
	s := strings.ToLower(err.Error())
	for _, p := range []string{"head request", "http2", "http/2", "protocol error", "protocol_error", "malformed"} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// record persists one exchange entirely in SQLite: metadata, bodies and the
// full-text index. Nothing is written to a per-request directory — only bodies
// above maxInlineBody spill to the content-addressed blob store.
func (t *Traffic) record(f *mproxy.Flow) {
	// The write lock covers blob + index writes, so DeleteHost (and its blob GC)
	// can run under the same lock without racing a concurrent record.
	t.wmu.Lock()
	defer t.wmu.Unlock()
	host := f.Request.URL.Hostname()
	method := f.Request.Method
	tmpl := db.TemplatePath(f.Request.URL.EscapedPath())
	n := t.seq.Add(1)
	now := time.Now()
	id := fmt.Sprintf("%d-%04d", now.Unix(), n%10000)

	ct := f.Response.Header.Get("Content-Type")
	url := f.Request.URL.String()
	reqHead := fmt.Sprintf("%s %s %s\n%s", method, f.Request.URL.RequestURI(), f.Request.Proto, requestHeaderLines(f.Request))
	respHead := fmt.Sprintf("HTTP %d\n%s", f.Response.StatusCode, headerLines(f.Response.Header))
	// Bodies are spilled before the transaction opens: blob writes are filesystem
	// work and must not sit inside the SQLite write lock.
	reqB := t.spill(f.Request.Body, f.Request.Header.Get("Content-Type"))
	respB := t.spill(f.Response.Body, ct)

	tx, err := t.db.Begin()
	if err != nil {
		log.Printf("[트래픽] %s 로깅 실패(오픈 트랜잭션): %v", url, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// path stays empty for db-resident exchanges; a non-empty path marks a legacy
	// row whose bodies still live in the old on-disk tree (see Get).
	res, err := tx.Exec(`INSERT OR REPLACE INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,'')`,
		id, now.Unix(), host, method, tmpl, url, f.Response.StatusCode, ct,
		len(f.Request.Body), len(f.Response.Body))
	if err != nil {
		log.Printf("[트래픽] %s 로깅 실패(인덱스 쓰기): %v", url, err)
		return
	}
	rowid, err := res.LastInsertId()
	if err != nil {
		log.Printf("[traffic] %s 로그 실패(rowid 가져오기): %v", url, err)
		return
	}

	if _, err := tx.Exec(`INSERT OR REPLACE INTO exchange_bodies(id,req_head,req_body,req_blob,resp_head,resp_body,resp_blob)
VALUES(?,?,?,?,?,?,?)`,
		id, reqHead, reqB.inline, nullIfEmpty(reqB.hash), respHead, respB.inline, nullIfEmpty(respB.hash)); err != nil {
		log.Printf("[traffic] %s 기록 실패(텍스트 쓰기): %v", url, err)
		return
	}

	for _, h := range []string{reqB.hash, respB.hash} {
		if h == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO blob_refs(hash,exchange_id) VALUES(?,?)`, h, id); err != nil {
			log.Printf("[트래픽] %s 기록 실패(BLOB 참조 등록): %v", url, err)
			return
		}
	}

	if t.fts {
		// The index is fed from memory, so a body that spilled to _blobs is still
		// fully searchable even though only its preview is stored inline.
		idx := strings.Join([]string{url, reqHead, reqB.index, respHead, respB.index}, "\n")
		if _, err := tx.Exec(`INSERT INTO ex_fts(rowid,content) VALUES(?,?)`, rowid, idx); err != nil {
			log.Printf("[트래픽] %s 로그 실패(전체 텍스트 인덱스 쓰기): %v", url, err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[트래픽] %s 로깅 실패(커밋): %v", url, err)
	}
}

// storedBody is one body after the inline/spill decision: inline is what goes in
// the SQLite column (the whole body, or a preview when spilled), hash names the
// blob when it spilled, and index is the text handed to FTS (empty for binary).
type storedBody struct {
	inline []byte
	hash   string
	index  string
}

// spill decides where one body lives. Small bodies stay inline. Large ones are
// written to the content-addressed store and keep a readable preview inline so a
// reader can identify them without fetching the blob. Either way, text bodies
// are handed to the full-text index in full (up to maxIndexBody) — indexing is
// independent of where the bytes end up.
func (t *Traffic) spill(body []byte, contentType string) storedBody {
	if len(body) == 0 {
		return storedBody{}
	}
	text := !isBinaryBody(contentType, body)
	indexText := func() string {
		if !text {
			return ""
		}
		return string(clipBytes(body, maxIndexBody))
	}
	if len(body) <= maxInlineBody {
		return storedBody{inline: body, index: indexText()}
	}

	sum := sha256.Sum256(body)
	h := hex.EncodeToString(sum[:])
	// One bucket level (256 buckets) is enough to keep any single directory small;
	// the store only ever holds bodies above maxInlineBody, deduplicated by hash.
	blobDir := filepath.Join(t.dir, "_blobs", "sha256", h[:2])
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		log.Printf("[traffic] Blob 디렉터리를 생성하지 못했습니다: %v", err)
		return storedBody{inline: clipBytes(body, blobPreview), index: indexText()}
	}
	blobPath := filepath.Join(blobDir, h+".bin")
	if _, err := os.Stat(blobPath); os.IsNotExist(err) {
		if err := os.WriteFile(blobPath, body, 0o644); err != nil {
			log.Printf("[traffic] blob %s 쓰기 실패: %v", h, err)
			return storedBody{inline: clipBytes(body, blobPreview), index: indexText()}
		}
	}
	sb := storedBody{hash: h, index: indexText()}
	if text {
		sb.inline = []byte(truncateUTF8(body, blobPreview))
	} else {
		sb.inline = []byte(binaryTag(contentType, body))
	}
	return sb
}

// binaryTypes are content-type prefixes whose bodies are never worth indexing or
// previewing as text. Anything not listed is treated as text (with a NUL-byte
// check as backstop), so unusual-but-searchable types like application/sql or a
// bare text/* are not silently dropped from the index.
var binaryTypes = []string{
	"image/", "audio/", "video/", "font/",
	"application/octet-stream", "application/zip", "application/gzip",
	"application/x-gzip", "application/x-tar", "application/x-7z-compressed",
	"application/x-rar", "application/pdf", "application/x-msdownload",
	"application/vnd.android.package-archive", "application/java-archive",
	"application/wasm", "application/x-shockwave-flash",
}

func isBinaryBody(contentType string, body []byte) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	for _, p := range binaryTypes {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	// Backstop for mislabeled or absent content types: real text does not carry
	// NUL bytes, so a NUL in the head of the body means binary regardless.
	return bytes.IndexByte(clipBytes(body, 512), 0) >= 0
}

// binaryTag describes a spilled binary body in one line, including the leading
// magic bytes so a reader can recognize the format without downloading it.
func binaryTag(contentType string, body []byte) string {
	ct := strings.TrimSpace(contentType)
	if ct == "" {
		ct = "application/octet-stream"
	}
	magic := hex.EncodeToString(clipBytes(body, 4))
	return fmt.Sprintf("[binary %s, %d bytes, magic=%s]", ct, len(body), magic)
}

func clipBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}

// truncateUTF8 cuts b to at most n bytes without splitting a multi-byte rune,
// so a preview never ends in half a CJK character.
func truncateUTF8(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	b = b[:n]
	// A rune is at most 4 bytes, so backing off 3 bytes always finds the boundary
	// (unless the input was already invalid UTF-8, in which case we keep the cut).
	for i := 0; i < utf8.UTFMax-1 && len(b) > 0; i++ {
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return string(b)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func headerLines(h map[string][]string) string {
	var b strings.Builder
	for k, vs := range h {
		for _, v := range vs {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// requestHeaderLines restores the HTTP Host header, which net/http stores on
// Request.Host rather than in Header. Keeping it in request.http makes the raw
// capture complete and directly replayable.
func requestHeaderLines(req *mproxy.Request) string {
	headers := req.Header.Clone()
	headers.Del("Host")
	host := req.URL.Host
	if raw := req.Raw(); raw != nil && strings.TrimSpace(raw.Host) != "" {
		host = raw.Host
	}
	var b strings.Builder
	if host = strings.TrimSpace(host); host != "" {
		b.WriteString("Host: ")
		b.WriteString(host)
		b.WriteByte('\n')
	}
	b.WriteString(headerLines(headers))
	return b.String()
}

func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "?", "_", "*", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	out := r.Replace(s)
	if out == "" || out == "_" {
		return "root"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

var blobHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// blobPath locates a stored blob. Current writes use one bucket level; blobs
// written before that change used two, so both layouts are probed rather than
// migrated.
func (t *Traffic) blobPath(hash string) (string, error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	// Validated as pure hex before touching the filesystem, so a crafted hash can
	// never traverse out of the blob directory.
	if !blobHashRe.MatchString(hash) {
		return "", fmt.Errorf("불법적인 blob 해시")
	}
	for _, p := range []string{
		filepath.Join(t.dir, "_blobs", "sha256", hash[:2], hash+".bin"),
		filepath.Join(t.dir, "_blobs", "sha256", hash[:2], hash[2:4], hash+".bin"),
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s 블롭이 존재하지 않습니다", hash)
}

// Blob opens a spilled body for streaming; the caller must close the file.
// Streaming matters here: the driver exposes no incremental BLOB API, so keeping
// large bodies on disk is what lets them be served without loading them whole.
func (t *Traffic) Blob(hash string) (*os.File, int64, error) {
	p, err := t.blobPath(hash)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

// BlobRange reads at most length bytes of a blob from offset, and reports the
// blob's total size. Used by the agent tool to page through a large body without
// pulling all of it into the model's context.
func (t *Traffic) BlobRange(hash string, offset, length int64) (data []byte, total int64, err error) {
	f, size, err := t.Blob(hash)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	if offset < 0 {
		offset = 0
	}
	if offset >= size {
		return nil, size, nil
	}
	if length <= 0 || offset+length > size {
		length = size - offset
	}
	buf := make([]byte, length)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, size, err
	}
	return buf[:n], size, nil
}

// ExchangeMeta is one row of the index (returned by Search).
type ExchangeMeta struct {
	ID          string `json:"id"`
	TS          int64  `json:"ts"`
	Host        string `json:"host"`
	Method      string `json:"method"`
	URLTemplate string `json:"url_template"`
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	RespLen     int    `json:"resp_len"`
	Path        string `json:"path"`
}

// Search returns paged exchange metadata (never bodies).
func (t *Traffic) Search(host string, page, size int) ([]ExchangeMeta, error) {
	if size <= 0 || size > 500 {
		size = 100
	}
	q := `SELECT id,ts,host,method,url_template,url,status,content_type,resp_len,path FROM exchanges`
	args := []any{}
	if host != "" {
		q += ` WHERE host=?`
		args = append(args, host)
	}
	q += ` ORDER BY ts DESC LIMIT ? OFFSET ?`
	args = append(args, size, page*size)
	rows, err := t.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExchangeMeta
	for rows.Next() {
		var m ExchangeMeta
		if err := rows.Scan(&m.ID, &m.TS, &m.Host, &m.Method, &m.URLTemplate, &m.URL, &m.Status, &m.ContentType, &m.RespLen, &m.Path); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ftsFilter builds the SQL condition restricting rows to those whose indexed
// text matches term. ok is false when full-text search cannot serve the term —
// no FTS index on this build, or fewer than three characters, which the trigram
// tokenizer cannot match — leaving callers to fall back to metadata matching.
func (t *Traffic) ftsFilter(term string) (cond string, arg any, ok bool) {
	term = strings.TrimSpace(term)
	if !t.fts || utf8.RuneCountInString(term) < minTrigram {
		return "", nil, false
	}
	return `rowid IN (SELECT rowid FROM ex_fts WHERE ex_fts MATCH ?)`, ftsQuote(term), true
}

// ftsQuote wraps a term as an FTS5 string literal so that punctuation and query
// operators inside it (quotes, AND/OR/NEAR, *, ^) are matched literally instead
// of being parsed as query syntax.
func ftsQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// Page returns one page of exchange metadata filtered by an optional host
// substring, exact method, and a free-text query q that matches across the
// indexed metadata columns (host/url/method/content-type/status) and, when the
// term is long enough for the trigram index, across captured request/response
// text as well. Also returns the total number of rows matching that filter (for
// the UI's pagination). Newest first. Bodies are never included in the rows.
func (t *Traffic) Page(host, method, q string, page, size int) (rows []ExchangeMeta, total int, err error) {
	if size <= 0 || size > 500 {
		size = 100
	}
	if page < 0 {
		page = 0
	}
	where := ""
	var args []any
	add := func(cond string, vs ...any) {
		if where == "" {
			where = " WHERE "
		} else {
			where += " AND "
		}
		where += cond
		args = append(args, vs...)
	}
	if h := strings.TrimSpace(host); h != "" {
		add("host LIKE ?", "%"+h+"%")
	}
	if m := strings.TrimSpace(method); m != "" {
		add("method=?", strings.ToUpper(m))
	}
	if s := strings.TrimSpace(q); s != "" {
		like := "%" + s + "%"
		const meta = "host LIKE ? OR url LIKE ? OR url_template LIKE ? OR method LIKE ? OR content_type LIKE ? OR CAST(status AS TEXT) LIKE ?"
		// Metadata match OR full-text match: one search box, widest recall. Terms
		// too short for trigram silently fall back to metadata only.
		if cond, arg, ok := t.ftsFilter(s); ok {
			add("(("+meta+") OR "+cond+")", like, like, like, like, like, like, arg)
		} else {
			add("("+meta+")", like, like, like, like, like, like)
		}
	}
	if err = t.db.QueryRow(`SELECT COUNT(*) FROM exchanges`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	sel := `SELECT id,ts,host,method,url_template,url,status,content_type,resp_len,path FROM exchanges` +
		where + ` ORDER BY ts DESC LIMIT ? OFFSET ?`
	qargs := append(append([]any{}, args...), size, page*size)
	rs, err := t.db.Query(sel, qargs...)
	if err != nil {
		return nil, 0, err
	}
	defer rs.Close()
	for rs.Next() {
		var m ExchangeMeta
		if err := rs.Scan(&m.ID, &m.TS, &m.Host, &m.Method, &m.URLTemplate, &m.URL, &m.Status, &m.ContentType, &m.RespLen, &m.Path); err != nil {
			return nil, 0, err
		}
		rows = append(rows, m)
	}
	return rows, total, rs.Err()
}

// Get returns the full request/response text of one exchange. Bodies come from
// the database; rows recorded before bodies moved into SQLite carry a non-empty
// path and are read from the legacy on-disk tree instead, so history stays
// readable without being migrated.
func (t *Traffic) Get(id string) (req, resp string, err error) {
	var reqHead, respHead string
	var reqBody, respBody []byte
	var reqBlob, respBlob sql.NullString
	var reqLen, respLen int
	err = t.db.QueryRow(`SELECT b.req_head,b.req_body,b.req_blob,b.resp_head,b.resp_body,b.resp_blob,e.req_len,e.resp_len
FROM exchange_bodies b JOIN exchanges e ON e.id=b.id WHERE b.id=?`, id).
		Scan(&reqHead, &reqBody, &reqBlob, &respHead, &respBody, &respBlob, &reqLen, &respLen)
	if err == nil {
		return assembleRaw(reqHead, reqBody, reqBlob, reqLen), assembleRaw(respHead, respBody, respBlob, respLen), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}

	var rel string
	if err = t.db.QueryRow(`SELECT path FROM exchanges WHERE id=?`, id).Scan(&rel); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(rel) == "" {
		return "", "", fmt.Errorf("%s 교환에 텍스트 레코드가 없습니다", id)
	}
	rb, _ := os.ReadFile(filepath.Join(t.dir, rel, "request.http"))
	pb, _ := os.ReadFile(filepath.Join(t.dir, rel, "response.http"))
	return string(rb), string(pb), nil
}

// assembleRaw rebuilds one side's raw HTTP text: head, blank line, body. A body
// that spilled to the blob store shows its inline preview followed by the blob
// pointer, so the reader can see what it is and fetch the rest by hash.
func assembleRaw(head string, body []byte, blob sql.NullString, total int) string {
	var b strings.Builder
	b.WriteString(head)
	b.WriteByte('\n')
	b.Write(body)
	if blob.Valid && blob.String != "" {
		fmt.Fprintf(&b, "\n…[truncated] @blob sha256:%s (len=%d)", blob.String, total)
	}
	return b.String()
}

// HostCount is one distinct recorded host plus its exchange count.
type HostCount struct {
	Host  string `json:"host"`
	Count int    `json:"count"`
}

// Hosts returns distinct recorded hosts with exchange counts, most recent
// activity first — powers the page's target picker.
func (t *Traffic) Hosts() ([]HostCount, error) {
	rows, err := t.db.Query(`SELECT host, COUNT(*) AS n, MAX(ts) AS last FROM exchanges GROUP BY host ORDER BY last DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostCount
	for rows.Next() {
		var h HostCount
		var last int64
		if err := rows.Scan(&h.Host, &h.Count, &last); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Count returns total recorded exchanges.
func (t *Traffic) Count() (int, error) {
	var n int
	err := t.db.QueryRow(`SELECT COUNT(*) FROM exchanges`).Scan(&n)
	return n, err
}

// DeleteHost removes every recorded exchange whose host contains the given
// substring — mirroring the UI's host filter, so what you filtered is what gets
// deleted. Everything lives in SQLite, so the deletion is one transaction across
// the index, bodies, full-text index and blob references. Content-addressed
// blobs are shared across hosts and so are garbage-collected afterwards rather
// than deleted by host. Returns rows deleted.
func (t *Traffic) DeleteHost(host string) (int64, error) {
	like := "%" + host + "%"
	t.wmu.Lock()
	defer t.wmu.Unlock()
	// Resolved before the rows go away: with a substring match, the index is the
	// only record of which host directories the filter actually hit.
	legacy, err := t.hostTrees(`host LIKE ?`, like)
	if err != nil {
		return 0, err
	}
	tx, err := t.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	n, err := t.deleteWhere(tx, `host LIKE ?`, like)
	if err != nil {
		return 0, err
	}
	// Staged before the commit so a filesystem failure can still abort the whole
	// deletion, and before gcBlobs so reference collection sees the correct live
	// set — a staged tree is invisible to it.
	stageDir, moves, err := t.stageTrees(legacy)
	if err != nil {
		return 0, errors.Join(err, restoreTrees(stageDir, moves))
	}
	if err := tx.Commit(); err != nil {
		return 0, errors.Join(fmt.Errorf("트래픽 인덱스 삭제 제출: %w", err), restoreTrees(stageDir, moves))
	}
	t.reapStage(stageDir)
	if n > 0 {
		if err := t.gcBlobs(); err != nil {
			return n, err
		}
	}
	return n, nil
}

// deleteWhere removes every trace of the exchanges matching the condition:
// full-text index rows, bodies, blob references, and finally the index rows.
// Order matters — each sub-select reads exchanges, so that table is emptied last.
func (t *Traffic) deleteWhere(tx *sql.Tx, where string, args ...any) (int64, error) {
	if t.fts {
		// ex_fts is contentless and addressed by rowid, hence the rowid sub-select.
		if _, err := tx.Exec(`DELETE FROM ex_fts WHERE rowid IN (SELECT rowid FROM exchanges WHERE `+where+`)`, args...); err != nil {
			return 0, fmt.Errorf("전체 텍스트 색인 삭제: %w", err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM exchange_bodies WHERE id IN (SELECT id FROM exchanges WHERE `+where+`)`, args...); err != nil {
		return 0, fmt.Errorf("텍스트 삭제: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM blob_refs WHERE exchange_id IN (SELECT id FROM exchanges WHERE `+where+`)`, args...); err != nil {
		return 0, fmt.Errorf("BLOB 참조 삭제: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM exchanges WHERE `+where, args...)
	if err != nil {
		return 0, fmt.Errorf("색인 행 삭제: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// hostTrees returns the on-disk directory of every host matching the condition.
// Exchanges recorded since bodies moved into SQLite have no directory at all, so
// most of these paths simply will not exist — staging skips those. The lookup is
// deliberately not restricted to rows with a path: a host whose index rows are
// already gone can still have an orphaned directory, and deleting the host should
// take that with it.
func (t *Traffic) hostTrees(where string, args ...any) ([]string, error) {
	rows, err := t.db.Query(`SELECT DISTINCT host FROM exchanges WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dirs []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		dirs = append(dirs, filepath.Join(t.dir, sanitize(h)))
	}
	return dirs, rows.Err()
}

type stagedTrafficPath struct {
	source string
	staged string
}

type hostDeleteStageJournal struct {
	Version   int                   `json:"version"`
	ArchiveID int64                 `json:"archive_id,omitempty"`
	TaskID    int64                 `json:"task_id,omitempty"`
	Hosts     []string              `json:"hosts,omitempty"`
	Moves     []hostDeleteStageMove `json:"moves"`
}

type hostDeleteStageMove struct {
	Source string `json:"source"`
	Staged string `json:"staged"`
}

const hostDeleteStageJournalName = "journal.json"

// stageTrees moves host directories aside onto the same filesystem. The rename is
// atomic and instant, which buys two things the unlink cannot: the deletion stays
// reversible until the transaction commits, and the tree stops being visible to
// blob reference collection right away (legacyBlobRefs skips underscore-prefixed
// directories, so anything under _delete_staging is already out of the live set).
// An empty stageDir is returned when there was nothing to stage.
func (t *Traffic) stageTrees(dirs []string) (stageDir string, moves []stagedTrafficPath, err error) {
	return t.stageTreesForArchive(dirs, nil, 0, 0)
}

func (t *Traffic) stageTreesForArchive(dirs, hosts []string, archiveID, taskID int64) (stageDir string, moves []stagedTrafficPath, err error) {
	seen := make(map[string]struct{}, len(dirs))
	planned := make([]stagedTrafficPath, 0, len(dirs))
	for _, source := range dirs {
		if _, dup := seen[source]; dup {
			continue
		}
		seen[source] = struct{}{}
		if _, err := os.Lstat(source); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return stageDir, moves, fmt.Errorf("과거 트래픽 디렉토리 %s 확인: %w", source, err)
		}
		planned = append(planned, stagedTrafficPath{source: source})
	}
	// 아카이브 경로에는 기록 호스트 디렉터리도 없습니다(새 설치에 대한 트래픽은 SQLite + _blobs에만 해당).
	// 저널도 남아 있어야 합니다. 충돌 지점이 PostgreSQL 제출과 SQLite 제출 사이에 있는 경우 다시 시작한 후
	// SQLite 트랜잭션이 롤백되고 이 저널만 복구 프로세스를 통해 호스트 행 삭제를 보충할 수 있습니다. 그것 없이는,
	// 콜드 스토리지로 전송된 작업의 독점 트래픽은 핫 스토리지에 영구적으로 유지됩니다.
	uniqueHosts := uniqueArchiveHosts(hosts)
	if len(planned) == 0 && len(uniqueHosts) == 0 {
		return "", nil, nil
	}
	parent := filepath.Join(t.dir, "_delete_staging")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", nil, fmt.Errorf("트래픽 임시 디렉토리 생성: %w", err)
	}
	if stageDir, err = os.MkdirTemp(parent, "hosts-"); err != nil {
		return "", nil, fmt.Errorf("트래픽 임시 디렉토리 생성: %w", err)
	}
	journal := hostDeleteStageJournal{Version: 1, ArchiveID: archiveID, TaskID: taskID, Hosts: uniqueHosts}
	for i := range planned {
		planned[i].staged = filepath.Join(stageDir, fmt.Sprintf("%d-%s", i, filepath.Base(planned[i].source)))
		journal.Moves = append(journal.Moves, hostDeleteStageMove{Source: planned[i].source, Staged: planned[i].staged})
	}
	if err := writeHostDeleteStageJournal(filepath.Join(stageDir, hostDeleteStageJournalName), journal); err != nil {
		_ = os.RemoveAll(stageDir)
		return "", nil, err
	}
	for _, move := range planned {
		source, staged := move.source, move.staged
		if err := os.Rename(source, staged); err != nil {
			return stageDir, moves, fmt.Errorf("과거 트래픽 디렉토리 %s 제거: %w", source, err)
		}
		moves = append(moves, stagedTrafficPath{source: source, staged: staged})
	}
	return stageDir, moves, nil
}

func writeHostDeleteStageJournal(path string, journal hostDeleteStageJournal) error {
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// restoreTrees puts staged directories back where they came from, newest move
// first, and drops the staging directory once everything is home.
func restoreTrees(stageDir string, moves []stagedTrafficPath) error {
	var errs []error
	for i := len(moves) - 1; i >= 0; i-- {
		move := moves[i]
		if _, err := os.Lstat(move.source); err == nil {
			errs = append(errs, fmt.Errorf("restore destination already exists: %s", move.source))
			continue
		} else if !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("inspect restore destination %s: %w", move.source, err))
			continue
		}
		if err := os.Rename(move.staged, move.source); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", move.source, err))
		}
	}
	if len(errs) == 0 && stageDir != "" {
		if err := os.RemoveAll(stageDir); err != nil {
			errs = append(errs, fmt.Errorf("remove traffic stage: %w", err))
		}
	}
	return errors.Join(errs...)
}

// reapStage unlinks a committed staging directory in the background. This is the
// step that used to stall the recorder: a legacy tree mirrors the URL path per
// request and can hold hundreds of thousands of small files, and the whole unlink
// ran while the write lock was held. The rows are already gone by now, so losing
// this to a shutdown leaves garbage under _delete_staging, never inconsistent
// state.
func (t *Traffic) reapStage(stageDir string) {
	if stageDir == "" {
		return
	}
	t.reaping.Go(func() {
		if err := os.RemoveAll(stageDir); err != nil {
			log.Printf("[traffic] 과거 트래픽 디렉터리 %s을(를) 지우지 못했습니다: %v", stageDir, err)
		}
	})
}

// DeleteHostsExact removes recorded exchanges for a set of EXACT hosts (the
// batch path for the page's multi-select delete): index rows + each host's file
// tree, then one blob-GC pass. Exact match — unlike DeleteHost's substring —
// so picking "api.example.com" never sweeps "api.example.com.cn". Returns rows
// deleted. Duplicate hosts are harmless (idempotent deletes, single tree pass).
func (t *Traffic) DeleteHostsExact(hosts []string) (int64, error) {
	stage, err := t.StageDeleteHostsExact(hosts)
	if err != nil {
		return 0, err
	}
	deleted := stage.Deleted()
	if err := stage.Commit(); err != nil {
		return deleted, err
	}
	return deleted, nil
}

// HostDeleteStage keeps the SQLite delete transaction open so a task deletion
// can be coordinated with PostgreSQL: the traffic side is staged here and only
// committed once the caller's own transaction has succeeded. The Traffic write
// lock is held until Commit or Rollback, so no recorder can add rows or a blob
// reference for a host that is mid-deletion. Rolling back is just a transaction
// rollback now that nothing is moved on disk.
type HostDeleteStage struct {
	traffic  *Traffic
	tx       *sql.Tx
	stageDir string
	moves    []stagedTrafficPath
	deleted  int64
	done     bool
}

// Deleted reports the number of exchange rows selected by this stage.
func (s *HostDeleteStage) Deleted() int64 {
	if s == nil {
		return 0
	}
	return s.deleted
}

// StageDeleteHostsExact prepares a reversible exact-host deletion. Callers must
// finish every successful stage with Commit or Rollback.
func (t *Traffic) StageDeleteHostsExact(hosts []string) (*HostDeleteStage, error) {
	return t.stageDeleteHostsExact(hosts, 0, 0)
}

// StageDeleteHostsExactForArchive ties the reversible traffic deletion to a
// persistent task archive. Startup recovery uses archiveCommitted to decide
// whether an interrupted stage must be completed or rolled back.
func (t *Traffic) StageDeleteHostsExactForArchive(hosts []string, archiveID, taskID int64) (*HostDeleteStage, error) {
	if archiveID <= 0 || taskID <= 0 {
		return nil, errors.New("archive and task ids must be positive")
	}
	return t.stageDeleteHostsExact(hosts, archiveID, taskID)
}

func (t *Traffic) stageDeleteHostsExact(hosts []string, archiveID, taskID int64) (*HostDeleteStage, error) {
	t.wmu.Lock()
	stage := &HostDeleteStage{traffic: t}
	fail := func(cause error) (*HostDeleteStage, error) {
		if rollbackErr := stage.rollbackLocked(); rollbackErr != nil {
			return nil, errors.Join(cause, fmt.Errorf("롤백 트래픽 삭제: %w", rollbackErr))
		}
		return nil, cause
	}
	abort := func(cause error) (*HostDeleteStage, error) {
		t.wmu.Unlock()
		stage.done = true
		return nil, cause
	}

	unique := make([]string, 0, len(hosts))
	seen := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		unique = append(unique, host)
	}

	// Exact hosts are known up front, so their directories are derived directly
	// rather than looked up: a host whose index rows are already gone can still
	// own an orphaned directory that this deletion should take with it.
	legacy := make([]string, 0, len(unique))
	for _, h := range unique {
		legacy = append(legacy, filepath.Join(t.dir, sanitize(h)))
	}

	tx, err := t.db.Begin()
	if err != nil {
		return abort(err)
	}
	stage.tx = tx
	for _, h := range unique {
		n, err := t.deleteWhere(tx, `host=?`, h)
		if err != nil {
			return fail(err)
		}
		stage.deleted += n
	}

	stage.stageDir, stage.moves, err = t.stageTreesForArchive(legacy, unique, archiveID, taskID)
	if err != nil {
		return fail(err)
	}
	return stage, nil
}

// RecoverHostDeleteStages resolves filesystem moves left by a process exit.
// SQLite rolls open transactions back on restart, while the supplied callback
// identifies task archives whose PostgreSQL compaction already committed.
func (t *Traffic) RecoverHostDeleteStages(archiveCommitted func(int64, int64) (bool, error)) error {
	if t == nil {
		return nil
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	parent := filepath.Join(t.dir, "_delete_staging")
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	needsGC := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		stageDir := filepath.Join(parent, entry.Name())
		raw, err := os.ReadFile(filepath.Join(stageDir, hostDeleteStageJournalName))
		if os.IsNotExist(err) {
			// Older versions did not persist enough information for lossless
			// recovery. Keep the directory for manual inspection.
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var journal hostDeleteStageJournal
		if err := json.Unmarshal(raw, &journal); err != nil {
			errs = append(errs, fmt.Errorf("트래픽 임시 로그 %s 읽기: %w", stageDir, err))
			continue
		}
		if journal.Version != 1 {
			errs = append(errs, fmt.Errorf("트래픽 임시 로그 %s 버전 %d은(는) 지원되지 않습니다", stageDir, journal.Version))
			continue
		}
		moves := make([]stagedTrafficPath, 0, len(journal.Moves))
		for _, move := range journal.Moves {
			if !pathWithin(t.dir, move.Source) || !pathWithin(stageDir, move.Staged) {
				errs = append(errs, fmt.Errorf("트래픽 임시 로그에 범위를 벗어난 경로가 포함되어 있습니다: %s", stageDir))
				moves = nil
				break
			}
			if _, err := os.Lstat(move.Staged); err == nil {
				moves = append(moves, stagedTrafficPath{source: move.Source, staged: move.Staged})
			} else if !os.IsNotExist(err) {
				errs = append(errs, err)
				moves = nil
				break
			}
		}
		if moves == nil {
			continue
		}
		committed := false
		if journal.ArchiveID > 0 {
			if archiveCommitted == nil {
				errs = append(errs, fmt.Errorf("트래픽 아카이브 %d Stateless Resolver", journal.ArchiveID))
				continue
			}
			committed, err = archiveCommitted(journal.ArchiveID, journal.TaskID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
		} else if len(journal.Hosts) > 0 {
			committed, err = t.hostsHaveNoExchanges(journal.Hosts)
			if err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if !committed {
			if err := restoreTrees(stageDir, moves); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if err := t.deleteArchivedHosts(journal.Hosts); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.RemoveAll(stageDir); err != nil {
			errs = append(errs, err)
			continue
		}
		needsGC = true
	}
	if needsGC {
		if err := t.gcBlobs(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (t *Traffic) hostsHaveNoExchanges(hosts []string) (bool, error) {
	for _, host := range hosts {
		var count int
		if err := t.db.QueryRow(`SELECT count(*) FROM exchanges WHERE host=?`, host).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return false, nil
		}
	}
	return true, nil
}

func (t *Traffic) deleteArchivedHosts(hosts []string) error {
	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, host := range hosts {
		if _, err := t.deleteWhere(tx, `host=?`, host); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func pathWithin(root, candidate string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, candidateAbs)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Rollback discards the staged deletion, leaving the traffic store untouched.
func (s *HostDeleteStage) Rollback() error {
	if s == nil || s.done {
		return nil
	}
	return s.rollbackLocked()
}

func (s *HostDeleteStage) rollbackLocked() error {
	if s == nil || s.done {
		return nil
	}
	var errs []error
	if s.tx != nil {
		if err := s.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			errs = append(errs, fmt.Errorf("롤백 트래픽 지수: %w", err))
		}
	}
	if err := restoreTrees(s.stageDir, s.moves); err != nil {
		errs = append(errs, err)
	}
	s.done = true
	s.traffic.wmu.Unlock()
	return errors.Join(errs...)
}

// Commit makes the staged deletion permanent. SQLite is committed only after
// the caller has committed its PostgreSQL task deletion.
func (s *HostDeleteStage) Commit() error {
	if s == nil || s.done {
		return nil
	}
	if err := s.tx.Commit(); err != nil {
		// A failed SQLite commit normally rolls the transaction back. Restore the
		// trees so the traffic store remains internally consistent and recoverable.
		restoreErr := restoreTrees(s.stageDir, s.moves)
		s.done = true
		s.traffic.wmu.Unlock()
		return errors.Join(fmt.Errorf("트래픽 인덱스 삭제 제출: %w", err), restoreErr)
	}
	// Unlinking the staged trees is what used to hold the write lock for hours;
	// it now runs in the background, while collection below only needs them to be
	// out of the live tree — which the staging rename already guaranteed.
	s.traffic.reapStage(s.stageDir)
	var errs []error
	if err := s.traffic.gcBlobs(); err != nil {
		errs = append(errs, fmt.Errorf("재활용 트래픽 blob: %w", err))
	}
	s.done = true
	s.traffic.wmu.Unlock()
	return errors.Join(errs...)
}

// gcBlobs removes blobs that no remaining exchange references. Live references
// come from blob_refs, so collection is one query plus a sweep of the blob
// directory — no exchange body is ever read. Emptied bucket directories are
// removed as well; the previous file-scanning collector deleted only files and
// left the buckets behind permanently. Best-effort: walk errors only skip the
// affected entries. Callers must hold wmu so this never races a concurrent
// record() writing a fresh blob + reference.
func (t *Traffic) gcBlobs() error {
	refs := make(map[string]struct{})
	rows, err := t.db.Query(`SELECT DISTINCT hash FROM blob_refs`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return err
		}
		refs[h] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := t.legacyBlobRefs(refs); err != nil {
		return err
	}

	root := filepath.Join(t.dir, "_blobs", "sha256")
	var buckets []string
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			if p != root {
				buckets = append(buckets, p)
			}
			return nil
		}
		h := strings.TrimSuffix(d.Name(), ".bin")
		if _, ok := refs[h]; !ok {
			os.Remove(p)
		}
		return nil
	}); err != nil {
		return err
	}
	// Deepest first, so a two-level bucket left by the old layout collapses fully.
	// Remove fails harmlessly on a non-empty directory — exactly the guard needed
	// to avoid deleting a bucket that still holds live blobs.
	sort.Slice(buckets, func(i, j int) bool { return len(buckets[i]) > len(buckets[j]) })
	for _, b := range buckets {
		os.Remove(b)
	}
	return nil
}

// legacyBlobRefs adds hashes referenced by pre-SQLite exchanges, whose bodies are
// still .http files on disk holding "@blob sha256:<hex>" pointers. Without this
// the first collection after the upgrade would delete blobs that history still
// points at. Skipped entirely once no legacy rows remain — the steady state — so
// the tree walk is transitional rather than a permanent cost.
func (t *Traffic) legacyBlobRefs(refs map[string]struct{}) error {
	var n int
	if err := t.db.QueryRow(`SELECT COUNT(*) FROM exchanges WHERE path<>''`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	blobRe := regexp.MustCompile(`@blob sha256:([0-9a-f]{64})`)
	return filepath.WalkDir(t.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			// _blobs/_index/_ca never contain references; skip their subtrees.
			if p != t.dir && strings.HasPrefix(d.Name(), "_") {
				return filepath.SkipDir
			}
			return nil
		}
		if nm := d.Name(); nm != "request.http" && nm != "response.http" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range blobRe.FindAllSubmatch(b, -1) {
			refs[string(m[1])] = struct{}{}
		}
		return nil
	})
}

// query returns one page of exchange metadata filtered by host and/or a url
// substring. Default page size is intentionally small (3) to keep tool results
// lightweight and capped at 10; page is 0-based (page*limit offset).
func (t *Traffic) query(host, contains, bodyContains string, page, limit int) ([]ExchangeMeta, error) {
	if limit <= 0 {
		limit = 3
	}
	if limit > 10 {
		limit = 10
	}
	if page < 0 {
		page = 0
	}
	q := `SELECT id,ts,host,method,url_template,url,status,content_type,resp_len,path FROM exchanges WHERE 1=1`
	args := []any{}
	if host != "" {
		hostName, port, err := normalizeSearchHost(host)
		if err != nil {
			return nil, err
		}
		q += ` AND host=?`
		args = append(args, hostName)
		if port != "" {
			// Recorded rows historically store URL.Hostname() (without the port),
			// so constrain the original URL authority as a compatibility fallback.
			// New and old captures therefore share the same search contract.
			authority := net.JoinHostPort(hostName, port)
			q += ` AND (url LIKE ? OR url LIKE ? OR url LIKE ?)`
			args = append(args,
				"%://"+authority+"/%",
				"%://"+authority+"?%",
				"%://"+authority,
			)
		}
	}
	if contains != "" {
		q += ` AND (url LIKE ? OR url_template LIKE ?)`
		args = append(args, "%"+contains+"%", "%"+contains+"%")
	}
	if b := strings.TrimSpace(bodyContains); b != "" {
		cond, arg, ok := t.ftsFilter(b)
		if !ok {
			if !t.fts {
				return nil, fmt.Errorf("현재 인스턴스는 전체 텍스트 인덱싱을 활성화하지 않으며 텍스트로 검색할 수 없습니다")
			}
			return nil, fmt.Errorf("텍스트 검색 키워드에는 최소 %d자(현재 %d)가 필요합니다.", minTrigram, utf8.RuneCountInString(b))
		}
		q += ` AND ` + cond
		args = append(args, arg)
	}
	q += ` ORDER BY ts DESC LIMIT ? OFFSET ?`
	args = append(args, limit, page*limit)
	rows, err := t.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExchangeMeta
	for rows.Next() {
		var m ExchangeMeta
		if err := rows.Scan(&m.ID, &m.TS, &m.Host, &m.Method, &m.URLTemplate, &m.URL, &m.Status, &m.ContentType, &m.RespLen, &m.Path); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// normalizeSearchHost accepts the forms agents commonly have at hand while
// keeping the stored host (URL.Hostname()) as the canonical host key. A port,
// when supplied, is applied to the URL authority so captures from different
// services on the same IP cannot be mixed.
func normalizeSearchHost(raw string) (host, port string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("호스트는 필수 매개변수입니다")
	}
	if strings.Contains(raw, "://") {
		u, parseErr := url.Parse(raw)
		if parseErr != nil || u.Host == "" {
			return "", "", fmt.Errorf("호스트를 확인할 수 없습니다: %q", raw)
		}
		host, port = u.Hostname(), u.Port()
	} else if h, p, splitErr := net.SplitHostPort(raw); splitErr == nil {
		host, port = h, p
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	} else {
		host = raw
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return "", "", fmt.Errorf("호스트를 확인할 수 없습니다: %q", raw)
	}
	if port != "" {
		p, parseErr := strconv.Atoi(port)
		if parseErr != nil || p < 1 || p > 65535 {
			return "", "", fmt.Errorf("잘못된 포트: %q", port)
		}
		port = strconv.Itoa(p)
	}
	return strings.ToLower(host), port, nil
}

// Tools exposes traffic lookup to work agents so they query already-captured
// traffic instead of re-curling the same resource (token + dedup win).
func (t *Traffic) Tools() []actool.CoreTool {
	allow := func(context.Context, json.RawMessage, permission.Context) permission.Decision {
		return permission.Allowed()
	}
	ro := func(json.RawMessage) bool { return true }

	search := actool.Build(actool.Spec{
		Name:        "traffic_search",
		Description: TrafficSearchDescription,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host":          map[string]any{"type": "string", "description": "호스트별 필터링(필수, '107.172.96.177', '107.172.96.177:8082' 또는 'http://107.172.96.177:8082/path' 등)"},
				"contains":      map[string]any{"type": "string", "description": "URL 하위 문자열 필터링('api' / 'login' 등 선택사항)"},
				"body_contains": map[string]any{"type": "string", "description": "본문 전체 텍스트 검색(선택 사항, 최소 3자), 요청/응답의 헤더 및 본문과 일치(예: 'password' / 'root:x:0' / 'Intranet test')"},
				"limit":         map[string]any{"type": "integer", "description": "페이지당 항목 수, 기본값 3, 최대 10"},
				"page":          map[string]any{"type": "integer", "description": "페이지 번호, 0부터 시작, 기본값은 0(ts를 기준으로 역순으로 페이징)"},
			},
			"required": []any{"host"},
		},
		ReadOnly:    ro,
		Permissions: allow,
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Host, Contains string
				BodyContains   string `json:"body_contains"`
				Limit          int
				Page           int
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Host) == "" {
				return actool.Errorf("host는 필수 매개변수입니다. 전체 데이터베이스 검사를 방지하려면 기본 호스트, 호스트:포트 또는 전체 URL을 지정하십시오."), nil
			}
			rows, err := t.query(a.Host, a.Contains, a.BodyContains, a.Page, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if len(rows) == 0 {
				return actool.Text("일치하는 트래픽이 없습니다."), nil
			}
			// 최소 인덱스로 감소: 응답 내용 없이 위치 지정에 필요한 필드 + 응답 코드/길이만 유지됩니다.
			type liteRow struct {
				ID      string `json:"id"`
				Method  string `json:"method"`
				URL     string `json:"url"`
				Status  int    `json:"status"`
				RespLen int    `json:"resp_len"`
			}
			lite := make([]liteRow, 0, len(rows))
			for _, r := range rows {
				lite = append(lite, liteRow{ID: r.ID, Method: r.Method, URL: r.URL, Status: r.Status, RespLen: r.RespLen})
			}
			b, _ := json.Marshal(lite)
			return actool.Text(string(b)), nil
		},
	})

	get := actool.Build(actool.Spec{
		Name:        "traffic_get",
		Description: "ID로 캡처한 요청과 응답의 원문을 조회합니다. 내용이 너무 크면 일부만 표시됩니다. 같은 URL을 다시 요청하지 않도록 traffic_search로 기록을 찾은 뒤 사용하세요.",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string", "description": "traffic_search에서 반환된 ID"}},
			"required":   []any{"id"},
		},
		ReadOnly:    ro,
		Permissions: allow,
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct{ ID string }
			_ = json.Unmarshal(in, &a)
			req, resp, err := t.Get(a.ID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("=== REQUEST ===\n" + clip(req, 2500) + "\n\n=== RESPONSE ===\n" + clip(resp, 4000)), nil
		},
	})

	blob := actool.Build(actool.Spec{
		Name:        "traffic_blob",
		Description: "매우 큰 요청/응답 본문의 원본 텍스트를 세그먼트로 읽습니다. traffic_get에서 '...[truncated] @blob sha256:<hash>'로 표시된 부분이 여기에 저장됩니다. 전체 콘텐츠를 얻으려면 해시를 전달하세요. 한 번에 최대 8KB까지 반환할 수 있으며, 계속 읽기 위해 오프셋을 사용합니다. (전체 길이는 반환 결과에 제공됩니다.) 백업 파일 탐색, 소스 코드 유출, 대규모 JSON 내보내기 등과 같이 인라인 임계값을 초과하는 응답에 적합합니다.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"hash":   map[string]any{"type": "string", "description": "@blob sha256의 traffic_get: 다음 64비트 16진수 값"},
				"offset": map[string]any{"type": "integer", "description": "시작 바이트 오프셋, 기본값 0"},
				"length": map[string]any{"type": "integer", "description": "이번에 읽은 바이트 수, 기본값 및 최대 8192"},
			},
			"required": []any{"hash"},
		},
		ReadOnly:    ro,
		Permissions: allow,
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Hash   string
				Offset int64
				Length int64
			}
			_ = json.Unmarshal(in, &a)
			if a.Length <= 0 || a.Length > maxBlobRead {
				a.Length = maxBlobRead
			}
			data, total, err := t.BlobRange(a.Hash, a.Offset, a.Length)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if len(data) == 0 {
				return actool.Text(fmt.Sprintf("오프셋 %d이(가) 콘텐츠 길이(총 %d바이트)를 초과합니다.", a.Offset, total)), nil
			}
			head := fmt.Sprintf("[오프셋=%d 이번 시간=%d 총 길이=%d]\n", a.Offset, len(data), total)
			if isBinaryBody("", data) {
				return actool.Text(head + "처음 512바이트를 16진수로 표시하는 바이너리 콘텐츠:\n" + hex.EncodeToString(clipBytes(data, 512))), nil
			}
			return actool.Text(head + truncateUTF8(data, len(data))), nil
		},
	})

	return []actool.CoreTool{search, get, blob}
}

// SeedToolMetas returns the traffic tools built on a ZERO receiver, for seeding the
// tools catalog (metadata only — Name/Description/InputSchema). The handlers close
// over the nil receiver but are never invoked on this instance, so it is safe.
func SeedToolMetas() []actool.CoreTool { return (&Traffic{}).Tools() }

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... [잘림, %d바이트; 트래픽 파일 트리에서 완료] ...", len(s))
}
