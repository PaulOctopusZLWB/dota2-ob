# Multica Configuration

Date: 2026-07-05

## Workspace

- Workspace: `dota2-ob`
- Workspace ID: `32c843ab-a05f-405f-a4e5-6bdbcada44ce`
- Issue prefix: `DOT`
- Repository: `https://github.com/PaulOctopusZLWB/dota2-ob.git`

## Project

- Project: `Dota2-OB Live Spectator Analytics`
- Project ID: `5598ec97-9c00-4ada-a41e-0416fdadc086`
- Lead: `G胖`
- Status: `in_progress`
- GitHub resource ID: `a552ae42-ad4b-4880-803d-2dd983234d4a`
- GitHub resource ref: `main`

Note: the authoritative resource id is available from:

```bash
multica project resource list 5598ec97-9c00-4ada-a41e-0416fdadc086 --output json
```

## Agents

### G胖

- Agent ID: `386a3cc3-312e-41ae-9484-97e76675888a`
- Runtime: `Codex (PaulPC4090)`
- Runtime ID: `30d187cc-9345-43da-ace2-bbafae4434ce`
- Model: `gpt-5.5`
- Thinking: `xhigh`
- Role: first secretary, specs, coordination, routing, and acceptance.

### Dota2 Fullstack Engineer

- Agent ID: `82015c12-c096-4654-b8e9-de8808d1f22d`
- Runtime: `Opencode (PaulPC4090)`
- Runtime ID: `73e08f93-5b69-4a58-afd7-ec4d1b02c8d9`
- Model: `opencode-go/glm-5.2`
- Role: pure code fullstack implementation from explicit specs.

### Dota2 Code Reviewer

- Agent ID: `fa5a741a-7266-4f68-85c8-2691fdde3681`
- Runtime: `Codex (PaulPC4090)`
- Runtime ID: `30d187cc-9345-43da-ace2-bbafae4434ce`
- Model: `gpt-5.5`
- Thinking: `xhigh`
- Role: independent review and acceptance gate.

## Workspace Skills

- `dota2-ob-project-workflow`: `37af587e-e8a4-4092-baab-33bfa89f961b`
- `dota2-ob-data-source-boundaries`: `68ccbc4d-ed91-4c89-8925-1cca318bf85a`
- `dota2-ob-fullstack-implementation`: `94f1d204-50b5-451e-b815-2d2a20e6ff63`
- `dota2-ob-code-review`: `ce5c7985-a7be-457a-8642-35cd8d670394`

## Skill Bindings

- G胖: workflow + data-source boundaries.
- Dota2 Fullstack Engineer: workflow + data-source boundaries + fullstack implementation.
- Dota2 Code Reviewer: workflow + data-source boundaries + code review.

## Notes

- Agent `description` is catalog metadata. Runtime behavior belongs in `instructions`.
- Project `description` is durable task context.
- Skills are bound after agent creation and should be added with `agent skills add`, not replacement `set`.
- Repository initial commit: `247e426`.
- Multica checkout was verified from a clean temporary directory with `multica repo checkout https://github.com/PaulOctopusZLWB/dota2-ob.git --ref main`.
