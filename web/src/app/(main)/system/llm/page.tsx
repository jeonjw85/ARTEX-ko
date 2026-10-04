"use client";

import * as React from "react";

import {
  Loader2Icon,
  PlugZapIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  SaveIcon,
  StarIcon,
  Trash2Icon,
  ZapIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { LLMPoolMember, LLMPoolStatus, LLMProfile, LLMRetryOverride } from "@/lib/types";
import { cn } from "@/lib/utils";

import { ProfileRetryFields, RetryPolicyPanel, ZERO_OVERRIDE } from "./_components/retry";

// 사고 스위치(thinking.type)와 사고 강도(reasoning_effort)는 두 가지 [상호 독립] 분야입니다.
// 각각 별도로 설정 - 일부 인터페이스에는 thinking 필드가 없으며 강도 매개변수에만 의존하여 사고를 활성화할 수 있으므로 분리해야 합니다.
// 인벤토리 빈 문자열 = 이 필드 [보내지 않음]; Radix Select는 빈 value를 허용하지 않으므로 UI는 "none"를 사용합니다.
// Sentinel은 보내지 않는다는 뜻으로 접속시 ""로 바꿔서 사용합니다(NONE / fromStore / toStore).
const NONE = "none";
const fromStore = (v?: string) => (v ? v : NONE);
const toStore = (v: string) => (v === NONE ? "" : v);
const THINKING_TYPES: { value: string; label: string }[] = [
  { value: NONE, label: "보내지 않음(기본값)" },
  { value: "disabled", label: "닫기" },
  { value: "enabled", label: "켜다" },
];
// 상한을 출력하는 데 사용되는 요청 필드 이름입니다(openai 형식만 의미 있음). NONE ← "" 동일한 센트리 변환 세트를 사용합니다.
const MAX_TOKENS_FIELDS: { value: string; label: string }[] = [
  { value: NONE, label: "max_tokens(기본값)" },
  { value: "max_completion_tokens", label: "max_completion_tokens" },
];
// 다른 두 형식은 각각 고정된 필드 이름을 가지며 옵션은 의미가 없습니다. 설명 문구에 명확하게 명시되어 있어야 합니다.
const MAX_TOKENS_FIELD_HINTS: Record<string, string> = {
  openai:
    "어떤 키가 상한선으로 전송됩니까? max_tokens는 기본값이며 대부분의 호환 게이트웨이는 이를 인식합니다. OpenAI 공식 추론 모델(o 시리즈/GPT-5)은 차례로 max_completion_tokens만 인식하며, max_tokens를 수신한 후 unsupported_parameter를 직접 보고합니다.",
  anthropic: "openai 형식만 사용할 수 있습니다. Anthropic의 필드 이름은 max_tokens로 고정됩니다.",
  "openai-responses": "openai 형식만 사용할 수 있습니다. Responses API의 필드 이름은 max_output_tokens로 고정됩니다.",
};
const EFFORT_LEVELS: { value: string; label: string }[] = [
  { value: NONE, label: "보내지 않음(기본값)" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

function cooldownText(secs: number) {
  if (secs <= 0) return "";
  if (secs < 60) return `${secs}s`;
  return `${Math.ceil(secs / 60)}min`;
}

// 카드에 표시된 구성「정상인가요?」。채워지지 않음 Key 구성에서 요청을 전혀 보낼 수 없습니다. 차단기를 끊는 것보다 먼저 이야기하는 것이 더 중요합니다.；
// 나머지 상태는 폴링 회로 차단기 레코드에서 가져옵니다. (폴링이 꺼지면 새 레코드가 생성되지 않습니다. 이때는「정상」= 알려진 결함 없음）。
type Health = { label: string; cls: string; hint?: string };
function healthOf(p: LLMProfile, m?: LLMPoolMember): Health {
  if (!p.api_key_hint) {
    return {
      label: "구성되지 않음 Key",
      cls: "border-muted-foreground/40 text-muted-foreground",
      hint: "API Key가 채워지지 않아 호출할 수 없습니다.",
    };
  }
  if (m?.state === "tripped") {
    return {
      label: m.cooldown_secs > 0 ? `퓨즈 · ${cooldownText(m.cooldown_secs)}` : "날아갔다",
      cls: "border-destructive/50 text-destructive",
      hint: m.last_error,
    };
  }
  if (m?.state === "degraded") {
    return {
      label: `예외 · ${m.fails} 횟수 실패`,
      cls: "border-amber-500/50 text-amber-600 dark:text-amber-400",
      hint: m.last_error,
    };
  }
  return { label: "정상", cls: "border-emerald-500/50 text-emerald-600 dark:text-emerald-400" };
}

// ─────────────────────────────────────────────────────────────────────────────
// 폴링 구성 서랍
// ─────────────────────────────────────────────────────────────────────────────

function PoolSheet({
  open,
  onOpenChange,
  pool,
  onReload,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  pool: LLMPoolStatus | null;
  onReload: () => Promise<void>;
}) {
  const [busy, setBusy] = React.useState(false);

  // 냉각 카운트다운은 백엔드에서 계산한 남은 시간(초)입니다. 서랍이 열려 있고 구성이 비정상적인 경우에만 서랍을 정기적으로 당겨서 시작하세요.。
  React.useEffect(() => {
    if (!open || !pool?.enabled || !pool.chain.some((m) => m.state !== "ok")) return;
    const t = setInterval(() => void onReload(), 10_000);
    return () => clearInterval(t);
  }, [open, pool, onReload]);

  async function toggle(patch: { llm_pool_enabled?: boolean; llm_pool_bind_fallback?: boolean }) {
    if (busy) return;
    setBusy(true);
    try {
      await api.setSettings(patch);
      await onReload();
      if (patch.llm_pool_enabled !== undefined) {
        toast.success(patch.llm_pool_enabled ? "LLM 폴링이 활성화되었습니다" : "LLM 폴링 종료");
      } else {
        toast.success("업데이트된 포켓 설정");
      }
    } catch (e) {
      toast.error(`설정 실패: ${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function recover(id?: string) {
    try {
      await api.resetLLMPool(id);
      await onReload();
      toast.success(id ? "구성이 복원되었습니다." : "모든 구성이 복원되었습니다.");
    } catch (e) {
      toast.error(`복구 실패: ${(e as Error).message}`);
    }
  }

  const enabled = pool?.enabled ?? false;
  const chain = pool?.chain ?? [];
  // 설문조사에 참여하는 회원(플래그된 회원 제외)「투표에 참여하지 않음」), 순서는 백엔드의 실제 시도 순서입니다.。
  const inChain = chain.filter((m) => m.active || !m.excluded);
  const tripped = chain.filter((m) => m.state === "tripped");

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex flex-col gap-0 p-0 data-[side=right]:sm:max-w-lg">
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            <ZapIcon className="size-4" /> LLM 폴링 · 장애 조치
          </SheetTitle>
          <SheetDescription>
            개봉 후,<b>모델이 지정되지 않았습니다.</b>Agent는 현재 구성에서 사용할 수 없습니다(잔액 부족/Key 유효하지 않음/현재 제한/
            서비스 예외)는 자동으로 다음 구성으로 전환됩니다.
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-6">
          <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
            <div className="grid gap-0.5">
              <Label className="text-sm">폴링 활성화</Label>
              <p className="text-muted-foreground text-xs">기본적으로 꺼져 있습니다. 종료할 때는 항상 활성 구성만 사용하십시오. 실패는 실패입니다.</p>
            </div>
            <Switch
              checked={enabled}
              disabled={busy}
              onCheckedChange={(v) => void toggle({ llm_pool_enabled: v })}
              aria-label="LLM 폴링 스위치"
            />
          </div>

          {enabled && (
            <>
              <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
                <div className="grid gap-0.5">
                  <Label className="text-sm">특정 모델이 실패하더라도 공개됩니다</Label>
                  <p className="text-muted-foreground text-xs">
                    기본적으로 꺼짐: Agent 또는 작업이 특정 구성을 지정하는 경우 해당 구성만 사용합니다. 실패하면 실패합니다(조용히 다른 모델로 전환되지 않습니다).
                    활성화된 후 지정된 구성이 실패하면 다음 폴링 체인으로 대체됩니다.
                  </p>
                </div>
                <Switch
                  checked={pool?.bind_fallback ?? false}
                  disabled={busy}
                  onCheckedChange={(v) => void toggle({ llm_pool_bind_fallback: v })}
                  aria-label="바인딩 구성 실패 커버 스위치"
                />
              </div>

              <Separator />

              <div className="grid gap-2">
                <div className="flex items-center justify-between">
                  <Label className="text-sm">폴링 순서</Label>
                  {tripped.length > 0 && (
                    <Button size="sm" variant="ghost" onClick={() => void recover()}>
                      <RotateCcwIcon /> 모두 복원
                    </Button>
                  )}
                </div>
                {inChain.length < 2 && (
                  <p className="text-muted-foreground text-xs">
                    현재는 {inChain.length} 사용 가능한 구성이 있지만 폴링은 적용되지 않습니다. API Key가 채워지고 폴링에 참여하는 구성이 2개 이상 필요합니다.
                  </p>
                )}
                {chain.map((m) => {
                  const excluded = m.excluded && !m.active;
                  const order = excluded ? null : inChain.findIndex((x) => x.profile_id === m.profile_id) + 1;
                  return (
                    <div
                      key={m.profile_id}
                      className={cn(
                        "grid gap-1 rounded-lg border p-2.5 text-sm",
                        excluded && "opacity-55",
                        m.state === "tripped" && "border-destructive/40",
                      )}
                    >
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                        <span className="w-5 shrink-0 text-center font-mono text-muted-foreground text-xs">
                          {order ?? "—"}
                        </span>
                        <span className="font-medium">{m.name}</span>
                        {m.active && (
                          <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                            활성화
                          </Badge>
                        )}
                        {excluded && <Badge variant="outline">투표에 참여하지 않음</Badge>}
                        <div className="ml-auto flex items-center gap-2">
                          {m.state === "tripped" && m.cooldown_secs > 0 && (
                            <span className="text-muted-foreground text-xs">진정해 {cooldownText(m.cooldown_secs)}</span>
                          )}
                          {m.state === "degraded" && (
                            <span className="text-muted-foreground text-xs">연속 실패 {m.fails} 이류</span>
                          )}
                          {m.state !== "ok" && (
                            <Button
                              size="icon"
                              variant="ghost"
                              className="size-7"
                              aria-label="지금 복원"
                              title="즉시 복구: 회로 차단기를 해제하고 다음에 호출될 때 구성을 다시 시도합니다."
                              onClick={() => void recover(m.profile_id)}
                            >
                              <RotateCcwIcon className="size-3.5" />
                            </Button>
                          )}
                        </div>
                      </div>
                      <div className="flex flex-wrap items-center gap-x-3 pl-7 text-muted-foreground text-xs">
                        <code className="truncate font-mono">{m.model}</code>
                        {!m.active && <span>우선 사항 {m.priority}</span>}
                      </div>
                      {m.last_error && (
                        <p className="truncate pl-7 font-mono text-muted-foreground text-xs" title={m.last_error}>
                          {m.last_error}
                        </p>
                      )}
                    </div>
                  );
                })}
                {chain.length === 0 && (
                  <div className="rounded-lg border border-dashed p-4 text-center text-muted-foreground text-sm">
                    아직 구성이 없습니다.
                  </div>
                )}
              </div>

              <div className="rounded-lg border border-dashed p-3 text-muted-foreground text-xs leading-relaxed">
                활성화된 구성은 항상 첫 번째 순위를 가지며, 나머지 구성은 높은 우선순위에서 낮은 우선순위로 정렬됩니다(각 구성에서 설정). 특정 구성 실패 후 냉각 시작(60초 → 5분 →
                30분) 냉각 기간 동안 건너뛰고 복구 후 자동으로 다시 전환됩니다. 컨텍스트 창이 맞지 않으면 현재 요청된 구성을 건너뜁니다. 모델이 지정된 Agent
                기본적으로 작업은 폴링에 참여하지 않습니다.
              </div>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// 모델 구성 서랍(신규 / 편집자는 동일한 양식 세트를 공유합니다.）
// ─────────────────────────────────────────────────────────────────────────────

function ProfileSheet({
  profile,
  open,
  onOpenChange,
  onSaved,
}: {
  profile: LLMProfile | null; // null = 새로운
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onSaved: (id: string) => void;
}) {
  const isNew = !profile;
  const [name, setName] = React.useState("");
  const [format, setFormat] = React.useState<"anthropic" | "openai" | "openai-responses">("anthropic");
  const [model, setModel] = React.useState("");
  const [baseUrl, setBaseUrl] = React.useState("");
  const [proxy, setProxy] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [keyHint, setKeyHint] = React.useState("");
  const [rps, setRps] = React.useState("0");
  const [rpm, setRpm] = React.useState("0");
  const [cw, setCw] = React.useState("0"); // 컨텍스트 창(K tokens);0=기본값200K
  const [thinkingType, setThinkingType] = React.useState(NONE);
  const [effort, setEffort] = React.useState(NONE);
  const [priority, setPriority] = React.useState("0"); // 폴링 순서;큰 것이 먼저다
  const [poolExclude, setPoolExclude] = React.useState(false);
  const [streaming, setStreaming] = React.useState(true); // true=스트리밍(기본값);false=비스트리밍
  const [maxTokens, setMaxTokens] = React.useState("0"); // 단일 응답 출력 상한;0=보내지 마세요
  const [maxTokensField, setMaxTokensField] = React.useState(NONE); // 상한에는 어떤 필드 이름을 사용해야 합니까?;NONE=max_tokens
  const [sessionHeaderKey, setSessionHeaderKey] = React.useState(""); // 맞춤 세션 헤더 이름;null=보내지 마세요
  const [retry, setRetry] = React.useState<LLMRetryOverride>(ZERO_OVERRIDE); // 이 구성에 대한 적용 범위를 다시 시도하세요.;완벽한 0=큰 그림을 따라가세요
  const [testing, setTesting] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [models, setModels] = React.useState<string[]>([]);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [modelsOpen, setModelsOpen] = React.useState(false);

  // 들어오는 것에서 열릴 때마다 profile 양식을 작성합니다(신규인 경우 기본값으로 재설정). 서랍을 닫았다가 열어보세요
  // 이전 구성의 잔상을 남기지 않고 깔끔하게 시작됩니다.。
  React.useEffect(() => {
    if (!open) return;
    setName(profile?.name ?? "");
    setFormat(profile?.format === "openai" || profile?.format === "openai-responses" ? profile.format : "anthropic");
    setModel(profile?.model ?? "");
    setBaseUrl(profile?.base_url ?? "");
    setProxy(profile?.proxy ?? "");
    setRps(String(profile?.rate_per_second ?? 0));
    setRpm(String(profile?.rate_per_minute ?? 0));
    setCw(String(profile?.context_window_k ?? 0));
    setThinkingType(fromStore(profile?.thinking_type));
    setEffort(fromStore(profile?.reasoning_effort));
    setPriority(String(profile?.priority ?? 0));
    setPoolExclude(profile?.pool_exclude ?? false);
    setStreaming(profile?.streaming ?? true);
    setMaxTokens(String(profile?.max_tokens ?? 0));
    setMaxTokensField(fromStore(profile?.max_tokens_field));
    setSessionHeaderKey(profile?.session_header_key ?? "");
    setRetry(profile?.retry ?? ZERO_OVERRIDE);
    setApiKey("");
    setKeyHint(profile?.api_key_hint ?? "");
    setModels([]);
    setModelsOpen(false);
  }, [open, profile]);

  const profileId = profile ? Number(profile.id) : undefined;

  async function loadModels() {
    if (loadingModels) return;
    setLoadingModels(true);
    setModels([]);
    try {
      const r = await api.fetchLLMModels(format, baseUrl, apiKey, proxy, profileId);
      if (r.ok && r.models && r.models.length > 0) {
        setModels(r.models);
        setModelsOpen(true);
        toast.success(`${r.models.length} 모델 로드됨`);
      } else {
        toast.error(`모델 로드 실패: ${r.error ?? "모델을 얻지 못했습니다."}`);
      }
    } catch (e) {
      toast.error(`모델 로드 오류: ${(e as Error).message}`);
    } finally {
      setLoadingModels(false);
    }
  }

  async function testConnection() {
    if (testing) return;
    setTesting(true);
    try {
      // 테스트를 위해 실제로 실행될 구성 매개변수를 사용하면 이 필드를 지원하지 않는 모델이 여기서 실패하게 됩니다.，
      // 폭발하는 임무를 실행할 때까지 기다리는 대신. 통과하다 profile id：Key 입력 상자를 비워 두면 저장된 입력 상자가 사용됩니다. Key。
      const r = await api.testLLM(
        format,
        model,
        baseUrl,
        apiKey,
        proxy,
        toStore(thinkingType),
        toStore(effort),
        profileId,
        streaming,
        sessionHeaderKey.trim(),
      );
      // 답변 내용도 함께 표시됩니다. 모델이 실제로 말한 것을 확인할 수 있어야만 대화와 동일한 것으로 간주됩니다.。
      if (r.ok)
        toast.success(`연결 성공 · ${r.latency_ms ?? "?"}ms · ${r.model ?? model}`, {
          description: r.reply ? `답장하다:${r.reply}` : undefined,
        });
      else toast.error(`연결 실패: ${r.error ?? "알 수 없음"}`);
    } catch (e) {
      toast.error(`테스트 오류: ${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  }

  async function save() {
    if (!name.trim() || !model.trim()) {
      toast.error("이름과 모델을 입력해주세요");
      return;
    }
    if (saving) return;
    setSaving(true);
    try {
      const { id } = await api.saveLLMProfile({
        ...(profile ? { id: Number(profile.id) } : {}),
        name: name.trim(),
        format,
        model: model.trim(),
        base_url: baseUrl.trim(),
        proxy: proxy.trim(),
        api_key: apiKey,
        rate_per_second: Number(rps) || 0,
        rate_per_minute: Number(rpm) || 0,
        context_window_k: Number(cw) || 0,
        thinking_type: toStore(thinkingType),
        reasoning_effort: toStore(effort),
        priority: Number(priority) || 0,
        pool_exclude: poolExclude,
        streaming,
        max_tokens: Math.max(0, Number(maxTokens) || 0),
        // 필드 이름 스위치는 openai(Chat Completions) 말이 되네요. 다른 모든 형식은 기본값으로 돌아갑니다.；
        // 백엔드도 동일한 정규화를 다시 수행합니다. UI 모순되는 값 보내기。
        max_tokens_field: format === "openai" ? toStore(maxTokensField) : "",
        session_header_key: sessionHeaderKey.trim(),
        retry,
      });
      if (isNew) toast.success(`새로 생성됨: ${name.trim()}(활성화하려면 카드에 ＂활성화로 설정＂)`);
      else toast.success(profile?.is_default ? "저장하면 다시 시작하지 않고도 활성화 구성이 즉시 적용됩니다." : "저장됨");
      onSaved(String(id));
      onOpenChange(false);
    } catch (e) {
      toast.error(`저장 실패: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:min-w-[420px] data-[side=right]:sm:max-w-xl"
      >
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            {isNew ? "새로운 모델 구성" : `편집기: ${profile?.name}`}
            {profile?.is_default && (
              <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                활성화
              </Badge>
            )}
          </SheetTitle>
          <SheetDescription>
            {isNew
              ? "생성된 후에는 자동으로 활성화되지 않습니다. 활성화하려면 카드에 ＂활성화 설정＂을 하십시오."
              : "수정 후 저장을 클릭하세요. 활성화 구성이 저장되면 모든 Agent에 즉시 적용됩니다."}
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="p-name">이름</Label>
              <Input
                id="p-name"
                placeholder="예: OpenAI 생산"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="grid gap-2">
              <Label>체재</Label>
              <Select value={format} onValueChange={(v) => setFormat(v as "anthropic" | "openai" | "openai-responses")}>
                <SelectTrigger>
                  <SelectValue placeholder="형식 선택" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="anthropic">Anthropic</SelectItem>
                  <SelectItem value="openai">OpenAI (Chat Completions)</SelectItem>
                  <SelectItem value="openai-responses">OpenAI (Responses API)</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-model">모델</Label>
            <div className="flex gap-2">
              <Input
                id="p-model"
                className="font-mono"
                placeholder="claude-opus-4-8"
                value={model}
                onChange={(e) => setModel(e.target.value)}
              />
              {/* modal: 이것 Popover 내용 portal 도착하다 <body>，존재하다 Sheet 스크롤 잠금 외부，
                  추가되지 않음 modal 목록을 렌더링할 수 있지만 스크롤할 수는 없습니다.。modal 상단 스크롤 잠금 장치를 자체적으로 유지하도록 하세요.。 */}
              <Popover open={modelsOpen} onOpenChange={setModelsOpen} modal>
                <PopoverTrigger asChild>
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    className="shrink-0"
                    disabled={loadingModels}
                    onClick={loadModels}
                    title="API에서 사용 가능한 모델 로드"
                  >
                    {loadingModels ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
                  </Button>
                </PopoverTrigger>
                {models.length > 0 && (
                  <PopoverContent className="max-h-72 w-72 gap-0 overflow-y-auto overscroll-contain p-1" align="end">
                    {models.map((m) => (
                      <button
                        key={m}
                        type="button"
                        className="w-full shrink-0 rounded-md px-2 py-1.5 text-left font-mono text-xs hover:bg-accent hover:text-accent-foreground"
                        onClick={() => {
                          setModel(m);
                          setModelsOpen(false);
                        }}
                      >
                        {m}
                      </button>
                    ))}
                  </PopoverContent>
                )}
              </Popover>
            </div>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-base-url">Base URL(옵션)</Label>
            <Input
              id="p-base-url"
              className="font-mono"
              placeholder="https://api.openai.com/v1"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
            />
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-proxy">에이전트(선택사항)</Label>
            <Input
              id="p-proxy"
              className="font-mono"
              placeholder="socks5://user:pass@127.0.0.1:1080 · http://127.0.0.1:8080"
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              LLM 아웃바운드 요청만 이 프록시를 통과하며 http/https/socks5가 지원되며 계정 비밀번호가 포함될 수 있습니다(예:
              socks5://user:pass@host:port，비밀번호에는 특수문자가 포함되어야 합니다. URL 부호화); 공백으로 두면 프록시를 사용하지 않음을 의미합니다(직접 연결）。
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-session-header">맞춤 세션 헤더(선택사항)</Label>
            <Input
              id="p-session-header"
              className="font-mono"
              placeholder="x-session-id 등(비워두기 = 보내지 않음)"
              value={sessionHeaderKey}
              onChange={(e) => setSessionHeaderKey(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              헤더 이름을 입력한 후 각 요청은 이 HTTP 헤더를 가져오고 헤더 값은 자동으로 다음과 같이 채워집니다. <b>현재 세션에 대한 session id</b>(chat 세션은
              conv-12, worker(예: exp3-worker-i87). session-id 헤더에 따라 프롬프트를 캐시하는 데 사용됩니다.
              끈적한 라우팅의 게이트웨이입니다. 동일한 세션은 여러 라운드 동안 안정적이지만 다른 세션은 서로 다릅니다. 보내지 않으려면 비워두세요.
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-api-key">API Key</Label>
            <Input
              id="p-api-key"
              type="password"
              placeholder={keyHint ? `이미 설정되어 있습니다(${keyHint}). 변경하지 않으려면 비워 두세요.` : "sk-…"}
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-3">
            <div className="grid gap-2">
              <Label htmlFor="p-rps">초당 속도 제한</Label>
              <Input id="p-rps" type="number" min={0} value={rps} onChange={(e) => setRps(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="p-rpm">분당 속도 제한</Label>
              <Input id="p-rpm" type="number" min={0} value={rpm} onChange={(e) => setRpm(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="p-cw">컨텍스트 창(K)</Label>
              <Input
                id="p-cw"
                type="number"
                min={0}
                max={1000}
                value={cw}
                onChange={(e) => setCw(e.target.value)}
                placeholder="200"
              />
            </div>
          </div>
          <p className="-mt-2 text-muted-foreground text-xs">
            속도 제한 0 = 제한 없음, 모든 Agent에서 공유됩니다. 컨텍스트 창 단위 K(천 token), 0 = 기본값 200K, 상한 1000(예:
            1M); 너무 높게 설정하면 압축이 실행되지 않습니다.
          </p>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-priority" className="text-sm">
                  폴링 우선순위
                </Label>
                <p className="text-muted-foreground text-xs">
                  숫자가 클수록 먼저 선택됩니다. 활성화된 구성은 이 값에 관계없이 항상 첫 번째입니다. 우선 순위가 동일한 구성이 차례로 시작되며 할당량은 자연스럽게 공유됩니다.
                </p>
              </div>
              <Input
                id="p-priority"
                type="number"
                className="w-24 shrink-0"
                value={priority}
                onChange={(e) => setPriority(e.target.value)}
              />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">투표에 참여하지 않음</Label>
                <p className="text-muted-foreground text-xs">
                  활성화되면 장애 조치 대상으로 사용되지 않습니다(여전히 Agent/task로 명시적으로 지정할 수 있음). "특정 Agent에만 적합
                  전용 구성으로 다른 구성이 실패해도 소각되고 싶지 않은 고가의 구성입니다.
                </p>
              </div>
              <Switch checked={poolExclude} onCheckedChange={setPoolExclude} aria-label="투표에 참여하지 않음" />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">스트리밍 출력 · streaming</Label>
                <p className="text-muted-foreground text-xs">
                  실시간 진행 및 실시간 token 계산을 실행하여 스트리밍 SSE를 활성화(기본값)합니다. 진정한 비스트리밍 모드(stream:false,
                  완전한 응답을 한 번에 반환) - 일부 게이트웨이의 열악한 SSE 구현을 우회할 수 있습니다(빈 프레임/사고 필드 프레임 손실).
                  실행 중에 실시간 진행 상황이 손실됩니다.
                </p>
              </div>
              <Switch checked={streaming} onCheckedChange={setStreaming} aria-label="스트리밍 출력" />
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-max-tokens" className="text-sm">
                  출력 상한 · max tokens
                </Label>
                <p className="text-muted-foreground text-xs">
                  단일 응답으로 생성된 최대 token 수는 각 요청과 함께 전송됩니다. 0(기본값) = 서버 기본값에 따라 결정된 이 필드를 보내지 않습니다.
                  이는 위의 "컨텍스트 창"과 다릅니다. 이는 모델의 총 용량이며 압축 임계값을 계산하기 위해 로컬로만 사용됩니다.
                  설정이 너무 작으면 사고 단계에서 추론 모델이 잘려 답변을 제공할 수 없습니다.
                </p>
              </div>
              <Input
                id="p-max-tokens"
                type="number"
                min={0}
                className="w-28 shrink-0"
                value={maxTokens}
                onChange={(e) => setMaxTokens(e.target.value)}
                placeholder="0"
              />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">상한 필드 이름</Label>
                <p className="text-muted-foreground text-xs">{MAX_TOKENS_FIELD_HINTS[format]}</p>
              </div>
              <Select
                value={format === "openai" ? maxTokensField : NONE}
                onValueChange={setMaxTokensField}
                disabled={format !== "openai"}
              >
                <SelectTrigger className="w-56 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {MAX_TOKENS_FIELDS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label className="text-sm">생각하는 스위치 · thinking.type</Label>
                <p className="text-muted-foreground text-xs">
                  thinking 필드가 전송되는지 여부를 제어합니다. 보내지 않음 = 이 필드를 포함하지 않음(지원하지 않는 MiniMax와 같은 모델과 호환 가능) 닫다 = 보내다
                  disabled; 열기 = enabled를 보냅니다. 아래 강도와는 무관합니다.
                </p>
              </div>
              <Select value={thinkingType} onValueChange={setThinkingType}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {THINKING_TYPES.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">사고강도 · reasoning_effort</Label>
                <p className="text-muted-foreground text-xs">
                  독립적인 강도 수준(OpenAI reasoning_effort / Anthropic output_config.effort). 일부 인터페이스에는 thinking가 없습니다.
                  현장, 사고는 강도로만 활성화할 수 있어 사고 스위치를 보내지 않고도 별도로 설정할 수 있다.
                </p>
              </div>
              <Select value={effort} onValueChange={setEffort}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {EFFORT_LEVELS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <ProfileRetryFields value={retry} onChange={setRetry} />
        </div>

        <div className="flex gap-2 border-t px-4 py-3">
          <Button variant="outline" onClick={testConnection} disabled={testing}>
            {testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />}
            {testing ? "테스트 중…" : "연결 테스트"}
          </Button>
          <Button onClick={save} disabled={saving} className="flex-1">
            {saving && <Loader2Icon className="animate-spin" />}
            {!saving && (isNew ? <PlusIcon /> : <SaveIcon />)}
            {isNew ? "새로운" : "저장"}
          </Button>
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────

export default function LLMPage() {
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [pool, setPool] = React.useState<LLMPoolStatus | null>(null);
  const [poolOpen, setPoolOpen] = React.useState(false);
  // 서랍의 스위치와 내용물은 별도로 보관됩니다: 닫았을 때 editing 변경하지 않고 그대로 두십시오. 그렇지 않으면 제목이 다음과 같이 변경됩니다.
  // 「편집 X」플래시로「새로운」。editing = null 새로운 것을 나타냅니다。
  const [editOpen, setEditOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<LLMProfile | null>(null);
  const openEditor = React.useCallback((p: LLMProfile | null) => {
    setEditing(p);
    setEditOpen(true);
  }, []);

  const loadPool = React.useCallback(async () => {
    try {
      setPool(await api.llmPool());
    } catch {
      /* ignore */
    }
  }, []);

  const load = React.useCallback(async () => {
    try {
      setProfiles(await api.llmProfiles());
    } catch {
      /* ignore */
    }
    await loadPool();
  }, [loadPool]);

  React.useEffect(() => {
    void load();
  }, [load]);

  // 카드의 건강 배지 버튼 profile id 폴링 상태 가져오기。
  const health = React.useMemo(() => {
    const m = new Map<string, LLMPoolMember>();
    for (const c of pool?.chain ?? []) m.set(c.profile_id, c);
    return m;
  }, [pool]);

  async function activate(id: string, name: string) {
    try {
      await api.activateLLMProfile(id);
      toast.success(`활성화됨: ${name}`);
      await load();
    } catch (e) {
      toast.error(`활성화 실패: ${(e as Error).message}`);
    }
  }

  async function remove(p: LLMProfile) {
    if (p.is_default) {
      toast.error("현재 활성 구성을 삭제할 수 없습니다.");
      return;
    }
    try {
      await api.deleteLLMProfile(p.id);
      toast.success(`삭제됨: ${p.name}`);
      await load();
    } catch (e) {
      toast.error(`삭제 실패: ${(e as Error).message}`);
    }
  }

  const poolOn = pool?.enabled ?? false;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">LLM</h1>
          <p className="text-muted-foreground text-sm">
            모든 Agent에서 공유되는 형식/모델/속도 제한 구성입니다. 카드 편집을 클릭하면 별표가 현재 활성화된 구성을 나타냅니다.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="outline" onClick={() => setPoolOpen(true)}>
            <ZapIcon /> 폴링 구성
            {poolOn && (
              <Badge variant="outline" className="ml-1 border-emerald-500/50 text-emerald-600 dark:text-emerald-400">
                이미 켜져 있음
              </Badge>
            )}
          </Button>
          <Button size="sm" variant="outline" onClick={() => openEditor(null)}>
            <PlusIcon /> 새로운
          </Button>
        </div>
      </div>

      <Tabs defaultValue="profiles" className="flex-1">
        <TabsList>
          <TabsTrigger value="profiles">모델 구성</TabsTrigger>
          <TabsTrigger value="retry">재시도 및 백오프</TabsTrigger>
        </TabsList>

        <TabsContent value="profiles" className="mt-4">
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
            {profiles.map((p) => {
              const h = healthOf(p, health.get(p.id));
              return (
                // biome-ignore lint/a11y/useSemanticElements: 카드에는 네이티브를 사용하는 자체 작업 버튼이 포함되어 있습니다. <button> 버튼 중첩이 발생합니다(불법 HTML）
                <Card
                  key={p.id}
                  role="button"
                  tabIndex={0}
                  onClick={() => openEditor(p)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      openEditor(p);
                    }
                  }}
                  className={cn(
                    "cursor-pointer gap-0 py-4 outline-none transition-colors hover:border-foreground/30",
                    p.is_default && "border-amber-400/50 bg-amber-400/5",
                  )}
                >
                  <CardContent className="grid gap-2 px-4">
                    <div className="flex items-start gap-2">
                      <StarIcon
                        className={cn(
                          "mt-0.5 size-4 shrink-0",
                          p.is_default ? "fill-amber-400 text-amber-400" : "text-muted-foreground",
                        )}
                      />
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="truncate font-medium text-sm">{p.name}</span>
                          <Badge variant="outline" className="uppercase">
                            {p.format}
                          </Badge>
                          <Badge variant="outline" className={cn("ml-auto", h.cls)} title={h.hint}>
                            {h.label}
                          </Badge>
                        </div>
                        <code className="mt-1 block truncate font-mono text-muted-foreground text-xs">{p.model}</code>
                      </div>
                    </div>

                    <div className="flex flex-wrap gap-x-3 gap-y-0.5 pl-6 text-muted-foreground text-xs">
                      {p.api_key_hint && <span>{p.api_key_hint}</span>}
                      <span>
                        {p.rate_per_second}/s · {p.rate_per_minute}/min
                      </span>
                      {p.proxy && <span className="truncate">연기 {p.proxy}</span>}
                      {p.reasoning_effort && (
                        <span>생각하다 {p.reasoning_effort === "off" ? "닫다" : p.reasoning_effort}</span>
                      )}
                      {/* 폴링과 관련된 두 필드는 폴링이 켜져 있을 때만 의미가 있고, 꺼져 있을 때는 공간을 차지하지 않습니다. */}
                      {poolOn &&
                        !p.is_default &&
                        (p.pool_exclude ? <span>투표에 참여하지 않음</span> : <span>우선 사항 {p.priority ?? 0}</span>)}
                    </div>

                    <div className="mt-1 flex gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        className="flex-1"
                        disabled={p.is_default}
                        onClick={(e) => {
                          e.stopPropagation();
                          void activate(p.id, p.name);
                        }}
                      >
                        {p.is_default ? "활성화됨" : "활성으로 설정"}
                      </Button>
                      <Button
                        size="icon"
                        variant="outline"
                        aria-label="구성 삭제"
                        onClick={(e) => {
                          e.stopPropagation();
                          void remove(p);
                        }}
                      >
                        <Trash2Icon className="text-destructive" />
                      </Button>
                    </div>
                  </CardContent>
                </Card>
              );
            })}
            {profiles.length === 0 && (
              <div className="col-span-full rounded-lg border border-dashed p-10 text-center text-muted-foreground text-sm">
                아직 모델 구성이 없습니다. 오른쪽 상단의 "새로 만들기"를 클릭하여 첫 번째 모델을 생성하세요.
              </div>
            )}
          </div>
        </TabsContent>

        <TabsContent value="retry" className="mt-4">
          <RetryPolicyPanel />
        </TabsContent>
      </Tabs>

      <ProfileSheet profile={editing} open={editOpen} onOpenChange={setEditOpen} onSaved={() => void load()} />
      <PoolSheet open={poolOpen} onOpenChange={setPoolOpen} pool={pool} onReload={loadPool} />
    </div>
  );
}
