export const mentionKinds = [
  { kind: "finding", label: "취약점", alias: "finding" },
  { kind: "asset", label: "자산", alias: "asset" },
  { kind: "company", label: "기업", alias: "company" },
  { kind: "endpoint", label: "엔드포인트", alias: "api" },
  { kind: "ip", label: "IP", alias: "ip" },
  { kind: "app", label: "애플리케이션", alias: "app" },
  { kind: "root_domain", label: "도메인", alias: "domain" },
  { kind: "subdomain", label: "하위 도메인", alias: "subdomain" },
  { kind: "service", label: "서비스", alias: "service" },
] as const;

// 기존 중국어 입력도 검색할 수 있게 유지하되 화면에는 한국어 이름만 표시합니다.
const legacyMentionAliases: Partial<Record<MentionKind, string>> = {
  finding: "\u6f0f\u6d1e",
  asset: "\u8d44\u4ea7",
  company: "\u4f01\u4e1a",
  endpoint: "\u63a5\u53e3",
  app: "\u5e94\u7528",
  root_domain: "\u57df\u540d",
  subdomain: "\u5b50\u57df\u540d",
  service: "\u670d\u52a1",
};

export type MentionKind = (typeof mentionKinds)[number]["kind"];
export interface ChatMention {
  kind: MentionKind;
  id: number;
  label: string;
  description: string;
}

export function activeMention(value: string, caret: number) {
  const before = value.slice(0, caret);
  const start = before.lastIndexOf("@");
  if (start < 0 || (start > 0 && /[\w.+/-]/.test(before[start - 1]))) return null;
  const query = before.slice(start + 1);
  if (/[[\]\r\n@]/.test(query) || query.length > 220) return null;
  return { start, end: caret, query };
}

export function mentionSearch(query: string) {
  const text = query.trimStart().toLowerCase();
  for (const item of mentionKinds) {
    for (const alias of [item.label.toLowerCase(), item.alias, legacyMentionAliases[item.kind]].filter(
      (value): value is string => Boolean(value),
    )) {
      if (text === alias || text.startsWith(`${alias} `) || (/[^a-z]/.test(alias) && text.startsWith(alias))) {
        return { kind: item.kind, query: query.trimStart().slice(alias.length).trim(), categories: [] };
      }
    }
  }
  const categories = mentionKinds.filter(
    (item) => item.label.toLowerCase().startsWith(text) || item.alias.startsWith(text),
  );
  return { kind: "" as const, query: query.trim(), categories };
}

export function mentionToken(item: ChatMention) {
  const kind = mentionKinds.find((entry) => entry.kind === item.kind)?.label ?? "자산";
  const label = item.label
    .replace(/[[\]]/g, (char) => (char === "[" ? "(" : ")"))
    .replace(/\s+/g, " ")
    .slice(0, 100);
  return `@[${kind}#${item.id} ${label}]`;
}

export function selectedMentions(value: string) {
  return [
    ...value.matchAll(
      /@\[(취약점|자산|기업|엔드포인트|IP|애플리케이션|도메인|하위 도메인|서비스|\u6F0F\u6D1E|\u8D44\u4EA7|\u4F01\u4E1A|\u63A5\u53E3|\u5E94\u7528|\u57DF\u540D|\u5B50\u57DF\u540D|\u670D\u52A1)#([0-9]+)(?: ([^\]\r\n]*))?\]/g,
    ),
  ].map((match) => {
    const kind =
      mentionKinds.find((item) => item.label === match[1] || legacyMentionAliases[item.kind] === match[1])?.label ??
      match[1];
    return {
      token: match[0],
      label: `${kind} #${match[2]}${match[3] ? ` · ${match[3]}` : ""}`,
      start: match.index,
    };
  });
}
