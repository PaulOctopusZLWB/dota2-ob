# Safety And Account Risk Policy

Date: 2026-07-05

## Position

The MVP must stay inside low-risk, spectator-only data collection.

Allowed by default:

- manually launching Steam and Dota 2,
- manually joining a DotaTV or spectator match,
- using Dota 2 Game State Integration configuration to POST JSON to localhost,
- using Steam Web API for public/authorized metadata,
- parsing downloaded replay/demo files,
- saving and analyzing local JSON/replay data.

Not allowed without explicit approval:

- reading Dota 2 process memory,
- injecting code or shared libraries into Dota 2 or Steam,
- modifying game binaries or protected runtime files,
- packet capture, packet decryption, or protocol bypass work,
- bypassing DotaTV delay or fog-of-war rules,
- automating gameplay, UI actions, matchmaking, or account interactions,
- using bots/macros/scripts to interact with live games,
- storing Steam credentials, cookies, tokens, or API keys in the repo.

## Risk Model

Low risk:

- GSI localhost receiver,
- Steam Web API calls with an API key stored outside the repo,
- replay parsing after match completion,
- manual spectator testing.

Medium risk:

- unofficial Steam/Game Coordinator clients,
- automated DotaTV match discovery/joining,
- high-frequency API polling without backoff,
- browser or UI automation that controls Steam/Dota 2.

High risk:

- process memory access,
- injection,
- packet sniffing/decryption,
- anti-cheat bypass,
- hidden-state extraction unavailable to a normal spectator.

## MVP Rule

If a proposed implementation is not obviously in the low-risk category, it is out of scope for MVP.

The MVP must prove what normal spectator-visible structured data can support before considering any higher-risk source.
