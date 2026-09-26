# Discoverable client contracts

The binary embeds and serves `/openapi.json`, `/schemas/{name}`, `/llms.txt`, and
`/llms-full.txt` through `internal/apidocs.RegisterRoutes`. Documents are public
client contracts and synthetic fixtures; they never embed database state or keys.

`schemas/dto.json` is generated from actual exported Go wire types, including
account, room, action, rule manifest, MCR participant/spectator and statistics
DTOs. `schemas/protocol.json` defines explicit closed HTTP/WS envelopes. Only
documented ruleset extension fields and author metadata accept arbitrary JSON.
Spectator DTOs are independently closed and have no hidden-card members.

Regenerate after changing routes or wire types:

```sh
go run ./api/generate
python3 api/generate_protocol.py
```

Validate offline:

```sh
python3 -m pip install -r api/requirements-test.lock
python3 api/check_contracts.py
go test ./internal/apidocs
```

The Go route coverage test rejects undocumented HTTP handlers. JSON Schema
2020-12 checks resolve only the local registry and verify both successful frames
and rejected hidden-card/action fields. Go, React, TypeScript and Python consume
the same `fixtures/decision.json`; no independently edited copies should exist.
The fixture uses synthetic opaque IDs and does not represent a recorded match.
`fixtures/spectator.json` retains one discarded card face with no physical tile
ID, including when that discard was claimed.

JSON Schema checks shape and audience, while engine/domain tests enforce actual
Mahjong state transitions and scoring. A structurally valid action still needs
session, seat, hand, controller, option and deadline authorization. An ACK is not
an observation; a read-only spectator schema can never validate private hands.
