"use client";

// LLM 재시도 구성의 공유 부분: 5가지 재시도 수준 각각에 대한 "회수 + 간격"입니다.
//
// 내부에서 외부까지 5개 계층: 연결 설정(SDK) → 빈 응답(SDK) → 동일한 공급자 보안 창 → 폴링 회로 차단기 → 재실행 의도.
// 처음 세 개의 레이어는 끝점을 따르므로 각 모델 구성은 전역 기본값을 재정의할 수 있습니다. 마지막 두 레이어는 프로세스 수준이며 전역 복사본만 갖습니다.
//
// 모든 입력은 백엔드 db.RetryRule과 일치하는 동일한 "비워 두기 = 구성되지 않음" 의미 체계 세트를 따릅니다.
//횟수 Null/0 = 내장된 기본값 사용 | -1 = 이 레이어를 끄고 다시 시도 | >0 = 이 횟수 사용
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
  /** 이 재시도 계층은 어디에서 발생하며 누가 수행합니까?*/
  where: string;
  /** 이 수준에 도달하는 오류 종류 - 상태 코드에 따라 다르므로 누구도 추측하지 못하게 하세요.*/
  trigger: string;
  /** 비슷해 보이지만 이정도 수준까지는 안가서 폼 작성 후 답변이 없으면 버그라고 생각하지 않기 위함입니다.*/
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** 횟수를 비워둘 경우 기본값으로 자리 표시자로 사용됩니다.*/
  defAttempts: number;
  /** 간격이 비어 있을 때의 기본 전략은 자리 표시자에 사용됩니다.*/
  defInterval: string;
  /** 횟수에 -1을 채우는 의미*/
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "연결 재시도",
    where: "SDK · HTTP 200 응답 전",
    trigger: "연결 실패, 연결 재설정, 읽기·쓰기 시간 초과, DNS 오류와 HTTP 408·429·500·502·503·504 응답.",
    skips: "HTTP 400·401·403·404·413·422 등 재시도해도 해결되지 않는 오류.",
    desc: "같은 요청을 다시 보냅니다. HTTP 200 응답을 받은 뒤 스트림이 끊기면 다른 재시도 단계에서 처리합니다.",
    attemptsLabel: "재시도 횟수",
    defAttempts: 3,
    defInterval: "0.5초→1초→2초 지수(최대 8초)",
    offHint: "-1 = 재시도하지 않고 오류를 바로 전달",
  },
  empty: {
    title: "빈 응답 재시도",
    where: "SDK · OpenAI 형식만 해당",
    trigger: "HTTP 200 응답과 정상 종료 표시가 있지만 콘텐츠 블록이 하나도 없는 경우.",
    skips: "max_tokens 제한으로 출력이 잘린 경우. 출력 한도를 늘려야 합니다.",
    desc: "전체 프롬프트를 다시 보내므로 긴 대화에서는 비용이 늘어납니다.",
    attemptsLabel: "재시도 횟수",
    defAttempts: 2,
    defInterval: "0.5초→1초→2초 지수(최대 8초)",
    offHint: "-1 = 빈 응답을 재시도 없이 전달",
  },
  stream: {
    title: "동일 제공자 스트림 재시도",
    where: "본 프로젝트 · 결과물이 전달되기 전",
    trigger:
      "HTTP 200 응답 후 스트림이 끊기거나 제공자 과부하·429·5xx 오류가 발생했지만, 아직 사용자에게 출력이 전달되지 않은 경우.",
    skips: "할당량 소진(402), 문맥 길이 초과(413), HTTP 400·401·403·404·422 등 재시도해도 해결되지 않는 오류.",
    desc: "같은 모델 설정으로 요청을 다시 보냅니다. 출력이 전달되기 전에만 재시도해 중복 실행을 방지합니다.",
    attemptsLabel: "재시도 횟수",
    defAttempts: 2,
    defInterval: "0.5초→1초 지수(최대 4초)",
    offHint: "-1 = 이 단계의 재시도를 건너뛰고 작업 재실행 단계로 전달",
  },
  breaker: {
    title: "모델 전환 회로 차단기",
    where: "ARTEX · 전체 시스템 설정",
    trigger:
      "HTTP 429·5xx·네트워크 오류가 연속으로 발생하면 차단합니다. 잔액 부족(402), 키 오류(401·403), 모델 없음(404)은 첫 실패에 차단합니다.",
    skips: "성공하면 연속 실패 횟수가 초기화됩니다.",
    desc: "차단된 모델은 냉각 기간 동안 자동 전환 대상에서 제외됩니다. 상태는 데이터베이스에 저장되어 재시작 후에도 유지됩니다.",
    attemptsLabel: "연속 실패 횟수",
    defAttempts: 3,
    defInterval: "1분→5분→30분",
    offHint: "-1 = 일시적 오류에는 차단기를 사용하지 않음. 확정적 오류는 계속 차단합니다.",
  },
  intent: {
    title: "작업 의도 재실행",
    where: "ARTEX · 전체 시스템 설정",
    trigger:
      "이전 단계에서 처리되지 않은 모델 오류로 작업자가 종료된 경우. 재시도가 모두 실패했거나 출력이 시작된 뒤 스트림이 끊긴 경우도 포함합니다.",
    skips:
      "할당량 소진은 모델 전환 단계에서 처리합니다. 작업이 일시 중지·종료·마무리 상태로 바뀌면 재실행하지 않습니다.",
    desc: "작업 의도를 처음부터 다시 실행합니다. 내부 재시도도 다시 수행되므로 비용과 시간이 늘어날 수 있습니다.",
    attemptsLabel: "재실행 횟수",
    defAttempts: 2,
    defInterval: "고정 3s",
    offHint: "-1 = 재실행하지 않고 해당 의도를 차단 상태로 처리",
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

/** 제어되는 디지털 입력: 빈 문자열 ⇔ 0, 중간 상태("-", "1e")는 상위 상태를 방해하지 않고 로컬 상태를 그대로 유지합니다. */
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
  // 상위 항목이 전체 값 집합(정책 읽기, 구성 전환)을 변경해도 계속 유지됩니다. 직접 입력할 때는 여기에 표시되지 않습니다.
  // 그 당시 값은 이미 로컬 텍스트 구문 분석 결과와 동일하기 때문입니다.
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

/** 한 레벨 재시도를 위한 두 개의 손잡이. idPrefix는 동일한 페이지가 여러 번 나타날 때 레이블의 htmlFor를 유지하는 데 사용됩니다. */
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
  /** true = 구성 서랍의 컴팩트 버전: 확장 설명을 생략하고 "이 수준에 도달하는 오류는 무엇입니까?"라는 문장만 남깁니다.*/
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
        {/* 이 수준에 도달하는 오류, 특히 상태 코드 - 손잡이를 채웠지만 효과를 볼 수 없다면 오류가 이 수준에 전혀 해당되지 않기 때문일 수 있습니다. */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">발생 조건</span>: {meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">적용되지 않는 경우</span>: {meta.skips}
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
            placeholder={`기본값 ${meta.defAttempts}`}
            onChange={(n) => onChange({ ...value, attempts: n })}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
            간격(ms)
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-ms`}
            min={0}
            value={value.interval_ms}
            placeholder="기본 백오프"
            onChange={(n) => onChange({ ...value, interval_ms: n })}
          />
          <span className="text-muted-foreground text-xs">{human ? `설정값 ${human}` : meta.defInterval}</span>
        </div>
      </div>
      {!compact && <p className="text-muted-foreground text-xs">비워두면 기본값을 사용합니다. {meta.offHint}</p>}
    </div>
  );
}

/** 모델 구성 서랍의 3개 레이어 적용 범위(종료점을 따르는 3개 레이어) */
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
        <Label className="text-sm">이 모델의 재시도 설정</Label>
        <p className="text-muted-foreground text-xs">
          이 모델에만 적용되며 전역 기본값보다 우선합니다. 비워두면 전역 설정을 따릅니다. 재시도 횟수에 -1을 입력하면
          해당 단계를 끄고, 간격을 입력하면 지수 백오프 대신 고정 간격을 사용합니다. 회로 차단기와 작업 의도 재실행은
          전역 설정에서 조정합니다.
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

/** "재시도 및 백오프" 탭: 레이어 5에 대한 전역 기본값입니다. */
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
      toast.error(`재시도 설정을 읽지 못했습니다: ${(e as Error).message}`);
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
      // 백엔드는 범위를 벗어난 값을 다시 범위로 고정하고 반환하며, 반환된 값으로 직접 새로 고칩니다. 당신이 보는 것은 당신이 저장하는 것입니다.
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("재시도 설정을 저장했습니다. 다음 모델 호출부터 적용됩니다.");
    } catch (e) {
      toast.error(`저장 실패:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

  if (loading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
        <Loader2Icon className="size-4 animate-spin" /> 재시도 설정을 읽는 중...
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
        모델 호출이 실패하면 다음 순서로 처리합니다.
        <span className="text-foreground">
          {" "}
          연결 재시도 → 빈 응답 재시도 → 스트림 재시도 → 회로 차단기 → 작업 의도 재실행
        </span>
        . 앞 단계가 실패하면 다음 단계로 넘어갑니다. 여러 단계의 횟수를 높이면 한 번의 오류에 많은 요청이 발생할 수
        있습니다. 값을 비워두면 기본값을 사용하며, 처음 세 단계는 모델별로 재정의할 수 있습니다.
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
