"""Training orchestrator. N-epoch hill-climbing loop that reuses the eval
pipeline as the reward function. Each epoch: teacher proposes patches →
apply → eval candidate → promote-if-better. Strict `>` comparison; early
stop after `patience` consecutive non-improvements.

Test seams: run_eval, propose_patches, build_teacher_agent, and
models.build_model are attributes on this module so tests can monkeypatch
them without hitting the network."""

from __future__ import annotations

import copy
import time
from typing import Any

from aikdm import models                                 # test seam: keep as module ref so tests can `monkeypatch.setattr(orch_mod.models, "build_model", ...)`. Do NOT change to `from aikdm.models import build_model` — that would rebind locally and break the seam.
from aikdm.config import Settings
from aikdm.eval.orchestrator import run_eval             # test seam
from aikdm.eval.schemas import EvalInput, EvalResult
from aikdm.train.patcher import apply_patches
from aikdm.train.reporter import ProgressEmitter
from aikdm.train.schemas import EpochRecord, SectionPatch, TrainingReport
from aikdm.train.teacher import build_teacher_agent, propose_patches   # test seams


async def run_training(
    inp: EvalInput,
    settings: Settings,
    *,
    epochs: int,
    patience: int,
    emitter: ProgressEmitter | None = None,
) -> tuple[dict[str, Any], TrainingReport]:
    """Runs the loop. Returns (final_bundle, TrainingReport)."""
    emit = emitter or ProgressEmitter()

    teacher_model = models.build_model("teacher", settings)
    teacher_agent = build_teacher_agent(teacher_model)

    best_bundle = copy.deepcopy(inp.agent_version.bundle)
    best_result = await run_eval(_with_bundle(inp, best_bundle), settings)
    initial_score = best_result.score
    best_score = initial_score

    emit("baseline_done", initial_score=initial_score)

    tried: list[SectionPatch] = []
    epoch_records: list[EpochRecord] = []
    no_improve_streak = 0
    stopped_reason: str = "max_epochs"

    for epoch in range(1, epochs + 1):
        started = time.time()
        baseline_at_epoch = best_score

        turn = await propose_patches(
            agent=teacher_agent,
            bundle=best_bundle,
            eval_result=best_result.model_dump(),
            tried=tried,
        )
        patches = turn.output.patches
        notes = turn.output.focus_notes
        emit("teacher_done", epoch=epoch, patches_count=len(patches), focus_notes=notes)

        if not patches:
            epoch_records.append(EpochRecord(
                epoch=epoch, baseline_score=baseline_at_epoch,
                candidate_score=baseline_at_epoch, promoted=False,
                patches=[], teacher_notes=notes,
                duration_seconds=time.time() - started,
                tokens_in=turn.tokens_in, tokens_out=turn.tokens_out,
            ))
            no_improve_streak += 1
            emit("epoch_done", epoch=epoch, promoted=False, best_score=best_score, reason="no_patches")
            if no_improve_streak >= patience:
                stopped_reason = "plateau"; break
            continue

        try:
            candidate_bundle = apply_patches(best_bundle, patches)
        except Exception as e:
            epoch_records.append(EpochRecord(
                epoch=epoch, baseline_score=baseline_at_epoch,
                candidate_score=0.0, promoted=False, patches=patches,
                teacher_notes=notes, duration_seconds=time.time() - started,
                tokens_in=turn.tokens_in, tokens_out=turn.tokens_out,
                error=f"apply_patches: {e}",
            ))
            tried.extend(patches)
            no_improve_streak += 1
            emit("epoch_done", epoch=epoch, promoted=False, error=str(e))
            if no_improve_streak >= patience:
                stopped_reason = "plateau"; break
            continue

        try:
            candidate_result = await run_eval(_with_bundle(inp, candidate_bundle), settings)
        except Exception as e:
            epoch_records.append(EpochRecord(
                epoch=epoch, baseline_score=baseline_at_epoch,
                candidate_score=0.0, promoted=False, patches=patches,
                teacher_notes=notes, duration_seconds=time.time() - started,
                tokens_in=turn.tokens_in, tokens_out=turn.tokens_out,
                error=f"run_eval: {e}",
            ))
            no_improve_streak += 1
            emit("epoch_done", epoch=epoch, promoted=False, error=str(e))
            if no_improve_streak >= patience:
                stopped_reason = "plateau"; break
            continue

        if candidate_result.score > best_score:
            best_bundle = candidate_bundle
            best_result = candidate_result
            best_score = candidate_result.score
            no_improve_streak = 0
            promoted = True
        else:
            tried.extend(patches)
            no_improve_streak += 1
            promoted = False

        epoch_records.append(EpochRecord(
            epoch=epoch, baseline_score=baseline_at_epoch,
            candidate_score=candidate_result.score, promoted=promoted,
            patches=patches, teacher_notes=notes,
            duration_seconds=time.time() - started,
            tokens_in=turn.tokens_in, tokens_out=turn.tokens_out,
        ))
        emit("epoch_done", epoch=epoch, promoted=promoted,
             candidate_score=candidate_result.score, best_score=best_score)

        if no_improve_streak >= patience:
            stopped_reason = "plateau"; break

    report = TrainingReport(
        input_eval_id=inp.eval_id,
        initial_score=initial_score,
        final_score=best_score,
        total_epochs_run=len(epoch_records),
        stopped_reason=stopped_reason,      # already a valid Literal value
        epochs=epoch_records,
        final_bundle_path="",               # populated by the CLI when it knows the out dir
    )
    return best_bundle, report


def _with_bundle(base: EvalInput, bundle: dict[str, Any]) -> EvalInput:
    """Rebuild the EvalInput with a swapped bundle. Only agent_version changes
    — dataset_version and other fields are read-only inputs to run_eval, so
    shallow-copying the outer EvalInput and rebuilding agent_version alone
    avoids copying the (potentially large) dataset on every eval call."""
    return base.model_copy(update={
        "agent_version": base.agent_version.model_copy(update={"bundle": bundle}),
    })
