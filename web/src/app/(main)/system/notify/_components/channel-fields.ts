// 채널 필드 테이블 및 구성 값에 대한 구문 분석 도구입니다.
//
// 이 부분은 뷰가 아닌 **데이터**이기 때문에 페이지와 분리되어 있습니다. 각 채널에 어떤 필드가 있는지 설명하고,
// 어떤 컨트롤을 사용해야 하며 양식 텍스트를 구성 값(JSON)으로 양방향 변환합니다.
// 별도의 파일을 배치한 후 여기로 이동하면 새 채널을 추가하기만 하면 되고, 페이지 자체는 변경할 필요가 없습니다.
// 채널 유형의 표시 이름 및 소개입니다. 카피라이팅에만 영향을 미치고 백엔드는 알 필요가 없기 때문에 프론트 엔드에 배치합니다.
export const KIND_LABEL: Record<string, string> = {
  dingtalk: "딩톡",
  feishu: "페이슈",
  wecom: "엔터프라이즈 위챗",
  webhook: "범용 Webhook",
  telegram: "Telegram",
  email: "우편",
};

// 각 채널에 대한 구성 필드 정의.
//
// 여기서는 백엔드 문제 schema를 허용하는 대신 의도적으로 프런트엔드 필드 테이블을 유지합니다. 백엔드는 다음 작업만 담당합니다.
// Validate(필수/형식), UI에는 레이아웃과 제어 유형이 필요하며 둘 다 서로 다른 것에 중점을 둡니다.
// 유일한 결합 지점은 secret_keys입니다. 비밀번호 상자로 렌더링되어야 하는 필드는 백엔드에서 제공됩니다.
// 채널만이 어떤 값이 자격 증명으로 계산되는지 알고 있기 때문에(Enterprise WeChat의 전체 Webhook가 자격 증명이므로,
// 그리고 Dingding은 그 중 하나일 뿐입니다(secret). 새 채널을 추가할 때 여기에 항목을 입력하지 않으면 양식이 비어 있게 됩니다.
// 자동 오류는 발생하지 않습니다(아래 hasFields 메시지가 표시됨).
export type FieldKind = "text" | "password" | "number" | "select" | "textarea" | "switch" | "kv" | "list";
export interface FieldDef {
  key: string;
  label: string;
  kind: FieldKind;
  placeholder?: string;
  help?: string;
  options?: { value: string; label: string }[];
}
export const CHANNEL_FIELDS: Record<string, FieldDef[]> = {
  dingtalk: [
    {
      key: "webhook",
      label: "Webhook 주소",
      kind: "text",
      placeholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
    },
    {
      key: "secret",
      label: "서명 키",
      kind: "password",
      help: "로봇 보안 설정에서 ＂서명 추가＂를 선택한 경우 작성합니다. ＂맞춤 키워드＂가 선택되어 있거나 보안 설정이 켜져 있지 않은 경우에는 공백으로 두십시오.",
    },
  ],
  feishu: [
    {
      key: "webhook",
      label: "Webhook 주소",
      kind: "text",
      placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    },
    { key: "secret", label: "서명 확인 키", kind: "password", help: "로봇이 ＂서명 확인＂을 켤 때 입력하고, 그렇지 않으면 비워 둡니다." },
  ],
  wecom: [
    {
      key: "webhook",
      label: "Webhook 주소",
      kind: "text",
      placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
    },
  ],
  webhook: [
    { key: "url", label: "타겟 URL", kind: "text", placeholder: "https://your-endpoint.example.com/hook" },
    {
      key: "method",
      label: "요청 방법",
      kind: "select",
      options: [
        { value: "POST", label: "POST(요청 본문 포함)" },
        { value: "PUT", label: "PUT(요청 본문 포함)" },
        { value: "PATCH", label: "PATCH(요청 본문 포함)" },
        { value: "GET", label: "GET(요청 본문 없음)" },
      ],
    },
    { key: "headers", label: "사용자 정의 요청 헤더", kind: "kv", help: "각 라인 KEY=VALUE(예: Authorization=Bearer xxx)" },
    {
      key: "body_template",
      label: "요청 본문 템플릿",
      kind: "textarea",
      help:
        "내장된 기본 템플릿을 사용하려면 비워 두세요. 변수: {{.Title}} {{.Batch}} {{.Count}} {{.HomeURL}} {{.SentAt}}," +
        "및 range .Items 아래의 .Name/.VulnClass/.Severity/.Summary/.Assets/.DetailURL/.StatusLabel." +
        "문자열을 삽입할 때 {{.Xxx}} 대신 {{json .Xxx}}를 사용하십시오. 그렇지 않으면 제목의 따옴표로 인해 JSON가 삭제됩니다.",
    },
  ],
  telegram: [
    { key: "bot_token", label: "Bot Token", kind: "password", placeholder: "123456:ABC-DEF..." },
    { key: "chat_id", label: "Chat ID", kind: "text", placeholder: "-1001234567890" },
    {
      key: "base_url",
      label: "API 주소",
      kind: "text",
      placeholder: "https://api.telegram.org",
      help: "공백으로 두고 공식 주소를 사용하세요. Bot API를 자체 생성하고 역생성 시 채워줍니다.",
    },
  ],
  email: [
    { key: "host", label: "SMTP 서버", kind: "text", placeholder: "smtp.example.com" },
    {
      key: "port",
      label: "포트",
      kind: "number",
      placeholder: "587",
      help: "587 가다 STARTTLS; 465 ＂암시적 TLS＂를 켜십시오.",
    },
    { key: "username", label: "계정", kind: "text" },
    { key: "password", label: "비밀번호/인증 코드", kind: "password" },
    { key: "from", label: "보내는 사람", kind: "text", placeholder: "artex@example.com" },
    { key: "to", label: "받는 사람", kind: "list", help: "여러 주소를 쉼표로 구분하세요." },
    { key: "tls", label: "암시적 TLS", kind: "switch", help: "포트 465가 열려 있습니다. 587은 닫힌 상태로 유지됩니다(자동으로 STARTTLS가 됩니다)." },
  ],
};

export const SEVERITY_OPTIONS = [
  { value: "", label: "제한 없음" },
  { value: "low", label: "위험도가 낮거나 그 이상" },
  { value: "medium", label: "약간 위험함 이상" },
  { value: "high", label: "고위험 이상" },
  { value: "critical", label: "단지 심한" },
];

export type ChannelForm = {
  name: string;
  kind: string;
  mode: "realtime" | "digest";
  enabled: boolean;
  ratePerMin: string;
  config: Record<string, unknown>;
  minSeverity: string;
  includeText: string;
  excludeText: string;
  taskIDsText: string;
  assetIDsText: string;
  onStatusChange: boolean;
};

export const emptyForm = (kind: string): ChannelForm => ({
  name: "",
  kind,
  mode: "realtime",
  enabled: true,
  ratePerMin: "",
  config: {},
  minSeverity: "",
  includeText: "",
  excludeText: "",
  taskIDsText: "",
  assetIDsText: "",
  onStatusChange: false,
});

// parseKV는 "각 줄 KEY=VALUE"의 텍스트 필드를 구문 분석합니다.
export function parseKV(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf("=");
    if (i > 0) out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  return out;
}
// parseIDs id의 쉼표/공백으로 구분된 목록을 구문 분석합니다.
export function parseIDs(text: string): number[] {
  return text
    .split(/[\s,，]+/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => Number(s))
    .filter((n) => Number.isFinite(n) && n > 0);
}
// parseKeywords는 줄/쉼표로 구분된 키워드 목록을 구문 분석합니다(취약성 유형 이름에는 공백이 포함될 수 있으므로 줄이나 쉼표로 잘라냅니다).
export function parseKeywords(text: string): string[] {
  return text
    .split(/[\n,，]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}
