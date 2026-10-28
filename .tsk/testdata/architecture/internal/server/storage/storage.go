package storage

import "example.com/architecture/spectrepb" // want `only the SPECTRE server API boundary may import example.com/architecture/spectrepb`

var _ = spectrepb.Divergence{}
