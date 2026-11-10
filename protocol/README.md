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

Transport status:

- current binaries use JSON request/response over HTTPS; loopback HTTP is for development only
- agents use bearer node tokens; optional operator-managed mTLS can protect the fleet routes
- there is no WebSocket stream or automatic certificate issuance/rotation in the current implementation
- the `Envelope` type is for shared/future protocol design and is not the current HTTP framing

See [`../docs/PROTOCOL.md`](../docs/PROTOCOL.md) for the implemented routes and the future stream proposal. [`../docs/CONNECTIVITY.md`](../docs/CONNECTIVITY.md) describes the target network design.

