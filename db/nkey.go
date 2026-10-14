package db

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// 정규화된 자연 키(nkey): 이전 graph/id.go에서 이식되었으며 StableID 해시를 제거합니다(PG는 BIGSERIAL 기본 키 +
// 중복을 제거하려면 UNIQUE(type, nkey)를 사용하세요. 하위 자산의 nkey는 상위 자산의 int64 ID를 포함하여 계층 구조를 키로 인코딩합니다.

func DomainKey(fqdn string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(fqdn)), ".")
}

func IPKey(ip string) string { return strings.TrimSpace(ip) }

// RootDomain returns the registrable domain (eTLD+1) for a host and whether the
// 호스트 자체가 정점입니다(§3.1). 엣지 케이스(§3.1 경계 처리): IP 리터럴 또는
// host publicsuffix can't classify (localhost / internal / non-ICANN TLD) is
// returned unchanged as its own root with isApex=true — best-effort, never treated
// as a subdomain.
func RootDomain(host string) (root string, isApex bool) {
	h := DomainKey(host)
	if h == "" || net.ParseIP(h) != nil {
		return h, true
	}
	etld1, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil || etld1 == "" {
		return h, true
	}
	return etld1, h == etld1
}

func PortKey(ipID int64, proto string, port int) string {
	return itoa(ipID) + "|" + strings.ToLower(proto) + "|" + strconv.Itoa(port)
}

func ServiceKey(portID int64, svcName string) string {
	return itoa(portID) + "|" + strings.ToLower(svcName)
}

func SiteKey(scheme, host string, port int) string {
	return strings.ToLower(scheme) + "|" + strings.ToLower(host) + "|" + strconv.Itoa(port)
}

func EndpointKey(siteID int64, method, urlTemplate string) string {
	return itoa(siteID) + "|" + strings.ToUpper(method) + "|" + urlTemplate
}

func ParameterKey(endpointID int64, location, name string) string {
	return itoa(endpointID) + "|" + strings.ToLower(location) + "|" + name
}

// NormalizeParamName 정규화된 매개변수 이름(endpoint.params 요소의 "동일 참조" 결정).
// 규칙: 하위 + 트림, 동의어 병합 없음(userId/user_id/uid는 다른 것으로 간주됩니다). 글쓰기는 이 구현을 쿼리와 공유합니다.
// "매개변수 이름으로 동일한 회사 인터페이스를 확인하십시오"가 재현될 수 있는지 확인하십시오.
func NormalizeParamName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func TechKey(name, version string) string {
	return strings.ToLower(name) + "|" + version
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

var (
	reNumeric = regexp.MustCompile(`^\d+$`)
	reUUID    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	reHex     = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
	reLong    = regexp.MustCompile(`^[A-Za-z0-9_-]{24,}$`)
)

// TemplatePath collapses high-cardinality path segments into placeholders so the
// graph is not flooded by instances: /user/123 -> /user/{id}.
func TemplatePath(path string) string {
	if path == "" {
		return "/"
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		switch {
		case s == "":
			continue
		case reNumeric.MatchString(s):
			segs[i] = "{id}"
		case reUUID.MatchString(s):
			segs[i] = "{uuid}"
		case reHex.MatchString(s):
			segs[i] = "{hex}"
		case reLong.MatchString(s):
			segs[i] = "{token}"
		}
	}
	return strings.Join(segs, "/")
}

// SplitURL parses a raw URL into scheme/host/port/urlTemplate/params for building
// site/endpoint/param keys.
func SplitURL(raw, method string) (scheme, host string, port int, urlTemplate string, params []string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, "", nil, err
	}
	scheme = strings.ToLower(u.Scheme)
	host = strings.ToLower(u.Hostname())
	port = defaultPort(scheme, u.Port())
	urlTemplate = TemplatePath(u.EscapedPath())
	for k := range u.Query() {
		params = append(params, k)
	}
	return scheme, host, port, urlTemplate, params, nil
}

func defaultPort(scheme, p string) int {
	if p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	switch scheme {
	case "https":
		return 443
	case "http":
		return 80
	default:
		return 0
	}
}
