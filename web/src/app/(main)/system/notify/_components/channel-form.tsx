"use client";

import { CheckIcon } from "lucide-react";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { NotificationFilter } from "@/lib/types";

// asText / inputType는 이 파일의 보조 값(컨트롤 렌더링과 밀접한 관련이 있음)이며 channel-fields에 배치되지 않습니다.
import { type FieldDef, type FieldKind, SEVERITY_OPTIONS } from "./channel-fields";

// asText 모든 구성 값을 입력 상자에서 사용할 수 있는 문자열로 렌더링합니다.
// config는 JSON에서 나오며, 값은 string / number / boolean / array / null일 수 있습니다.
// 여기서 유일한 관심사는 "텍스트 상자에 들어갈 수 있는지 여부"이며 buildConfig는 특정 직렬화를 담당합니다.
function asText(v: unknown): string {
  if (typeof v === "string") return v;
  if (v === null || v === undefined) return "";
  return String(v);
}

// inputType는 필드 유형을 input의 type 속성에 매핑합니다.
function inputType(kind: FieldKind): "text" | "password" | "number" {
  if (kind === "password") return "password";
  if (kind === "number") return "number";
  return "text";
}

// ConfigField는 필드 정의에 따라 해당 컨트롤을 렌더링합니다.
//
// 여기서는 마스크 필드 처리가 유일한 세부 사항입니다. 입력 상자에는 마스크 값 자체가 **표시되지 않고** 한 줄만 표시됩니다.
// "저장됨" 프롬프트. 이러한 방식으로 인터페이스에는 단 하나의 규칙만 있습니다. 즉, 상자 안의 단어는 사용자가 채우는 것입니다.
// 빈 상자는 빈 값입니다. 입력창에 "__masked__:...abc123"를 입력하면 사용자는 다음과 같이 생각하게 됩니다.
// 삭제된 자리 표시자 텍스트로 인해 자격 증명을 오해하기가 더 쉽습니다.
export function ConfigField({
  def,
  value,
  isSecret,
  onChange,
}: {
  def: FieldDef;
  value: unknown;
  isSecret: boolean;
  onChange: (v: unknown) => void;
}) {
  const id = `n-cfg-${def.key}`;
  const raw = asText(value);
  // 백엔드에서 에코된 마스크 값: 형식 "__masked__:…abc123"，꼬리는 원래 값의 식별 가능한 조각입니다.。
  const masked = isSecret && raw.startsWith("__masked__");
  const maskedTail = masked ? (raw.split("…")[1] ?? "") : "";

  if (def.kind === "switch") {
    return (
      <div className="flex items-center gap-2 text-sm">
        <Switch checked={value === true} onCheckedChange={onChange} aria-label={def.label} />
        {def.label}
        {def.help && <span className="text-muted-foreground">（{def.help}）</span>}
      </div>
    );
  }

  if (def.kind === "select") {
    return (
      <div className="grid gap-2">
        <Label>{def.label}</Label>
        <Select value={raw || def.options?.[0]?.value} onValueChange={onChange}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(def.options ?? []).map((o) => (
              <SelectItem key={o.value} value={o.value}>
                {o.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    );
  }

  // 컨트롤은 필드 유형별로 전달됩니다. 사용 if 여기서는 네 가지 유형의 컨트롤을 구별할 수 있으므로 중첩된 삼항 대신 체인을 사용합니다.，
  // 3단계 삼항법을 읽을 때는 멈추고 괄호를 세어야 합니다.。
  function control() {
    if (def.kind === "textarea" || def.kind === "kv") {
      return (
        <Textarea
          id={id}
          className="font-mono"
          placeholder={def.placeholder}
          value={masked ? "" : raw}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    }
    if (def.kind === "list") {
      return (
        <Input
          id={id}
          value={Array.isArray(value) ? (value as string[]).join(", ") : raw}
          onChange={(e) => onChange(e.target.value)}
          placeholder={def.placeholder}
        />
      );
    }
    return (
      <Input
        id={id}
        className={def.kind === "text" ? "font-mono" : ""}
        type={inputType(def.kind)}
        placeholder={def.placeholder}
        value={masked ? "" : raw}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }

  const hint = masked ? (
    <p className="text-muted-foreground flex items-center gap-1 text-xs">
      <CheckIcon className="size-3" />
      저장됨{maskedTail ? `(테일 번호 ${maskedTail})` : ""} · 새 값을 입력하면 덮어쓰게 되며, 지우면 해당 항목이 삭제됩니다.
    </p>
  ) : (
    def.help && <p className="text-muted-foreground text-xs">{def.help}</p>
  );

  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{def.label}</Label>
      {control()}
      {hint}
    </div>
  );
}

// FilterSummary 카드를 펼치지 않고도 이 채널이 홍보하는 내용을 확인할 수 있도록 필터 조건을 한 줄로 요약합니다.。
export function FilterSummary({ filter }: { filter: NotificationFilter }) {
  const parts: string[] = [];
  if (filter.min_severity) {
    parts.push(SEVERITY_OPTIONS.find((o) => o.value === filter.min_severity)?.label ?? filter.min_severity);
  }
  if (filter.vulnclass_include?.length) parts.push(`유형에 ${filter.vulnclass_include.length}라는 단어가 포함되어 있습니다.`);
  if (filter.vulnclass_exclude?.length) parts.push(`단어 ${filter.vulnclass_exclude.length} 제외`);
  if (filter.task_ids?.length) parts.push(`${filter.task_ids.length} 작업`);
  if (filter.asset_ids?.length) parts.push(`${filter.asset_ids.length} 자산`);
  if (filter.on_status_change) parts.push("상태 변경 사항이 포함되어 있습니다.");
  if (parts.length === 0) {
    return <p className="text-muted-foreground text-sm">모든 취약점</p>;
  }
  return <p className="text-muted-foreground text-sm">{parts.join(" · ")}</p>;
}
