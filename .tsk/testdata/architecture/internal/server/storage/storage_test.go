package storage_test

import "example.com/architecture/spectrepb/spectrepbconnect" // want `only the SPECTRE server API boundary may import`

var _ = spectrepbconnect.Client{}
