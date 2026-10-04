"use client";

import * as React from "react";

import { BellIcon, PlusIcon, SendIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { NotificationChannel, NotificationFilter, NotificationMeta } from "@/lib/types";

import {
  CHANNEL_FIELDS,
  type ChannelForm,
  emptyForm,
  KIND_LABEL,
  parseIDs,
  parseKeywords,
  parseKV,
  SEVERITY_OPTIONS,
} from "./_components/channel-fields";
import { ConfigField, FilterSummary } from "./_components/channel-form";
import { DeliveryList } from "./_components/delivery-list";
import { formatBacklog, StatTile } from "./_components/stat-tile";

// 이 페이지는 오케스트레이션(데이터 로드, 양식 상태 유지, 인터페이스 호출)만 담당합니다.
// 필드 정의 및 구문 분석은 _comComponents/channel-fields.ts에 있고 컨트롤 및 필터링 요약은
// _comComponents/channel-form.tsx, 배송 기록은 _comComponents/delivery-list.tsx에 있습니다.
// 각각 개별적으로 읽을 수 있기 때문에 분할하고 하나의 파일로 압축하면 이 페이지는 1100줄에 가깝습니다.
export default function NotifyPage() {
  const [meta, setMeta] = React.useState<NotificationMeta | null>(null);
  const [channels, setChannels] = React.useState<NotificationChannel[]>([]);
  const [tab, setTab] = React.useState<"channels" | "deliveries">("channels");

  const [open, setOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<NotificationChannel | null>(null);
  const [form, setForm] = React.useState<ChannelForm>(emptyForm("dingtalk"));
  const [saving, setSaving] = React.useState(false);
  const [testing, setTesting] = React.useState(false);

  const [globalSaving, setGlobalSaving] = React.useState(false);
  const [baseURL, setBaseURL] = React.useState("");
  const [digestMin, setDigestMin] = React.useState("");

  const load = React.useCallback(() => {
    api
      .notifyMeta()
      .then((m) => {
        setMeta(m);
        setBaseURL(m.public_base_url);
        setDigestMin(m.digest_interval_min);
      })
      .catch((e) => toast.error("푸시 구성을 읽지 못했습니다." + (e as Error).message));
    // 채널 목록 로드 실패는 보고되어야 합니다. 자동 실패는 "단일 채널이 아님"으로 표시됩니다.
    // 사용자는 구성이 손실되었다고 생각하게 되며 이는 오류를 직접 보고하는 것보다 더 놀라운 일입니다.
    api
      .notifyChannels()
      .then(setChannels)
      .catch((e) => toast.error("채널 목록을 읽지 못했습니다:" + (e as Error).message));
  }, []);
  React.useEffect(() => {
    load();
  }, [load]);

  function setF(patch: Partial<ChannelForm>) {
    setForm((f) => ({ ...f, ...patch }));
  }
  function setCfg(key: string, value: unknown) {
    setForm((f) => ({ ...f, config: { ...f.config, [key]: value } }));
  }

  function openAdd() {
    setEditing(null);
    setForm(emptyForm(meta?.kinds[0]?.kind ?? "dingtalk"));
    setOpen(true);
  }

  function openEdit(ch: NotificationChannel) {
    setEditing(ch);
    // filter는 백엔드의 Go 구조로, 항상 객체로 직렬화되므로(null는 아님) 자세히 설명할 필요가 없습니다.
    const f = ch.filter;
    setForm({
      name: ch.name,
      kind: ch.kind,
      mode: ch.mode,
      enabled: ch.enabled,
      ratePerMin: String(ch.rate_per_min),
      // 백엔드에서 반향된 config의 자격 증명은 마스크 값입니다. 양식에 그대로 입력하고 제출 시 그대로 다시 보냅니다.
      // 따라서 백엔드는 라이브러리의 원래 값을 유지합니다.
      config: { ...ch.config },
      minSeverity: f.min_severity ?? "",
      includeText: (f.vulnclass_include ?? []).join("\n"),
      excludeText: (f.vulnclass_exclude ?? []).join("\n"),
      taskIDsText: (f.task_ids ?? []).join(","),
      assetIDsText: (f.asset_ids ?? []).join(","),
      onStatusChange: f.on_status_change ?? false,
    });
    setOpen(true);
  }

  // buildConfig 양식 상태를 채널 config로 변환합니다.
  //
  // 유일한 규칙은 두 가지 유형의 값입니다.
  //   - 마스크 값("__masked__...")을 그대로 돌려보냄 → 백엔드에서는 "이 필드는 변경되지 않았으며 라이브러리의 원래 값은 유지됩니다."로 해석합니다.
  //   - 나머지는 사용자 입력에 따라 제출됩니다. 빈 문자열은 "이 필드 지우기"를 의미합니다.
  //
  // 자격 증명 필드(예: "자격 증명이 비어 있으면 건너뛰기")를 특별히 관리하지 않는 이유는 사용자가 자격 증명을 지울 수 없게 만들기 때문입니다**
  // 잘못 구성된 키 - 인터페이스에 "삭제하고 싶습니다"라는 작업이 없습니다. 현행 규정에 따르면,
  // 입력 상자를 지우는 것은 고유한 의미를 가지며 사용자가 제어할 수 있는 필드를 지우는 것과 같습니다.
  // 마스크 값은 입력 상자에 표시되지 않으므로(ConfigField 참조) "상자에 단어가 있습니다"는 항상 동일합니다.
  // "사용자가 입력했습니다."
  function buildConfig(): Record<string, unknown> {
    const defs = CHANNEL_FIELDS[form.kind] ?? [];
    const out: Record<string, unknown> = {};
    for (const d of defs) {
      const raw = form.config[d.key];
      if (d.kind === "switch") {
        out[d.key] = raw === true;
        continue;
      }
      if (typeof raw === "string" && raw.startsWith("__masked__")) {
        out[d.key] = raw;
        continue;
      }
      if (d.kind === "number") {
        const n = Number(raw);
        out[d.key] = Number.isFinite(n) && n > 0 ? n : 0;
        continue;
      }
      if (d.kind === "kv") {
        out[d.key] = parseKV(String(raw ?? ""));
        continue;
      }
      if (d.kind === "list") {
        out[d.key] = String(raw ?? "")
          .split(/[\s,，]+/)
          .map((s) => s.trim())
          .filter(Boolean);
        continue;
      }
      out[d.key] = String(raw ?? "").trim();
    }
    return out;
  }

  function buildFilter(): NotificationFilter {
    return {
      min_severity: form.minSeverity || undefined,
      vulnclass_include: parseKeywords(form.includeText),
      vulnclass_exclude: parseKeywords(form.excludeText),
      task_ids: parseIDs(form.taskIDsText),
      asset_ids: parseIDs(form.assetIDsText),
      on_status_change: form.onStatusChange,
    };
  }

  async function saveForm() {
    if (!form.name.trim()) {
      toast.error("채널명을 입력해주세요");
      return;
    }
    setSaving(true);
    try {
      const payload = {
        name: form.name.trim(),
        kind: form.kind,
        mode: form.mode,
        enabled: form.enabled,
        config: buildConfig(),
        filter: buildFilter(),
        rate_per_min: form.ratePerMin.trim() === "" ? undefined : Number(form.ratePerMin),
      };
      if (editing) {
        await api.notifyUpdateChannel(editing.id, payload);
        toast.success("저장됨");
        setOpen(false);
      } else {
        await api.notifyCreateChannel(payload);
        toast.success("채널이 추가되었습니다");
        setOpen(false);
      }
      load();
    } catch (e) {
      toast.error("저장 실패:" + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function testChannel() {
    if (!editing) return;
    setTesting(true);
    try {
      const r = await api.notifyTestChannel(editing.id);
      toast.success(`테스트 메시지(${r.latency_ms} ms)가 전송되었습니다. 그룹에서 확인하세요.`);
    } catch (e) {
      // 백엔드는 채널에서 반환한 원래 오류를 사실대로 반환합니다. 이것이 구성 문제를 해결하는 유일한 단서입니다. 그대로 표시합니다.。
      toast.error("테스트 실패:" + (e as Error).message, { duration: 12000 });
    } finally {
      setTesting(false);
    }
  }

  async function removeChannel(ch: NotificationChannel) {
    try {
      await api.notifyDeleteChannel(ch.id);
      toast.success(`삭제됨: ${ch.name}`);
      setOpen(false);
      load();
    } catch (e) {
      toast.error("삭제 실패:" + (e as Error).message);
    }
  }

  async function toggleEnabled(ch: NotificationChannel) {
    try {
      await api.notifyUpdateChannel(ch.id, { enabled: !ch.enabled });
      load();
    } catch (e) {
      toast.error("작업 실패:" + (e as Error).message);
    }
  }

  async function toggleGlobal(on: boolean) {
    setGlobalSaving(true);
    try {
      await api.setSettings({ notify_enabled: on });
      setMeta((m) => (m ? { ...m, enabled: on } : m));
      toast.success(on ? "푸시가 활성화되었습니다." : "푸시가 일시중지되었습니다.");
    } catch (e) {
      toast.error("작업 실패:" + (e as Error).message);
    } finally {
      setGlobalSaving(false);
    }
  }

  async function saveGlobal() {
    setGlobalSaving(true);
    try {
      const patch: Record<string, unknown> = { notify_public_base_url: baseURL.trim() };
      const n = Number(digestMin);
      if (Number.isFinite(n) && n > 0) patch.notify_digest_interval_min = n;
      await api.setSettings(patch);
      toast.success("저장됨");
      load();
    } catch (e) {
      toast.error("저장 실패:" + (e as Error).message);
    } finally {
      setGlobalSaving(false);
    }
  }

  const fields = CHANNEL_FIELDS[form.kind] ?? [];
  const secretKeys = new Set(meta?.kinds.find((k) => k.kind === form.kind)?.secret_keys ?? []);
  const defaultRate = meta?.kinds.find((k) => k.kind === form.kind)?.default_rate_per_min ?? 0;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">알림 전송</h1>
          <p className="text-muted-foreground text-sm">
            취약점 발견 시 DingTalk/Feishu/Business WeChat 및 기타 채널로 푸시 · 각 채널은 독립적으로 푸시 타이밍 및 필터링 규칙 설정 가능
          </p>
        </div>
        {meta && (
          // 사용 div 대신에 label：Switch 나만의 것을 가져오세요 aria-label，바깥쪽에 또 다른 레이어를 얹어주세요 label
          // 기본 컨트롤과 연결할 수 없으며 텍스트를 클릭하면 전환할 수 있는 것처럼 보입니다.。
          <div className="flex shrink-0 items-center gap-2 text-sm">
            <span className="text-muted-foreground">메인 스위치</span>
            <Switch
              checked={meta.enabled}
              disabled={globalSaving}
              onCheckedChange={toggleGlobal}
              aria-label="푸시 마스터 스위치"
            />
          </div>
        )}
      </div>

      {meta && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
          <StatTile label="채널" value={`${meta.stats.channels_on} / ${meta.stats.channels}`} hint="활성화/전체" />
          <StatTile label="오늘 배송됨" value={String(meta.stats.sent_today)} />
          <StatTile label="전송 대기" value={String(meta.stats.pending)} />
          <StatTile label="실패" value={String(meta.stats.failed)} tone={meta.stats.failed > 0 ? "red" : undefined} />
          <StatTile
            label="가장 긴 백로그"
            value={formatBacklog(meta.stats.backlog_age_ms)}
            // 백로그 수명은 백로그 번호보다 훨씬 더 유용합니다. 3 기사 출처는 다음과 같습니다. 3 도착까지 몇 초 3 시간。
            hint={meta.stats.backlog_age_ms > 5 * 60_000 ? "푸시가 멈췄을 수 있습니다." : undefined}
            tone={meta.stats.backlog_age_ms > 5 * 60_000 ? "red" : undefined}
          />
        </div>
      )}

      <Card className="gap-3">
        <CardHeader>
          <CardTitle className="text-base">전역 설정</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <div className="grid gap-2">
            <Label htmlFor="n-base">백링크 주소</Label>
            <Input
              id="n-base"
              placeholder="https://artex.example.com"
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">메시지에서 "세부정보 보기" 버튼이 가리키는 주소입니다. 버튼이 없으면 비워두세요.</p>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="n-digest">요약 기간(분)</Label>
            <Input
              id="n-digest"
              type="number"
              min={1}
              max={1440}
              placeholder="30"
              value={digestMin}
              onChange={(e) => setDigestMin(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">"집계" 모드의 채널에만 적용됩니다.</p>
          </div>
          <div className="sm:col-span-2">
            <Button onClick={saveGlobal} disabled={globalSaving}>
              전역 설정 저장
            </Button>
          </div>
        </CardContent>
      </Card>

      <Tabs value={tab} onValueChange={(v) => setTab(v as "channels" | "deliveries")} className="flex flex-col gap-4">
        <TabsList>
          <TabsTrigger value="channels">채널</TabsTrigger>
          <TabsTrigger value="deliveries">배송기록</TabsTrigger>
        </TabsList>

        <TabsContent value="channels">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <button
              type="button"
              onClick={openAdd}
              className="text-foreground/70 border-foreground/70 hover:bg-muted/60 hover:shadow-sm flex min-h-[130px] flex-col items-center justify-center gap-2 rounded-xl border border-dashed transition"
            >
              <PlusIcon className="size-6" />
              <span className="text-sm">채널 추가</span>
            </button>

            {channels.map((ch) => (
              <Card
                key={ch.id}
                onClick={() => openEdit(ch)}
                className="hover:border-primary/60 cursor-pointer gap-3 transition hover:shadow-sm"
              >
                <CardHeader>
                  <div className="flex items-center gap-2">
                    <BellIcon className="text-muted-foreground size-4 shrink-0" />
                    <CardTitle className="truncate text-base">{ch.name}</CardTitle>
                    {/* 전체 카드를 클릭하여 편집에 들어갈 수 있으므로 이 두 컨트롤은 거품을 별도로 삼켜야 합니다.，
                        그렇지 않으면 스위치/삭제하면 우연히 편집이 시작됩니다. 묶음 stopPropagation 컨트롤 자체를 기다려라
                        레이어 대신 본체 div：세트 div 만들 것입니다「대화형처럼 보이지만 그렇지 않습니다.
                        역할」두 가지 모두를 트리거하는 정적 요소 a11y 경고, 의미상으로도 이해가 되지 않습니다.。 */}
                    <div className="ml-auto flex items-center gap-2">
                      <Switch
                        checked={ch.enabled}
                        onCheckedChange={() => toggleEnabled(ch)}
                        onClick={(e) => e.stopPropagation()}
                        aria-label="활성화"
                      />
                      <Button
                        size="icon"
                        variant="outline"
                        aria-label="삭제"
                        onClick={(e) => {
                          e.stopPropagation();
                          // void 명시적으로 삭제 Promise：removeChannel 소유하다 catch 그리고 toast，
                          // 여기서는 필요하지 않습니다 await（onClick 아니요 async）。
                          void removeChannel(ch);
                        }}
                      >
                        <Trash2Icon className="text-destructive" />
                      </Button>
                    </div>
                  </div>
                </CardHeader>
                <CardContent className="grid gap-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="outline">{KIND_LABEL[ch.kind] ?? ch.kind}</Badge>
                    <Badge variant="outline">{ch.mode === "digest" ? "요약" : "실시간"}</Badge>
                    {!ch.enabled && <Badge variant="outline">비활성화됨</Badge>}
                  </div>
                  <FilterSummary filter={ch.filter} />
                </CardContent>
              </Card>
            ))}
          </div>
        </TabsContent>

        <TabsContent value="deliveries">
          <DeliveryList channels={channels} />
        </TabsContent>
      </Tabs>

      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="right" className="w-full data-[side=right]:sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>{editing ? editing.name : "알림 채널 추가"}</SheetTitle>
            <SheetDescription>
              {KIND_LABEL[form.kind] ?? form.kind}
              {defaultRate > 0 ? ` ·기본 전류 제한 ${defaultRate} 항목/분` : " · 현재 제한 없음"}
            </SheetDescription>
          </SheetHeader>

          <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">
            <div className="grid gap-4 py-4">
              <div className="grid gap-2">
                <Label>채널 유형</Label>
                <Select
                  value={form.kind}
                  onValueChange={(v) => {
                    // 유형을 변경하는 것은 자격 증명 필드 집합을 변경하는 것과 동일하며 이전 구성은 병합할 수 없습니다.。
                    setF({ kind: v, config: {} });
                  }}
                  disabled={!!editing}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(meta?.kinds ?? []).map((k) => (
                      <SelectItem key={k.kind} value={k.kind}>
                        {KIND_LABEL[k.kind] ?? k.kind}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {editing && (
                  <p className="text-muted-foreground text-xs">
                    채널 유형은 수정할 수 없습니다. 유형을 변경하는 것은 자격 증명 세트를 변경하는 것과 같습니다. 새 채널을 만들어 주세요.
                  </p>
                )}
              </div>

              <div className="grid gap-2">
                <Label htmlFor="n-name">채널 이름</Label>
                <Input
                  id="n-name"
                  placeholder="비상대응반/일상보고반"
                  value={form.name}
                  onChange={(e) => setF({ name: e.target.value })}
                />
              </div>

              {fields.length === 0 ? (
                <p className="text-muted-foreground text-sm">
                  이 채널의 형식이 정의되지 않았습니다(프런트 엔드에 CHANNEL_FIELDS 항목이 없음). 완료하고 다시 시도하십시오.
                </p>
              ) : (
                fields.map((d) => (
                  <ConfigField
                    key={d.key}
                    def={d}
                    value={form.config[d.key]}
                    isSecret={secretKeys.has(d.key)}
                    onChange={(v) => setCfg(d.key, v)}
                  />
                ))
              )}

              <div className="grid gap-2">
                <Label>푸시 타이밍</Label>
                <Select value={form.mode} onValueChange={(v) => setF({ mode: v as "realtime" | "digest" })}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="realtime">실시간 · 각 취약점별로 개별 발송</SelectItem>
                    <SelectItem value="digest">요약·기간별로 하나로 묶음</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-xs">
                  "고위험 실시간 및 기타 요약"을 수행하려는 경우 실시간 + 고위험 임계값 1개, 요약 + 무제한 수준 1개의 두 가지 채널을 만들 수 있습니다.
                </p>
              </div>

              <div className="grid gap-2">
                <Label htmlFor="n-rate">전류 한도(bar/분)</Label>
                <Input
                  id="n-rate"
                  type="number"
                  min={0}
                  placeholder={defaultRate > 0 ? String(defaultRate) : "0 = 제한 없음"}
                  value={form.ratePerMin}
                  onChange={(e) => setF({ ratePerMin: e.target.value })}
                />
                <p className="text-muted-foreground text-xs">
                  공백으로 두고 채널 기본값을 사용하십시오. 0은 흐름 제한이 없음을 의미합니다. 제한을 초과하면 메시지가 손실되지는 않지만 전송이 지연될 뿐입니다.
                </p>
              </div>

              <div className="border-t pt-4">
                <p className="mb-3 text-sm font-medium">필터 규칙(필터링하지 않으려면 비워 두세요)</p>
                <div className="grid gap-4">
                  <div className="grid gap-2">
                    <Label>가장 낮은 수준</Label>
                    <Select
                      value={form.minSeverity || "all"}
                      onValueChange={(v) => setF({ minSeverity: v === "all" ? "" : v })}
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {SEVERITY_OPTIONS.map((o) => (
                          <SelectItem key={o.value || "all"} value={o.value || "all"}>
                            {o.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-inc">이러한 취약점 유형만 푸시하세요.</Label>
                    <Textarea
                      id="n-inc"
                      placeholder={"SQL 주입 \n 명령 실행"}
                      value={form.includeText}
                      onChange={(e) => setF({ includeText: e.target.value })}
                    />
                    <p className="text-muted-foreground text-xs">
                      한 줄에 하나의 키워드, 대소문자를 구분하지 않는 하위 문자열 일치. 비워두기 = 모든 유형.
                    </p>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-exc">다음 취약점 유형 제외</Label>
                    <Textarea
                      id="n-exc"
                      placeholder={"정보 유출"}
                      value={form.excludeText}
                      onChange={(e) => setF({ excludeText: e.target.value })}
                    />
                    <p className="text-muted-foreground text-xs">제외가 포함보다 우선합니다. 동시 적중은 제외됩니다.</p>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-tasks">한정 미션 ID</Label>
                    <Input
                      id="n-tasks"
                      placeholder="1, 2, 3"
                      value={form.taskIDsText}
                      onChange={(e) => setF({ taskIDsText: e.target.value })}
                    />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-assets">한정자산 ID</Label>
                    <Input
                      id="n-assets"
                      placeholder="10, 11"
                      value={form.assetIDsText}
                      onChange={(e) => setF({ assetIDsText: e.target.value })}
                    />
                    <p className="text-muted-foreground text-xs">작업/자산을 비워두세요 = 제한 없음; 작성 후 요구사항은 취약점과 겹쳐야 합니다.</p>
                  </div>
                  <div className="flex items-center gap-2 text-sm">
                    <Switch
                      checked={form.onStatusChange}
                      onCheckedChange={(v) => setF({ onStatusChange: v })}
                      aria-label="상태 변경 수신"
                    />
                    취약점 처리 상태가 변경되는 경우에도 푸시됩니다. (실시간 모드에서만 가능)
                  </div>
                </div>
              </div>

              <div className="flex items-center gap-2 text-sm">
                <Switch checked={form.enabled} onCheckedChange={(v) => setF({ enabled: v })} aria-label="활성화" />
                이 채널을 활성화합니다
              </div>
            </div>

            <div className="flex gap-2 pt-2 pb-6">
              <Button onClick={saveForm} disabled={saving}>
                {editing ? "저장" : "추가"}
              </Button>
              {editing && (
                <Button variant="outline" onClick={testChannel} disabled={testing}>
                  <SendIcon /> 테스트 메시지 보내기
                </Button>
              )}
            </div>
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}
