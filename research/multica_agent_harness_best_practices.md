# Multica Agent Harness, Prompt, And Skill Best Practices

Date: 2026-07-05

## Confirmed Multica Contracts

- Agent `description` is human-facing catalog metadata. It is not injected into runtime prompts.
- Agent `instructions` are the durable runtime behavior contract and are read by the daemon at task claim time.
- Agent `model` and `thinking_level` are persisted fields consumed by the daemon.
- Skills are not bound at agent creation time. Use `multica agent skills add` after the agent exists.
- `agent skills add` is additive. `agent skills set` replaces all current bindings and should be avoided unless replacement is intended.
- Project `description` is durable context injected into issue/task briefings when work is associated with the project.
- Project `github_repo` resources affect future task context and default checkout behavior.
- Squad routing is leader-based. A squad does not automatically fan out work to every member.

## Harness Principles

The runtime harness should receive:

- a stable agent role,
- explicit decision rights,
- task input expectations,
- output expectations,
- boundaries around side effects,
- verification expectations.

Keep mutable project knowledge in project descriptions, docs, issues, and skills. Keep persona and durable behavior in `instructions`.

## Prompt Rules

Good agent prompts should:

- name the role and audience,
- state when to act vs ask,
- define required workflow gates,
- define evidence standards,
- constrain risky behavior,
- explain expected final/report format,
- avoid embedding secrets or unstable credentials.

For this workspace:

- G胖 owns specs, decomposition, coordination, acceptance, and memory.
- Fullstack agent owns code implementation only after a clear spec.
- Reviewer owns independent review and should not rewrite the implementation unless explicitly assigned a fix task.

## Skill Rules

Create small workspace skills for reusable knowledge:

- project workflow,
- Dota 2 data-source boundaries,
- implementation standards,
- review standards.

Bind only relevant skills to each agent:

- G胖: workflow + data-source boundaries.
- Fullstack: workflow + implementation + data-source boundaries.
- Reviewer: workflow + review + data-source boundaries.

## Development Gate

Default flow:

1. Spec.
2. Implementation.
3. Review.
4. Acceptance.

Skipping the spec gate is allowed only for trivial administrative changes.
