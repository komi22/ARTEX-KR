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

// 사고 스위치(thinking.type)와 사고 강도(reasoning_effort)는 두 개의 [상호 독립적] 필드입니다.
// 각각 별도로 설정 - 일부 인터페이스에는 사고 영역이 없으며 사고는 강도 매개변수에 의해서만 활성화될 수 있으므로 분리해야 합니다.
// 인벤토리 빈 문자열 = 이 필드 [보내지 않음]; Radix Select는 빈 값을 허용하지 않으므로 UI는 "none"을 사용합니다.
// Sentinel은 보내지 않는다는 뜻으로, 접속시 (NONE / fromStore / toStore) ""로 교체됩니다.
const NONE = "none";
const fromStore = (v?: string) => (v ? v : NONE);
const toStore = (v: string) => (v === NONE ? "" : v);
const THINKING_TYPES: { value: string; label: string }[] = [
  { value: NONE, label: "보내지 않음(기본값)" },
  { value: "disabled", label: "닫기" },
  { value: "enabled", label: "열기" },
];
// 상한을 출력하는 데 사용되는 요청 필드 이름입니다(openai 형식만 의미가 있습니다). NONE ← ""는 동일한 센티널 변환 세트를 사용합니다.
const MAX_TOKENS_FIELDS: { value: string; label: string }[] = [
  { value: NONE, label: "max_tokens(기본값)" },
  { value: "max_completion_tokens", label: "max_completion_tokens" },
];
// 다른 두 형식은 각각 고정된 필드 이름을 가지며 옵션은 의미가 없습니다. 설명 문구에 명확하게 명시되어 있어야 합니다.
const MAX_TOKENS_FIELD_HINTS: Record<string, string> = {
  openai:
    "보낼 수 있는 상한 키는 무엇입니까? max_tokens가 기본값이며 대부분의 호환 게이트웨이는 이를 인식합니다. OpenAI 공식 추론 모델(o 시리즈 / GPT-5)은 max_completion_tokens만 인식하고 max_tokens를 수신할 때 unsupported_parameter를 직접 보고합니다.",
  anthropic: "openai 형식만 사용할 수 있습니다. Anthropic의 필드 이름은 max_tokens로 고정됩니다.",
  "openai-responses": "openai 형식만 사용할 수 있습니다. Responses API의 필드 이름은 max_output_tokens로 고정되어 있습니다.",
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

//구성은 카드에 "정상 여부"를 표시합니다. Key를 채우지 않은 구성은 전혀 요청을 보낼 수 없으므로 차단기보다 먼저 언급해야 합니다.
// 나머지 상태는 폴링된 회로 차단기 레코드에서 나옵니다(폴링이 꺼지면 새 레코드가 생성되지 않으며 "정상" = 현재 알려진 오류가 없음).
type Health = { label: string; cls: string; hint?: string };
function healthOf(p: LLMProfile, m?: LLMPoolMember): Health {
  if (!p.api_key_hint) {
    return {
      label: "키가 구성되지 않음",
      cls: "border-muted-foreground/40 text-muted-foreground",
      hint: "API Key가 입력되지 않아 호출할 수 없습니다",
    };
  }
  if (m?.state === "tripped") {
    return {
      label: m.cooldown_secs > 0 ? `깨짐 · ${cooldownText(m.cooldown_secs)}` : "깨짐",
      cls: "border-destructive/50 text-destructive",
      hint: m.last_error,
    };
  }
  if (m?.state === "degraded") {
    return {
      label: `예외 · 실패 ${m.fails} 회`,
      cls: "border-amber-500/50 text-amber-600 dark:text-amber-400",
      hint: m.last_error,
    };
  }
  return { label: "보통", cls: "border-emerald-500/50 text-emerald-600 dark:text-emerald-400" };
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

  // 냉각 카운트다운은 백엔드에서 계산한 남은 시간(초)입니다. 서랍이 열려 있고 구성이 비정상적인 경우에만 정기적으로 당겨서 실행되도록 하세요.
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
        toast.success(patch.llm_pool_enabled ? "LLM 순환 사용 켜짐" : "LLM 순환 사용이 꺼졌습니다");
      } else {
        toast.success("보안 설정이 업데이트되었습니다");
      }
    } catch (e) {
      toast.error(`설정 실패:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function recover(id?: string) {
    try {
      await api.resetLLMPool(id);
      await onReload();
      toast.success(id ? "구성이 복원되었습니다" : "모든 구성이 복원되었습니다");
    } catch (e) {
      toast.error(`복원 실패:${(e as Error).message}`);
    }
  }

  const enabled = pool?.enabled ?? false;
  const chain = pool?.chain ?? [];
  // 폴링에 참여하는 멤버의 순서("폴링에 참여하지 않음"으로 표시된 멤버 제외)는 백엔드의 실제 시도 순서입니다.
  const inChain = chain.filter((m) => m.active || !m.excluded);
  const tripped = chain.filter((m) => m.state === "tripped");

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex flex-col gap-0 p-0 data-[side=right]:sm:max-w-lg">
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            <ZapIcon className="size-4" /> LLM 순환 사용 · 장애 조치
          </SheetTitle>
          <SheetDescription>
            켜면 별도 모델을 지정하지 않은 에이전트가 현재 설정을 사용할 수 없을 때(잔액 부족, 유효하지 않은 키, 사용 한도 또는 서비스 오류)
            다음 LLM 설정으로 자동 전환합니다.
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-6">
          <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
            <div className="grid gap-0.5">
              <Label className="text-sm">폴링 활성화</Label>
              <p className="text-muted-foreground text-xs">기본값은 꺼짐입니다. 끄면 현재 활성 LLM 설정만 사용하고, 요청이 실패해도 다른 설정으로 전환하지 않습니다.</p>
            </div>
            <Switch
              checked={enabled}
              disabled={busy}
              onCheckedChange={(v) => void toggle({ llm_pool_enabled: v })}
              aria-label="LLM 순환 사용 스위치"
            />
          </div>

          {enabled && (
            <>
              <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
                <div className="grid gap-0.5">
                  <Label className="text-sm">특정 모델이 실패하더라도 복구</Label>
                  <p className="text-muted-foreground text-xs">
                    기본적으로 꺼짐: 에이전트 또는 작업이 특정 구성을 지정하는 경우 해당 구성만 사용합니다. 실패하면 실패합니다(조용히 다른 모델로 전환되지 않습니다).
                    활성화한 후 지정된 구성이 실패하면 다음 폴링 체인으로 대체됩니다.
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
                    현재만 {inChain.length} 사용 가능한 구성이 있으면 폴링이 적용되지 않습니다. API 키가 채워지고 폴링에 참여하는 구성이 2개 이상 필요합니다.
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
                        {excluded && <Badge variant="outline">설문조사에 참여하지 마세요</Badge>}
                        <div className="ml-auto flex items-center gap-2">
                          {m.state === "tripped" && m.cooldown_secs > 0 && (
                            <span className="text-muted-foreground text-xs">냉방 {cooldownText(m.cooldown_secs)}</span>
                          )}
                          {m.state === "degraded" && (
                            <span className="text-muted-foreground text-xs">지속적인 실패 {m.fails} 회</span>
                          )}
                          {m.state !== "ok" && (
                            <Button
                              size="icon"
                              variant="ghost"
                              className="size-7"
                              aria-label="지금 복원"
                              title="즉시 복구: 차단 상태를 해제하고 다음에 호출할 때 구성을 다시 시도하세요."
                              onClick={() => void recover(m.profile_id)}
                            >
                              <RotateCcwIcon className="size-3.5" />
                            </Button>
                          )}
                        </div>
                      </div>
                      <div className="flex flex-wrap items-center gap-x-3 pl-7 text-muted-foreground text-xs">
                        <code className="truncate font-mono">{m.model}</code>
                        {!m.active && <span>우선순위 {m.priority}</span>}
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
                    설정이 없습니다
                  </div>
                )}
              </div>

              <div className="rounded-lg border border-dashed p-3 text-muted-foreground text-xs leading-relaxed">
                활성화된 구성은 항상 첫 번째 순위를 가지며, 나머지 구성은 높은 우선순위에서 낮은 우선순위로 정렬됩니다(각 구성에서 설정됨). 특정 구성 실패 후 냉각 시작(60초 → 5분 →
                30분), 냉각 기간 동안 건너뛰고 복구 후 자동으로 다시 전환됩니다. 컨텍스트 창이 맞지 않으면 현재 요청된 구성을 건너뜁니다. 지정된 모델의 에이전트
                기본적으로 작업 폴링에 참여하지 않습니다.
              </div>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// 모델 구성 서랍(신규/편집은 동일한 양식 세트를 공유함)
// ─────────────────────────────────────────────────────────────────────────────

function ProfileSheet({
  profile,
  open,
  onOpenChange,
  onSaved,
}: {
  profile: LLMProfile | null; // 널 = 새로운
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
  const [cw, setCw] = React.useState("0"); // 컨텍스트 창(K 토큰) 0=기본값 200K
  const [thinkingType, setThinkingType] = React.useState(NONE);
  const [effort, setEffort] = React.useState(NONE);
  const [priority, setPriority] = React.useState("0"); // 폴링 순서 큰 것이 먼저다
  const [poolExclude, setPoolExclude] = React.useState(false);
  const [streaming, setStreaming] = React.useState(true); // true=스트리밍(기본값); false=비스트리밍
  const [maxTokens, setMaxTokens] = React.useState("0"); // 단일 응답 출력 제한; 0 = 보내지 않음
  const [maxTokensField, setMaxTokensField] = React.useState(NONE); // 상한에 사용해야 하는 필드 이름;NONE=max_tokens
  const [sessionHeaderKey, setSessionHeaderKey] = React.useState(""); //사용자 정의 세션 헤더 이름. 비어 있음 = 보내지 않음
  const [retry, setRetry] = React.useState<LLMRetryOverride>(ZERO_OVERRIDE); // 이 구성에 대한 적용 범위를 다시 시도하십시오. 모두 0 = 글로벌 팔로우
  const [testing, setTesting] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [models, setModels] = React.useState<string[]>([]);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [modelsOpen, setModelsOpen] = React.useState(false);

  // 열릴 때마다 수신 프로필에서 양식을 작성합니다(새 프로필을 만들 때 기본값으로 재설정). 서랍을 닫았다가 열어보세요
  // 이전 구성의 잔상이 남지 않는 깔끔한 시작입니다.
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
        toast.success(`로드됨 ${r.models.length} 모델`);
      } else {
        toast.error(`모델 로드 실패:${r.error ?? "모델을 얻지 못했습니다"}`);
      }
    } catch (e) {
      toast.error(`모델 로드 중 오류 발생:${(e as Error).message}`);
    } finally {
      setLoadingModels(false);
    }
  }

  async function testConnection() {
    if (testing) return;
    setTesting(true);
    try {
      // 테스트를 위해 실제로 실행될 구성 매개변수를 사용하면 이 필드를 지원하지 않는 모델이 여기서 실패하게 됩니다.
      // 폭발하는 임무를 실행할 때까지 기다리는 대신. 프로필 ID 전달: 키 입력 상자가 비어 있는 경우 저장된 키를 사용합니다.
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
      // 답변 내용이 함께 표시됩니다. 모델이 실제로 말한 것을 볼 수 있어야만 대화와 동일합니다.
      if (r.ok)
        toast.success(`연결 성공 · ${r.latency_ms ?? "?"}ms · ${r.model ?? model}`, {
          description: r.reply ? `답글:${r.reply}` : undefined,
        });
      else toast.error(`연결 실패:${r.error ?? "알 수 없음"}`);
    } catch (e) {
      toast.error(`테스트 오류:${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  }

  async function save() {
    if (!name.trim() || !model.trim()) {
      toast.error("이름과 모델을 입력하세요");
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
        // 필드 이름 스위치는 openai(채팅 완료)에만 의미가 있으며 다른 형식은 기본값으로 대체됩니다.
        // 백엔드는 동일한 정규화를 다시 수행하지만 UI가 모순되는 값을 보내지 않도록 하십시오.
        max_tokens_field: format === "openai" ? toStore(maxTokensField) : "",
        session_header_key: sessionHeaderKey.trim(),
        retry,
      });
      if (isNew) toast.success(`새로운:${name.trim()}(활성화하려면 카드에서 활성화로 설정)`);
      else toast.success(profile?.is_default ? "저장되었습니다. 활성화 구성이 즉시 적용되므로 다시 시작할 필요가 없습니다." : "저장됨");
      onSaved(String(id));
      onOpenChange(false);
    } catch (e) {
      toast.error(`저장 실패:${(e as Error).message}`);
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
            {isNew ? "새로운 모델 구성" : `수정:${profile?.name}`}
            {profile?.is_default && (
              <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                활성화 중
              </Badge>
            )}
          </SheetTitle>
          <SheetDescription>
            {isNew
              ? "생성 후 자동으로 활성화되지 않습니다. 활성화하려면 카드에 \"활성화 설정\"을 하십시오."
              : "수정 후 저장을 클릭하세요. 활성화 구성은 저장 후 모든 에이전트에 즉시 적용됩니다."}
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="p-name">이름</Label>
              <Input
                id="p-name"
                placeholder="예: OpenAI 프로덕션"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="grid gap-2">
              <Label>형식</Label>
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
              {/* 모달: 이 팝오버의 콘텐츠는 시트의 스크롤 잠금 외부에 있는 <body>로 포탈됩니다.
                  모달이 없으면 목록을 렌더링할 수 있지만 스크롤할 수는 없습니다. 모달을 사용하면 상단 스크롤 잠금 자체를 유지할 수 있습니다. */}
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
            <Label htmlFor="p-base-url">기본 URL(선택사항)</Label>
            <Input
              id="p-base-url"
              className="font-mono"
              placeholder="https://api.openai.com/v1"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
            />
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-proxy">프록시(선택사항)</Label>
            <Input
              id="p-proxy"
              className="font-mono"
              placeholder="socks5://user:pass@127.0.0.1:1080 · http://127.0.0.1:8080"
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              LLM 아웃바운드 요청만 이 프록시를 통과하고 http/https/socks5를 지원하며 계정 비밀번호(예:
              socks5://user:pass@host:port, 비밀번호에는 특수 문자가 포함되어 있으며 URL 인코딩이 필요합니다); 공백으로 두면 프록시(직접 연결)를 사용하지 않는다는 의미입니다.
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-session-header">맞춤 세션 헤더(선택사항)</Label>
            <Input
              id="p-session-header"
              className="font-mono"
              placeholder="예: x-session-id(비워두기 = 보내지 않음)"
              value={sessionHeaderKey}
              onChange={(e) => setSessionHeaderKey(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              헤더 이름을 입력한 후 각 요청은 이 HTTP 헤더를 가져오고 헤더 값은 자동으로 채워집니다. <b>현재 세션의 세션 ID</b>(다음과 같은 채팅 세션
              conv-12, exp3-worker-i87과 같은 작업자). 세션 ID 헤더를 기반으로 프롬프트 캐싱에 사용됩니다. /
              고정 라우팅을 위한 게이트웨이; 동일한 세션은 여러 라운드 동안 안정적이지만 다른 세션은 서로 다릅니다. 보내지 않으려면 비워두세요.
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-api-key">API Key</Label>
            <Input
              id="p-api-key"
              type="password"
              placeholder={keyHint ? `이미 설정됨(${keyHint}), 변경되지 않고 그대로 유지하려면 비워두세요` : "sk-…"}
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
            속도 제한 0 = 제한이 없으며 모든 에이전트가 공유합니다. 컨텍스트 창 단위 K(천개 토큰), 0 = 기본값 200K, 상한 1000(예:
            100만); 너무 높게 설정하면 압축이 실행되지 않습니다.
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
                <Label className="text-sm">설문조사에 참여하지 마세요</Label>
                <p className="text-muted-foreground text-xs">
                  활성화되면 장애 조치 대상으로 사용되지 않습니다(에이전트/작업에서 명시적으로 지정할 수 있음). "특정 에이전트에게만
                  다른 구성이 실패해도 소각되고 싶지 않은 고가의 전용 구성입니다.
                </p>
              </div>
              <Switch checked={poolExclude} onCheckedChange={setPoolExclude} aria-label="설문조사에 참여하지 마세요" />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">스트리밍 출력 · 스트리밍</Label>
                <p className="text-muted-foreground text-xs">
                  작업 중 실시간 진행 및 실시간 토큰 계산을 통해 스트리밍 SSE를 활성화(기본값)합니다. 닫히면 true이고 스트리밍되지 않습니다(스트림: false,
                  한 번에 완전한 응답 반환) - 일부 게이트웨이의 열악한 SSE 구현(빈 프레임/사고 필드 프레임 드롭)을 우회할 수 있습니다.
                  비용은 실행 중 실시간 진행 손실입니다.
                </p>
              </div>
              <Switch checked={streaming} onCheckedChange={setStreaming} aria-label="스트리밍 출력" />
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-max-tokens" className="text-sm">
                  출력 상한 · 최대 토큰
                </Label>
                <p className="text-muted-foreground text-xs">
                  각 요청과 함께 전송되는 단일 응답에 대해 생성된 최대 토큰 수입니다. 0(기본값) = 서버 기본값에 따라 이 필드를 보내지 않습니다.
                  이것은 위의 "컨텍스트 창"과 다릅니다. 즉, 압축 임계값을 계산하기 위해 로컬에서만 사용되는 모델의 총 용량입니다.
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
                <Label className="text-sm">사고 스위치 · Thinking.type</Label>
                <p className="text-muted-foreground text-xs">
                  사고 필드 전송 여부를 제어합니다. 보내지 않음 = 이 필드를 포함하지 않음(지원하지 않는 MiniMax와 같은 모델과 호환 가능) 닫기 = 보내기
                  비활성화됨; 활성화되었습니다. 아래 강도와는 무관합니다.
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
                  독립적인 강도 수준(OpenAI reasoning_effort / Anthropic output_config.effort). 일부 인터페이스에는 사고 기능이 없습니다.
                  현장, 사고는 힘만으로 활성화할 수 있어 사고 스위치를 보내지 않고도 별도로 설정할 수 있습니다.
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
            {testing ? "테스트 중..." : "연결 테스트"}
          </Button>
          <Button onClick={save} disabled={saving} className="flex-1">
            {saving && <Loader2Icon className="animate-spin" />}
            {!saving && (isNew ? <PlusIcon /> : <SaveIcon />)}
            {isNew ? "신규" : "저장"}
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
  // 서랍의 스위치와 콘텐츠는 별도로 저장됩니다. 편집 내용은 닫혀도 변경되지 않습니다. 그렇지 않으면 제목이 변경됩니다.
  //"Edit X"가 "New"로 깜박입니다. edit = null은 새로운 것을 생성한다는 의미입니다.
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

  // 카드의 상태 배지는 프로필 ID별로 폴링 상태를 가져옵니다.
  const health = React.useMemo(() => {
    const m = new Map<string, LLMPoolMember>();
    for (const c of pool?.chain ?? []) m.set(c.profile_id, c);
    return m;
  }, [pool]);

  async function activate(id: string, name: string) {
    try {
      await api.activateLLMProfile(id);
      toast.success(`활성화됨:${name}`);
      await load();
    } catch (e) {
      toast.error(`활성화 실패:${(e as Error).message}`);
    }
  }

  async function remove(p: LLMProfile) {
    if (p.is_default) {
      toast.error("현재 활성화된 구성을 삭제할 수 없습니다");
      return;
    }
    try {
      await api.deleteLLMProfile(p.id);
      toast.success(`삭제됨:${p.name}`);
      await load();
    } catch (e) {
      toast.error(`삭제 실패:${(e as Error).message}`);
    }
  }

  const poolOn = pool?.enabled ?? false;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">LLM</h1>
          <p className="text-muted-foreground text-sm">
            형식/모델/속도 제한 구성은 모든 에이전트가 공유합니다. 카드 편집을 클릭하면 별표가 현재 활성화된 구성을 나타냅니다.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="outline" onClick={() => setPoolOpen(true)}>
            <ZapIcon /> 폴링 구성
            {poolOn && (
              <Badge variant="outline" className="ml-1 border-emerald-500/50 text-emerald-600 dark:text-emerald-400">
                활성화
              </Badge>
            )}
          </Button>
          <Button size="sm" variant="outline" onClick={() => openEditor(null)}>
            <PlusIcon /> 신규
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
                // biome-ignore lint/a11y/useSemanticElements: 카드에는 자체 작동 버튼이 포함되어 있습니다. 기본 <button>을 사용하면 버튼 중첩이 발생합니다(불법 HTML).
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
                      {p.proxy && <span className="truncate">에이전트 {p.proxy}</span>}
                      {p.reasoning_effort && (
                        <span>생각 {p.reasoning_effort === "off" ? "끄기" : p.reasoning_effort}</span>
                      )}
                      {/* 폴링과 관련된 두 필드는 폴링이 켜져 있을 때만 의미가 있고, 꺼져 있을 때는 공간을 차지하지 않습니다.*/}
                      {poolOn &&
                        !p.is_default &&
                        (p.pool_exclude ? <span>설문조사에 참여하지 마세요</span> : <span>우선순위 {p.priority ?? 0}</span>)}
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
                아직 모델 구성이 없습니다. 오른쪽 상단에 있는 "새로 만들기"를 클릭하여 첫 번째 모델을 만드세요.
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
