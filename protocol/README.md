# `protocol` — shared Porter/Fleet contract

Small shared Go module for wire types used by both `porter` and `porter-agent`.

Planned contents:

```text
protocol/
  envelope.go
  enroll.go
  node.go
  vm.go
  operation.go
  version.go
```

No business logic belongs here.

See [`../docs/PROTOCOL.md`](../docs/PROTOCOL.md).
This module should stay tiny. It is the contract, not the implementation.

Transport baseline:

- enrollment over HTTPS
- control channel over outbound mTLS WebSocket
- optional WireGuard overlay metadata

See [`../docs/CONNECTIVITY.md`](../docs/CONNECTIVITY.md).


