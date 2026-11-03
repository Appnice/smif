# ADR-0009: `message.cancel` returns the state after the call

**Status:** accepted, 2026-11-02

1. **What.** The response of `message.cancel` is `api.CancelResult` `{id, state}`. `state` is the state the
   item has after the call. The RFC (7.1) named the method only; it gave no response contract.
2. **Before.** The daemon always returned `"state": "cancelling"`. An item that was `queued`, `retry_wait`,
   `accepted` or `needs_human` was already `rejected` (reason `cancelled`) when the response came. A client
   that showed the field showed a wrong state.
3. **Values.** `rejected` (`api.CancelRejected`): the daemon rejected the item in the call, in the same
   transaction as its audit row and the synthetic "cancelled" reply. `cancelling` (`api.CancelCancelling`):
   the item is `running`; the daemon told its worker to stop and the item stays `running` until the run
   ends. The final state then shows in `thread.get` (normally `rejected`, run `cancelled`; a run that ends
   first keeps its own outcome). Any other item state is an RPC error (`rejected` code), as before.
4. **Clients.** `mailroom cancel` prints `cancelled <id>: rejected` or `cancelling <id>: the worker is
   stopping; see mailroom thread <id>`. `mailroom ui` shows a `rejected` answer as the final result at once
   and follows a `cancelling` answer with `thread.get` reads, as before (up to 30 s). A response without a
   state (an older daemon) gets the `thread.get` follow-up too.
5. **Unchanged.** No new method. How the daemon stops a worker is unchanged. The method stays human-only.
6. **Tests.** `e2e/cancel_state_test.go` (`TestCancelReturnsState`, `TestCancelCLIRunning`) and
   `internal/ui/item_test.go` (`TestCancelRejectedUsesReturnedState`).
