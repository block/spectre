package scripted

import . "github.com/alecthomas/types/optional"

// Row is one row of a statement's reply by column label, with NULL as None.
type Row map[string]Option[string]
