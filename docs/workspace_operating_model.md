# Dota2-OB Workspace Operating Model

Date: 2026-07-05

## Purpose

This workspace is dedicated to a local Linux Dota 2 live-spectator analytics system on PaulPC4090.

Primary goal:

- Observe live Dota 2 games through Steam/Dota 2 on Linux.
- Collect all stable live telemetry available from official or low-risk local interfaces.
- Build deep analytics around player performance, positions, economy, items, skills, objectives, and derived events.

## Roles

### G胖

First secretary and technical coordinator.

- Owns project memory, research notes, specs, issue decomposition, and final acceptance.
- Writes clear specs before development starts.
- Routes implementation work to the fullstack agent.
- Routes independent review to the code review agent.
- Keeps verified facts separate from assumptions and experiments.

### Dota2 Fullstack Engineer

Pure code implementation agent.

- Runtime: PaulPC4090 local Opencode.
- Model target: `opencode-go/glm-5.2`.
- Works only from an explicit spec or issue.
- Implements locally, runs relevant checks, and reports exact commands/results.

### Dota2 Code Reviewer

Independent verification agent.

- Runtime: PaulPC4090 local Codex.
- Model target: `gpt-5.5`.
- Thinking level: `xhigh`.
- Reviews changes for correctness, regressions, missing tests, security, data integrity, and spec fit.

## Workflow

1. G胖 creates or updates a spec.
2. G胖 creates implementation work with acceptance criteria.
3. Fullstack agent implements the change and verifies it locally.
4. Reviewer agent reviews the diff and test evidence.
5. G胖 synthesizes the result and decides whether the work is accepted, needs fixes, or needs scope revision.

No implementation task should start from a vague idea. Every build task needs:

- objective,
- source of truth,
- non-goals,
- expected files or modules,
- acceptance criteria,
- verification commands.

## Source Policy

Use stable/low-risk sources first:

1. Steam Web API for discovery and metadata.
2. Local Dota 2 Game State Integration for live telemetry.
3. Replay/demo parsing for post-match truth and validation.
4. Computer vision only for visible UI facts not available from GSI.
5. Unofficial Game Coordinator or packet/memory approaches only after explicit approval.

## Safety Gate

MVP and normal development stay inside low-risk spectator-only collection:

- local Dota 2 Game State Integration,
- Steam Web API metadata,
- downloaded replay/demo parsing,
- local files and local dashboards.

The project does not use process memory reads, code injection, packet capture, protocol bypass, anti-cheat bypass, gameplay automation, or DotaTV delay/fog-of-war bypass unless Paul explicitly approves a separate high-risk research scope.

## Repository

Primary repository:

`https://github.com/PaulOctopusZLWB/dota2-ob.git`

Current note: the repository is initialized on `main` and Multica checkout has been verified with `--ref main`.

## Local Documents

- `FIRST_SECRETARY_INSTRUCTIONS.md` - G胖 persistent role rules.
- `research/dota2_live_data_sources.md` - first-pass Dota 2 live data source survey.
- `research/multica_agent_harness_best_practices.md` - Multica/harness/prompt/skill operating notes.
- `docs/spec_template.md` - required spec structure.
- `docs/review_checklist.md` - reviewer checklist.
- `docs/safety_and_account_risk.md` - allowed/disallowed data-source and account-risk boundaries.
- `docs/plans/2026-07-05-mvp-gsi-validation.md` - MVP plan and acceptance requirements.
