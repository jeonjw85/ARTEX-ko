"use client";

import * as React from "react";

import {
  BuildingIcon,
  ChevronRightIcon,
  CircleDashedIcon,
  GlobeIcon,
  LayoutTemplateIcon,
  LinkIcon,
  type LucideIcon,
  NetworkIcon,
  RefreshCwIcon,
  SearchIcon,
  SmartphoneIcon,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import type { FindingAssetKind, FindingAssetNode } from "@/lib/types";
import { cn } from "@/lib/utils";

// 아이콘은 자산 페이지의 유형 매핑을 따르며 동일한 자산은 두 위치 모두에서 동일하게 보입니다.
const KIND_ICON: Record<FindingAssetKind, LucideIcon> = {
  company: BuildingIcon,
  root_domain: GlobeIcon,
  subdomain: GlobeIcon,
  ip: NetworkIcon,
  app: SmartphoneIcon,
  service: LayoutTemplateIcon,
  endpoint: LinkIcon,
  none: CircleDashedIcon,
};

const KIND_LABEL: Record<FindingAssetKind, string> = {
  company: "기업",
  root_domain: "루트 도메인 이름",
  subdomain: "하위 도메인",
  ip: "IP",
  app: "애플리케이션",
  service: "서비스",
  endpoint: "엔드포인트",
  none: "연결되지 않음",
};

// TreeNode는 노드 배열로 조립된 트리입니다. 백엔드는 "가장 먼저 발견되는 동일한 상위를 가진 백엔드"별로 정렬되었습니다.
// 여기서는 배열 순서대로 마운트하면 됩니다.
interface TreeNode extends FindingAssetNode {
  children: TreeNode[];
  depth: number;
  /** 트리에 실제 렌더링된 텍스트입니다. 전체 label는 여전히 label에 유지됩니다(호버 프롬프트 및 이동 경로용). */
  display: string;
}

function stripBrackets(host: string) {
  return host.startsWith("[") && host.endsWith("]") ? host.slice(1, -1) : host;
}

function parseAssetURL(raw: string): URL | null {
  try {
    return new URL(raw);
  } catch {
    return null;
  }
}

// hostOf는 노드로 표시되는 호스트를 사용합니다. URL는 hostname를 사용하고 "host:port"는 host를 사용하며 나머지는 다음과 같습니다.
// 라벨 자체(루트 도메인 이름/하위 도메인 이름/IP).
function hostOf(label: string): string {
  const url = parseAssetURL(label);
  if (url) return stripBrackets(url.hostname);
  const hostPort = label.match(/^(.+):(\d+)$/);
  return stripBrackets(hostPort ? hostPort[1] : label);
}

// shortLabel는 상위 노드와 중복되는 접두사를 제거합니다. service / endpoint의 label는 완전한 URL이고,
// 호스트 도메인 이름/IP 이전 줄은 이미 작성되었습니다. 깊은 노드는 본질적으로 좁습니다. host를 다시 반복합니다. 실제로는
// 정보의 모든 포트와 경로가 차단됩니다. 전체 값은 여전히 ​​title 및 이동 경로에 있습니다.
function shortLabel(node: FindingAssetNode, parent?: FindingAssetNode): string {
  if (!parent) return node.label;

  // 하위 도메인 이름은 루트 도메인 이름 아래에 있습니다. 루트 도메인 이름의 접미사를 제거하고 자신의 섹션만 남겨두세요.
  if (node.kind === "subdomain" && node.label.endsWith(`.${parent.label}`)) {
    return node.label.slice(0, -(parent.label.length + 1)) || node.label;
  }
  if (node.kind !== "service" && node.kind !== "endpoint") return node.label;

  // 상위 태그는 자체 접두사입니다.(인터페이스가 동일하게 유지됩니다. URL 해당 서비스에서 해당 서비스는 동일한 서비스에 연결됩니다. IP 아래에):그냥 잘라버려。
  if (node.label.startsWith(parent.label)) {
    return node.label.slice(parent.label.length) || node.label;
  }

  // 그렇지 않으면 상위 노드만 실제로는 다음과 같습니다. URL 호스트의 약어,그렇지 않으면 식별 정보가 손실됩니다.
  // (예를 들어, 하위 도메인 이름 자산이 부족하여 서비스가 루트 도메인 이름에 직접 연결됩니다.,그런 다음 완전히 표시되어야합니다. URL)。
  if (hostOf(node.label) !== hostOf(parent.label)) return node.label;

  const url = parseAssetURL(node.label);
  if (!url) return node.label;
  if (node.kind === "endpoint") return `${url.pathname}${url.search}` || "/";
  const scheme = url.protocol.replace(":", "");
  const port = url.port || (url.protocol === "https:" ? "443" : "80");
  return `${scheme} :${port}`;
}

export function buildAssetTree(nodes: FindingAssetNode[]): TreeNode[] {
  const byKey = new Map<string, TreeNode>();
  for (const node of nodes) {
    byKey.set(node.key, { ...node, children: [], depth: 0, display: node.label });
  }
  const roots: TreeNode[] = [];
  for (const node of nodes) {
    const current = byKey.get(node.key);
    if (!current) continue;
    const parent = node.parent ? byKey.get(node.parent) : undefined;
    // 상위 노드가 누락되었습니다.(잘린 레벨은 폐기됩니다.)시간이 최상위 레벨로 올라갔을 때,전체 하위 트리가 사라지는 것을 방지。
    if (parent) {
      parent.children.push(current);
      current.display = shortLabel(node, parent);
    } else {
      roots.push(current);
    }
  }
  const setDepth = (node: TreeNode, depth: number) => {
    node.depth = depth;
    for (const child of node.children) setDepth(child, depth + 1);
  };
  for (const root of roots) setDepth(root, 0);
  return roots;
}

// assetPathOf 최상위 수준에서 이 노드까지의 경로를 반환합니다.,오른쪽에는 빵가루. 각 레벨에는 상대 항목만 표시됩니다.
// 이전 레벨의 증가(display),전체 가치는 다음에 남아 있습니다. label 내부에。
export function assetPathOf(nodes: FindingAssetNode[], key: string | null): (FindingAssetNode & { display: string })[] {
  if (!key) return [];
  const byKey = new Map(nodes.map((n) => [n.key, n]));
  const path: FindingAssetNode[] = [];
  const seen = new Set<string>();
  let current = byKey.get(key);
  while (current && !seen.has(current.key)) {
    seen.add(current.key);
    path.unshift(current);
    current = current.parent ? byKey.get(current.parent) : undefined;
  }
  return path.map((node, index) => ({ ...node, display: shortLabel(node, path[index - 1]) }));
}

// filterTree 키워드로 필터링:적중 노드는 유지됩니다.,전체 조상 체인을 유지합니다.(조상 자신도 놓칠 수 있다)。
// 히트 노드의 자손도 유지됩니다.,드릴링을 계속하는 것이 더 쉽습니다.。
function filterTree(nodes: TreeNode[], keyword: string): TreeNode[] {
  const kw = keyword.trim().toLowerCase();
  if (!kw) return nodes;
  const walk = (node: TreeNode): TreeNode | null => {
    const hit = node.label.toLowerCase().includes(kw);
    if (hit) return node;
    const children = node.children.map(walk).filter((c): c is TreeNode => c !== null);
    if (children.length === 0) return null;
    return { ...node, children };
  };
  return nodes.map(walk).filter((n): n is TreeNode => n !== null);
}

// collectKeys 하나를 모으다(아들)모두 나무에 있다 key,에 사용「모든 일치 항목 펼치기」。
function collectKeys(nodes: TreeNode[], out: Set<string> = new Set()): Set<string> {
  for (const node of nodes) {
    out.add(node.key);
    collectKeys(node.children, out);
  }
  return out;
}

interface AssetTreeProps {
  nodes: FindingAssetNode[];
  selected: string | null;
  onSelect: (key: string | null) => void;
  loading?: boolean;
  truncated?: boolean;
  droppedKinds?: string[];
  /** 자산을 선택하지 않은 경우 오른쪽에 표시되는 총 발견 항목 수,에 사용「모든 자산」그 줄。 */
  findingTotal: number;
  /** 자산 보기가 폴링되지 않음,나무 개수는 이 버튼을 누르거나 페이지 내에서 추가, 삭제, 수정하여 새로 고쳐집니다.。 */
  onRefresh?: () => void;
}

export function AssetTree({
  nodes,
  selected,
  onSelect,
  loading,
  truncated,
  droppedKinds,
  findingTotal,
  onRefresh,
}: AssetTreeProps) {
  const [keyword, setKeyword] = React.useState("");
  const [expanded, setExpanded] = React.useState<Set<string>>(() => new Set());
  // 사용자가 수동으로 축소한 노드를 기억합니다.,...하지 않도록「기본적으로 최상위 수준 확장」새로고침할 때마다 다시 확장。
  const [collapsed, setCollapsed] = React.useState<Set<string>>(() => new Set());

  const roots = React.useMemo(() => buildAssetTree(nodes), [nodes]);
  const visible = React.useMemo(() => filterTree(roots, keyword), [roots, keyword]);

  // 검색 시 일치하는 모든 분기 확장,그렇지 않으면 히트 항목이 접힌 노드에 숨겨지며, 이는 검색이 없음을 의미합니다.。
  const searching = keyword.trim() !== "";
  const searchKeys = React.useMemo(() => (searching ? collectKeys(visible) : null), [searching, visible]);

  const isExpanded = React.useCallback(
    (node: TreeNode) => {
      if (searchKeys) return searchKeys.has(node.key);
      if (expanded.has(node.key)) return true;
      // 최상위 레이어는 기본적으로 확장됩니다.:레벨이 아무리 깊어도 사용자는 직접 클릭해야 합니다.,한 번에 수천 줄이 퍼지는 것을 방지하려면。
      return node.depth === 0 && !collapsed.has(node.key);
    },
    [collapsed, expanded, searchKeys],
  );

  const toggle = React.useCallback(
    (node: TreeNode) => {
      const open = isExpanded(node);
      setExpanded((prev) => {
        const next = new Set(prev);
        if (open) next.delete(node.key);
        else next.add(node.key);
        return next;
      });
      setCollapsed((prev) => {
        const next = new Set(prev);
        if (open) next.add(node.key);
        else next.delete(node.key);
        return next;
      });
    },
    [isExpanded],
  );

  let emptyHint = "현재 필터에는 자산과 관련된 발견 항목이 없습니다.";
  if (loading) emptyHint = "로드 중…";
  else if (searching) emptyHint = "일치하는 자산이 없습니다.";

  const rows: React.ReactNode[] = [];
  const pushRows = (list: TreeNode[]) => {
    for (const node of list) {
      const open = isExpanded(node);
      rows.push(
        <AssetTreeRow
          key={node.key}
          node={node}
          open={open}
          selected={selected === node.key}
          onToggle={() => toggle(node)}
          onSelect={() => onSelect(selected === node.key ? null : node.key)}
        />,
      );
      if (open && node.children.length > 0) pushRows(node.children);
    }
  };
  pushRows(visible);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="flex items-center gap-1">
        <InputGroup className="flex-1">
          <InputGroupInput
            type="search"
            value={keyword}
            onChange={(event) => setKeyword(event.target.value)}
            placeholder="자산 필터링"
            aria-label="자산 필터링"
          />
          <InputGroupAddon>
            <SearchIcon aria-hidden="true" />
          </InputGroupAddon>
        </InputGroup>
        {onRefresh && (
          <Button
            size="icon"
            variant="ghost"
            className="size-8 shrink-0 text-muted-foreground"
            onClick={onRefresh}
            disabled={loading}
            aria-label="자산 트리 새로 고침"
            title="자산 트리 새로 고침"
          >
            <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} />
          </Button>
        )}
      </div>

      <button
        type="button"
        onClick={() => onSelect(null)}
        className={cn(
          "flex items-center justify-between gap-2 rounded-md px-2 py-1.5 text-left text-sm",
          selected === null ? "bg-accent font-medium" : "hover:bg-accent/50",
        )}
      >
        <span>모든 자산</span>
        <span className="text-xs tabular-nums text-muted-foreground">{findingTotal}</span>
      </button>

      <div className="max-h-[24rem] min-h-0 flex-1 overflow-y-auto pr-2 lg:max-h-[calc(100vh-16rem)]">
        <div className="flex flex-col">
          {rows}
          {rows.length === 0 && <p className="px-2 py-8 text-center text-xs text-muted-foreground">{emptyHint}</p>}
        </div>
      </div>

      {truncated && (
        <p className="px-1 text-xs text-muted-foreground">
          숨겨진 자산이 너무 많습니다.{(droppedKinds ?? []).map((k) => KIND_LABEL[k as FindingAssetKind] ?? k).join(" / ")}
          계층(수는 여전히 상위 계층에 포함됨) 필터 또는 필터 상자를 사용하여 범위를 좁혀 전체 계층 구조를 확인하세요.
        </p>
      )}
    </div>
  );
}

function AssetTreeRow({
  node,
  open,
  selected,
  onToggle,
  onSelect,
}: {
  node: TreeNode;
  open: boolean;
  selected: boolean;
  onToggle: () => void;
  onSelect: () => void;
}) {
  const Icon = KIND_ICON[node.kind] ?? GlobeIcon;
  const hasChildren = node.children.length > 0;
  return (
    <div
      className={cn(
        "group flex items-center gap-1 rounded-md pr-1 text-sm",
        selected ? "bg-accent" : "hover:bg-accent/50",
      )}
      style={{ paddingLeft: `${node.depth * 10}px` }}
    >
      {hasChildren ? (
        <button
          type="button"
          onClick={onToggle}
          className="flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
          aria-label={open ? "겹" : "펼치기"}
          aria-expanded={open}
        >
          <ChevronRightIcon className={cn("size-3.5 transition-transform", open && "rotate-90")} />
        </button>
      ) : (
        <span className="size-5 shrink-0" />
      )}
      <button
        type="button"
        onClick={onSelect}
        className="flex min-w-0 flex-1 items-center gap-1.5 py-1 text-left"
        title={`${KIND_LABEL[node.kind] ?? node.kind} · ${node.label}`}
      >
        <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className={cn("min-w-0 truncate", selected && "font-medium")}>{node.display}</span>
      </button>
      <span className="flex shrink-0 items-center gap-1 text-xs tabular-nums">
        {node.critical > 0 && (
          <span className="text-rose-600" title={`진지한 ${node.critical}`}>
            {node.critical}
          </span>
        )}
        {node.high > 0 && (
          <span className="text-red-500" title={`고위험 ${node.high}`}>
            {node.high}
          </span>
        )}
        <span className="text-muted-foreground" title={`총 ${node.total} 조사 결과`}>
          {node.total}
        </span>
      </span>
    </div>
  );
}
