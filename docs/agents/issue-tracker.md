# Issue tracker: Obsidian vault

Issues, specs and wayfinder maps for tagshelf live in the user's Obsidian vault (`Notes`), not in this repo. Access the vault only through the `obsidian` CLI (see the `obsidian-cli` skill), never by editing vault files directly.

## Conventions

- Everything lives under the vault folder `tagshelf/`, one folder per feature or effort: `tagshelf/<feature>/`
- The spec is `tagshelf/<feature>/Spec.md`
- Implementation issues are one note per ticket at `tagshelf/<feature>/Tickets/NN - <Title>.md`, numbered from `01`, never one combined tickets note
- Every note has frontmatter: `project: tagshelf`, `status` (`todo` | `in-progress` | `done`), `created: YYYY-MM-DD`
- Set properties with `obsidian property:set`; link related notes with `[[wikilinks]]`
- Other folders in the vault share note names like `Map`, so link tagshelf notes by path: `[[tagshelf/TUI/Map]]`, not `[[Map]]`
- Comments and conversation history append to the bottom of the note under a `## Comments` heading

## When a skill says "publish to the issue tracker"

Create the note with `obsidian create "path=tagshelf/<feature>/..." "content=..."`. Check the name isn't taken first, and never pass `overwrite`.

## When a skill says "fetch the relevant ticket"

`obsidian read path=...`. If you only have a name or number, find it with `obsidian search "query=path:tagshelf/ ..."`.

## Wayfinding operations

Used by `/wayfinder`. The **map** is a note with one **child** note per ticket.

- **Map**: `tagshelf/<effort>/Map.md` (Destination / Notes / Decisions so far / Not yet specified / Out of scope)
- **Child ticket**: `tagshelf/<effort>/Tickets/NN - <Title>.md`, with the question under `## Question` and frontmatter `type` (`research` | `prototype` | `grilling` | `task`), `tags: [wayfinder/<type>]`, `status`, `parent: "[[tagshelf/<effort>/Map]]"`, `blocked_by: ["[[tagshelf/<effort>/Tickets/NN - <Title>]]", ...]`
- **Blocking**: a ticket is unblocked when every note in its `blocked_by` has `status: done`
- **Frontier**: tickets with `status: todo` that are unblocked; lowest number wins
- **Claim**: set `status: in-progress` (and `assignee`) before any work
- **Resolve**: append the answer under `## Resolution`, set `status: done`, then add a one-line gist with a `[[link]]` to the ticket under the map's Decisions so far
- **Out of scope**: set `status: done`, add `resolution: out-of-scope`, and list it under the map's Out of scope
