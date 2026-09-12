// SPDX-License-Identifier: Apache-2.0
package buildinfo

import (
	"fmt"
	"runtime"
)

// version is set by the linker: -X github.com/akenhq/aken/internal/buildinfo.version=v0.1.0
var version = "dev"

func Version() string { return version }

func String(name string) string {
	return fmt.Sprintf("%s %s (%s, %s/%s)", name, Version(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
