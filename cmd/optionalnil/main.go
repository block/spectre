// Command optionalnil reports nil used as an absent value instead of Option.
package main

import "golang.org/x/tools/go/analysis/singlechecker"

func main() {
	singlechecker.Main(newOptionalNil())
}
