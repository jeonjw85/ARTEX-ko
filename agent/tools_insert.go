package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// assetInterceptCandidates는 삽입할 자산 입력 항목의 도메인 이름/IP/URL 후보 문자열을 추출하며, 이는 자산 가로채기 및 매칭에 사용됩니다.
// URL의 host는 분류되지 않으므로 "URL 전용"이 있는 서비스/엔드포인트 자산도 도메인 이름/IP 규칙에 의해 적중될 수 있습니다.
func assetInterceptCandidates(item assetInputItem) (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, item.Domain)
	for _, d := range item.BoundDomains {
		add(&domains, d)
	}
	add(&ips, item.IP)
	add(&ips, item.ServiceIP)
	add(&urls, item.URL)
	if item.URL != "" {
		if u, err := url.Parse(item.URL); err == nil {
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

// assetInputLabel는 설명 메시지를 가로채는 데 사용되는 삽입할 자산의 짧은 ID를 반환합니다.
func assetInputLabel(item assetInputItem) string {
	typ := strings.TrimSpace(item.Type)
	var target string
	switch {
	case strings.TrimSpace(item.Domain) != "":
		target = strings.TrimSpace(item.Domain)
	case strings.TrimSpace(item.URL) != "":
		target = strings.TrimSpace(item.URL)
	case strings.TrimSpace(item.IP) != "":
		target = strings.TrimSpace(item.IP)
	case strings.TrimSpace(item.ServiceIP) != "":
		target = strings.TrimSpace(item.ServiceIP)
	default:
		target = "(알려지지 않은)"
	}
	if typ != "" {
		return fmt.Sprintf("[%s] %s", typ, target)
	}
	return target
}

// =====================================================================
// Unified asset insertion tools
// =====================================================================

// SetAssetStore wires the asset store and company store onto this ToolSet
// so the insert_assets, add_company_scope, and list_assets tools are active.
func (t *ToolSet) SetAssetStore(as *db.AssetStore, cs *db.CompanyStore) {
	t.as = as
	t.cs = cs
}

// assetInputItem is one element of the insert_assets "assets" array.
type assetInputItem struct {
	Type string `json:"type"` // root_domain|ip|subdomain|app|service|endpoint

	// ---- root_domain / subdomain ----
	Domain      string   `json:"domain"`
	ICP         string   `json:"icp"`
	RecordType  string   `json:"record_type"`
	RecordValue []string `json:"record_value"`

	// ---- ip ----
	IP           string           `json:"ip"`
	BoundDomains []string         `json:"bound_domains"`
	OpenPorts    []db.PortService `json:"open_ports"`

	// ---- app ----
	AppName     string `json:"app_name"`
	BundleID    string `json:"bundle_id"`
	Category    string `json:"category"`
	Description string `json:"description"`
	AppICP      string `json:"app_icp"`
	CompanyID   *int64 `json:"company_id"` // explicit company link (app only; others auto-attribute via scope)

	// ---- service (http) ----
	URL           string           `json:"url"`
	Technologies  []string         `json:"technologies"`
	StatusCode    *int             `json:"status_code"`
	ContentLength *int64           `json:"content_length"`
	PageTitle     string           `json:"page_title"`
	FaviconMMH3   string           `json:"favicon_mmh3"`
	Auth          []map[string]any `json:"auth"`
	ServiceName   string           `json:"service_name"`
	ServiceIP     string           `json:"service_ip"` // optional enrichment IP

	// ---- service (other) ----
	Port  int    `json:"port"`
	Proto string `json:"proto"`

	// ---- endpoint ----
	Method string           `json:"method"`
	Params []map[string]any `json:"params"`
}

// insertAssets is the unified insert_assets agent tool.
func (t *ToolSet) insertAssets() actool.CoreTool {
	return writeTool(
		"insert_assets",
		"새로 발견된 자산을 일괄 등록하여 여러 유형을 한 번에 혼합합니다(type 열거 참조). \n"+
			"유형별 필수 입력사항：root_domain→domain；ip→ip（반드시 IPv4/IPv6，호스트 이름이 아닌 이름）；subdomain→domain；app→app_name；service(HTTP)→url；service(아니요HTTP)→service_name+port（ip/domain 하나 이상 입력하세요.）；endpoint→url+method。다른 필드의 의미는 해당 설명을 참조하세요.。\n"+
			"auth/technologies/params는 추가 병합(append)이며 원래 값을 덮어쓰지 않습니다. \n"+
			"반환: {results:[{index,id,type}], errors:[{index,error}]}",
		obj(map[string]any{
			// task_id는 모델에 노출되지 않습니다. worker는 task가 SetTaskID를 통해 프로그램에 의해 정식으로 할당된 모델에 속합니다(handler 참조).
			"assets": map[string]any{
				"type":        "array",
				"description": "자산 배열, 각 요소는 자산 기록에 해당합니다.",
				"items": obj(map[string]any{
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"},
						"description": "자산 유형",
					},
					// root_domain / subdomain
					"domain":      str("루트 도메인 이름 또는 하위 도메인 이름(root_domain/subdomain 필요)"),
					"icp":         str("ICP 등록번호(선택)"),
					"record_type": str("DNS 분석 유형: A/AAAA/CNAME/MX 등(subdomain 옵션)"),
					"record_value": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "DNS 구문 분석된 값 목록(subdomain는 선택 사항입니다. 예: [\"1.2.3.4\",\"2.3.4.5\"])",
					},
					// ip
					"ip": str("IP 주소는 IPv4/IPv6 주소여야 하며 호스트 이름은 입력할 수 없습니다(호스트 이름의 경우 type=subdomain의 domain 필드를 사용하십시오). ip 유형이 필요합니다. service/endpoint 유형은 선택 사항이며 IP를 연결하는 데 사용됩니다."),
					"bound_domains": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "이 IP에 바인딩된 도메인 이름 목록(ip 유형은 선택 사항)",
					},
					"open_ports": map[string]any{
						"type":        "array",
						"description": "오픈 포트 목록(ip 유형은 옵션)",
						"items": obj(map[string]any{
							"port":    intp("포트 번호"),
							"service": str("http/ssh/mysql 등의 서비스 이름(선택 사항)"),
						}, "port"),
					},
					// app
					"app_name":    str("애플리케이션 이름(app 유형에 필수)"),
					"bundle_id":   str("Bundle ID (app 유형 옵션)"),
					"category":    str("애플리케이션 분류(선택)"),
					"description": str("애플리케이션 설명(선택사항)"),
					"app_icp":     str("ICP 등록 신청(선택)"),
					"company_id":  intp("기업 id 귀속(app 유형은 선택 사항입니다. app는 scope에 의해 자동으로 귀속될 수 없으며 명시적으로 지정해야 합니다. id는 add_company_scope에 의해 반환됩니다.)"),
					// service (http)
					"url":         str("프로토콜 및 포트를 포함한 전체 URL(HTTP 서비스가 필요하며 service_type는 자동으로 http로 설정됨)"),
					"status_code": intp("HTTP 응답 상태 코드(예: 200/301/403/404)(선택 사항)"),
					"content_length": map[string]any{
						"type":        "integer",
						"description": "HTTP 응답 본문 바이트 수(선택 사항)",
					},
					"page_title":   str("페이지 <title> 콘텐츠(선택 사항)"),
					"favicon_mmh3": str("favicon MMH3 해시(선택 사항)"),
					"technologies": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "지문/기술 스택 목록(예: [\"Nginx\",\"Vue\",\"Bootstrap\"](선택 사항)",
					},
					"auth": map[string]any{
						"type":        "array",
						"description": "각각 type/username/password 및 기타 필드를 포함하는 검색된 인증 정보 목록(선택 사항, 추가 시 포함되지 않음)",
						"items":       map[string]any{"type": "object"},
					},
					// service(other, HTTP 아님)
					"service_name": str("ssh/mysql/redis와 같은 서비스 이름(service가 HTTP가 아닌 경우 필수)"),
					"port":         intp("포트 번호(service가 HTTP가 아닌 경우 필수)"),
					// endpoint
					"method": str("HTTP 방법: GET/POST/PUT/PATCH/DELETE 등 (endpoint 필요)"),
					"params": map[string]any{
						"type":        "array",
						"description": "각각 location(query/body/header/path)/name/value/type를 포함하는 요청 매개변수 목록(선택사항, 추가 시 포함되지 않음)",
						"items":       map[string]any{"type": "object"},
					},
				}, "type"),
			},
		}, "assets"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("insert_assets가 활성화되지 않음: AssetStore가 초기화되지 않음"), nil
			}
			var a struct {
				Assets []assetInputItem `json:"assets"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			// task_id는 프로그램(worker: SetTaskID)에 의해 정식으로 할당되었으며 모델이 허용되지 않습니다. - 모델 누락/잘못 전송을 방지하기 위해
			// 결과적으로 자산이 작업에 할당되지 않거나 잘못된 작업에 할당됩니다. 작업 컨텍스트가 없는 호출자(auto/pentest/chat)는 t.taskID=0입니다.
			taskID := t.taskID

			type result struct {
				Index int    `json:"index"`
				ID    int64  `json:"id"`
				Type  string `json:"type"`
			}
			type errEntry struct {
				Index int    `json:"index"`
				Error string `json:"error"`
			}

			var results []result
			var errs []errEntry

			// 자산 게이트 규칙은 한 번 로드됩니다. 판독에 실패하면 판정을 건너뜁니다(삽입이 차단되지 않음).
			// 차단 규칙 = 전역 ∪ 작업 수준 block; 허용 규칙 = 작업 수준 allow.
			blockRules, _ := t.as.ListAssetInterceptRules()
			var allowRules []db.AssetInterceptRule
			if t.taskID > 0 {
				if tb, ta, err := t.as.TaskInterceptRulesSplit(t.taskID); err == nil {
					blockRules = append(blockRules, tb...)
					allowRules = ta
				}
			}

			for i, item := range a.Assets {
				// 자산 게이트(Asset Gate): 먼저 차단한 다음 허용합니다. 거부된 자산은 삽입이 금지됩니다(Upsert 및 후속 부작용 건너뛰기).
				domains, ips, urls := assetInterceptCandidates(item)
				if d := db.EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
					errs = append(errs, errEntry{
						Index: i,
						Error: fmt.Sprintf("자산 %s %s, 삽입 비활성화됨", assetInputLabel(item), d.Reason),
					})
					continue
				}

				typ := strings.TrimSpace(item.Type)
				var id int64
				var err error

				switch typ {
				case "root_domain":
					id, err = t.as.UpsertRootDomain(db.UpsertRootDomainReq{
						Domain: item.Domain,
						ICP:    item.ICP,
						TaskID: taskID,
					})

				case "ip":
					id, err = t.as.UpsertIP(db.UpsertIPReq{
						IP:           item.IP,
						BoundDomains: item.BoundDomains,
						OpenPorts:    item.OpenPorts,
						TaskID:       taskID,
					})

				case "subdomain":
					id, err = t.as.UpsertSubdomain(db.UpsertSubdomainReq{
						Domain:      item.Domain,
						RecordType:  item.RecordType,
						RecordValue: item.RecordValue,
						ICP:         item.ICP,
						TaskID:      taskID,
					})

				case "app":
					id, err = t.as.UpsertApp(db.UpsertAppReq{
						Name:        item.AppName,
						BundleID:    item.BundleID,
						Category:    item.Category,
						Description: item.Description,
						ICP:         item.AppICP,
						CompanyID:   item.CompanyID,
						TaskID:      taskID,
					})

				case "service":
					// distinguish HTTP vs other by presence of url
					if item.URL != "" {
						// agent may send "ip" or "service_ip" for the enrichment IP; accept both
						svcIP := item.ServiceIP
						if svcIP == "" {
							svcIP = item.IP
						}
						id, err = t.as.UpsertHTTPService(db.UpsertHTTPServiceReq{
							URL:           item.URL,
							Technologies:  item.Technologies,
							StatusCode:    item.StatusCode,
							ContentLength: item.ContentLength,
							PageTitle:     item.PageTitle,
							FaviconMMH3:   item.FaviconMMH3,
							Auth:          item.Auth,
							IP:            svcIP,
							TaskID:        taskID,
						})
					} else {
						id, err = t.as.UpsertOtherService(db.UpsertOtherServiceReq{
							Domain:      item.Domain,
							IP:          item.IP,
							Port:        item.Port,
							ServiceName: item.ServiceName,
							Auth:        item.Auth,
							TaskID:      taskID,
						})
					}

				case "endpoint":
					id, err = t.as.UpsertEndpoint(db.UpsertEndpointReq{
						URL:    item.URL,
						Method: item.Method,
						Params: item.Params,
						IP:     item.ServiceIP,
						TaskID: taskID,
					})

				default:
					errs = append(errs, errEntry{Index: i, Error: "unknown type: " + typ})
					continue
				}

				if err != nil {
					errs = append(errs, errEntry{Index: i, Error: err.Error()})
					continue
				}
				results = append(results, result{Index: i, ID: id, Type: typ})
				t.writes.Assets++
				t.anchorOwner(id)
				if taskID > 0 {
					var sourceNodeID *int64
					if t.ownerNode > 0 {
						nodeID := t.ownerNode
						sourceNodeID = &nodeID
					}
					summary := "insert_assets를 통해 등록된 Agent"
					if t.ownerNode > 0 {
						summary = fmt.Sprintf("Worker 의도 #%d insert_assets에 의해 등록됨", t.ownerNode)
					}
					_ = t.as.SetTaskAssetSource(taskID, id, "agent", summary, sourceNodeID)
				}
				// 테스트 범위 자동 입력(source='auto'): worker의 최상위 레벨에 명시적으로 삽입된 항목에 대해서만
				// 유형과 보수적인 범위; side-effect에서 파생된 자산은 이를 통과하지 않으므로 범위가 맹목적으로 확장되지 않습니다. taskID=0일 때 작동하지 않습니다.
				// 이는 적용 범위 스위치와 아무 관련이 없습니다. task_scope는 작업의 범위 경계(list/ 쿼리의 필터링 기반)입니다.
				// 커버리지 스위치는 범위 자체를 누적할지 여부가 아니라 지표 계산을 위한 분모로 사용할지 여부만 결정합니다.
				{
					svcIP := item.ServiceIP
					if svcIP == "" {
						svcIP = item.IP
					}
					_ = t.as.AddAutoScope(taskID, typ, item.Domain, item.URL, svcIP)
				}
			}

			return jsonResult(map[string]any{
				"results": results,
				"errors":  errs,
			})
		},
	)
}

// addCompanyScope writes to company_scope table and triggers asset attribution.
func (t *ToolSet) addCompanyScope() actool.CoreTool {
	return writeTool(
		"add_company_scope",
		"도메인 이름/IP/CIDR/ICP 등록/기업 키워드를 회사의 [자산 범위]에 추가하세요. 도메인 이름, 네트워크 및 ICP는 자동으로 히트 자산을 청구하며 키워드는 범위 프롬프트로만 에이전트에 제공됩니다. \n"+
			"회사 이름은 고유합니다: company. 존재하지 않는 경우 새로 작성하십시오. 이미 존재하는 경우 다시 사용하세요(범위 병합만 가능). \n"+
			"scope는 한 번에 한 줄씩 자동으로 식별됩니다. 루트 도메인 이름 / URL / 단일 IP / CIDR 네트워크 세그먼트 / ICP 등록 / 기업 키워드. \n"+
			"reason에 귀속근거(whois/ 인증서/ASN 등)를 반드시 설명해주세요. \n"+
			"Guardrail: 네이키드 TLD 및 광범위한 네트워크 세그먼트(IPv4 접두사는 /16-/32여야 하고, IPv6 접두사는 /32-/128여야 함)를 거부하고, 잘못된 줄을 건너뛰고 errors에 반환합니다.",
		obj(map[string]any{
			"company": str("회사명(없으면 생성, 있으면 재사용, 이름은 고유함)"),
			"scope":   str("자산 범위, 한 줄에 한 줄: 도메인 이름 / URL / IP / CIDR / ICP 파일링 / 기업 키워드"),
			"reason":  str("귀속근거(증거/출처)를 반드시 기재해야 합니다."),
			"logo":    str("회사 아이콘 URL(선택 사항, 새 회사를 생성하는 경우에만 유효)"),
		}, "company", "scope"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("add_company_scope가 활성화되지 않음: CompanyStore가 초기화되지 않음"), nil
			}
			var a struct {
				Company string `json:"company"`
				Scope   string `json:"scope"`
				Reason  string `json:"reason"`
				Logo    string `json:"logo"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if strings.TrimSpace(a.Company) == "" {
				return actool.Errorf("company는 비워둘 수 없습니다."), nil
			}
			companyID, _, err := t.cs.UpsertCompany(a.Company, a.Logo)
			if err != nil {
				return actool.Errorf("회사를 생성/가져오지 못했습니다: " + err.Error()), nil
			}
			lines := splitLines(a.Scope)
			added, skipped, invalid, errMsgs := t.cs.AddScope(companyID, lines, a.Reason)
			out := map[string]any{
				"company_id": companyID,
				"added":      added,
				"skipped":    skipped,
				"invalid":    invalid,
			}
			if len(errMsgs) > 0 {
				out["errors"] = errMsgs
			}
			return jsonResult(out)
		},
	)
}

// addTaskScope lets the plan agent add test scope to THE CURRENT TASK — the coverage
// denominator and the task's authorization edge. Worker discoveries are auto-scoped
// (precise host) by insertAssets; this tool is for DELIBERATELY WIDENING: pull a whole
// root domain or whole company into scope, or add a specific subdomain / ip.
func (t *ToolSet) addTaskScope() actool.CoreTool {
	return writeTool(
		"add_task_scope",
		"[이 작업]에 테스트 범위를 추가합니다. 이는 이 작업의 인증 경계이자 자산 테스트 범위의 분모이기도 합니다. \n"+
			"kind는 다음을 지원합니다: company(전체 회사 자산) / root_domain(모든 하위 도메인을 포함한 전체 루트 도메인) / subdomain(정확한 단일 하위 도메인) / ip / cidr / icp / keyword. \n"+
			"설명: 호스트 worker가 하나씩 발견되면 시스템에 의해 범위(정확한 하위 도메인)에 [자동으로] 추가됩니다. 이 도구는 [활성 확장]에 사용됩니다. 전체 루트 도메인/회사 전체를 포함하거나 특정 하위 도메인/IP를 추가로 지정합니다. \n"+
			"value: company는 회사 이름 또는 id를 전달합니다(회사는 이미 존재해야 함). root_domain/subdomain는 도메인 이름을 전달합니다. ip/cidr는 IP 또는 네트워크 세그먼트를 전달합니다. icp/keyword는 등록 번호 또는 회사 키워드를 전달합니다. \n"+
			"reason(감사 가능)에 대한 근거를 반드시 설명하세요. 여러 라인에는 entries 어레이를 사용하십시오.",
		obj(map[string]any{
			"entries": map[string]any{"type": "array", "description": "일괄：[{kind, value}]。kind∈company/root_domain/subdomain/ip/cidr/icp/keyword。", "items": map[string]any{"type": "object"}},
			"kind":    str("[하나의] company / root_domain / subdomain / ip / cidr / icp / keyword"),
			"value":   str("[하나의] 회사 이름 또는id / 도메인 / IP / CIDR / ICP / 키워드"),
			"reason":  str("가입근거(감사용), 반드시 기재"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("add_task_scope가 활성화되지 않음: AssetStore가 초기화되지 않음"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("add_task_scope에는 작업 컨텍스트가 필요합니다(현재 task는 없음)."), nil
			}
			type scopeEntry struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			}
			var a struct {
				Entries    []scopeEntry `json:"entries"`
				scopeEntry              // 단일 모드
				Reason     string       `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			items := a.Entries
			if len(items) == 0 {
				items = []scopeEntry{a.scopeEntry}
			}
			var added []map[string]any
			errs := map[string]string{}
			for i, e := range items {
				ts, err := t.as.AddAgentScope(t.taskID, strings.TrimSpace(e.Kind), e.Value, a.Reason, "agent")
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				added = append(added, map[string]any{"kind": ts.Kind, "domain": ts.Domain, "net": ts.Net, "value": ts.Value, "company_id": ts.CompanyID})
			}
			out := map[string]any{"added": added}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		},
	)
}

// listUntestedAssets lets the plan agent pull the current + directly inherited
// scope's not-yet-tested assets on demand (filter by type, paginated).
func (t *ToolSet) listUntestedAssets() actool.CoreTool {
	return readTool(
		"list_untested_assets",
		"팩트 앵커에 포함되지 않은 [이 작업 및 직접 관련된 작업] 범위 내의 자산을 쿼리합니다(관련 범위는 읽기 전용이므로 보충 테스트 여부를 판단할 수 있으며 사용자를 대신하여 결정을 내리지 않습니다). \n"+
			"자산 유형별 선택적 필터링: root_domain/subdomain/service/app/endpoint/ip. \n"+
			"페이징: page는 1부터 시작하고, page_size의 기본값은 10입니다. Return {assets:[{id,type,label}], total, page, page_size}. 작업 컨텍스트만 사용할 수 있습니다.",
		obj(map[string]any{
			"type":      str("자산 유형 필터링(선택 사항): root_domain/subdomain/service/app/endpoint/ip"),
			"page":      intp("1부터 시작하는 페이지 번호(기본값 1)"),
			"page_size": intp("페이지당 수(기본값 10)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_untested_assets가 활성화되지 않음: AssetStore가 초기화되지 않음"), nil
			}
			if t.taskID <= 0 || t.ts == nil {
				return actool.Errorf("list_untested_assets에는 작업 컨텍스트가 필요합니다."), nil
			}
			var a struct {
				Type     string `json:"type"`
				Page     int    `json:"page"`
				PageSize int    `json:"page_size"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Page <= 0 {
				a.Page = 1
			}
			if a.PageSize <= 0 {
				a.PageSize = 10
			}
			offset := (a.Page - 1) * a.PageSize
			assets, total, err := t.as.ListUntestedAssetsWithSources(t.taskID, strings.TrimSpace(a.Type), a.PageSize, offset)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"assets": assets, "total": total, "page": a.Page, "page_size": a.PageSize,
			})
		},
	)
}

// listAssets lets an agent query the asset table.
func (t *ToolSet) listAssets() actool.CoreTool {
	return readTool(
		"list_assets",
		"자산 라이브러리 쿼리: DSL 표현식 검색 또는 id/ids를 눌러 직접 가져옵니다. 페이징이 지원됩니다. [이 작업 및 직접 관련된 작업]의 테스트 범위 내의 자산만 반환됩니다. \n"+
			"DSL: field=value 퍼지(ILIKE) | field==value 정밀 | field!=value 제외 | 숫자 필드 지원 > >= < <= | 단순한 단어 = 전체 텍스트가 흐릿함; AND/OR 조합(AND는 우선순위가 높음), 괄호로 그룹화할 수 있습니다. 자산 유형은 독립적인 type 매개변수를 사용하며 DSL에 쓰지 않습니다. \n"+
			"id/ids가 전송되지 않는 경우 dsl는 비어 있지 않아야 합니다(무조건 전체 쿼리는 허용되지 않음). \n"+
			"사용 가능한 필드：domain(뿌리/아들/서비스 도메인 이름)、root_domain、ip、url、page_title、icp、service_name、app_name、method(좋다 GET/POST)、service_type(http|other)、record_type(좋다 A/CNAME)、technology(정렬，=희미한 ==정확한)、port/status_code/company_id(정수)。\n"+
			"예: status_code>=400 AND technology=shiro; (port==80 OR port==443) AND technology=nginx",
		obj(map[string]any{
			"dsl":    str(`DSL 쿼리 표현식(구문/필드에 대한 도구 설명 참조) id/ids가 전달되지 않은 경우 비어 있으면 안 됩니다.`),
			"type":   str("자산 유형 필터：root_domain|ip|subdomain|app|service|endpoint（독립 필드로 다음과 함께 사용할 수 있습니다. dsl 씌우다; 홀로 type 쿼리할 만큼 충분하지 않지만 여전히 필요합니다. dsl）"),
			"id":     intp("단일 자산 id에서 직접 가져오기(선택 사항, dsl/type와 상호 배타적)"),
			"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "여러 자산 id에서 직접 가져오기(선택 사항, dsl/type와 상호 배타적)"},
			"limit":  intp("상한값을 반환합니다. 기본값은 10입니다(선택 사항)."),
			"offset": intp("페이징 오프셋, 기본값 0(선택 사항)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_assets가 활성화되지 않음: AssetStore가 초기화되지 않음"), nil
			}
			var a struct {
				DSL    string  `json:"dsl"`
				Type   string  `json:"type"`
				ID     int64   `json:"id"`
				IDs    []int64 `json:"ids"`
				Limit  int     `json:"limit"`
				Offset int     `json:"offset"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Limit <= 0 {
				a.Limit = 10
			}

			var assets []*db.Asset
			var err error
			switch {
			case a.ID > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, []int64{a.ID})
			case len(a.IDs) > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, a.IDs)
			case a.DSL != "":
				assets, err = t.as.QueryDSLInScope(a.DSL, a.Type, t.taskID, a.Limit, a.Offset)
			default:
				return actool.Errorf("id/ids가 전송되지 않는 경우 dsl는 비워둘 수 없습니다. 모든 자산에 대한 무조건 쿼리는 허용되지 않습니다. 쿼리 조건을 제공하십시오."), nil
			}
			if err != nil {
				return actool.Errorf("DSL 오류: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"count":  len(assets),
				"assets": assets,
			})
		},
	)
}

// listCompanies lets an agent enumerate companies (엔터프라이즈) with their scope + asset count.
func (t *ToolSet) listCompanies() actool.CoreTool {
	return readTool(
		"list_companies",
		"자산 라이브러리의 [기업/회사], 해당 자산 범위(scope) 및 귀속된 자산 수를 나열합니다. 어느 회사인지 알아보는데,"+
			"company_id를 가져옵니다(insert_assets는 app, list_assets와 연결되어 있으며 company_id로 필터링할 때 사용됩니다)."+
			"선택적 search 회사 이름별 퍼지 필터(대소문자 구분 안 함), 모두 반환하려면 비워 두세요.",
		obj(map[string]any{
			"search": str("회사 이름별 퍼지 필터(선택 사항, 대소문자 구분) 모두 반환하려면 비워 두세요."),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("list_companies가 활성화되지 않음: CompanyStore가 초기화되지 않음"), nil
			}
			var a struct {
				Search string `json:"search"`
			}
			_ = json.Unmarshal(in, &a)
			cos, err := t.cs.ListCompanies()
			if err != nil {
				return actool.Errorf("회사를 쿼리하지 못했습니다: " + err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Search))
			type companyOut struct {
				ID         int64    `json:"id"`
				Name       string   `json:"name"`
				AssetCount int      `json:"asset_count"`
				Scope      []string `json:"scope"`
			}
			out := make([]companyOut, 0, len(cos))
			for _, c := range cos {
				if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
					continue
				}
				scope := make([]string, 0, len(c.Scope))
				for _, r := range c.Scope {
					scope = append(scope, r.Raw)
				}
				out = append(out, companyOut{ID: c.ID, Name: c.Name, AssetCount: c.AssetCount, Scope: scope})
			}
			return jsonResult(map[string]any{"count": len(out), "companies": out})
		},
	)
}

// splitLines splits a multi-line string into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// WorkerTools returns the tool set for a work agent.
func (t *ToolSet) WorkerTools() []actool.CoreTool {
	return []actool.CoreTool{
		// list_findings 예약됨: 취약점을 보고하기 전에 이 작업에서 확인된 취약점을 확인하여 동일한 취약점이 반복적으로 보고되는 것을 방지하세요.
		t.listFindings(),
		t.addFinding(), t.recordFact(),
		// asset management (handlers guard nil store internally)。
		// add_company_scope 제공되지 않음 worker: 기업 자산의 범위 정의는 계획/마스터/Auto의 책임이며, worker는 탐색만 수행합니다.
		t.insertAssets(), t.listAssets(),
		// work를 되돌아보세요: worker는 작업 중복을 피하기 위해 다른 work의 관찰을 재사용할 수도 있습니다.
		// search_all_worker_traces: intent_id를 먼저 알 필요는 없습니다. 키워드를 따라가면 전 세계적으로 히트 단계를 얻을 수 있습니다.
		// get_worker_trace: 특정 work/검색/즉시 전체 콘텐츠 가져오기의 다음 단계를 잠급니다.
		t.searchAllWorkerTraces(), t.getWorkerTrace(),
		// node_detail: worker intent_id/노드 id를 얻은 후 노드의 전체 세부 정보를 확인할 수 있습니다(위 검토에 협조).
		t.nodeDetail(),
		// 다음 도구는 worker에 대해 아직 사용할 수 없으며 planner/main만 사용할 수 있습니다(컨텍스트를 읽고 work 전체를 검토하는 것은 계획 책임입니다.
		// worker는 단일 의도(list_facts / list_companies / list_worker_traces)만 실행하고 다시 작성합니다.
	}
}

// MainAgentTools returns the human-interface tool set.
func (t *ToolSet) MainAgentTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		t.expandDigest(), // cold-digest §6.1
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.addHint(), t.addIntent(),
		// steer_work: 사람들은 실행 중인 의도(work)에 실시간으로(진행을 중단하거나 손실하지 않고) 수정 지침을 주입할 수 있습니다.
		t.steerWorkTool(),
		// set_goals: 사람들은 런타임에 이 작업에 새로운 최종 목표를 추가할 수 있습니다(플래너는 이를 기반으로 달성 여부를 다시 판단합니다).
		t.setGoals(),
		// set_constraints: 사람들은 런타임에 이 작업에 대한 작업 제약 조건(allow/deny)을 추가/수정하고 planner/worker의 탐색 경계를 제약할 수 있습니다.
		t.setConstraints(),
		// asset management (handlers guard nil store internally)
		t.insertAssets(), t.addCompanyScope(), t.listAssets(),
		t.addFinding(), t.recordFact(),
		t.addTaskScope(),
		// list_untested_assets: 필요에 따라 이 작업 범위 내에서 테스트되지 않은 자산(유형 + 페이징)을 확인하고 재량에 따라 추가 테스트를 수행합니다.
		t.listUntestedAssets(),
	}
}

// AllDomainTools returns the union of all domain tools across all agent types,
// deduped by name (mainagent order wins). Used by the server to build a registry
// for injecting domain tools into agents (Auto, custom) that don't own a per-task
// ToolSet. The caller provides real stores; tools are callable at taskID=0 scope.
func (t *ToolSet) AllDomainTools() []actool.CoreTool {
	seen := map[string]bool{}
	var out []actool.CoreTool
	all := append(append(t.MainAgentTools(), t.PlannerTools()...), t.WorkerTools()...)
	for _, tool := range all {
		if !seen[tool.Name()] {
			seen[tool.Name()] = true
			out = append(out, tool)
		}
	}
	return out
}
