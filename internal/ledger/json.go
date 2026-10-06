package ledger

import "encoding/json"

func jsonValid(value []byte) bool { return json.Valid(value) }
