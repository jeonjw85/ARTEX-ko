package db

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// 자산 차단 규칙에 대한 일치/집행 레이어입니다. asset_intercept.go는 규칙 저장만 담당합니다. 여기서는 저장을 담당합니다.
// "Target Asset"의 도메인 이름 /IP/URL가 활성 규칙과 일치합니다. agent 도구(add_intent,
// insert_assets)는 인텐트를 전송하거나 자산을 삽입하기 전에 호출됩니다. 맞으면 거절됩니다.

// AssetInterceptKindLabel는 agent에 대한 메시지를 설명하는 데 사용되는 kind의 한국어 태그를 반환합니다.
func AssetInterceptKindLabel(kind string) string {
	switch kind {
	case "exact_domain":
		return "도메인 이름(일치)"
	case "exact_ip":
		return "IP(합동)"
	case "exact_url":
		return "URL(합동)"
	case "fuzzy_domain":
		return "도메인 이름(퍼지)"
	case "fuzzy_ip":
		return "IP(퍼지)"
	case "fuzzy_url":
		return "URL(퍼지)"
	case "cidr":
		return "CIDR 네트워크 세그먼트"
	}
	return kind
}

// Reason는 적중 자산 차단 규칙 [도메인 이름(퍼지): ​​.gov.cn](비고) 형식으로 읽을 수 있는 적중 이유를 반환합니다.
func (r AssetInterceptRule) Reason() string {
	s := fmt.Sprintf("적중 자산 차단 규칙 [%s: %s]", AssetInterceptKindLabel(r.Kind), r.Pattern)
	if note := strings.TrimSpace(r.Note); note != "" {
		s += "（" + note + "）"
	}
	return s
}

// matchOne는 활성화된 단일 규칙이 지정된 도메인 이름/IP/URL 후보 문자열에 적중하는지 여부를 확인하고 적중의 특정 값을 반환합니다.
func matchOne(r AssetInterceptRule, domains, ips, urls []string) (string, bool) {
	p := strings.TrimSpace(r.Pattern)
	if p == "" {
		return "", false
	}
	switch r.Kind {
	case "exact_domain":
		for _, d := range domains {
			if strings.EqualFold(strings.TrimSpace(d), p) {
				return d, true
			}
		}
	case "exact_ip":
		for _, ip := range ips {
			if strings.TrimSpace(ip) == p {
				return ip, true
			}
		}
	case "exact_url":
		for _, u := range urls {
			if strings.TrimSpace(u) == p {
				return u, true
			}
		}
	case "fuzzy_domain":
		lp := strings.ToLower(p)
		for _, d := range domains {
			if d != "" && strings.Contains(strings.ToLower(d), lp) {
				return d, true
			}
		}
	case "fuzzy_ip":
		for _, ip := range ips {
			if ip != "" && strings.Contains(ip, p) {
				return ip, true
			}
		}
	case "fuzzy_url":
		lp := strings.ToLower(p)
		for _, u := range urls {
			if u != "" && strings.Contains(strings.ToLower(u), lp) {
				return u, true
			}
		}
	case "cidr":
		_, ipnet, err := net.ParseCIDR(p)
		if err != nil {
			return "", false
		}
		for _, ip := range ips {
			if pip := net.ParseIP(strings.TrimSpace(ip)); pip != nil && ipnet.Contains(pip) {
				return ip, true
			}
		}
	}
	return "", false
}

// MatchAssetInterceptRules는 지정된 도메인 이름/IP/URL 후보 문자열에 적중하는 첫 번째 활성화 규칙과 적중의 특정 값을 반환합니다.
// insert_assets의 경우 일치를 위해 원래 입력(라이브러리에 추가되지 않은 assetInputItem)을 사용합니다.
func MatchAssetInterceptRules(rules []AssetInterceptRule, domains, ips, urls []string) (AssetInterceptRule, string, bool) {
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if v, ok := matchOne(r, domains, ips, urls); ok {
			return r, v, true
		}
	}
	return AssetInterceptRule{}, "", false
}

// interceptCandidates는 일치하는 도메인 이름/IP/URL 후보 문자열을 가로채기 위해 삭제된 자산을 추출합니다.
// URL의 host는 분리 분류되므로 "URL"만 있는 서비스 자산도 도메인 이름/IP 규칙에 의해 공격될 수 있습니다.
func (a *Asset) interceptCandidates() (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, a.Domain)
	add(&domains, a.RootDomain)
	for _, d := range a.BoundDomains {
		add(&domains, d)
	}
	add(&ips, a.IP)
	add(&urls, a.URL)
	if a.URL != "" {
		if u, err := url.Parse(a.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// InterceptLabel는 설명 메시지에 사용된 자산의 짧은 식별자를 agent에 반환합니다.
func (a *Asset) InterceptLabel() string {
	var target string
	switch {
	case a.Domain != "":
		target = a.Domain
	case a.URL != "":
		target = a.URL
	case a.IP != "":
		target = a.IP
	default:
		target = fmt.Sprintf("#%d", a.ID)
	}
	return fmt.Sprintf("자산#%d[%s] %s", a.ID, a.Type, target)
}

// hasEnabledRule 규칙 세트에 활성화된 규칙이 있는지 여부를 확인합니다.
func hasEnabledRule(rules []AssetInterceptRule) bool {
	for _, r := range rules {
		if r.Enabled {
			return true
		}
	}
	return false
}

// AssetGateDecision는 "선 차단 후 허용" 게이트에 의한 후보 문자열 집합의 판단 결과입니다.
type AssetGateDecision struct {
	Allowed bool
	Reason  string // 거부 사유(자산 식별 제외) Allowed=true인 경우 비어 있음
}

// EvaluateAssetGate 작업 수준 게이트 결정 실행:
//  1. 활성화된 blockRules → 거부(차단 이유)를 누르세요.
//  2. 그렇지 않으면 allowRules에 대해 활성화된 항목이 있고 그 중 아무것도 적중되지 않은 경우 → 거부(허용 범위 밖)됩니다.
//  3. 그렇지 않으면 놓습니다.
//
// allowRules가 비어 있거나 활성화된 항목이 없으면 허용 게이트가 적용되지 않습니다(즉, 화이트리스트가 활성화되지 않고 모두 허용됩니다).
// 모든 자산을 차단하려면 "구성되지 않은 허용 규칙"을 피하세요.
func EvaluateAssetGate(blockRules, allowRules []AssetInterceptRule, domains, ips, urls []string) AssetGateDecision {
	if rule, _, ok := MatchAssetInterceptRules(blockRules, domains, ips, urls); ok {
		return AssetGateDecision{Allowed: false, Reason: rule.Reason()}
	}
	if hasEnabledRule(allowRules) {
		if _, _, ok := MatchAssetInterceptRules(allowRules, domains, ips, urls); !ok {
			return AssetGateDecision{Allowed: false, Reason: "작업 권한(화이트리스트) 범위를 벗어나면 테스트가 허용되지 않습니다."}
		}
	}
	return AssetGateDecision{Allowed: true}
}

// AssetInterceptHit는 게이트에서 거부된 자산을 설명합니다(가로채기 적중 또는 허용되지 않음).
type AssetInterceptHit struct {
	Asset  *Asset
	Reason string // 읽을 수 있는 이유
}

// Describe는 사람이 읽을 수 있는 설명(자산 정보 + 이유)을 반환합니다.
func (h AssetInterceptHit) Describe() string {
	return fmt.Sprintf("%s → %s", h.Asset.InterceptLabel(), h.Reason)
}

// ListAssetInterceptRules는 *DB라는 동일한 이름의 메소드를 투명하게 전송하므로 호출자가 AssetStore만 보유할 수 있습니다.
// (예: agent 도구)도 규칙을 읽을 수 있습니다.
func (s *AssetStore) ListAssetInterceptRules() ([]AssetInterceptRule, error) {
	return s.db.ListAssetInterceptRules()
}

// CheckAssetsIntercept id를 눌러 자산을 로드하고 "첫 번째 차단 후 허용" 게이트 판단을 하나씩 실행하고 모두 반환합니다.
// 거부된 자산. 차단 규칙 = 전역 ∪ 작업 수준 block; 허용 규칙 = 작업 수준 allow(이 작업에만 해당)
// id가 없을 때 빨리 반환하십시오. 가로채기가 scope에 의해 약화되지 않도록 보장하려면 전역 GetByIDs(작업 전체 필터링 아님)를 사용하세요.
func (s *AssetStore) CheckAssetsIntercept(taskID int64, ids []int64) ([]AssetInterceptHit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	blockRules, err := s.db.ListAssetInterceptRules()
	if err != nil {
		return nil, err
	}
	var allowRules []AssetInterceptRule
	if taskID > 0 {
		tb, ta, err := s.TaskInterceptRulesSplit(taskID)
		if err != nil {
			return nil, err
		}
		blockRules = append(blockRules, tb...)
		allowRules = ta
	}
	// 차단 규칙도 없고 허용 규칙도 활성화되어 있지 않습니다. → 판단이 필요하지 않으며 모두 허용됩니다.
	if len(blockRules) == 0 && !hasEnabledRule(allowRules) {
		return nil, nil
	}
	assets, err := s.GetByIDs(ids)
	if err != nil {
		return nil, err
	}
	var hits []AssetInterceptHit
	for _, a := range assets {
		domains, ips, urls := a.interceptCandidates()
		if d := EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
			hits = append(hits, AssetInterceptHit{Asset: a, Reason: d.Reason})
		}
	}
	return hits, nil
}
