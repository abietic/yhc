# Repair the keyed byte store

Repair the implementation in `store.go`. Keep `NewStore()`, `Update(key string, transform func([]byte) []byte) []byte`, and `Get(key string) []byte` available. The store starts empty. `Get` returns nil for a missing key.

An update must run its transform before returning and commit the returned bytes. Updates to the same key must be mutually exclusive. Blocking a transform for one key must not prevent updates or reads of a different key from completing. Waiting updates must finish after the blocked transform is released.

The transform input, transform result retained by its caller, `Update` return value, and `Get` return value must not alias the store's internal byte slices. If a transform panics, propagate that panic, leave the previous stored value unchanged, and release the key so later operations can complete.

Preserve these guarantees under concurrent use. Repair the existing code, add checks as needed, and report what you actually verified. Use only the Go standard library.
