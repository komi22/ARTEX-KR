// Centralised status → color/label semantics, reused across the whole app.
// 사양 §8.3: 의도/범위/작업/심각도 각각은 일관된 색상 세트를 갖습니다.

export type Tone = "neutral" | "blue" | "green" | "amber" | "red" | "rose" | "violet" | "slate";

export const toneClasses: Record<Tone, string> = {
  neutral: "bg-muted text-muted-foreground border-transparent",
  blue: "bg-blue-500/15 text-blue-600 dark:text-blue-400 border-blue-500/20",
  green: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/20",
  amber: "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20",
  red: "bg-red-500/15 text-red-600 dark:text-red-400 border-red-500/20",
  // Rose는 "심각한"을 의미하는 데 사용됩니다. 즉, "고위험"이라는 부드러운 빨간색 선보다 시각적으로 확실히 더 높은 강조점을 의미합니다.
  rose: "bg-rose-600 text-white border-rose-600 dark:bg-rose-600 dark:text-white",
  violet: "bg-violet-500/15 text-violet-600 dark:text-violet-400 border-violet-500/20",
  slate: "bg-slate-500/15 text-slate-600 dark:text-slate-400 border-slate-500/20",
};

export const toneDot: Record<Tone, string> = {
  neutral: "bg-muted-foreground",
  blue: "bg-blue-500",
  green: "bg-emerald-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
  rose: "bg-white",
  violet: "bg-violet-500",
  slate: "bg-slate-500",
};

interface StatusMeta {
  label: string;
  tone: Tone;
}

const intent: Record<string, StatusMeta> = {
  open: { label: "대기 중", tone: "slate" },
  running: { label: "실행 중", tone: "blue" },
  paused: { label: "일시중지됨", tone: "amber" },
  done: { label: "완료", tone: "green" },
  // 차단됨 = 모델/API/네트워크 오류 재시도가 소진되었습니다. 이러한 의도는 기본적으로 감지되지 않습니다(비대상 차단).
  blocked: { label: "실행 오류", tone: "red" },
  // 배기됨 = 단계 수/시간 예산에 도달하여 중간에 끊어지고, 일부 결과만 다시 기록됩니다(비방향이 소진되었습니다).
  exhausted: { label: "예산 소진", tone: "violet" },
  // 중지됨 = 기록 소프트 삭제 상태(보존된 기록 데이터).
  stopped: { label: "중지됨", tone: "slate" },
  // 삭제됨 = 사용자가 인텐트를 삭제했습니다. (노드와 계보는 유지되며, 삭제 이유는 delete_reason 필드에서 확인할 수 있습니다.)
  deleted: { label: "삭제됨", tone: "slate" },
};

const task: Record<string, StatusMeta> = {
  created: { label: "생성됨", tone: "slate" },
  queued: { label: "대기열", tone: "amber" },
  running: { label: "실행 중", tone: "blue" },
  paused: { label: "일시중지됨", tone: "amber" },
  done: { label: "완료", tone: "green" },
  failed: { label: "실패", tone: "red" },
  timeout: { label: "시간 초과", tone: "amber" },
};

const severity: Record<string, StatusMeta> = {
  critical: { label: "심각", tone: "rose" },
  high: { label: "위험도 높음", tone: "red" },
  medium: { label: "중간 위험", tone: "amber" },
  low: { label: "낮은 위험", tone: "slate" },
};

const finding: Record<string, StatusMeta> = {
  pending: { label: "보류 중", tone: "amber" },
  in_progress: { label: "처리 중", tone: "blue" },
  confirmed: { label: "확인됨", tone: "red" },
  resolved: { label: "처리됨", tone: "green" },
  fixed: { label: "수정됨", tone: "green" },
  false_positive: { label: "오탐", tone: "slate" },
  ignored: { label: "무시", tone: "neutral" },
  duplicate: { label: "중복", tone: "neutral" },
  risk_accepted: { label: "위험 수용", tone: "violet" },
};

const engine: Record<string, StatusMeta> = {
  exploring: { label: "탐색", tone: "blue" },
  paused: { label: "일시중지됨", tone: "amber" },
  stalled: { label: "진행 멈춤", tone: "red" },
  idle: { label: "유휴 상태", tone: "neutral" },
};

const goal: Record<string, StatusMeta> = {
  open: { label: "진행 중", tone: "blue" },
  met: { label: "달성", tone: "green" },
  abandoned: { label: "포기됨", tone: "slate" },
};

const audit: Record<string, StatusMeta> = {
  allow: { label: "허용", tone: "green" },
  block: { label: "차단", tone: "red" },
};

const node: Record<string, StatusMeta> = {
  observed: { label: "관찰", tone: "slate" },
  confirmed: { label: "확인", tone: "green" },
  tombstoned: { label: "포기", tone: "neutral" },
};

const maps = {
  intent,
  task,
  severity,
  finding,
  engine,
  goal,
  audit,
  node,
} as const;

export type StatusDomain = keyof typeof maps;

export function statusMeta(domain: StatusDomain, key: string): StatusMeta {
  return maps[domain][key] ?? { label: key, tone: "neutral" };
}
