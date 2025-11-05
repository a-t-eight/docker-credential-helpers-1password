// SPDX-License-Identifier: MIT
package main

import (
	"github.com/docker/docker-credential-helpers/credentials"
	"github.com/docker/docker-credential-helpers/onepasswordconnect"
)

func main() {
	credentials.Serve(onepasswordconnect.Onepasswordconnect{})
}