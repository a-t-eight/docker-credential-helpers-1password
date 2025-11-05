package main

import (
	"github.com/docker/docker-credential-helpers/credentials"
	"github.com/docker/docker-credential-helpers/onepassword"
)

func main() {
	credentials.Serve(onepassword.OnePassword{})
}