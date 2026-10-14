"use client";

import * as React from "react";
import type { Graph as G6Graph } from "@antv/g6";
import {
  AppWindow,
  Building2,
  Globe,
  Link2,
  type LucideIcon,
  Radio,
  RefreshCw,
  Server,
  Waypoints,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { api } from "@/lib/api";
import type { CoverageAssetRef, CoverageAssetRefs, CoverageGraphEdge, CoverageGraphNode } from "@/lib/types";
import { cn } from "@/lib/utils";

// 각 상위 노드 아래에 표시되는 동일한 유형의 하위 노드의 기본 수입니다. 접힌 부분을 초과하면 "Show More"는 매번 이만큼 더 끌어 당깁니다.
const FOLD_LIMIT = 20;
const FOLD_STEP = 20;

type Kind = CoverageGraphNode["kind"];

type KindMeta = { label: string; icon: LucideIcon; iconBg: string; hex: string; size: number };

const kindMeta: Record<Kind, KindMeta> = {
  company: { label: "회사", icon: Building2, iconBg: "bg-slate-500", hex: "#64748b", size: 46 },
  root_domain: { label: "루트 도메인 이름", icon: Globe, iconBg: "bg-indigo-500", hex: "#6366f1", size: 38 },
  subdomain: { label: "하위 도메인", icon: Waypoints, iconBg: "bg-blue-500", hex: "#3b82f6", size: 30 },
  ip: { label: "IP", icon: Server, iconBg: "bg-cyan-600", hex: "#0891b2", size: 28 },
  service: { label: "서비스", icon: Radio, iconBg: "bg-amber-500", hex: "#f59e0b", size: 26 },
  app: { label: "App", icon: AppWindow, iconBg: "bg-fuchsia-500", hex: "#d946ef", size: 26 },
  endpoint: { label: "엔드포인트", icon: Link2, iconBg: "bg-rose-500", hex: "#f43f5e", size: 20 },
};

// G6 노드 아이콘은 플랫폼과 일치하는 lucide 아이콘을 사용합니다. lucide SVG 경로(v1.22)를 흰색 선으로 렌더링합니다.
// 데이터 URI, 노드 iconSrc(단색/회색 배경에서 흰색이 선명함). 반응 돔/서버를 피하기 위해 인라인으로 직접 작성
// React19/Next의 클라이언트 측 패키징 관련 문제.
function svgUri(inner: string, filled = false): string {
  const attrs = filled
    ? 'fill="#fff" stroke="none"'
    : 'fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"';
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" ${attrs}>${inner}</svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

const KIND_ICON: Record<Kind, string> = {
  company: svgUri(
    '<path d="M10 12h4"/><path d="M10 8h4"/><path d="M14 21v-3a2 2 0 0 0-4 0v3"/><path d="M6 10H4a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-2"/><path d="M6 21V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v16"/>',
  ),
  root_domain: svgUri(
    '<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>',
  ),
  subdomain: svgUri(
    '<path d="m10.586 5.414-5.172 5.172"/><path d="m18.586 13.414-5.172 5.172"/><path d="M6 12h12"/><circle cx="12" cy="20" r="2"/><circle cx="12" cy="4" r="2"/><circle cx="20" cy="12" r="2"/><circle cx="4" cy="12" r="2"/>',
  ),
  ip: svgUri(
    '<rect width="20" height="8" x="2" y="2" rx="2" ry="2"/><rect width="20" height="8" x="2" y="14" rx="2" ry="2"/><line x1="6" x2="6.01" y1="6" y2="6"/><line x1="6" x2="6.01" y1="18" y2="18"/>',
  ),
  service: svgUri(
    '<path d="M16.247 7.761a6 6 0 0 1 0 8.478"/><path d="M19.075 4.933a10 10 0 0 1 0 14.134"/><path d="M4.925 19.067a10 10 0 0 1 0-14.134"/><path d="M7.753 16.239a6 6 0 0 1 0-8.478"/><circle cx="12" cy="12" r="2"/>',
  ),
  app: svgUri(
    '<rect x="2" y="4" width="20" height="16" rx="2"/><path d="M10 4v4"/><path d="M2 8h20"/><path d="M6 4v4"/>',
  ),
  endpoint: svgUri(
    '<path d="M9 17H7A5 5 0 0 1 7 7h2"/><path d="M15 7h2a5 5 0 1 1 0 10h-2"/><line x1="8" x2="16" y1="12" y2="12"/>',
  ),
};

const FOLD_ICON = svgUri(
  '<circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/>',
  true,
);

// ---------------------------------------------------------------------------
//접기: 전체 그래프 → 현재 표시되는 노드/에지 세트(하향식 계단식 접기).
// ---------------------------------------------------------------------------
type FoldNode = {
  fold: true;
  key: string;
  groupId: string;
  parentKey: string;
  kind: Kind;
  hidden: CoverageGraphNode[];
};
type AssetRenderNode = { fold: false; key: string; kind: Kind; node: CoverageGraphNode };
type RenderNode = AssetRenderNode | FoldNode;

function sortChildren(a: CoverageGraphNode, b: CoverageGraphNode): number {
  if (a.tested !== b.tested) return a.tested ? -1 : 1;
  return (a.label || "").localeCompare(b.label || "");
}

function computeVisible(
  nodes: CoverageGraphNode[],
  edges: CoverageGraphEdge[],
  expanded: Map<string, number>,
): { renderNodes: RenderNode[]; renderEdges: CoverageGraphEdge[] } {
  const byKey = new Map(nodes.map((n) => [n.key, n]));
  const parentOf = new Map<string, string>();
  const childrenOf = new Map<string, string[]>();
  for (const e of edges) {
    if (!byKey.has(e.src) || !byKey.has(e.dst)) continue;
    if (!parentOf.has(e.src)) parentOf.set(e.src, e.dst);
    const arr = childrenOf.get(e.dst);
    if (arr) arr.push(e.src);
    else childrenOf.set(e.dst, [e.src]);
  }

  const renderNodes: RenderNode[] = [];
  const visible = new Set<string>();
  const foldNodes: FoldNode[] = [];

  const queue: string[] = [];
  for (const n of nodes) {
    if (!parentOf.has(n.key)) {
      queue.push(n.key);
      visible.add(n.key);
    }
  }

  for (let qi = 0; qi < queue.length; qi++) {
    const parent = queue[qi];
    const kids = childrenOf.get(parent) ?? [];
    if (kids.length === 0) continue;
    const groups = new Map<Kind, CoverageGraphNode[]>();
    for (const ck of kids) {
      const cn = byKey.get(ck);
      if (!cn) continue;
      const g = groups.get(cn.kind);
      if (g) g.push(cn);
      else groups.set(cn.kind, [cn]);
    }
    for (const [kind, arr] of groups) {
      arr.sort(sortChildren);
      const groupId = `${parent}::${kind}`;
      const shown = expanded.get(groupId) ?? FOLD_LIMIT;
      const visibleChildren = arr.slice(0, shown);
      const hidden = arr.slice(shown);
      for (const c of visibleChildren) {
        if (!visible.has(c.key)) {
          visible.add(c.key);
          queue.push(c.key);
        }
      }
      if (hidden.length > 0) {
        foldNodes.push({ fold: true, key: `fold:${groupId}`, groupId, parentKey: parent, kind, hidden });
      }
    }
  }

  for (const n of nodes) {
    if (visible.has(n.key)) renderNodes.push({ fold: false, key: n.key, kind: n.kind, node: n });
  }
  const renderEdges: CoverageGraphEdge[] = edges.filter((e) => visible.has(e.src) && visible.has(e.dst));
  for (const f of foldNodes) {
    renderNodes.push(f);
    renderEdges.push({ src: f.key, dst: f.parentKey });
  }
  return { renderNodes, renderEdges };
}

// ---------------------------------------------------------------------------
// G6 데이터 매핑. 사용자 정의 필드는 노드의 최상위 수준에 배치됩니다(G6 v5 공식 강제 예제 규칙: 스타일/레이아웃 콜백은 d.<field>를 직접 읽음).
// ---------------------------------------------------------------------------
type G6NodeDatum = {
  id: string;
  kind: Kind;
  fold: boolean;
  tested: boolean;
  inScope: boolean;
  lbl: string;
  size: number;
};

function trunc(s: string, n = 26): string {
  return s.length > n ? `${s.slice(0, n - 1)}…` : s;
}

// 그림의 노드 복사본: 서비스는 전체 URL을 표시하지 않고 포트, 제목 및 상태 코드만 표시합니다. 끝점은 경로만 표시합니다.
// 나머지 유형은 백엔드에서 지정한 레이블을 따릅니다.
function graphLabel(n: CoverageGraphNode): string {
  if (n.kind === "service") {
    const parts: string[] = [];
    if (n.port) parts.push(`:${n.port}`);
    if (n.page_title) parts.push(n.page_title);
    if (n.status_code) parts.push(String(n.status_code));
    return parts.length ? parts.join(" · ") : n.domain || n.ip || n.label;
  }
  if (n.kind === "endpoint" && n.url) {
    try {
      return new URL(n.url).pathname || "/";
    } catch {
      /* 잘못된 URL: 전체 라벨로 대체*/
    }
  }
  return n.label;
}

function toG6Nodes(renderNodes: RenderNode[]): G6NodeDatum[] {
  return renderNodes.map((rn) => {
    if (rn.fold) {
      return {
        id: rn.key,
        kind: rn.kind,
        fold: true,
        tested: false,
        inScope: false,
        lbl: `또한 ${rn.hidden.length} 조각${kindMeta[rn.kind].label}`,
        size: 24,
      };
    }
    return {
      id: rn.key,
      kind: rn.kind,
      fold: false,
      tested: rn.node.tested,
      inScope: rn.node.in_scope,
      lbl: trunc(graphLabel(rn.node) || kindMeta[rn.kind].label),
      size: kindMeta[rn.kind].size,
    };
  });
}

// G6의 유형은 데이터 아래에 사용자 정의 필드를 그룹화하지만 공식 강제 예제(및 런타임)는 최상위 수준에서 d.<field>를 읽습니다.
// 콜백 형식 매개변수는 G6 서명을 충족하기 위해 알 수 없음을 사용하고, nd()는 내부적으로 최상위 구조로 강제로 다시 전송하는 데 사용됩니다.
const nd = (d: unknown) => d as G6NodeDatum;

// 측정됨 = 단색 하이라이트; 측정되지 않은 범위 = 회색; 범위를 벗어남/접힘됨 = 밝은 회색 + 점선 획.
function nodeFill(d: G6NodeDatum): string {
  if (d.fold) return "#f1f5f9";
  if (!d.inScope) return "#e2e8f0";
  if (d.tested) return kindMeta[d.kind].hex;
  return "#94a3b8";
}
function nodeStroke(d: G6NodeDatum): string {
  if (d.fold || !d.inScope) return "#94a3b8";
  if (d.tested) return "#0f766e";
  return "#64748b";
}

// ---------------------------------------------------------------------------
// 서랍: 세부정보를 볼 수 있는 자산 노드입니다. 숨겨진 목록을 보려면 노드를 축소하고 + "더 보기"를 표시합니다.
// ---------------------------------------------------------------------------
function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  if (children === undefined || children === null || children === "") return null;
  return (
    <div className="flex items-start gap-3 py-1 text-sm">
      <span className="text-muted-foreground w-16 shrink-0">{label}</span>
      <span className="text-foreground min-w-0 flex-1 break-words">{children}</span>
    </div>
  );
}

function RefList({ title, items }: { title: string; items: CoverageAssetRef[] }) {
  if (items.length === 0) return null;
  return (
    <div>
      <h4 className="text-muted-foreground mb-1 text-xs font-medium">
        {title}({items.length})
      </h4>
      <div className="flex flex-col gap-1">
        {items.map((r) => (
          <div
            key={`${r.kind}-${r.id}`}
            className="flex flex-wrap items-start gap-2 rounded-md bg-muted/50 px-2 py-1.5 text-xs"
          >
            <span className="shrink-0 font-mono text-muted-foreground">#{r.id}</span>
            {r.state && <span className="shrink-0 text-muted-foreground">{r.state}</span>}
            <span className="min-w-32 flex-1 break-words text-foreground">{r.summary || "—"}</span>
            {r.inherited && r.source_task_id && (
              <Badge variant="outline" className="shrink-0">
                소스 #{r.source_task_id} ·읽기 전용
              </Badge>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

function AssetSheet({
  node,
  taskId,
  onOpenChange,
}: {
  node: CoverageGraphNode | null;
  taskId: string;
  onOpenChange: (open: boolean) => void;
}) {
  const meta = node ? kindMeta[node.kind] : null;
  const Icon = meta?.icon;
  const raw = node ? JSON.stringify(node, null, 2) : "";
  const [refs, setRefs] = React.useState<CoverageAssetRefs | null>(null);

  React.useEffect(() => {
    setRefs(null);
    if (!node?.asset_id) return;
    let cancelled = false;
    api
      .taskAssetRefs(taskId, node.asset_id)
      .then((r) => {
        if (!cancelled) setRefs(r);
      })
      .catch(() => {
        /* 관련 없음 또는 오류: 이 블록을 표시하지 않습니다.*/
      });
    return () => {
      cancelled = true;
    };
  }, [node, taskId]);
  return (
    <Sheet open={node !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-md">
        {node && meta && Icon && (
          <>
            <SheetHeader className="border-b p-4">
              <div className="flex items-center gap-2.5 pr-8">
                <span className={cn("flex size-8 shrink-0 items-center justify-center rounded-lg shadow-sm", meta.iconBg)}>
                  <Icon className="size-4 text-white" />
                </span>
                <div className="min-w-0">
                  <SheetTitle className="leading-tight">{meta.label}</SheetTitle>
                  <span className="text-muted-foreground truncate font-mono text-xs" title={node.label}>
                    {node.label}
                  </span>
                </div>
              </div>
            </SheetHeader>
            <ScrollArea className="min-h-0 flex-1">
              <div className="flex w-full min-w-0 flex-col gap-4 p-4">
                <section>
                  <h4 className="text-muted-foreground mb-1 text-xs font-medium">속성</h4>
                  <DetailRow label="유형">{meta.label}</DetailRow>
                  <DetailRow label="테스트 상태">
                    {node.in_scope ? (
                      node.tested ? (
                        <span className="text-emerald-600 dark:text-emerald-400">테스트됨</span>
                      ) : (
                        <span className="text-neutral-500">테스트되지 않음</span>
                      )
                    ) : (
                      <span className="text-neutral-400">범위 벗어남(연결 노드)</span>
                    )}
                  </DetailRow>
                  <DetailRow label="도메인">{node.domain}</DetailRow>
                  <DetailRow label="루트 도메인 이름">{node.root_domain}</DetailRow>
                  <DetailRow label="IP">{node.ip}</DetailRow>
                  <DetailRow label="포트">{node.port ? node.port : undefined}</DetailRow>
                  <DetailRow label="URL">
                    {node.url ? <span className="font-mono text-xs break-all">{node.url}</span> : undefined}
                  </DetailRow>
                  <DetailRow label="제목">{node.page_title}</DetailRow>
                  <DetailRow label="상태 코드">{node.status_code ? node.status_code : undefined}</DetailRow>
                  <DetailRow label="App">{node.app_name}</DetailRow>
                  <DetailRow label="asset_ids">
                    {node.asset_id ? <span className="font-mono text-xs">{node.asset_id}</span> : undefined}
                  </DetailRow>
                </section>
                {refs && (refs.intents.length > 0 || refs.facts.length > 0 || refs.findings.length > 0) && (
                  <section className="flex flex-col gap-3 border-t pt-3">
                    <RefList title="연관 의도" items={refs.intents} />
                    <RefList title="관련 사실" items={refs.facts} />
                    <RefList title="연관 검색" items={refs.findings} />
                  </section>
                )}
                <section className="border-t pt-3">
                  <h4 className="text-muted-foreground mb-1.5 text-xs font-medium">원본 데이터</h4>
                  <pre className="bg-muted/50 text-foreground max-w-full overflow-hidden rounded-md border p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">
                    {raw}
                  </pre>
                </section>
              </div>
            </ScrollArea>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

function FoldSheet({
  fold,
  onOpenChange,
  onShowMore,
  onPick,
}: {
  fold: FoldNode | null;
  onOpenChange: (open: boolean) => void;
  onShowMore: (groupId: string) => void;
  onPick: (n: CoverageGraphNode) => void;
}) {
  const meta = fold ? kindMeta[fold.kind] : null;
  const Icon = meta?.icon;
  return (
    <Sheet open={fold !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-md">
        {fold && meta && Icon && (
          <>
            <SheetHeader className="border-b p-4">
              <SheetTitle className="text-base">
                표시되지 않음{meta.label}({fold.hidden.length})
              </SheetTitle>
              <p className="text-muted-foreground text-xs">먼저 테스트를 거쳐 표시되었습니다. 다음 배치를 사진으로 가져오려면 "더 보기"를 클릭하세요.</p>
            </SheetHeader>
            <ScrollArea className="min-h-0 flex-1">
              <div className="flex flex-col gap-1 p-3">
                {fold.hidden.map((n) => (
                  <button
                    key={n.key}
                    type="button"
                    onClick={() => onPick(n)}
                    className="hover:bg-accent flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm"
                  >
                    <span
                      className={cn(
                        "flex size-5 shrink-0 items-center justify-center rounded",
                        n.tested ? meta.iconBg : "bg-neutral-400 dark:bg-neutral-600",
                      )}
                    >
                      <Icon className="size-3 text-white" />
                    </span>
                    <span className="min-w-0 flex-1 truncate font-mono text-xs" title={n.label}>
                      {n.label}
                    </span>
                    {n.tested && <span className="size-1.5 shrink-0 rounded-full bg-emerald-500" />}
                  </button>
                ))}
              </div>
            </ScrollArea>
            <div className="border-t p-3">
              <Button className="w-full" variant="outline" onClick={() => onShowMore(fold.groupId)}>
                더 보기(+{FOLD_STEP})
              </Button>
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

// ---------------------------------------------------------------------------
function GraphInner({ taskId, coverageEnabled = true }: { taskId: string; coverageEnabled?: boolean }) {
  const [data, setData] = React.useState<{
    nodes: CoverageGraphNode[];
    edges: CoverageGraphEdge[];
  } | null>(null);
  const [expanded, setExpanded] = React.useState<Map<string, number>>(new Map());
  const [selectedAsset, setSelectedAsset] = React.useState<CoverageGraphNode | null>(null);
  const [selectedFoldId, setSelectedFoldId] = React.useState<string | null>(null);
  const [loading, setLoading] = React.useState(true);

  const containerRef = React.useRef<HTMLDivElement>(null);
  const graphRef = React.useRef<G6Graph | null>(null);
  // 클릭 처리에는 최신 키→RenderNode 매핑(G6 이벤트 콜백 클로저 외부 참조 읽기)이 필요합니다.
  const renderMapRef = React.useRef<Map<string, RenderNode>>(new Map());
  const gDataRef = React.useRef<{ nodes: G6NodeDatum[]; edges: { source: string; target: string }[] }>({
    nodes: [],
    edges: [],
  });

  const fetchGraph = React.useCallback(() => {
    setLoading(true);
    api
      .taskCoverageGraph(taskId)
      .then((g) => {
        // 자산 적용 범위 기능이 꺼진 경우(B1): 지도는 계속 표시되지만(범위 내의 자산/회사 연결은 계속 표시됨) 부드럽게 처리됩니다.
        //테스트된 상태 - 테스트 진행 상황을 표시하지 않으며 테스트된 결과를 강조 표시하지 않습니다.
        const nodes = coverageEnabled ? (g.nodes ?? []) : (g.nodes ?? []).map((n) => ({ ...n, tested: false }));
        setData({ nodes, edges: g.edges ?? [] });
      })
      .catch(() => {
        /* 마지막 데이터 유지*/
      })
      .finally(() => setLoading(false));
  }, [taskId, coverageEnabled]);

  React.useEffect(() => {
    fetchGraph();
  }, [fetchGraph]);

  const { renderNodes, renderEdges } = React.useMemo(
    () =>
      data
        ? computeVisible(data.nodes, data.edges, expanded)
        : { renderNodes: [] as RenderNode[], renderEdges: [] as CoverageGraphEdge[] },
    [data, expanded],
  );

  // 구조적 서명: 불필요한 지터를 피하기 위해 표시되는 노드/에지 세트가 변경될 때만 그래프를 다시 작성하고 레이아웃을 다시 실행합니다.
  const sig = React.useMemo(
    () =>
      `${renderNodes.map((n) => `${n.key}:${n.fold ? "f" : n.node.tested ? "t" : "u"}`).sort().join(",")}|${renderEdges.length}`,
    [renderNodes, renderEdges],
  );

  // gDataRef + renderMapRef(이벤트 및 그래프 데이터 애플리케이션에서 읽기)를 유지합니다.
  gDataRef.current = {
    nodes: toG6Nodes(renderNodes),
    edges: renderEdges.map((e) => ({ source: e.src, target: e.dst })),
  };
  const rmap = new Map<string, RenderNode>();
  for (const rn of renderNodes) rmap.set(rn.key, rn);
  renderMapRef.current = rmap;

  const applyData = React.useCallback(() => {
    const graph = graphRef.current;
    if (!graph || graph.destroyed) return;
    graph.setData(gDataRef.current);
    // render()는 d3-force 레이아웃을 비동기적으로 실행합니다. 레이아웃이 구현되기 전에 구성요소가 언로드/삭제되면 g6는
    // 컨텍스트에서 변환에 액세스하면 오류가 발생합니다(런타임/레이아웃 변환 DataAfterLayout 참조). 이것은 순수하다
    // 분해 경쟁 조건은 삼켜도 기능에 영향을 미치지 않습니다. 실제 렌더링 오류(이미지가 파괴되지 않음)는 계속 기록됩니다.
    void graph.render().catch((err) => {
      if (!graph.destroyed) console.error("[coverage-graph] render:", err);
    });
  }, []);

  // 지도를 만듭니다(1회). 동적 가져오기는 SSR/정적 내보내기 중에 창 종속성을 방지합니다.
  React.useEffect(() => {
    let destroyed = false;
    let graph: G6Graph | null = null;
    void (async () => {
      const { Graph } = await import("@antv/g6");
      if (destroyed || !containerRef.current) return;
      graph = new Graph({
        container: containerRef.current,
        autoResize: true,
        autoFit: "view",
        background: "#f0f2f7",
        node: {
          style: {
            size: (d: unknown) => nd(d).size,
            fill: (d: unknown) => nodeFill(nd(d)),
            stroke: (d: unknown) => nodeStroke(nd(d)),
            lineWidth: (d: unknown) => (nd(d).fold || !nd(d).inScope ? 1 : 1.5),
            lineDash: (d: unknown) => (nd(d).fold || !nd(d).inScope ? [3, 3] : [0]),
            iconSrc: (d: unknown) => (nd(d).fold ? FOLD_ICON : KIND_ICON[nd(d).kind]),
            iconWidth: (d: unknown) => Math.max(12, nd(d).size * 0.55),
            iconHeight: (d: unknown) => Math.max(12, nd(d).size * 0.55),
            labelText: (d: unknown) => nd(d).lbl,
            labelFontSize: 10,
            labelPlacement: "bottom",
            labelFill: "#475569",
            labelBackground: true,
            labelBackgroundFill: "rgba(255,255,255,0.75)",
            labelBackgroundRadius: 3,
            labelPadding: [1, 3],
          },
        },
        edge: {
          style: { stroke: "#cbd5e1", lineWidth: 1, endArrow: false },
        },
        layout: {
          type: "d3-force",
          collide: { radius: (d: unknown) => (nd(d).size ? nd(d).size : 20) + 8 },
          link: {
            distance: (edge: unknown) => {
              // 최상위 수준(회사/루트 도메인 이름)은 이온 노드에서 더 멀리 떨어져 있고 잎은 더 가깝습니다. edge.source는 ID이거나 확인된 노드일 수 있습니다.
              const s = (edge as { source: string | { id?: string } }).source;
              const srcId = typeof s === "string" ? s : (s?.id ?? "");
              const src = renderMapRef.current.get(srcId);
              const k = src && !src.fold ? src.kind : "endpoint";
              return k === "company" || k === "root_domain" ? 120 : 60;
            },
          },
          manyBody: {
            strength: (d: unknown) => (nd(d).kind === "endpoint" || nd(d).fold ? -60 : -200),
          },
        },
        behaviors: ["drag-element-force", "drag-canvas", "zoom-canvas"],
      });
      graph.on("node:click", (evt: unknown) => {
        const id = (evt as { target?: { id?: string } }).target?.id;
        if (!id) return;
        const rn = renderMapRef.current.get(id);
        if (!rn) return;
        if (rn.fold) setSelectedFoldId(rn.key);
        else setSelectedAsset(rn.node);
      });
      graphRef.current = graph;
      applyData();
    })();
    return () => {
      destroyed = true;
      // 먼저 레이아웃을 중지한 다음 삭제합니다. "레이아웃이 실행 중이고 컨텍스트가 지워졌습니다"라는 레이스 창을 줄이십시오.
      try {
        graph?.stopLayout();
      } catch {
        /* 다이어그램이 아직 작성되지 않았거나 레이아웃 컨텍스트가 없을 수 있습니다.*/
      }
      graph?.destroy();
      graphRef.current = null;
    };
  }, [applyData]);

  // 눈에 보이는 컬렉션 변경 → 데이터 리필 + 레이아웃. sig는 재정렬 트리거로만 작동합니다(applyData가 gDataRef를 읽음).
  //biome-ignore lint/corrightness/useExhaustiveDependities: sig는 트리거 종속성을 의도적으로 재배치한 것입니다.
  React.useEffect(() => {
    applyData();
  }, [sig, applyData]);

  const showMore = React.useCallback((groupId: string) => {
    setExpanded((prev) => {
      const m = new Map(prev);
      m.set(groupId, (m.get(groupId) ?? FOLD_LIMIT) + FOLD_STEP);
      return m;
    });
  }, []);

  const selectedFold =
    selectedFoldId != null
      ? ((renderNodes.find((n) => n.fold && n.key === selectedFoldId) as FoldNode | undefined) ?? null)
      : null;
  React.useEffect(() => {
    if (selectedFoldId != null && !selectedFold) setSelectedFoldId(null);
  }, [selectedFoldId, selectedFold]);

  const total = data?.nodes.length ?? 0;
  const tested = data?.nodes.filter((n) => n.in_scope && n.tested).length ?? 0;
  const inScope = data?.nodes.filter((n) => n.in_scope).length ?? 0;

  return (
    <div className="relative h-full w-full">
      <div ref={containerRef} className="h-full w-full" />

      {/* 범례 + 통계 + 새로 고침(오버레이)*/}
      <div className="bg-card/95 pointer-events-auto absolute top-3 left-3 flex max-w-[340px] flex-col gap-2.5 rounded-lg border p-3 text-xs shadow-sm backdrop-blur">
        <div className="flex items-center justify-between gap-3">
          {total > 0 ? (
            <span className="text-muted-foreground">
              범위 내 <span className="text-foreground font-semibold tabular-nums">{inScope}</span>
              {coverageEnabled && (
                <>
                  {" "}
                  · 테스트됨{" "}
                  <span className="font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">{tested}</span>
                </>
              )}
            </span>
          ) : (
            <span className="text-muted-foreground">{loading ? "불러오는 중…" : "현재 범위에 자산이 없습니다(먼저 작업 범위를 고정하세요)"}</span>
          )}
          <Button variant="ghost" size="icon" className="size-6" onClick={fetchGraph} title="새로고침">
            <RefreshCw className={cn("size-3.5", loading && "animate-spin")} />
          </Button>
        </div>
        <div className="flex flex-wrap gap-x-3 gap-y-1.5">
          {(Object.keys(kindMeta) as Kind[]).map((k) => {
            const m = kindMeta[k];
            const Icon = m.icon;
            return (
              <span key={k} className="text-foreground inline-flex items-center gap-1.5">
                <span className={cn("flex size-4 items-center justify-center rounded", m.iconBg)}>
                  <Icon className="size-2.5 text-white" />
                </span>
                {m.label}
              </span>
            );
          })}
        </div>
        <div className="border-border/60 text-muted-foreground flex flex-wrap gap-x-3 gap-y-1.5 border-t pt-2">
          {coverageEnabled && (
            <>
              <span className="inline-flex items-center gap-1.5">
                <span className="size-3 rounded-full bg-emerald-500" /> 측정됨(강조표시됨)
              </span>
              <span className="inline-flex items-center gap-1.5">
                <span className="size-3 rounded-full bg-neutral-400" /> 테스트되지 않음
              </span>
            </>
          )}
          <span className="inline-flex items-center gap-1.5">
            <span className="size-3 rounded-full border border-dashed border-neutral-400 bg-neutral-200" /> 범위를 벗어났습니다
          </span>
        </div>
        <p className="text-muted-foreground/80 border-border/60 border-t pt-2 leading-relaxed">
          강제 지정 레이아웃, 스크롤 휠을 사용하여 노드를 드래그하고 확대/축소할 수 있습니다. 회색 "⋯"은 접힌 노드이며, 클릭하면 더 확장할 수 있습니다.
        </p>
      </div>

      <AssetSheet node={selectedAsset} taskId={taskId} onOpenChange={(o) => !o && setSelectedAsset(null)} />
      <FoldSheet
        fold={selectedFold}
        onOpenChange={(o) => !o && setSelectedFoldId(null)}
        onShowMore={showMore}
        onPick={(n) => {
          setSelectedFoldId(null);
          setSelectedAsset(n);
        }}
      />
    </div>
  );
}

export function CoverageGraphTab({ taskId, coverageEnabled = true }: { taskId: string; coverageEnabled?: boolean }) {
  return (
    <Card>
      <CardContent className="p-0">
        <div className="h-[72vh] w-full overflow-hidden rounded-xl">
          <GraphInner taskId={taskId} coverageEnabled={coverageEnabled} />
        </div>
      </CardContent>
    </Card>
  );
}
