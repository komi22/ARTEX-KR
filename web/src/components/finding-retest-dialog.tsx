"use client";

import * as React from "react";

import { RotateCcwIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { FindingRetest } from "@/lib/types";

interface FindingRetestDialogProps {
  findingId: string;
  findingName?: string;
  onClose: () => void;
  onStarted?: (retest: FindingRetest) => void;
}

// 열 때만 마운트되며, 닫으면 설명이 지워집니다. 목록과 세부 정보는 제출 잠금 및 오류 처리를 공유하며 시작 후에도 현재 페이지에 유지됩니다.
export function FindingRetestDialog({ findingId, findingName, onClose, onStarted }: FindingRetestDialogProps) {
  const notesId = React.useId();
  const [notes, setNotes] = React.useState("");
  const [submitting, setSubmitting] = React.useState(false);
  const submitLock = React.useRef(false);

  async function start() {
    if (submitLock.current) return;
    submitLock.current = true;
    setSubmitting(true);
    try {
      const result = await api.startFindingRetest(findingId, notes.trim());
      onStarted?.(result.retest);
      onClose();
      toast.success(result.created ? "재테스트가 시작되었습니다. \"재테스트\"를 클릭하여 세션을 볼 수 있습니다" : "이 취약점은 다시 테스트 중이며 기존 세션을 볼 수 있습니다");
    } catch (e) {
      toast.error(`재테스트 시작 실패:${(e as Error).message}`);
    } finally {
      submitLock.current = false;
      setSubmitting(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && !submitLock.current && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>취약점 #{findingId}</DialogTitle>
          <DialogDescription className="break-words">
            {findingName ? <span className="mb-2 block">{findingName}</span> : null}
            에이전트 재테스트
            원래 증거와 테스트 제약 조건을 읽고 별도의 세션에서 타겟 검증을 수행합니다. 재테스트가 성공적으로 완료되고 수정 사항이 확인되면 취약점 상태가 자동으로 "수정됨"으로 변경되고 다른 결론은 원래 상태로 유지됩니다.
          </DialogDescription>
        </DialogHeader>
        <FieldGroup>
          <Field data-disabled={submitting}>
            <FieldLabel htmlFor={notesId}>추가 설명(선택사항)</FieldLabel>
            <Textarea
              id={notesId}
              value={notes}
              maxLength={4000}
              rows={4}
              disabled={submitting}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="예: 원래 테스트 계정을 사용하여 원래 인터페이스를 확인합니다. 수리된 버전은 v2입니다."
            />
            <FieldDescription>수정된 버전, 테스트 조건 또는 제한사항이 추가될 수 있습니다.</FieldDescription>
          </Field>
        </FieldGroup>
        <DialogFooter>
          <Button variant="outline" disabled={submitting} onClick={onClose}>
            취소
          </Button>
          <Button disabled={submitting} onClick={() => void start()}>
            {submitting ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}
            {submitting ? "만드는 중..." : "재테스트 시작"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
