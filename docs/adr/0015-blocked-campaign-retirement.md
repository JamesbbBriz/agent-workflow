# ADR 0015: Receipt-bound retirement of blocked Campaigns

Status: Accepted for implementation

A blocked Campaign cannot currently be safely retired by an embedding product.
Provider cancellation is not business termination. Add an Engine operation for
an already admitted, blocked Campaign whose child executions are all quiescent.

The versioned request binds Job, Campaign, expected previous receipt hash,
actor and reason. The existing terminal envelope records a `retired` state
and that request. The reducer validates the prior blocked state, immutable
identity, head binding and absence of unfinished children. Existing completed
terminal receipts are unchanged. Exact retries return the original receipt;
changed requests or heads fail. No receipt after a terminal state is legal.

This is an additive Go embedding API, not a new HTTP listener or scheduler.
The trusted host authenticates the operator and verifies vertical external
effects have settled before calling. It neither kills running providers nor
claims successful completion or erases prior failure. Existing consumers must
upgrade their replay reader before accepting retirement receipts.
