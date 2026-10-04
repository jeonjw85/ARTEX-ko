"use client";

// LLM 공유 재시도 구성: 5개 계층의 재시도 각각에 대해 "회수 + 간격"입니다.
//
// 내부에서 외부로의 5개 계층: 연결 설정(SDK) → 빈 응답(SDK) → provider 보안 창과 동일 → 폴링 퓨즈 → 재실행 의도.
// 처음 세 개의 레이어는 끝점을 따르므로 각 모델 구성은 전역 기본값을 재정의할 수 있습니다. 마지막 두 레이어는 프로세스 수준이며 전역 복사본만 갖습니다.
//
// 모든 입력은 백엔드 db.RetryRule와 일치하는 동일한 "공백 = 구성 없음" 의미 체계 세트를 따릅니다.
//   횟수 Null/0 = 내장된 기본값 사용 | -1 = 이 레이어를 끄고 다시 시도 | >0 = 이 횟수 사용
//   간격 비어 있음/0 = 이 레이어의 원래 지수 백오프 사용 | >0 = 대신 이 고정된 밀리초 간격을 사용합니다.

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { LLMRetryOverride, LLMRetryPolicy, LLMRetryRule } from "@/lib/types";

export const ZERO_RULE: LLMRetryRule = { attempts: 0, interval_ms: 0 };
export const ZERO_OVERRIDE: LLMRetryOverride = {
  connect: ZERO_RULE,
  empty: ZERO_RULE,
  stream: ZERO_RULE,
};
const ZERO_POLICY: LLMRetryPolicy = {
  ...ZERO_OVERRIDE,
  breaker: ZERO_RULE,
  intent: ZERO_RULE,
};

type LayerMeta = {
  title: string;
  /** 이 재시도 계층은 어디에서 발생하며 누가 수행합니까? */
  where: string;
  /** 이 수준에 도달하는 오류 종류 - 상태 코드에 따라 다르며 누구도 추측하지 못하게 하세요 */
  trigger: string;
  /** 비슷해 보이지만 이 수준까지 안가서 빈칸을 채우지 않고 bug라고 생각하는 것은 실수입니다 */
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** 횟수를 비워둘 경우 기본값으로 자리표시자로 사용됨 */
  defAttempts: number;
  /** 간격이 비어 있을 때의 기본 전략, 자리 표시자에 사용됨 */
  defInterval: string;
  /** 횟수에 -1을 채우는 의미 */
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "연결 설정을 다시 시도하세요.",
    where: "SDK · 200을 얻기 전에",
    trigger:
      "연결할 수 없거나 200을 얻을 수 없습니다: 연결 재설정/읽기/쓰기 시간 초과/DNS 오류 및 기타 네트워크 계층 오류뿐만 아니라 HTTP 408, 429, 500, 502, 503, 504.",
    skips: "나머지 상태 코드(400/401/403/404/413/422 등)는 결정론적 거부이며 재전송도 실패하고 직접 발생합니다.",
    desc: "동일한 요청을 변경하지 않고 다시 보냅니다. 흐름이 시작되면(200 획득) 중간에 연결이 끊어지면 이 관리 계층에 속하지 않습니다.",
    attemptsLabel: "재시도 횟수",
    defAttempts: 3,
    defInterval: "0.5초→1초→2초 지수(8초로 제한)",
    offHint: "-1 = 한 번만 재시도하지 않고, 실패하면 즉시 토해냅니다.",
  },
  empty: {
    title: "빈 응답으로 다시 시도",
    where: "SDK · openai 형식만 해당",
    trigger:
      "HTTP 200, finish_reason는 일반 stop이지만 전체 응답에는 콘텐츠 블록이 없습니다. 게이트웨이 빈 프레임, 사고 필드 프레임 손실 및 샘플링 딸꾹질이 모두 다음과 같습니다.",
    skips: "max_tokens 잘림으로 인해 내용이 없는 내용은 계산되지 않습니다(이 문제는 출력 상한을 높여 해결해야 하며, 재전송하면 또 다른 충돌만 발생하게 됩니다).",
    desc: "전체 prompt를 다시 보내면 긴 컨텍스트에서 비용이 더 많이 들고 횟수가 많아져서는 안 됩니다.",
    attemptsLabel: "재시도 횟수",
    defAttempts: 2,
    defInterval: "0.5초→1초→2초 지수(8초로 제한)",
    offHint: "-1 = 빈 응답이 있는 그대로 직접 전달됩니다.",
  },
  stream: {
    title: "provider 보안 창으로 재시도",
    where: "본 프로젝트 · 납품 및 출력 전",
    trigger:
      "흐름이 이미 설정된 후에 문제가 발생했습니다(200개 획득). 중간에 연결이 끊겼고, 공급자 overloaded, 흐름 내에서 429/5xx 오류 이벤트가 발생했으며 심지어 token도 호출자에게 전달되지 않았습니다.",
    skips:
      "할당량이 소진되었습니다(402/insufficient_quota, 구성을 위해 폴링에 전달됨), 컨텍스트가 너무 깁니다(413/context length, 압축에 전달됨), 400/401/403/404/422 결정론적 거부, 재시도 없음.",
    desc: "동일한 구성에서 동일한 요청을 재생합니다. 재생은 아직 출력이 전달되지 않았기 때문에 모델 출력이나 도구 실행을 반복하지 않습니다.",
    attemptsLabel: "재시도 횟수",
    defAttempts: 2,
    defInterval: "0.5초→1초 지수(4초로 제한)",
    offHint: "-1 = 흐름을 차단하고 재실행을 위해 외부 레이어에 직접 넘겨줍니다.",
  },
  breaker: {
    title: "폴링 회로 차단기",
    where: "본 프로젝트 · 프로세스 수준, 글로벌 복사",
    trigger:
      "일시적인 오류(429, 5xx, 네트워크 오류)는 임계값에 지속적으로 누적되면 사라집니다. 잔고 부족(402), 키 무효화(401/403), 모델 존재하지 않음(404)과 같은 결정론적 오류는 임계값을 확인하지 않고 처음으로 날려버립니다.",
    skips: "성공하면 클리어되기 때문에 가끔씩 초안이 나오는 구성은 융합될 때까지 천천히 쌓이지 않습니다.",
    desc: "퓨즈가 끊어진 후 냉각 모드로 들어갑니다. 냉각 기간 동안 폴링은 이 구성을 직접 건너뜁니다. 상태는 라이브러리에 삭제되며 다시 시작해도 손실되지 않습니다.",
    attemptsLabel: "연속으로 여러번 실패",
    defAttempts: 3,
    defInterval: "1분→5분→30분 변화도",
    offHint: "-1 = 순간적인 오류가 발생해도 폭발하지 않음(결정론적 오류가 발생해도 여전히 폭발)",
  },
  intent: {
    title: "다시 뛰겠다는 의지",
    where: "본 프로젝트 · 프로세스 수준, 글로벌 복사",
    trigger:
      "처음 몇 개의 레이어는 포착되지 않습니다. worker는 model_error로 끝납니다. 모든 내부 레이어 재시도가 소진되거나 출력 전달이 시작된 후 스트림이 중단됩니다(그 시점에서는 재생이 안전하지 않으며 전체 스트림만 다시 시작할 수 있습니다).",
    skips: "할당량 소진은 폴링 및 구성 변경을 통해 처리되었으며 여기서는 다시 실행되지 않습니다. 작업은 일시 중단/종료/끝에 들어가면 즉시 포기되며 백오프 시간을 차지하지 않습니다.",
    desc: "전체 의도를 처음부터 다시 실행하세요. 가장 바깥층입니다. 재실행은 내부 레이어의 시간이 다시 곱해짐을 의미합니다.",
    attemptsLabel: "재방송 횟수",
    defAttempts: 2,
    defInterval: "고정 3초",
    offHint: "-1 = 재방송 없음, blocked로 직접 판단",
  },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** 밀리초 단위의 인간 단어, 0 계산을 피하기 위해 입력 상자 옆에 에코하는 데만 사용됩니다. */
function humanMs(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** 제어되는 디지털 입력: 빈 문자열 ↔ 0，중간 상태（"-"、"1e"）부모를 방해하지 않고 그대로 로컬에 둡니다.。 */
function NumField({
  id,
  value,
  onChange,
  placeholder,
  min,
}: {
  id: string;
  value: number;
  onChange: (n: number) => void;
  placeholder: string;
  min: number;
}) {
  const [text, setText] = React.useState(value === 0 ? "" : String(value));
  // 상위 항목이 전체 값 집합(정책 읽기, 스위치 구성 읽기)을 변경할 때 계속 확인하세요. 혼자 입력하면 여기에 도착하지 않습니다，
  // 왜냐하면 그때 value 이미 로컬 텍스트와 동일합니다. parse 이후의 결과。
  React.useEffect(() => {
    const incoming = value === 0 ? "" : String(value);
    setText((cur) => (Number(cur || 0) === value ? cur : incoming));
  }, [value]);
  return (
    <Input
      id={id}
      type="number"
      min={min}
      className="w-28 shrink-0"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const n = Number(e.target.value);
        onChange(e.target.value.trim() === "" || !Number.isFinite(n) ? 0 : Math.trunc(n));
      }}
    />
  );
}

/** 재시도를 위한 2개의 손잡이로 구성된 1개 레이어。idPrefix 같은 페이지가 여러번 나타날 때 저장하기 위해 사용됩니다. label ~의 htmlFor。 */
export function RetryRuleFields({
  layer,
  idPrefix,
  value,
  onChange,
  compact,
}: {
  layer: LayerKey;
  idPrefix: string;
  value: LLMRetryRule;
  onChange: (r: LLMRetryRule) => void;
  /** true = 구성 서랍의 컴팩트 버전: 확장 지침을 생략하고 그대로 유지합니다.「어떤 오류가 이 수준에 도달합니까?」이 문장 */
  compact?: boolean;
}) {
  const meta = RETRY_LAYERS[layer];
  const human = humanMs(value.interval_ms);
  return (
    <div className={compact ? "grid gap-2" : "grid gap-3 rounded-lg border p-3"}>
      <div className="grid gap-0.5">
        <div className="flex flex-wrap items-baseline gap-2">
          <Label className="text-sm">{meta.title}</Label>
          <span className="text-muted-foreground text-xs">{meta.where}</span>
        </div>
        {/* 이 수준에 도달하는 오류, 특히 상태 코드 - 손잡이를 채웠지만 효과를 볼 수 없다면 오류가 이 수준에 전혀 해당되지 않기 때문일 수 있습니다.。 */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">방아쇠</span>：{meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">이 층을 차지하지 마세요</span>：{meta.skips}
          </p>
        )}
        {!compact && <p className="text-muted-foreground text-xs">{meta.desc}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-n`} className="text-muted-foreground text-xs">
            {meta.attemptsLabel}
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-n`}
            min={-1}
            value={value.attempts}
            placeholder={`기본 ${meta.defAttempts}`}
            onChange={(n) => onChange({ ...value, attempts: n })}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
            간격 ms
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-ms`}
            min={0}
            value={value.interval_ms}
            placeholder="기본 백오프"
            onChange={(n) => onChange({ ...value, interval_ms: n })}
          />
          <span className="text-muted-foreground text-xs">{human ? `${human} 수정됨` : meta.defInterval}</span>
        </div>
      </div>
      {!compact && <p className="text-muted-foreground text-xs">비워두기 = 기본값 사용;{meta.offHint}。</p>}
    </div>
  );
}

/** 모델 구성 서랍의 적용 범위 3개 계층(엔드포인트를 따르는 3개 계층)）。 */
export function ProfileRetryFields({
  value,
  onChange,
}: {
  value: LLMRetryOverride;
  onChange: (o: LLMRetryOverride) => void;
}) {
  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="grid gap-0.5">
        <Label className="text-sm">적용 범위 재시도</Label>
        <p className="text-muted-foreground text-xs">
          이 구성에만 적용되며 "재시도 및 백오프"의 전역 기본값을 재정의합니다. 각 상자를 비워 두십시오 = 전반적인 상황을 따르십시오. 횟수에 -1을 입력합니다. = 이 레이어를 끄고 다시 시도하세요.
          간격이 채워지면 지수 백오프가 고정 간격으로 대체됩니다. 회로 차단기 및 의도 재실행은 프로세스 수준에 있으며 전역 페이지에서만 조정할 수 있습니다.
        </p>
      </div>
      {(["connect", "empty", "stream"] as const).map((k) => (
        <div key={k} className="border-t pt-3 first:border-t-0 first:pt-0">
          <RetryRuleFields
            compact
            layer={k}
            idPrefix="pf"
            value={value[k]}
            onChange={(r) => onChange({ ...value, [k]: r })}
          />
        </div>
      ))}
    </div>
  );
}

/** 「재시도 및 백오프」tab：레이어 5의 전역 기본값。 */
export function RetryPolicyPanel() {
  const [policy, setPolicy] = React.useState<LLMRetryPolicy>(ZERO_POLICY);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const p = await api.llmRetryPolicy();
      setPolicy({ ...ZERO_POLICY, ...p });
    } catch (e) {
      toast.error(`읽기 재시도 정책 실패: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    if (saving) return;
    setSaving(true);
    try {
      // 백엔드는 범위를 벗어난 값을 다시 범위로 고정하여 반환하고 반환된 값으로 직접 새로 고칩니다. 당신이 보는 것은 저장된 것입니다.。
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("저장됨, 즉시 적용됨(현재 호출 라운드에서는 여전히 이전 매개변수를 사용함)");
    } catch (e) {
      toast.error(`저장 실패: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

  if (loading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
        <Loader2Icon className="size-4 animate-spin" /> 재시도 전략 읽기…
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
        모델 호출이 실패하면 내부에서 외부로 순서대로 5개 계층의 재시도를 거칩니다.
        <span className="text-foreground"> 연결 설정 → 빈 응답 → provider 보안 창과 동일 → 폴링 회로 차단기 → 재실행 의도</span>
        . 내층이 소진되면 외층의 차례가 되므로 횟수는
        <span className="text-foreground">곱하다</span>
        ——각 레이어가 가득 차면 한 번의 지터로 수십 개의 요청을 태울 수 있습니다.
        모든 항목을 비워 두는 것이 현재 기본값이며, 이는 이 페이지가 없는 동작과 정확히 동일합니다. 처음 세 개의 레이어는 각 모델 구성에서 개별적으로 재정의될 수 있습니다.
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {(Object.keys(RETRY_LAYERS) as LayerKey[]).map((k) => (
          <RetryRuleFields key={k} layer={k} idPrefix="gl" value={policy[k]} onChange={set(k)} />
        ))}
      </div>

      <div className="flex gap-2">
        <Button onClick={save} disabled={saving}>
          {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
          저장
        </Button>
        <Button variant="outline" onClick={() => setPolicy(ZERO_POLICY)} disabled={saving}>
          모두 기본값으로 복원
        </Button>
      </div>
    </div>
  );
}
