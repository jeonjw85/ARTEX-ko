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

// latestURL는 GitHub의 "최신 공식 버전" 인터페이스입니다. prerelease 및 draft를 자동으로 건너뜁니다.
const latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// allowedHosts는 업그레이드 링크가 액세스할 수 있는 도메인 이름을 제한합니다. 아래 checkRedirect와 연계하여,
// 목록 외부의 호스트로 리디렉션된 모든 홉은 직접 실패합니다. 이는 DNS 오염/중간자(man-in-the-middle)를 방지하기 위한 것입니다.
// 바이너리를 대체하는 첫 번째 게이트, 두 번째 게이트는 SHA256SUMS 비교입니다.
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true, // release 자산이 실제로 구현되는 객체 스토리지
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com":            true,
}

// Release는 GitHub Release에서 우리가 관심을 갖는 필드입니다.
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

// Asset는 Release에 첨부된 파일입니다.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// NewClient는 GitHub 도메인 이름만 인식하는 HTTP 클라이언트를 구성합니다. proxy가 비어 있으면 직접 연결됩니다.
//
// 의도적으로 기본 Transport를 재사용하지 않음: 업그레이드 링크를 강제로 TLS로 이동해야 하며 확인 인증서를 다른 곳에서 사용할 수 없습니다.
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
				return fmt.Errorf("리디렉션이 너무 많습니다.")
			}
			return checkURL(req.URL)
		},
	}
}

// checkURL는 https + 도메인 이름 화이트리스트를 강제합니다.
func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("HTTPS 주소가 아닌 주소 거부: %s", u.Scheme+"://"+u.Host)
	}
	if !allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("GitHub가 아닌 도메인 이름 거부: %s", u.Hostname())
	}
	return nil
}

// FetchLatest 최신 공식 버전을 확인하세요.
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
		return nil, fmt.Errorf("GitHub에 액세스하지 못했습니다(글로벌 프록시는 시스템 설정에서 구성할 수 있음): %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusTooManyRequests:
		// 미인증 GitHub API는 IP당 시간당 60회이며 콘센트 IP를 공유할 때 쉽게 맞을 수 있습니다.
		return nil, fmt.Errorf("GitHub 인터페이스 전류 제한(시간당 60회), 나중에 다시 시도하십시오")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("Warehouse %s는 아직 공식 버전을 출시하지 않았습니다.", Repo)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub %d 반환", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("Release 구문 분석 실패: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, fmt.Errorf("Release에 tag가 없습니다.")
	}
	return &rel, nil
}

// AssetName는 현재 플랫폼에 해당하는 릴리스 패키지 이름을 반환하며 이는 build.sh의 package_binary와 일치합니다.
// artex-<버전>-<os>-<arch>.zip(버전 번호에는 v 접두사가 없습니다).
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("artex-%s-%s-%s.zip", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// FindAsset Release에서 이름으로 자산을 찾습니다.
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}
