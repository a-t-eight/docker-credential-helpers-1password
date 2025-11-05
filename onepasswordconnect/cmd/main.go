// SPDX-License-Identifier: MIT
package main

import (
	"github.com/docker/docker-credential-helpers/credentials"
	"github.com/a-t-eight/docker-credential-helpers-1password/onepasswordconnect"
)

func main() {
	credentials.Serve(onepasswordconnect.Onepasswordconnect{})
}