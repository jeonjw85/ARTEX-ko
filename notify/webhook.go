package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// webhookChannel는 범용 Webhook 어댑터입니다: 사용자 정의 URL, 메소드, 요청 헤더 및 JSON 템플릿.
// 이 존재로 인해 Slack / Mattermost / Discord / 자체 구축 시스템 각각에 대한 구현을 작성할 필요가 없습니다.
// 이러한 플랫폼은 모두 구성 가능한 템플릿으로 처리될 수 있습니다.
type webhookChannel struct{}

func (webhookChannel) Kind() string { return KindWebhook }

// 일반 Webhook에는 공식적인 제한이 없습니다. 0을 반환하면 기본적으로 현재 제한이 없음을 의미하며 이는 피어의 기능에 따라 사용자가 사용자 정의할 수 있습니다.
func (webhookChannel) DefaultRatePerMin() int { return 0 }

// 마스크 url 및 headers: 대상 주소 자체에는 token가 포함되는 경우가 많으며 사용자 지정 헤더에는 일반적으로 인증 자격 증명이 포함됩니다.
// 둘 다 인터페이스 에코에 나타나므로 차단해야 합니다.
// 가격은 편집 중에 헤더 중 하나를 변경하려면 헤더 그룹 전체를 다시 채워야 한다는 것입니다(마스크 값은 "원래 값 유지"로 해석됩니다)——
// 이러한 절충안은 의도적인 것입니다. 브라우저에 자격 증명을 표시하는 것보다 한 번 더 작성하는 것이 좋습니다.
func (webhookChannel) SecretKeys() []string { return []string{"url", "headers"} }

// 대상은 url입니다. url를 변경할 때 위치는 headers로 다시 지정되어야 합니다. 그렇지 않으면 원래 Authorization 헤더입니다.
// 마스크 우회의 주요 경로인 변경되지 않은 새 주소로 전송됩니다.
func (webhookChannel) DestinationKeys() []string { return []string{"url"} }

// webhookDefaultTemplate는 템플릿이 채워지지 않은 경우의 백업 요청 본문입니다. 간단한 JSON 구조,
// "하나의 JSON를 수신하여 데이터베이스에 저장"하는 자체 제작 수신기의 대부분을 다룹니다.
const webhookDefaultTemplate = `{
  "title": {{json .Title}},
  "batch": {{.Batch}},
  "count": {{.Count}},
  "items": [
{{- range $i, $it := .Items}}
{{- if $i}},{{end}}
    {
      "finding_id": {{$it.FindingID}},
      "name": {{json $it.Name}},
      "vulnclass": {{json $it.VulnClass}},
      "severity": {{json $it.Severity}},
      "summary": {{json $it.Summary}},
      "assets": {{json $it.Assets}},
      "detail_url": {{json $it.DetailURL}}
    }
{{- end}}
  ]
}`

// webhookTemplateData는 사용자 템플릿에 노출된 컨텍스트입니다.
type webhookTemplateData struct {
	Title   string
	Batch   bool
	Count   int
	Items   []webhookItem
	HomeURL string
	// SentAt는 수신 측이 기록할 전달 시간(RFC3339)입니다.
	SentAt string
}

type webhookItem struct {
	FindingID     int64
	Name          string
	VulnClass     string
	Severity      string
	SeverityLabel string
	Summary       string
	Assets        []string
	DetailURL     string
	FromStatus    string
	ToStatus      string
	// StatusLabel는 "보류 중 → 수정됨"과 같은 상태 변경에 대한 읽기 가능한 설명입니다. 상태 변경이 없으면 비어 있습니다.
	StatusLabel string
}

func (webhookChannel) Validate(cfg map[string]any) error {
	raw := cfgString(cfg, "url")
	if raw == "" {
		return errors.New("누락된 대상 URL")
	}
	if err := validateHTTPURL(raw); err != nil {
		return fmt.Errorf("대상 URL 유효하지 않음: %w", err)
	}
	if m := strings.ToUpper(cfgString(cfg, "method")); m != "" && m != http.MethodGet && m != http.MethodPost && m != http.MethodPut && m != http.MethodPatch {
		return fmt.Errorf("지원되지 않는 방법 %s(GET/POST/PUT/PATCH 사용 가능)", m)
	}
	if tpl := cfgString(cfg, "body_template"); tpl != "" {
		if _, err := parseWebhookTemplate(tpl); err != nil {
			return fmt.Errorf("요청 본문 템플릿 구문 오류: %w", err)
		}
	}
	return nil
}

func (c webhookChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	method := strings.ToUpper(cfgString(cfg, "method"))
	if method == "" {
		method = http.MethodPost
	}

	// GET에는 요청 본문이 없습니다. 콘텐츠를 query에 채우는 것은 템플릿의 범위를 벗어나며 GET의 의미를 따르지 않습니다.
	// 따라서 GET는 "히트 트리거 후크"와 같은 수신기에만 적합합니다.
	var payload any
	if method != http.MethodGet {
		body, err := renderWebhookBody(cfgString(cfg, "body_template"), m)
		if err != nil {
			return 0, Permanent(err)
		}
		// 템플릿은 JSON를 문자열 형식으로 렌더링합니다. 여기서는 json.RawMessage로 변환되어 그대로 전송됩니다.
		// 사용자가 신중하게 구성한 구조를 JSON 문자열에 넣으려면 이중 이스케이프를 피하세요.
		if !json.Valid([]byte(body)) {
			return 0, Permanent(errors.New("요청 본문 템플릿의 렌더링 결과가 올바르지 않습니다. JSON"))
		}
		payload = json.RawMessage(body)
	}

	headers := cfgMap(cfg, "headers")
	if ct := cfgString(cfg, "content_type"); ct != "" {
		// 덮어쓰기는 허용되지만 명시적인 구성이 우선적으로 적용되도록 headers 이후에 적용됩니다.
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = ct
	}
	if _, err := doJSON(ctx, method, cfgString(cfg, "url"), headers, payload); err != nil {
		return 0, err
	}
	// 일반 Webhook는 텍스트를 자르지 않습니다(수신측은 사용자 고유의 서비스이며 크기는 body_template에 의해 결정됨).
	// 따라서 전체 배치가 배송된 것으로 간주됩니다.
	return len(m.Items), nil
}

// renderWebhookBody 사용자 템플릿(또는 기본 템플릿)을 사용하여 요청 본문을 렌더링합니다.
func renderWebhookBody(tpl string, m Message) (string, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = webhookDefaultTemplate
	}
	t, err := parseWebhookTemplate(tpl)
	if err != nil {
		return "", fmt.Errorf("요청 본문 템플릿 구문 오류: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, newWebhookTemplateData(m)); err != nil {
		return "", fmt.Errorf("요청 본문 템플릿을 렌더링하지 못했습니다: %w", err)
	}
	return buf.String(), nil
}

// parseWebhookTemplate 구문 분석 템플릿.
//
// missingkey=zero로 인해 누락된 map 키가 오류를 보고하는 대신 0 값으로 렌더링됩니다. 하지만 이 파일의 컨텍스트는 구조입니다.
// 주요 기능은 .Items가 비어 있을 때 range를 오류 없이 만드는 것입니다. 실제로 경계해야 할 것은 .Items가 nil로 바뀌는 것입니다.
func parseWebhookTemplate(tpl string) (*template.Template, error) {
	return template.New("body").Funcs(webhookTemplateFuncs).Option("missingkey=zero").Parse(tpl)
}

// webhookTemplateFuncs는 템플릿에 노출되는 도우미 기능입니다.
var webhookTemplateFuncs = template.FuncMap{
	// json 모든 값을 JSON로 직렬화합니다.
	//
	// 이 기능은 좋은 기능은 아니지만 필수입니다. 이를 생략하면 사용자는 직접 보간을 위해 {{.Title}}만 쓸 수 있습니다.
	// 취약점 제목에 따옴표나 줄 바꿈이 있는 한 전체 요청 본문은 더 이상 유효하지 않습니다. JSON - 수신측에서는
	// 거부되었으며 "JSON 구문 분석에 실패했습니다."라는 오류 메시지가 표시되었습니다. 제목에 따옴표가 있는 것은 전혀 예상치 못한 일이었습니다.
	"json": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	},
	// jsons는 JSON 조각을 다른 JSON 문자열 값에 포함하는 데 사용됩니다(문자열 이스케이프 레이어 수행).
	"jsons": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		quoted, err := json.Marshal(string(raw))
		if err != nil {
			return "", err
		}
		// 외부 따옴표 제거: 호출자가 따옴표 추가 여부를 결정합니다.
		return string(quoted[1 : len(quoted)-1]), nil
	},
}

func newWebhookTemplateData(m Message) webhookTemplateData {
	d := webhookTemplateData{
		Title:   markdownTitle(m),
		Batch:   m.Batch,
		Count:   len(m.Items),
		HomeURL: m.HomeURL,
		SentAt:  time.Now().Format(time.RFC3339),
		Items:   make([]webhookItem, 0, len(m.Items)),
	}
	for _, it := range m.Items {
		wi := webhookItem{
			FindingID:     it.FindingID,
			Name:          it.Name,
			VulnClass:     it.VulnClass,
			Severity:      it.Severity,
			SeverityLabel: SeverityLabel(it.Severity),
			Summary:       it.Summary,
			Assets:        append([]string{}, it.Assets...),
			DetailURL:     it.DetailURL,
			FromStatus:    it.FromStatus,
			ToStatus:      it.ToStatus,
		}
		if it.IsStatusChange() {
			wi.StatusLabel = StatusLabel(it.FromStatus) + " → " + StatusLabel(it.ToStatus)
		}
		d.Items = append(d.Items, wi)
	}
	return d
}
