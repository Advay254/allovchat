# Sync

How a signed-in device catches up on everything that happened while it was away, and how it avoids losing or doubling a message when the network drops.

## The cursor

Every chat, message and removal gets the next number from one database sequence (`sync_seq`). Database triggers do this, so no write path can forget to. A device remembers the number it last reached (its **cursor**) and asks for everything after it:

```
GET /api/sync?cursor=0&limit=100
```

The answer has:

| Field | Meaning |
|---|---|
| `threads` | Chats that changed: status, last activity, expiry, keys, unread count. A status of `blocked` means remove it from the list |
| `messages` | Messages that changed: new ones, and ones whose read flag changed. Each has `id`, `thread_id`, `side`, `body`, `ts`, `cid`, `read` |
| `gone` | Ids of chats that were deleted, left, or removed from this account |
| `gone_links` | Ids of links that were deleted |
| `cursor` | Where this answer reaches. Send it next time |
| `more` | `true` means there is another page. Ask again straight away with the new cursor |
| `reset` | `true` means the server's database was reset. Throw local data away and start from cursor 0 |

Start with `cursor=0`, keep asking until `more` is false, and store the last cursor. `limit` is 1 to 300 and defaults to 100. Applying any change twice is harmless, so a device can always safely repeat a request.

## Sending without doubling

Every message carries a `cid`, an id the device makes before sending. The server has a unique index on the chat and `cid`, so sending the same message again, from a retry, a second tab or the offline queue, stores it once. A retry gets the normal success answer. After a retry the device matches the message in its next sync by `cid`.

## Late commits

Sequence numbers are handed out before a transaction commits, so a slow transaction can commit after a later one. To avoid missing it, every sync answer also repeats rows changed in the last 10 seconds that sit up to 200 numbers below the cursor. These repeats never count against the page limit. Devices key everything by id, so they are ignored. A removal is left out of the repeats if the chat has since come back to the account.

## Read state and privacy

A message's `read` flag follows the chat screen's rule. If the other person turned on privacy mode, the sender is not told when a message was read. Turning privacy mode on or off re-sends the affected messages, so a device never keeps a stale flag.

## Live events

`GET /api/app/events` is the event stream the Android service holds open. Each event now carries an `id:` line with the server's current sync position. A device that reconnects sends the last id it saw, as the `Last-Event-ID` header or a `cursor` query value. If anything changed while it was away, the server sends one `sync` event straight away, and the device then calls `/api/sync`. If nothing changed, nothing extra is sent. A connection with no cursor behaves exactly as before.

## Limits

- Removal notices are kept for 14 days. Chats last at most 7 days, so a device that was offline longer than that has nothing left to remove.
- Sync is for signed-in accounts. A guest without an account keeps using the chat screen's own state endpoint.
- The cursor is a position in one server's database. After a restore from backup it can be ahead of the server, which is what `reset` is for.
