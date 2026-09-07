# Task 4 fix round 1 review

Addressed independent review findings:

- Runtime stats/probe timestamp columns are bounded `VARCHAR(32)` so new MySQL indexes do not index `TEXT`.
- Probe writes now carry the current lease node/epoch and use atomic `INSERT ... SELECT` fencing, with service-side lease lookup.
- Runtime aggregates reject negative counters/durations and windows longer than one minute; cursors validate the requested agent and range.
- Probe execution receives a bounded context, with a 10-second default when the request omits a timeout; probe cursors validate agent scope.
- Focused storage/server tests pass after updating fixtures to create leases.

The live MySQL contract remains deferred until `TUNNELMESH_TEST_MYSQL_DSN` is configured.
