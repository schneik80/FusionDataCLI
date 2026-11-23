# Cost watch

`fusiondatacli -cost-watch` watches one project and writes an **Estimated Cost** to every newly saved design. It is a demo of concurrent writes to the Manufacturing Data Model GraphQL API: the values appear in the data model and in Fusion's properties.

```sh
fusiondatacli -cost-watch -hub "My Hub" -project "Staging"
```

## What it does

Every `-interval` (default 20s) it lists the project's designs, in the root and in every folder, and looks at each one that is new or whose save time changed. A design is priced when:

- it has no Estimated Cost, or
- its description differs from the one its cost was estimated from in this run.

Designs that already have a cost are left alone unless `-reprice` is given. Designs with child components (assemblies) are always skipped, even with `-reprice`: their Estimated Cost is the roll-up of their children's costs, so only parts with no children get a cost written. With `-clear-assemblies`, a cost already stored on an assembly is removed (once per run; `-dry-run` only reports it). Designs with no model data, or that the signed-in user cannot write, are skipped. Up to `-concurrency` designs (default 4) are priced and written at the same time.

Writing the cost can itself move a design's save time. The next check then finds the same description and does nothing, so the watch does not chase its own writes. What a design was priced from is kept in memory only: a restarted watch treats every design with a cost as already priced.

## Where prices come from

| `-estimator` | Price |
|---|---|
| `random` | A random number between $0.50 and $250. |
| `claude` | Claude Sonnet searches the web for the part's name, description, part number and material, and reads a unit price out of the results. If a lookup fails, a random guess is written and the line says why. |
| `auto` (default) | `claude` when an Anthropic API key is available, else `random`. |

The Anthropic API key comes from `ANTHROPIC_API_KEY`. If that is unset and the command runs in a terminal, it asks for one; the key is used for that run and is not saved. Claude lookups are billed to that key, one request with up to three web searches per design priced.

The property holds a bare number. Prices are US dollars by convention; nothing in the data model records the currency.

## Flags

| Flag | Meaning |
|---|---|
| `-hub` | Hub name or id. Required when the account sees more than one hub. |
| `-project` | The project to watch: name or id. |
| `-interval` | Time between checks (minimum 5s). |
| `-once` | Check once and exit. Exit code 3 if any design failed. |
| `-reprice` | Also price designs that already have an Estimated Cost. |
| `-dry-run` | Estimate and print, but write nothing. |
| `-estimator` | `claude`, `random` or `auto`. |
| `-concurrency` | Designs priced and written at once (1 to 16). |
| `-debug` | Log every request and response to `debug.log`. |

## Sign-in

Writing a property needs the `data:write` scope. A login saved by an older version has only `data:read`, and refreshing it keeps the old scopes, so the first `-cost-watch` run without `-dry-run` opens the browser to sign in again.

## How the cost is stored

Estimated Cost is not a field of the v3 schema. It is a system base property of a `Component` (collection `Component`, type `FLOAT`), and it only appears on a component once it has a value.

- **Read:** `component.baseProperties(filter: { names: ["Estimated Cost"] })`.
- **Write:** `setProperties(input: { targetId: <component id>, propertyInputs: [{ propertyDefinitionId, value }] })`.

The property definition id is a constant in `api/estcost.go`. It was read off a component on one hub and is assumed to be the same on every hub. The property's behaviour is `STANDARD`, so a write may create a new version of the component.
