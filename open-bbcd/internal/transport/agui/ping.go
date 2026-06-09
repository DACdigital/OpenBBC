package agui

// ping.go imports a known package from the AG-UI SDK to lock the
// dep in place (otherwise go mod tidy removes unreferenced deps).
// This file will be removed by Task B19 when the real adapter lands.

import (
	_ "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
)
