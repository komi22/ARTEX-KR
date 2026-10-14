package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Repo는 릴리스 소스입니다. 구성 항목에 포함되지 않고 하드 코딩되어 구성을 변경할 수 있는 누구에게나 업데이트 소스를 할당할 수 있습니다.
// 원격 코드 실행 채널은 침투 테스트 플랫폼을 위한 통로가 아닙니다.
const Repo = "Autumn-27/artex"

// 최신 URL은 GitHub의 "최신 공식 버전" 인터페이스입니다. 시험판과 초안을 자동으로 건너뜁니다.
const latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// allowedHosts는 업그레이드 링크가 액세스할 수 있는 도메인 이름을 제한합니다. 아래 checkRedirect와 함께,
// 목록에 없는 호스트로 리디렉션된 모든 홉은 직접 실패합니다. 이는 DNS 오염/중간자(man-in-the-middle)를 방지하기 위한 것입니다.
// 바이너리를 대체하는 첫 번째 게이트, 두 번째 게이트는 SHA256SUMS 비교입니다.
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true, // 릴리스 자산이 실제로 구현되는 객체 스토리지
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com":            true,
}

// 릴리스는 GitHub 릴리스에서 관심을 갖는 필드입니다.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset은 Release에 첨부된 파일입니다.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// NewClient는 GitHub 도메인 이름만 인식하는 HTTP 클라이언트를 구성합니다. 프록시가 비어 있으면 직접 연결됩니다.
//
// 기본 전송을 의도적으로 재사용하지 않음: 업그레이드 링크는 강제로 TLS를 사용하고 인증서를 확인해야 하며 다른 곳에서는 사용할 수 없습니다.
// InsecureSkipVerify와 같은 설정이 영향을 받습니다.
func NewClient(proxy string) *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Minute, // 요청 수준의 시간 초과로 인해 전체 패키지 다운로드를 차단할 수 없습니다.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("리디렉션이 너무 많습니다")
			}
			return checkURL(req.URL)
		},
	}
}

// checkURL은 https + 도메인 이름 허용 목록을 강제합니다.
func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("HTTPS가 아닌 주소 거부: %s", u.Scheme+"://"+u.Host)
	}
	if !allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("GitHub가 아닌 도메인 이름 거부: %s", u.Hostname())
	}
	return nil
}

// FetchLatest는 최신 공식 버전을 쿼리합니다.
func FetchLatest(ctx context.Context, c *http.Client) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "artex-selfupdate")

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub에 액세스하지 못했습니다(글로벌 프록시는 시스템 설정에서 구성할 수 있습니다): %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusTooManyRequests:
		//인증되지 않은 GitHub API는 IP당 시간당 60개이므로 송신 IP를 공유할 때 쉽게 공격받을 수 있습니다.
		return nil, fmt.Errorf("GitHub 인터페이스 현재 제한(시간당 60회), 나중에 다시 시도하세요")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s 저장소는 공식 버전을 출시하지 않았습니다", Repo)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub가 %d을(를) 반환했습니다", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("릴리스 구문 분석 실패: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, fmt.Errorf("누락된 태그 해제")
	}
	return &rel, nil
}

// AssetName은 현재 플랫폼에 해당하는 릴리스 패키지 이름을 반환하며 이는 build.sh의 package_binary와 일치합니다.
// artex-<버전>-<os>-<arch>.zip(v 접두사가 없는 버전 번호).
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("artex-%s-%s-%s.zip", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// FindAsset은 Release에서 이름으로 자산을 찾습니다.
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}
