"use client";

import * as React from "react";
import { toast } from "sonner";
import {
  BotIcon,
  ListFilterIcon,
  PencilIcon,
  PlusIcon,
  ShieldAlertIcon,
  Trash2Icon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Card, CardContent } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type {
  InterceptAction,
  InterceptRule,
  JudgeConfig,
  JudgeDayUsage,
  JudgeUsage,
  LLMProfile,
  Tool,
} from "@/lib/types";

// fmtTokens는 승인 사용 통계를 위해 token 번호를 압축 형식(1.2k / 3.4M)으로 압축합니다.
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
  if (n >= 1000) return (n / 1000).toFixed(1) + "k";
  return String(n);
}

// JudgeStat는 통계 수치(라벨 + 값)입니다.
function JudgeStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border bg-muted/20 px-3 py-2">
      <div className="text-[10px] text-muted-foreground">{label}</div>
      <div className="mt-0.5 text-lg font-semibold tabular-nums">{value}</div>
    </div>
  );
}

// JudgeSparkbars는 순수 div를 사용하여 거의 N일 동안 일일 소비(입력 + 출력)에 대한 미니 열 차트를 그려 차트 라이브러리에 대한 의존성을 제거합니다.
function JudgeSparkbars({ daily }: { daily: JudgeDayUsage[] }) {
  const max = Math.max(1, ...daily.map((d) => d.input_tokens + d.output_tokens));
  return (
    <div className="flex h-16 items-end gap-0.5">
      {daily.map((d) => {
        const total = d.input_tokens + d.output_tokens;
        const h = Math.max(2, Math.round((total / max) * 100));
        return (
          <div
            key={d.date}
            title={`${d.date} · ${d.calls} 배 · ${fmtTokens(total)} tokens`}
            className="min-w-[2px] flex-1 rounded-sm bg-violet-500/60 hover:bg-violet-500"
            style={{ height: `${h}%` }}
          />
        );
      })}
    </div>
  );
}

// ---- tool scope ----

// SDK tools are intentionally not seeded into the DB (they apply to every agent
// and have no per-agent binding). We hardcode them here so they still appear in
// the scope dialog.
function sdkTool(key: string, description: string): Tool {
  return { key, system: true, description, schema: {}, agents: [], enabled: true, kind: "builtin" };
}

const SDK_EXEC: Tool[] = [
  sdkTool("Bash",        "shell에서 명령을 실행하십시오."),
  sdkTool("WebFetch",    "HTTP/HTTPS 요청 시작(프록시 지원 포함)"),
  sdkTool("web_search",  "웹 검색"),
  sdkTool("shell_open",  "영구 PTY 대화형 세션 시작"),
  sdkTool("shell_send",  "대화형 세션에 입력 보내기"),
  sdkTool("shell_read",  "대화형 세션 출력 읽기"),
  sdkTool("shell_close", "대화형 세션 닫기"),
  sdkTool("shell_list",  "모든 대화형 세션 나열"),
];

const SDK_WRITE: Tool[] = [
  sdkTool("Write",     "파일 쓰기"),
  sdkTool("Edit",      "파일 편집(정확한 대체)"),
  sdkTool("MultiEdit", "일괄 편집 파일"),
];

const SDK_KEYS = new Set([...SDK_EXEC, ...SDK_WRITE].map((t) => t.key));

function groupTools(dbTools: Tool[]) {
  const sys: Tool[] = [], custom: Tool[] = [];
  for (const t of dbTools) {
    if (SDK_KEYS.has(t.key)) continue; // already covered by hardcoded groups
    if (t.system) sys.push(t);
    else          custom.push(t);
  }
  return [
    { label: "실행 클래스",      tools: SDK_EXEC },
    { label: "쓰기/편집 수업", tools: SDK_WRITE },
    { label: "시스템 도구",    tools: sys },
    { label: "맞춤형 도구",  tools: custom },
  ].filter((g) => g.tools.length > 0);
}

// ---- form state ----

type RuleForm = {
  name: string;
  enabled: boolean;
  priority: number;
  match_target: "tool_name" | "tool_input";
  match_type: "string" | "regex";
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
};

const defaultForm = (): RuleForm => ({
  name: "",
  enabled: true,
  priority: 0,
  match_target: "tool_name",
  match_type: "string",
  pattern: "",
  action: "deny",
  message: "",
  timeout_enabled: true,
  timeout_seconds: 60,
  timeout_action: "deny",
});

// ---- small components ----

function ActionBadge({ action }: { action: InterceptAction }) {
  if (action === "allow") return <Badge variant="secondary">허용</Badge>;
  if (action === "deny")  return <Badge variant="destructive">금지하다</Badge>;
  return <Badge variant="outline" className="border-amber-400 text-amber-600">적용하다</Badge>;
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
        {label}
      </Label>
      {children}
    </div>
  );
}

// ---- LLM fallback judge card ----

const FOLLOW_ACTIVE = "0"; // profile_id 0 = 활성화를 따르다/기본 구성

const defaultJudge = (): JudgeConfig => ({
  enabled: false,
  profile_id: 0,
  prompt: "",
  timeout_seconds: 15,
  fail_action: "allow",
  ask_timeout_seconds: 300,
  ask_timeout_action: "deny",
});

function JudgeCard() {
  const [cfg, setCfg] = React.useState<JudgeConfig>(defaultJudge());
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);
  const [usage, setUsage] = React.useState<JudgeUsage | null>(null);

  // 승인 사용 통계:실패해도 구성 페이지가 중단되지 않습니다.,켜져 있을 때만 당기세요。
  const loadUsage = React.useCallback(async () => {
    try {
      setUsage(await api.interceptJudgeUsage(30));
    } catch {
      // 무시:통계를 사용할 수 없어도 구성 편집에 영향을 주어서는 안 됩니다.
    }
  }, []);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const [j, ps] = await Promise.all([api.interceptGetJudgeConfig(), api.llmProfiles()]);
      setCfg(j);
      setProfiles(ps);
    } catch (e) {
      toast.error("모델 구성을 로드하지 못했습니다. " + (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  // 개봉 후(초기 로딩을 포함하여 스위치는 다음과 같이 읽혀집니다. true 시간)승인 사용 통계 가져오기。
  React.useEffect(() => {
    if (cfg.enabled) loadUsage();
  }, [cfg.enabled, loadUsage]);

  function patch(p: Partial<JudgeConfig>) {
    setCfg((c) => ({ ...c, ...p }));
  }

  async function save() {
    setSaving(true);
    try {
      await api.interceptSetJudgeConfig(cfg);
      toast.success("모델 구성이 저장되었습니다.");
      await load(); // 다시 읽어봐:프롬프트 단어가 지워지면 기본 제공 템플릿이 다시 채워집니다.
    } catch (e) {
      toast.error("저장 실패: " + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function restorePrompt() {
    // 프롬프트 단어를 지우고 저장 → 서버는 다음 번에 내장된 템플릿의 전체 텍스트를 반환합니다.,입력 상자에 백필。
    setSaving(true);
    try {
      await api.interceptSetJudgeConfig({ ...cfg, prompt: "" });
      const j = await api.interceptGetJudgeConfig();
      setCfg(j);
      toast.success("내장된 기본 템플릿이 복원되었습니다.");
    } catch (e) {
      toast.error("복구 실패: " + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="space-y-4">
      {/* 스위치 활성화 - 독립적인 강조 표시줄 */}
      <div
        className={`flex items-center justify-between gap-3 rounded-lg border px-4 py-3 ${
          cfg.enabled ? "border-violet-400/50 bg-violet-50/40 dark:bg-violet-950/20" : "bg-muted/40"
        }`}
      >
        <div className="flex items-center gap-2.5">
          <BotIcon className={`h-5 w-5 shrink-0 ${cfg.enabled ? "text-violet-600" : "text-muted-foreground"}`} />
          <div>
            <p className="text-sm font-semibold leading-tight">모델 승인</p>
            <p className="text-xs text-muted-foreground mt-0.5">
              존재하다<span className="font-medium text-foreground">차단 범위</span>안에, 그리고<span className="font-medium text-foreground">차단 규칙이 적용되지 않음</span>모델은 명령이 내려진 후에만 의미론적 판단(해제/수동으로 전환/차단)을 내립니다.
            </p>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span className="text-xs text-muted-foreground">{cfg.enabled ? "활성화됨" : "활성화되지 않음"}</span>
          <Switch checked={cfg.enabled} disabled={loading} onCheckedChange={(v) => patch({ enabled: v })} />
        </div>
      </div>

      {/* 승인 Token 사용통계(글로벌 축적,각 모델 구성에 독립적) */}
      {cfg.enabled && usage && (
        <Card>
          <CardContent className="p-4">
            <div className="mb-3 flex items-center justify-between">
              <div>
                <p className="text-sm font-medium">Token 소비 승인</p>
                <p className="text-xs text-muted-foreground">
                  모델 승인 누적 사용량은 별도로 측정(worker=judge)되며, 각 모델 구성의 통계와 혼합되지 않습니다.
                </p>
              </div>
              <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={loadUsage}>
                새로고침
              </Button>
            </div>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-5">
              <JudgeStat label="승인 통화" value={usage.calls.toLocaleString()} />
              <JudgeStat label="Token를 입력하세요" value={fmtTokens(usage.input_tokens)} />
              <JudgeStat label="출력 Token" value={fmtTokens(usage.output_tokens)} />
              <JudgeStat label="캐시 읽기" value={fmtTokens(usage.cache_read_tokens)} />
              <JudgeStat label="캐시 쓰기" value={fmtTokens(usage.cache_write_tokens)} />
            </div>
            {usage.daily.length > 0 && (
              <div className="mt-4">
                <p className="mb-2 text-[10px] uppercase tracking-wider text-muted-foreground">
                  지난 30일 동안의 일일 소비(입력 + 출력)
                </p>
                <JudgeSparkbars daily={usage.daily} />
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {cfg.enabled && (
        <div className="grid gap-4 lg:grid-cols-5">
          {/* 왼쪽:프롬프트 단어 편집기(직접 확장,주요 지역) */}
          <Card className="lg:col-span-3">
            <CardContent className="flex h-full flex-col gap-2 p-4">
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm font-medium">승인 판단 프롬프트</p>
                  <p className="text-xs text-muted-foreground">모델은 이를 기반으로 ALLOW / ASK / DENY를 결정하며, 이를 직접 편집할 수 있습니다.</p>
                </div>
                <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={restorePrompt} disabled={saving}>
                  기본 템플릿 복원
                </Button>
              </div>
              <Textarea
                className="min-h-[22rem] flex-1 resize-none font-mono text-xs leading-relaxed"
                value={cfg.prompt}
                onChange={(e) => patch({ prompt: e.target.value })}
                placeholder="기본 제공 템플릿을 사용하려면 비워두세요."
                spellCheck={false}
              />
              <p className="text-right text-[11px] text-muted-foreground">{cfg.prompt.length} 성격</p>
            </CardContent>
          </Card>

          {/* 오른쪽:결정 매개변수(설정바) */}
          <Card className="lg:col-span-2">
            <CardContent className="space-y-5 p-4">
              <div className="space-y-4">
                <p className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">의사결정 모델 및 전략</p>
                <Field label="승인모델">
                  <Select value={String(cfg.profile_id || 0)} onValueChange={(v) => patch({ profile_id: Number(v) })}>
                    <SelectTrigger><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value={FOLLOW_ACTIVE}>활성화 구성을 따르세요.</SelectItem>
                      {profiles.map((p) => (
                        <SelectItem key={p.id} value={p.id}>
                          {p.name}（{p.model}）
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label="모델 판단 시간 초과(초)">
                  <Input
                    type="number"
                    min={1}
                    value={cfg.timeout_seconds}
                    onChange={(e) => {
                      const n = parseInt(e.target.value, 10);
                      if (n > 0) patch({ timeout_seconds: n });
                    }}
                  />
                </Field>
                <Field label="모델이 실패하는 경우(오류/시간 초과/파싱할 수 없음)">
                  <Select value={cfg.fail_action} onValueChange={(v) => patch({ fail_action: v as JudgeConfig["fail_action"] })}>
                    <SelectTrigger><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="allow">허용</SelectItem>
                      <SelectItem value="ask">수동 승인으로 전환</SelectItem>
                      <SelectItem value="deny">차단</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </div>

              <Separator />

              <div className="space-y-4">
                <p className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">수동 승인(모델이 "수동으로 전환"되었다고 판단되는 경우)</p>
                <Field label="승인 대기 시간 초과(초)">
                  <Input
                    type="number"
                    min={5}
                    value={cfg.ask_timeout_seconds}
                    onChange={(e) => {
                      const n = parseInt(e.target.value, 10);
                      if (n > 0) patch({ ask_timeout_seconds: n });
                    }}
                  />
                </Field>
                <Field label="시간 초과 후 기본 동작">
                  <Select value={cfg.ask_timeout_action} onValueChange={(v) => patch({ ask_timeout_action: v as JudgeConfig["ask_timeout_action"] })}>
                    <SelectTrigger><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="deny">차단</SelectItem>
                      <SelectItem value="allow">허용</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </div>
            </CardContent>
          </Card>
        </div>
      )}

      <div className="flex justify-end">
        <Button size="sm" onClick={save} disabled={saving || loading}>
          {saving ? "절약…" : "구성 저장"}
        </Button>
      </div>
    </div>
  );
}

// ---- page ----

export default function InterceptPage() {
  const [rules, setRules]     = React.useState<InterceptRule[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [open, setOpen]       = React.useState(false);
  const [editing, setEditing] = React.useState<InterceptRule | null>(null);
  const [form, setForm]       = React.useState<RuleForm>(defaultForm());
  const [saving, setSaving]   = React.useState(false);
  const [regexErr, setRegexErr] = React.useState("");
  const [regexWarn, setRegexWarn] = React.useState(false); // true = JS 구문 분석할 수 없지만 아마도 합법적일 수 있음 Go 문법

  // ---- tool scope dialog ----
  const [scopeOpen, setScopeOpen]       = React.useState(false);
  const [allTools, setAllTools]         = React.useState<Tool[]>([]);
  const [enabledTools, setEnabledTools] = React.useState<Set<string>>(new Set());
  const [scopeLoading, setScopeLoading] = React.useState(false);
  const [scopeSaving, setScopeSaving]   = React.useState(false);
  const [scopeTools, setScopeTools]     = React.useState<string[]>([]); // 헤더 정보 표시줄:현재 항목 차단 도구

  // ---- data ----

  const loadScope = React.useCallback(async () => {
    try {
      const cfg = await api.interceptGetToolConfig();
      setScopeTools(cfg.enabled_tools);
    } catch {
      // 정보 표시줄은 중요하지 않습니다.,실패 시 침묵
    }
  }, []);

  const load = React.useCallback(async () => {
    try {
      const r = await api.interceptRules();
      setRules(r);
    } catch {
      toast.error("차단 규칙을 로드하지 못했습니다.");
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => { load(); loadScope(); }, [load, loadScope]);

  React.useEffect(() => {
    if (form.match_type !== "regex" || !form.pattern) { setRegexErr(""); setRegexWarn(false); return; }
    try {
      new RegExp(form.pattern);
      setRegexErr("");
      setRegexWarn(false);
    } catch {
      // JS RegExp 지원되지 않음 Go RE2 확장 구문(예: (?i) 인라인 flag）。
      // 이것은 단지 미리보기 검증 실패일 뿐이지 의미는 없습니다. Go 클라이언트가 유효하지 않습니다. 최종 확인을 위해 서버에 넘겨주세요.。
      setRegexErr("");
      setRegexWarn(true);
    }
  }, [form.pattern, form.match_type]);

  // ---- rule handlers ----

  function set(patch: Partial<RuleForm>) { setForm(f => ({ ...f, ...patch })); }

  function openNew() {
    setEditing(null);
    setForm(defaultForm());
    setRegexErr("");
    setOpen(true);
  }

  function openEdit(rule: InterceptRule) {
    setEditing(rule);
    setForm({
      name: rule.name, enabled: rule.enabled, priority: rule.priority,
      match_target: rule.match_target, match_type: rule.match_type,
      pattern: rule.pattern, action: rule.action, message: rule.message,
      timeout_enabled: rule.timeout_enabled, timeout_seconds: rule.timeout_seconds,
      timeout_action: rule.timeout_action,
    });
    setRegexErr("");
    setOpen(true);
  }

  async function handleSave() {
    if (!form.name.trim())    { toast.error("이름은 비워둘 수 없습니다."); return; }
    if (!form.pattern.trim()) { toast.error("패턴은 비워둘 수 없습니다."); return; }
    if (regexErr)             { toast.error("잘못된 정규식 구문"); return; }
    setSaving(true);
    try {
      if (editing) {
        await api.updateInterceptRule(editing.id, form);
        toast.success("규칙이 업데이트되었습니다.");
      } else {
        await api.createInterceptRule(form);
        toast.success("규칙이 생성되었습니다.");
      }
      setOpen(false);
      load();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete(id: number) {
    try {
      await api.deleteInterceptRule(id);
      toast.success("규칙이 삭제되었습니다.");
      load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function handleToggle(rule: InterceptRule) {
    try {
      await api.toggleInterceptRule(rule.id, !rule.enabled);
      load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  // ---- scope handlers ----

  async function openScope() {
    setScopeOpen(true);
    setScopeLoading(true);
    try {
      const [tools, cfg] = await Promise.all([api.tools(), api.interceptGetToolConfig()]);
      setAllTools(tools);
      setEnabledTools(new Set(cfg.enabled_tools));
    } catch (e) {
      toast.error("로드 실패: " + (e as Error).message);
    } finally {
      setScopeLoading(false);
    }
  }

  function toggleTool(key: string, val: boolean) {
    setEnabledTools(prev => {
      const next = new Set(prev);
      if (val) next.add(key); else next.delete(key);
      return next;
    });
  }

  async function saveScope() {
    setScopeSaving(true);
    try {
      await api.interceptSetToolConfig([...enabledTools]);
      toast.success("차단 범위가 저장되었습니다.");
      setScopeTools([...enabledTools]);
      setScopeOpen(false);
    } catch (e) {
      toast.error("저장 실패: " + (e as Error).message);
    } finally {
      setScopeSaving(false);
    }
  }

  const toolGroups = React.useMemo(() => groupTools(allTools), [allTools]);

  // ---- render ----

  return (
    <div className="flex flex-1 flex-col gap-5 p-6">

      {/* ---- header ---- */}
      <div className="flex items-center gap-2.5">
        <ShieldAlertIcon className="h-5 w-5 shrink-0" />
        <div>
          <h1 className="text-lg font-semibold leading-tight">명령 가로채기</h1>
          <p className="text-sm text-muted-foreground mt-0.5">
            도구가 실행되기 전에 차단 규칙에 따라 일치됩니다. unhit 명령은 최종 판단을 위해 모델에 넘겨질 수 있습니다.
          </p>
        </div>
      </div>

      {/* ---- 차단 범위 정보 표시줄(규칙 일치와 모델 적용 범위 간에 공유됨: 범위 내에 없는 도구는 둘 중 하나에도 관여하지 않음)）---- */}
      <div
        className={`flex items-center justify-between gap-3 rounded-lg border px-4 py-2.5 ${
          scopeTools.length === 0
            ? "border-amber-400/60 bg-amber-50/50 dark:bg-amber-950/20"
            : "bg-muted/40"
        }`}
      >
        <div className="flex min-w-0 items-center gap-2 text-sm">
          <ListFilterIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
          <span className="shrink-0 font-medium">차단 범위</span>
          {scopeTools.length === 0 ? (
            <span className="text-amber-700 dark:text-amber-500">
              활성화된 도구가 없습니다. 차단 규칙과 모델 프로파일링이 적용되지 않습니다.
            </span>
          ) : (
            <>
              <Badge variant="secondary" className="shrink-0">{scopeTools.length} 도구</Badge>
              <span className="truncate text-muted-foreground" title={scopeTools.join("、")}>
                {scopeTools.join("、")}
              </span>
            </>
          )}
        </div>
        <Button
          variant={scopeTools.length === 0 ? "default" : "outline"}
          size="sm"
          className="shrink-0"
          onClick={openScope}
        >
          <ListFilterIcon className="h-4 w-4" />
          조정 범위
        </Button>
      </div>

      <Tabs defaultValue="rules" className="flex-1">
        <TabsList>
          <TabsTrigger value="rules">차단 규칙</TabsTrigger>
          <TabsTrigger value="judge">모델 구성</TabsTrigger>
        </TabsList>

        {/* ---- tab: 차단 규칙 ---- */}
        <TabsContent value="rules" className="mt-4 flex flex-col gap-4">
          <div className="flex items-center justify-between gap-3">
            <p className="text-xs text-muted-foreground">
              우선순위에 따라(숫자가 클수록 첫 번째) 항목을 하나씩 일치시키면 첫 번째 적중 규칙이 적용됩니다.
            </p>
            <Button onClick={openNew} size="sm" className="shrink-0">
              <PlusIcon className="h-4 w-4" />
              새로운 규칙
            </Button>
          </div>

          <Card>
            <CardContent className="p-0">
          {loading ? (
            <p className="p-6 text-sm text-muted-foreground">로드 중…</p>
          ) : rules.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 py-16 text-center">
              <ShieldAlertIcon className="h-8 w-8 text-muted-foreground/40" />
              <p className="text-sm text-muted-foreground">아직 규칙이 없습니다.</p>
              <Button size="sm" variant="outline" onClick={openNew}>
                <PlusIcon className="h-4 w-4" />
                첫 번째 규칙 만들기
              </Button>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-[72px]">우선 사항</TableHead>
                  <TableHead>이름</TableHead>
                  <TableHead className="w-[90px]">목표</TableHead>
                  <TableHead className="w-[80px]">유형</TableHead>
                  <TableHead>모델</TableHead>
                  <TableHead className="w-[72px]">전략</TableHead>
                  <TableHead className="w-[64px] text-center">활성화</TableHead>
                  <TableHead className="w-[80px]" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rules.map((rule) => (
                  <TableRow key={rule.id} className={!rule.enabled ? "opacity-40" : ""}>
                    <TableCell>
                      <span className="font-mono text-xs tabular-nums">{rule.priority}</span>
                    </TableCell>
                    <TableCell className="font-medium text-sm">{rule.name}</TableCell>
                    <TableCell>
                      <span className="text-xs text-muted-foreground">
                        {rule.match_target === "tool_name" ? "도구 이름" : "내용 입력"}
                      </span>
                    </TableCell>
                    <TableCell>
                      <span className="text-xs text-muted-foreground">
                        {rule.match_type === "regex" ? "정기적인" : "끈"}
                      </span>
                    </TableCell>
                    <TableCell className="max-w-[220px]">
                      <code className="block truncate rounded bg-muted px-1.5 py-0.5 text-xs font-mono">
                        {rule.pattern}
                      </code>
                    </TableCell>
                    <TableCell>
                      <ActionBadge action={rule.action} />
                    </TableCell>
                    <TableCell className="text-center">
                      <Switch
                        checked={rule.enabled}
                        onCheckedChange={() => handleToggle(rule)}
                      />
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center justify-end gap-0.5">
                        <Button
                          size="icon" variant="ghost" className="h-7 w-7"
                          onClick={() => openEdit(rule)}
                        >
                          <PencilIcon className="h-3.5 w-3.5" />
                        </Button>
                        <Button
                          size="icon" variant="ghost"
                          className="h-7 w-7 text-destructive hover:text-destructive"
                          onClick={() => handleDelete(rule.id)}
                        >
                          <Trash2Icon className="h-3.5 w-3.5" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
            </CardContent>
          </Card>
        </TabsContent>

        {/* ---- tab: 모델 구성 ---- */}
        <TabsContent value="judge" className="mt-4">
          <JudgeCard />
        </TabsContent>
      </Tabs>

      {/* ---- editor sheet ---- */}
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="right" className="flex flex-col gap-0 p-0 sm:max-w-md">
          <SheetHeader className="border-b px-6 py-4">
            <SheetTitle>{editing ? "규칙 편집" : "새로운 규칙"}</SheetTitle>
            <SheetDescription className="text-xs">
              우선순위가 높을수록 가장 먼저 일치합니다. 첫 번째 적중 규칙이 적용되고 후속 규칙은 건너뜁니다.
            </SheetDescription>
          </SheetHeader>

          <div className="flex-1 min-h-0 overflow-y-auto px-6 py-5 space-y-5">
            <Field label="이름">
              <Input
                placeholder="규칙에 이름을 지정하세요."
                value={form.name}
                onChange={(e) => set({ name: e.target.value })}
              />
            </Field>

            <Field label="우선순위(숫자가 클수록 더 빨리 매칭됩니다)">
              <Input
                type="number"
                value={form.priority}
                onChange={(e) => set({ priority: parseInt(e.target.value) || 0 })}
              />
            </Field>

            <Separator />

            <Field label="일치 대상">
              <Select
                value={form.match_target}
                onValueChange={(v) => set({ match_target: v as RuleForm["match_target"] })}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="tool_name">도구 이름(tool_name)</SelectItem>
                  <SelectItem value="tool_input">입력 내용(tool_input JSON)</SelectItem>
                </SelectContent>
              </Select>
            </Field>

            <Field label="일치 유형">
              <Select
                value={form.match_type}
                onValueChange={(v) => set({ match_type: v as RuleForm["match_type"] })}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="string">문자열에 다음이 포함됨</SelectItem>
                  <SelectItem value="regex">정규식</SelectItem>
                </SelectContent>
              </Select>
            </Field>

            <Field label="모델">
              <Input
                placeholder={form.match_type === "regex" ? "^Bash$" : "rm -rf"}
                value={form.pattern}
                onChange={(e) => set({ pattern: e.target.value })}
                className={regexErr ? "border-destructive focus-visible:ring-destructive" : ""}
              />
              {regexErr && (
                <p className="text-xs text-destructive mt-1">{regexErr}</p>
              )}
              {regexWarn && (
                <p className="text-xs text-amber-600 mt-1">Go RE2 확장 구문(예: <code className="font-mono">(?i)</code>) 브라우저에서는 미리 볼 수 없으며 제출 후 서버에서 확인됩니다.</p>
              )}
            </Field>

            <Separator />

            <Field label="차단 전략">
              <Select
                value={form.action}
                onValueChange={(v) => set({ action: v as InterceptAction })}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="allow">허용 - 후속 규칙을 건너뛰고 직접 릴리스할 수 있도록 허용합니다.</SelectItem>
                  <SelectItem value="deny">비활성화 — 차단, 거부 메시지를 모델에 반환</SelectItem>
                  <SelectItem value="ask">사용자에게 적용 - 승인 대기 중</SelectItem>
                </SelectContent>
              </Select>
            </Field>

            {form.action !== "allow" && (
              <Field label={form.action === "deny" ? "거부 메시지(모델로 반환됨)" : "승인 지침(선택사항)"}>
                <Textarea
                  placeholder={form.action === "deny" ? "보안 정책에 의해 차단된 작업" : ""}
                  value={form.message}
                  onChange={(e) => set({ message: e.target.value })}
                  rows={2}
                  className="resize-none"
                />
              </Field>
            )}

            {form.action === "ask" && (
              <>
                <Separator />
                <div className="flex items-center justify-between">
                  <div>
                    <p className="text-sm font-medium">승인 시간 초과 활성화</p>
                    <p className="text-xs text-muted-foreground">시간 초과 후 자동으로 처리되므로 더 이상 기다리지 않아도 됩니다.</p>
                  </div>
                  <Switch
                    checked={form.timeout_enabled}
                    onCheckedChange={(v) => set({ timeout_enabled: v })}
                  />
                </div>
                {form.timeout_enabled && (
                  <div className="flex items-end gap-3">
                    <Field label="시간 초과(초)">
                      <Input
                        type="number"
                        min={5}
                        className="w-28"
                        value={form.timeout_seconds}
                        onChange={(e) => {
                          const n = parseInt(e.target.value, 10);
                          if (n > 0) set({ timeout_seconds: n });
                        }}
                      />
                    </Field>
                    <Field label="시간 초과 작업">
                      <Select
                        value={form.timeout_action}
                        onValueChange={(v) => set({ timeout_action: v as "deny" | "allow" })}
                      >
                        <SelectTrigger className="w-32"><SelectValue /></SelectTrigger>
                        <SelectContent>
                          <SelectItem value="deny">자동으로 거부</SelectItem>
                          <SelectItem value="allow">자동으로 허용</SelectItem>
                        </SelectContent>
                      </Select>
                    </Field>
                  </div>
                )}
              </>
            )}

            <Separator />

            <div className="flex items-center gap-3">
              <Switch
                id="rule-enabled"
                checked={form.enabled}
                onCheckedChange={(v) => set({ enabled: v })}
              />
              <Label htmlFor="rule-enabled" className="cursor-pointer">이 규칙을 활성화하세요</Label>
            </div>
          </div>

          <SheetFooter className="border-t px-6 py-4 flex-row justify-end gap-2">
            <Button variant="outline" onClick={() => setOpen(false)}>취소</Button>
            <Button onClick={handleSave} disabled={saving || !!regexErr}>
              {saving ? "절약…" : "저장"}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      {/* ---- scope dialog ---- */}
      <Dialog open={scopeOpen} onOpenChange={setScopeOpen}>
        <DialogContent className="sm:max-w-lg flex flex-col overflow-hidden p-0 gap-0" style={{ maxHeight: "min(80vh, 560px)" }}>
          <DialogHeader className="shrink-0 border-b px-6 py-4">
            <DialogTitle className="flex items-center gap-2">
              <ListFilterIcon className="h-4 w-4" />
              차단 범위
            </DialogTitle>
            <DialogDescription className="text-xs">
              차단이 활성화된 도구만 ​​규칙 일치에 들어갑니다. 다른 도구는 직접 허용됩니다.
            </DialogDescription>
          </DialogHeader>

          <div className="flex-1 min-h-0 overflow-y-auto px-6 py-4 space-y-5">
            {scopeLoading ? (
              <p className="text-sm text-muted-foreground py-4">로드 중…</p>
            ) : (
              toolGroups.map((group, gi) => (
                <div key={group.label}>
                  {gi > 0 && <Separator className="mb-5" />}
                  <p className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider mb-2">
                    {group.label}
                  </p>
                  <div className="space-y-0.5">
                    {group.tools.map((t) => (
                      <div key={t.key} className="flex items-center gap-3 rounded-md px-2 py-1.5 hover:bg-muted/50">
                        <Switch
                          id={`scope-${t.key}`}
                          checked={enabledTools.has(t.key)}
                          onCheckedChange={(v) => toggleTool(t.key, v)}
                        />
                        <label htmlFor={`scope-${t.key}`} className="flex-1 min-w-0 cursor-pointer">
                          <div className="flex items-center gap-1.5">
                            <span className="font-mono text-sm">{t.key}</span>
                            {(t.kind && t.kind !== "builtin") && (
                              <Badge variant="outline" className="px-1 py-0 text-[10px]">{t.kind}</Badge>
                            )}
                          </div>
                          {t.description && (
                            <p className="text-[11px] text-muted-foreground line-clamp-1">{t.description}</p>
                          )}
                        </label>
                      </div>
                    ))}
                  </div>
                </div>
              ))
            )}
          </div>

          <div className="shrink-0 border-t px-6 py-3 flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setScopeOpen(false)}>취소</Button>
            <Button size="sm" onClick={saveScope} disabled={scopeSaving || scopeLoading}>
              {scopeSaving ? "절약…" : "저장"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
