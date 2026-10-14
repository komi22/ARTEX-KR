"use client";

import * as React from "react";

import { PlusIcon, Trash2Icon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import type { AssetInterceptKind, AssetInterceptRuleInput } from "@/lib/types";

// shadcn Select 대신 NativeSelect(네이티브 <select>) 사용: 이 편집기는 시트 서랍에서 사용됩니다.
// shadcn Select의 본문에 대한 드롭다운 포털을 클릭하고 외부를 클릭하면 서랍의 "닫으려면 외부 클릭"이 트리거되어 실수로 닫힙니다. 기본 드롭다운에는 이 문제가 없습니다.
export const ASSET_INTERCEPT_KIND_OPTIONS: {
  value: AssetInterceptKind;
  label: string;
  placeholder: string;
}[] = [
  { value: "exact_domain", label: "도메인 이름(일치)", placeholder: "example.gov.cn" },
  { value: "exact_ip", label: "IP(합동)", placeholder: "203.0.113.10" },
  { value: "exact_url", label: "URL(일치)", placeholder: "https://example.com/login" },
  { value: "fuzzy_domain", label: "도메인 이름(모호함)", placeholder: ".gov.cn" },
  { value: "fuzzy_ip", label: "IP(퍼지)", placeholder: "203.0.113." },
  { value: "fuzzy_url", label: "URL(흐릿함)", placeholder: "/admin" },
  { value: "cidr", label: "CIDR 네트워크 세그먼트", placeholder: "192.168.0.0/16" },
];

// AssetInterceptRulesEditor는 "차단/허용 규칙"(차단/허용 +
// 유형 + 일치하는 내용 + 설명), 지속성이 제공되지 않습니다. 상위 구성 요소가 제출 시기를 결정합니다.
export function AssetInterceptRulesEditor({
  value,
  onChange,
}: {
  value: AssetInterceptRuleInput[];
  onChange: (v: AssetInterceptRuleInput[]) => void;
}) {
  function update(i: number, patch: Partial<AssetInterceptRuleInput>) {
    onChange(value.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  }
  function remove(i: number) {
    onChange(value.filter((_, idx) => idx !== i));
  }
  function add() {
    onChange([...value, { action: "block", kind: "fuzzy_domain", pattern: "", note: "", enabled: true }]);
  }
  return (
    <div className="grid gap-2">
      {value.map((r, i) => {
        const ph = ASSET_INTERCEPT_KIND_OPTIONS.find((o) => o.value === r.kind)?.placeholder ?? "";
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: 행에 안정적인 ID가 없으며 인덱스로 제어할 수 있습니다.
          <div key={i} className="flex items-center gap-2">
            <NativeSelect
              size="sm"
              className="w-[84px] shrink-0"
              value={r.action}
              onChange={(e) => update(i, { action: e.target.value as "block" | "allow" })}
            >
              <NativeSelectOption value="block">차단</NativeSelectOption>
              <NativeSelectOption value="allow">허용</NativeSelectOption>
            </NativeSelect>
            <NativeSelect
              size="sm"
              className="w-[120px] shrink-0"
              value={r.kind}
              onChange={(e) => update(i, { kind: e.target.value as AssetInterceptKind })}
            >
              {ASSET_INTERCEPT_KIND_OPTIONS.map((o) => (
                <NativeSelectOption key={o.value} value={o.value}>
                  {o.label}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <Input
              className="flex-1"
              placeholder={ph}
              value={r.pattern}
              onChange={(e) => update(i, { pattern: e.target.value })}
            />
            <Input
              className="w-[120px] shrink-0"
              placeholder="비고(선택사항)"
              value={r.note}
              onChange={(e) => update(i, { note: e.target.value })}
            />
            <Button
              type="button"
              size="icon"
              variant="ghost"
              className="text-destructive hover:text-destructive size-8 shrink-0"
              onClick={() => remove(i)}
            >
              <Trash2Icon className="size-4" />
            </Button>
          </div>
        );
      })}
      <Button type="button" size="sm" variant="outline" className="w-fit" onClick={add}>
        <PlusIcon className="size-4" /> 하나 추가
      </Button>
    </div>
  );
}
